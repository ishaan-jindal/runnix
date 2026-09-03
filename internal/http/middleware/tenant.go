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
