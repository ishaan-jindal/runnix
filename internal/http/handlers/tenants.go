package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var validSlug = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)

// TenantStore is the storedb surface tenant handlers need.
// *storedb.Queries satisfies it; unit tests supply fakes.
type TenantStore interface {
	CreateTenant(ctx context.Context, arg storedb.CreateTenantParams) (storedb.Tenant, error)
	GetTenant(ctx context.Context, id pgtype.UUID) (storedb.Tenant, error)
	AddMembership(ctx context.Context, arg storedb.AddMembershipParams) error
}

// TenantsHandler serves org-tenant create/get.
type TenantsHandler struct {
	Pool  *pgxpool.Pool
	Store TenantStore
}

type createTenantRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// NewTenantsHandler wires the handler over a live pool.
func NewTenantsHandler(pool *pgxpool.Pool) *TenantsHandler {
	return &TenantsHandler{Pool: pool, Store: storedb.New(pool)}
}

// Create provisions an org tenant and makes the caller its owner.
//
//	POST /tenants {name, slug?} -> 201
func (h *TenantsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if !decodeBody(w, r, &req) {
		return
	}
	userID := middleware.UserIDFrom(r.Context())
	if userID == "" {
		// No authenticated identity (development without JWT_SECRET):
		// refuse rather than create an ownerless tenant.
		writeErr(w, http.StatusForbidden, "authentication required to create a tenant")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) < 3 || len(req.Name) > 60 {
		writeErr(w, http.StatusBadRequest, "name must be 3-60 characters")
		return
	}
	base := strings.TrimSpace(req.Slug)
	if base == "" {
		base = slugify(req.Name)
	} else {
		base = strings.ToLower(base)
		if !validSlug.MatchString(base) {
			writeErr(w, http.StatusBadRequest, "slug must match [a-z0-9-]{3,40}")
			return
		}
	}
	uid, err := store.ParsePg(userID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid user identity")
		return
	}

	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create tenant")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := storedb.New(tx)

	tenantID := uuid.New()
	var tenant storedb.Tenant
	for attempt := 0; ; attempt++ {
		slug := base
		if attempt > 0 {
			slug = fmt.Sprintf("%s-%s", base, uuid.NewString()[:4])
		}
		// Savepoint per attempt: a slug conflict aborts only the
		// savepoint, leaving the outer tx usable for the retry.
		t, err := createTenantTx(ctx, tx, tenantID, slug)
		if err == nil {
			tenant = t
			break
		}
		if pgConflictStatus(err) != http.StatusConflict || attempt >= 4 {
			writeErr(w, pgConflictStatus(err), pgConflictMessage(err, "slug is taken"))
			return
		}
	}
	if err := q.AddMembership(ctx, storedb.AddMembershipParams{
		UserID:   uid,
		TenantID: tenant.ID,
		Role:     "owner",
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create membership")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create tenant")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         store.PgToString(tenant.ID),
		"slug":       tenant.Slug,
		"namespace":  tenant.Namespace,
		"tier":       tenant.Tier,
		"role":       "owner",
		"created_at": tenant.CreatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}

// createTenantTx inserts one tenant inside a savepoint of tx. A slug
// conflict aborts only the savepoint (ROLLBACK TO SAVEPOINT on defer), so
// the caller can retry with a different slug in the same outer transaction.
// (Postgres aborts the whole tx on any statement error; without the
// savepoint every retry after the first conflict would fail with 25P02.)
func createTenantTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, slug string) (storedb.Tenant, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return storedb.Tenant{}, err
	}
	defer func() { _ = sp.Rollback(ctx) }()
	t, err := storedb.New(sp).CreateTenant(ctx, storedb.CreateTenantParams{
		ID:        store.UUIDToPg(id),
		Slug:      slug,
		Namespace: "runnix-tenant-" + id.String(),
		Tier:      "free",
	})
	if err != nil {
		return storedb.Tenant{}, err
	}
	if err := sp.Commit(ctx); err != nil {
		return storedb.Tenant{}, err
	}
	return t, nil
}

// Get returns one tenant. The path id must equal the request's tenant scope
// (enforced by middleware in production); otherwise 403.
//
//	GET /tenants/{id} -> 200, 403, 404.
func (h *TenantsHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenant := requestTenant(r)
	if tenant == "" {
		writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
		return
	}
	idParam := chi.URLParam(r, "id")
	if idParam != tenant {
		writeErr(w, http.StatusForbidden, "tenant id does not match X-Tenant-ID")
		return
	}
	id, err := store.ParsePg(idParam)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid tenant id")
		return
	}

	tenantRow, err := h.Store.GetTenant(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "tenant not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not get tenant")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         store.PgToString(tenantRow.ID),
		"slug":       tenantRow.Slug,
		"namespace":  tenantRow.Namespace,
		"tier":       tenantRow.Tier,
		"created_at": tenantRow.CreatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}
