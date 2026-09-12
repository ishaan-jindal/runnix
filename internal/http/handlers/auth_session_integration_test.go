package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
)

func TestRefreshRotation(t *testing.T) {
	pool := testPool(t)
	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}

	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.Post("/auth/refresh", authH.Refresh)

	sess := registerSession(t, m, "RotUser", "rot@example.com")

	code, ref := postJSON(t, m, "/auth/refresh", map[string]string{
		"refreshToken": sess.RefreshToken,
	})
	if code != http.StatusOK || ref.RefreshToken == "" || ref.AccessToken == "" {
		t.Fatalf("refresh = %d, want 200 with rotated pair", code)
	}
	if ref.RefreshToken == sess.RefreshToken {
		t.Fatal("rotated refresh token must differ")
	}

	// Old token was revoked at rotation time.
	code, _ = postJSON(t, m, "/auth/refresh", map[string]string{
		"refreshToken": sess.RefreshToken,
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("reuse of old refresh = %d, want 401", code)
	}

	// New token still works (rotates again).
	code, ref2 := postJSON(t, m, "/auth/refresh", map[string]string{
		"refreshToken": ref.RefreshToken,
	})
	if code != http.StatusOK || ref2.RefreshToken == "" {
		t.Fatalf("second rotation = %d, want 200", code)
	}

	// Bogus token.
	code, _ = postJSON(t, m, "/auth/refresh", map[string]string{
		"refreshToken": "bogus",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("bad refresh = %d, want 401", code)
	}
}

func TestLogoutRevokes(t *testing.T) {
	pool := testPool(t)
	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}

	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.Post("/auth/refresh", authH.Refresh)
	m.Post("/auth/logout", authH.Logout)

	sess := registerSession(t, m, "LogoutUser", "logout@example.com")

	code, raw := doJSON(t, m, http.MethodPost, "/auth/logout", nil, map[string]string{
		"refreshToken": sess.RefreshToken,
	})
	if code != http.StatusOK {
		t.Fatalf("logout = %d (%s), want 200", code, raw)
	}
	var logged struct {
		Status string `json:"status"`
	}
	decodeRaw(t, raw, &logged)
	if logged.Status != "logged out" {
		t.Fatalf("logout status = %q, want logged out", logged.Status)
	}

	code, _ = postJSON(t, m, "/auth/refresh", map[string]string{
		"refreshToken": sess.RefreshToken,
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d, want 401", code)
	}

	// Unknown token is not an oracle: still 200.
	code, _ = doJSON(t, m, http.MethodPost, "/auth/logout", nil, map[string]string{
		"refreshToken": "bogus",
	})
	if code != http.StatusOK {
		t.Fatalf("logout bogus = %d, want 200", code)
	}

	// Empty body is still 200.
	code, _ = doJSON(t, m, http.MethodPost, "/auth/logout", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("logout empty body = %d, want 200", code)
	}

	// Malformed JSON is still 200.
	badReq := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader("{invalid"))
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	m.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusOK {
		t.Fatalf("logout malformed body = %d, want 200", badRec.Code)
	}
}

func TestLogoutAllRevokes(t *testing.T) {
	pool := testPool(t)
	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}
	usersH := NewUsersHandler(pool)

	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.Post("/auth/login", authH.Login)
	m.Post("/auth/refresh", authH.Refresh)
	m.With(middleware.RequireUser(testJWTSecret)).Post("/auth/logout-all", authH.LogoutAll)
	m.With(middleware.RequireUser(testJWTSecret)).Get("/users/me", usersH.Me)

	sess := registerSession(t, m, "LogoutAllUser", "logoutall@example.com")
	code, second := postJSON(t, m, "/auth/login", map[string]string{
		"email":    "logoutall@example.com",
		"password": "SecurePass123",
	})
	if code != http.StatusOK {
		t.Fatalf("second login = %d, want 200", code)
	}

	code, raw := doJSON(t, m, http.MethodPost, "/auth/logout-all", map[string]string{
		"Authorization": "Bearer " + sess.AccessToken,
	}, nil)
	if code != http.StatusOK {
		t.Fatalf("logout-all = %d (%s), want 200", code, raw)
	}

	for name, tok := range map[string]string{"first": sess.RefreshToken, "second": second.RefreshToken} {
		code, _ = postJSON(t, m, "/auth/refresh", map[string]string{"refreshToken": tok})
		if code != http.StatusUnauthorized {
			t.Fatalf("refresh %s after logout-all = %d, want 401", name, code)
		}
	}

	// Refresh-only revocation: the access token still works until its
	// 15-min expiry.
	code, _ = doJSON(t, m, http.MethodGet, "/users/me", map[string]string{
		"Authorization": "Bearer " + sess.AccessToken,
	}, nil)
	if code != http.StatusOK {
		t.Fatalf("users/me after logout-all = %d, want 200 (access outlives refresh revocation)", code)
	}

	// No token → 401.
	code, _ = doJSON(t, m, http.MethodPost, "/auth/logout-all", nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("logout-all without token = %d, want 401", code)
	}
}

func TestUsersMe(t *testing.T) {
	pool := testPool(t)
	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}
	usersH := NewUsersHandler(pool)

	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.With(middleware.RequireUser(testJWTSecret)).Get("/users/me", usersH.Me)

	sess := registerSession(t, m, "MeUser", "me@example.com")

	// No X-Tenant-ID needed.
	code, raw := doJSON(t, m, http.MethodGet, "/users/me", map[string]string{
		"Authorization": "Bearer " + sess.AccessToken,
	}, nil)
	if code != http.StatusOK {
		t.Fatalf("users/me = %d (%s), want 200", code, raw)
	}
	var got struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
		Tenants []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Role string `json:"role"`
		} `json:"tenants"`
	}
	decodeRaw(t, raw, &got)
	if got.User.ID != sess.User.ID || got.User.Username != "MeUser" || got.User.Email != "me@example.com" {
		t.Fatalf("user = %+v, want session user", got.User)
	}
	if len(got.Tenants) != 1 || got.Tenants[0].ID != sess.Tenants[0].ID || got.Tenants[0].Role != "owner" {
		t.Fatalf("tenants = %+v, want one owner", got.Tenants)
	}

	code, _ = doJSON(t, m, http.MethodGet, "/users/me", nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("users/me without token = %d, want 401", code)
	}
}
