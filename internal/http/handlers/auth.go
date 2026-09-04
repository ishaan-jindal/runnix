package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"github.com/ishaan-jindal/runnix/internal/auth"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuthHandler serves register, login, and refresh against Postgres.
type AuthHandler struct {
	Pool      *pgxpool.Pool
	JWTSecret string
}

type registerRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type tenantJSON struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type userJSON struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// Register creates a user plus a personal tenant (owner membership) and issues tokens.
//
//	POST /auth/register {username, email, password} → 201
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeBody(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if len(req.Username) < 3 {
		writeErr(w, http.StatusBadRequest, "username must be 3+ characters")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid email")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be 8+ characters")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create user")
		return
	}

	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create user")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := storedb.New(tx)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:        req.Email,
		Username:     req.Username,
		PasswordHash: hash,
	})
	if err != nil {
		writeErr(w, pgConflictStatus(err), pgConflictMessage(err, "username or email already exists"))
		return
	}

	base := slugify(req.Username)
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
			writeErr(w, pgConflictStatus(err), pgConflictMessage(err, "username is taken"))
			return
		}
	}
	if err := q.AddMembership(ctx, storedb.AddMembershipParams{
		UserID:   user.ID,
		TenantID: tenant.ID,
		Role:     "owner",
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create membership")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create user")
		return
	}

	h.writeSession(w, http.StatusCreated, store.PgToString(user.ID), user.Username, user.Email)
}

// Login verifies credentials and issues tokens.
//
//	POST /auth/login {email|username, password} → 200
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeBody(w, r, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Username = strings.TrimSpace(req.Username)
	if req.Password == "" || (req.Email == "" && req.Username == "") {
		writeErr(w, http.StatusBadRequest, "email or username and password are required")
		return
	}

	ctx := r.Context()
	q := storedb.New(h.Pool)
	var (
		user storedb.User
		err  error
	)
	if req.Email != "" {
		user, err = q.GetUserByEmail(ctx, req.Email)
	} else {
		user, err = q.GetUserByUsername(ctx, req.Username)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not log in")
		return
	}
	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	h.writeSession(w, http.StatusOK, store.PgToString(user.ID), user.Username, user.Email)
}

// Refresh validates a refresh token and issues a new token pair with
// freshly resolved memberships.
//
//	POST /auth/refresh {refreshToken} → 200
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RefreshToken == "" {
		writeErr(w, http.StatusBadRequest, "refreshToken is required")
		return
	}
	userID, err := auth.ParseRefreshToken(h.JWTSecret, req.RefreshToken)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	ctx := r.Context()
	uid, err := store.ParsePg(userID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}
	user, err := storedb.New(h.Pool).GetUserByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusUnauthorized, "invalid refresh token")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not refresh")
		return
	}

	h.writeSession(w, http.StatusOK, store.PgToString(user.ID), user.Username, user.Email)
}

// writeSession resolves memberships, signs a token pair, and writes the envelope.
func (h *AuthHandler) writeSession(w http.ResponseWriter, code int, userID, username, email string) {
	// Memberships are resolved per request so responses never go stale.
	// A brand-new user always has exactly one (owner of the personal tenant).
	uid, err := store.ParsePg(userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create session")
		return
	}
	rows, err := storedb.New(h.Pool).ListTenantMemberships(context.Background(), uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create session")
		return
	}
	tenants := make([]tenantJSON, 0, len(rows))
	claims := make([]auth.TenantClaim, 0, len(rows))
	for _, m := range rows {
		id := store.PgToString(m.ID)
		tenants = append(tenants, tenantJSON{ID: id, Slug: m.Slug, Role: m.Role})
		claims = append(claims, auth.TenantClaim{ID: id, Role: m.Role})
	}

	access, err := auth.SignAccessToken(h.JWTSecret, userID, claims)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create session")
		return
	}
	refresh, err := auth.SignRefreshToken(h.JWTSecret, userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create session")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user":         userJSON{ID: userID, Username: username, Email: email},
		"tenants":      tenants,
		"accessToken":  access,
		"refreshToken": refresh,
	})
}

// slugify lowers a username into a URL-safe tenant slug.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	if slug == "" {
		slug = "tenant"
	}
	return slug
}

// pgConflictStatus maps unique violations to 409, everything else to 500.
func pgConflictStatus(err error) int {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// pgConflictMessage hides driver detail on conflicts.
func pgConflictMessage(err error, fallback string) string {
	if pgConflictStatus(err) == http.StatusConflict {
		return fallback
	}
	return "could not complete request"
}
