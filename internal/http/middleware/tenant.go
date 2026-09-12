package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/ishaan-jindal/runnix/internal/auth"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgtype"
)

// context keys (unexported to avoid collisions).
type ctxKey string

const (
	ctxUserKey   ctxKey = "user_id"
	ctxTenantKey ctxKey = "tenant_id"
	ctxRoleKey   ctxKey = "tenant_role"
)

// MembershipChecker reports whether user belongs to tenant and with which role.
type MembershipChecker func(ctx context.Context, userID, tenantID string) (role string, ok bool)

// MembershipStore is the storedb subset RequireAuth needs. *storedb.Queries
// satisfies it; tests stub it in memory.
type MembershipStore interface {
	CheckMembership(ctx context.Context, arg storedb.CheckMembershipParams) (string, error)
}

// DBChecker builds a MembershipChecker over Postgres.
// Malformed UUIDs and missing rows report not-a-member (no error surface:
// callers map that to 403).
func DBChecker(q MembershipStore) MembershipChecker {
	return func(ctx context.Context, userID, tenantID string) (string, bool) {
		uid, err := uuid.Parse(userID)
		if err != nil {
			return "", false
		}
		tid, err := uuid.Parse(tenantID)
		if err != nil {
			return "", false
		}
		role, err := q.CheckMembership(ctx, storedb.CheckMembershipParams{
			UserID:   pgtype.UUID{Bytes: uid, Valid: true},
			TenantID: pgtype.UUID{Bytes: tid, Valid: true},
		})
		if err != nil {
			return "", false
		}
		return role, true
	}
}

// UserIDFrom returns the authenticated user id from context.
func UserIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserKey).(string)
	return v
}

// TenantIDFrom returns the resolved tenant id from context.
func TenantIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxTenantKey).(string)
	return v
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// RequireAuth validates Bearer JWT and scopes the request to X-Tenant-ID.
// 401 = missing/invalid token. 400 = missing tenant. 403 = not a member.
func RequireAuth(secret string, check MembershipChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeErr(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			claims, err := auth.ParseAccessToken(secret, strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid token")
				return
			}
			tenantID := r.Header.Get("X-Tenant-ID")
			if tenantID == "" {
				writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
				return
			}
			role, ok := "", false
			for _, t := range claims.TenantClaims {
				if t.ID == tenantID {
					role, ok = t.Role, true
					break
				}
			}
			// Fall back to store check so refreshed memberships apply before re-login.
			if !ok && check != nil {
				role, ok = check(r.Context(), claims.Subject, tenantID)
			}
			if !ok {
				writeErr(w, http.StatusForbidden, "not a member of tenant")
				return
			}
			ctx := context.WithValue(r.Context(), ctxUserKey, claims.Subject)
			ctx = context.WithValue(ctx, ctxTenantKey, tenantID)
			ctx = context.WithValue(ctx, ctxRoleKey, role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireUser validates Bearer JWT for non-tenant routes.
// 401 = missing/invalid token. Sets only the user-id context key.
func RequireUser(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeErr(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			claims, err := auth.ParseAccessToken(secret, strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid token")
				return
			}
			ctx := context.WithValue(r.Context(), ctxUserKey, claims.Subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// APIKeyChecker resolves a key id + expected secret hash to its owner user
// and tenant. The handler layer supplies the storedb-backed implementation.
type APIKeyChecker func(ctx context.Context, keyID, secretHash string) (userID, tenantID string, ok bool)

// RequireAuthWithAPIKeys accepts either a Bearer JWT (delegated to
// RequireAuth unchanged) or a Bearer API key (sk_live_<id>.<secret>).
// Posture: any tenant member may mint and use keys (no role gating); the
// role context key follows live membership and is kept for future use.
// API-key path: 401 = malformed key or unknown/revoked/wrong secret
// (indistinguishable). 400 = missing tenant. 403 = key issued for a
// different tenant, or the owner is no longer a member (resolved through
// the live membership check so a removed user loses key access immediately).
func RequireAuthWithAPIKeys(secret string, check MembershipChecker, keys APIKeyChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			raw := ""
			if strings.HasPrefix(h, "Bearer ") {
				raw = strings.TrimPrefix(h, "Bearer ")
			}
			if !strings.HasPrefix(raw, "sk_live_") {
				RequireAuth(secret, check)(next).ServeHTTP(w, r)
				return
			}
			keyID, keySecret, err := auth.ParseAPIKey(raw)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			userID, keyTenantID, ok := "", "", false
			if keys != nil {
				userID, keyTenantID, ok = keys(r.Context(), keyID, auth.HashAPIKeySecret(keySecret))
			}
			if !ok {
				writeErr(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			tenantID := r.Header.Get("X-Tenant-ID")
			if tenantID == "" {
				writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
				return
			}
			if keyTenantID != tenantID {
				writeErr(w, http.StatusForbidden, "API key is not valid for this tenant")
				return
			}
			role, ok := "", false
			if check != nil {
				role, ok = check(r.Context(), userID, tenantID)
			}
			if !ok {
				writeErr(w, http.StatusForbidden, "not a member of tenant")
				return
			}
			ctx := context.WithValue(r.Context(), ctxUserKey, userID)
			ctx = context.WithValue(ctx, ctxTenantKey, tenantID)
			ctx = context.WithValue(ctx, ctxRoleKey, role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
