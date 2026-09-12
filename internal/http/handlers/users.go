package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserStore is the storedb surface the users handler needs.
// *storedb.Queries satisfies it; unit tests supply fakes.
type UserStore interface {
	GetUserByID(ctx context.Context, id pgtype.UUID) (storedb.User, error)
	ListTenantMemberships(ctx context.Context, userID pgtype.UUID) ([]storedb.ListTenantMembershipsRow, error)
}

// UsersHandler serves the current-user profile.
type UsersHandler struct {
	Pool  *pgxpool.Pool
	Store UserStore
}

// NewUsersHandler wires the handler over a live pool.
func NewUsersHandler(pool *pgxpool.Pool) *UsersHandler {
	return &UsersHandler{Pool: pool, Store: storedb.New(pool)}
}

// Me returns the caller plus their tenant memberships. It sits behind
// RequireUser, not the tenant group, so no X-Tenant-ID is needed.
//
//	GET /users/me -> 200 {user: {id, username, email}, tenants: [{id, slug, role}]}
func (h *UsersHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFrom(r.Context())
	if userID == "" {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	uid, err := store.ParsePg(userID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid user identity")
		return
	}
	user, err := h.Store.GetUserByID(r.Context(), uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusUnauthorized, "invalid user identity")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not get user")
		return
	}
	tenants, _, err := resolveMemberships(r.Context(), h.Store, uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not get user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":    userJSON{ID: store.PgToString(user.ID), Username: user.Username, Email: user.Email},
		"tenants": tenants,
	})
}
