package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ishaan-jindal/runnix/internal/auth"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// APIKeyStore is the storedb surface API-key handlers need.
// *storedb.Queries satisfies it; unit tests supply fakes.
type APIKeyStore interface {
	CreateAPIKey(ctx context.Context, arg storedb.CreateAPIKeyParams) (storedb.CreateAPIKeyRow, error)
	GetAPIKey(ctx context.Context, id string) (storedb.ApiKey, error)
	ListAPIKeys(ctx context.Context, tenantID pgtype.UUID) ([]storedb.ListAPIKeysRow, error)
	RevokeAPIKey(ctx context.Context, id string) error
}

// APIKeysHandler serves tenant-scoped API keys.
type APIKeysHandler struct {
	Pool  *pgxpool.Pool
	Store APIKeyStore
}

type createAPIKeyRequest struct {
	Name string `json:"name"`
}

type apiKeyJSON struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	RevokedAt *string `json:"revoked_at,omitempty"`
}

// NewAPIKeysHandler wires the handler over a live pool.
func NewAPIKeysHandler(pool *pgxpool.Pool) *APIKeysHandler {
	return &APIKeysHandler{Pool: pool, Store: storedb.New(pool)}
}

// CheckAPIKey implements middleware.APIKeyChecker: by-id lookup, reject
// revoked keys, constant-time compare of the expected secret hash.
// The middleware hashes the presenting secret before calling, so secretHash
// is compared directly against the stored hash.
func (h *APIKeysHandler) CheckAPIKey(ctx context.Context, keyID, secretHash string) (string, string, bool) {
	key, err := h.Store.GetAPIKey(ctx, keyID)
	if err != nil {
		return "", "", false
	}
	if key.RevokedAt.Valid {
		return "", "", false
	}
	if !auth.EqualAPIKeyHash(key.SecretHash, secretHash) {
		return "", "", false
	}
	return store.PgToString(key.UserID), store.PgToString(key.TenantID), true
}

// Create issues a tenant-scoped API key. The full key is returned ONCE.
// Must stay behind RequireAuthWithAPIKeys: membership is enforced by
// middleware, not in-handler.
//
//	POST /tenants/{id}/api-keys {name} -> 201 {id, name, key, created_at}
func (h *APIKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenant := requestTenant(r)
	if tenant == "" {
		writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
		return
	}
	if chi.URLParam(r, "id") != tenant {
		writeErr(w, http.StatusForbidden, "tenant id does not match X-Tenant-ID")
		return
	}
	userID := middleware.UserIDFrom(r.Context())
	if userID == "" {
		writeErr(w, http.StatusForbidden, "authentication required to create an API key")
		return
	}
	var req createAPIKeyRequest
	if !decodeBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) < 1 || len(name) > 60 {
		writeErr(w, http.StatusBadRequest, "name must be 1-60 characters")
		return
	}
	uid, err := store.ParsePg(userID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid user identity")
		return
	}
	tid, err := store.ParsePg(tenant)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid tenant id")
		return
	}

	keyID, secret, fullKey, err := auth.GenerateAPIKey()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create API key")
		return
	}
	var row storedb.CreateAPIKeyRow
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			var genErr error
			keyID, secret, fullKey, genErr = auth.GenerateAPIKey()
			if genErr != nil {
				writeErr(w, http.StatusInternalServerError, "could not create API key")
				return
			}
		}
		var err error
		row, err = h.Store.CreateAPIKey(r.Context(), storedb.CreateAPIKeyParams{
			ID:         keyID,
			UserID:     uid,
			TenantID:   tid,
			Name:       name,
			SecretHash: auth.HashAPIKeySecret(secret),
		})
		if err == nil {
			break
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && attempt < 2 {
			continue
		}
		writeErr(w, http.StatusInternalServerError, "could not create API key")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         row.ID,
		"name":       row.Name,
		"key":        fullKey,
		"created_at": row.CreatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}

// List returns API keys for the tenant. Secrets and hashes are never exposed.
// Revoked keys are included with revoked_at set.
// Must stay behind RequireAuthWithAPIKeys: membership is enforced by
// middleware, not in-handler.
//
//	GET /tenants/{id}/api-keys -> 200 {api_keys: [{id, name, created_at, revoked_at}]}
func (h *APIKeysHandler) List(w http.ResponseWriter, r *http.Request) {
	tenant := requestTenant(r)
	if tenant == "" {
		writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
		return
	}
	if chi.URLParam(r, "id") != tenant {
		writeErr(w, http.StatusForbidden, "tenant id does not match X-Tenant-ID")
		return
	}
	if middleware.UserIDFrom(r.Context()) == "" {
		writeErr(w, http.StatusForbidden, "authentication required to list API keys")
		return
	}
	tid, err := store.ParsePg(tenant)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid tenant id")
		return
	}

	rows, err := h.Store.ListAPIKeys(r.Context(), tid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list API keys")
		return
	}
	items := make([]apiKeyJSON, 0, len(rows))
	for _, k := range rows {
		var revoked *string
		if k.RevokedAt.Valid {
			s := k.RevokedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00")
			revoked = &s
		}
		items = append(items, apiKeyJSON{
			ID:        k.ID,
			Name:      k.Name,
			CreatedAt: k.CreatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
			RevokedAt: revoked,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": items})
}

// Delete revokes an API key. Idempotent: already-revoked keys still return
// 200; unknown ids return 404, as do keys from another tenant (no existence
// leak across tenants). Must stay behind RequireAuthWithAPIKeys: membership
// is enforced by middleware, not in-handler.
//
//	DELETE /tenants/{id}/api-keys/{keyId} -> 200
func (h *APIKeysHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenant := requestTenant(r)
	if tenant == "" {
		writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
		return
	}
	if chi.URLParam(r, "id") != tenant {
		writeErr(w, http.StatusForbidden, "tenant id does not match X-Tenant-ID")
		return
	}
	if middleware.UserIDFrom(r.Context()) == "" {
		writeErr(w, http.StatusForbidden, "authentication required to revoke an API key")
		return
	}
	keyID := chi.URLParam(r, "keyId")
	if keyID == "" {
		writeErr(w, http.StatusNotFound, "API key not found")
		return
	}

	key, err := h.Store.GetAPIKey(r.Context(), keyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "API key not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not revoke API key")
		return
	}
	if store.PgToString(key.TenantID) != tenant {
		writeErr(w, http.StatusNotFound, "API key not found")
		return
	}
	if err := h.Store.RevokeAPIKey(r.Context(), keyID); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke API key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}
