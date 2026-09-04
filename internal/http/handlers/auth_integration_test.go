package handlers

import (
	"net/http"
	"testing"
)

func TestRegisterLoginRefreshRoundTrip(t *testing.T) {
	pool := testPool(t)
	h := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}

	code, reg := postJSON(t, http.HandlerFunc(h.Register), "/auth/register", map[string]string{
		"username": "AdaLovelace",
		"email":    "ada@example.com",
		"password": "SecurePass123",
	})
	if code != http.StatusCreated {
		t.Fatalf("register = %d, want 201", code)
	}
	if reg.User.Username != "AdaLovelace" || reg.AccessToken == "" || reg.RefreshToken == "" {
		t.Fatalf("bad register envelope: %+v", reg)
	}
	if len(reg.Tenants) != 1 || reg.Tenants[0].Role != "owner" || reg.Tenants[0].Slug != "adalovelace" {
		t.Fatalf("expected one owner tenant slug=adalovelace, got %+v", reg.Tenants)
	}

	code, login := postJSON(t, http.HandlerFunc(h.Login), "/auth/login", map[string]string{
		"email":    "ada@example.com",
		"password": "SecurePass123",
	})
	if code != http.StatusOK || login.AccessToken == "" {
		t.Fatalf("login = %d, want 200 with tokens", code)
	}

	code, byName := postJSON(t, http.HandlerFunc(h.Login), "/auth/login", map[string]string{
		"username": "AdaLovelace",
		"password": "SecurePass123",
	})
	if code != http.StatusOK || byName.User.ID != reg.User.ID {
		t.Fatalf("username login = %d, want 200 for same user", code)
	}

	code, _ = postJSON(t, http.HandlerFunc(h.Login), "/auth/login", map[string]string{
		"email":    "ada@example.com",
		"password": "wrong-password",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("bad password = %d, want 401", code)
	}

	code, dup := postJSON(t, http.HandlerFunc(h.Register), "/auth/register", map[string]string{
		"username": "AdaLovelace",
		"email":    "ada@example.com",
		"password": "SecurePass123",
	})
	if code != http.StatusConflict {
		t.Fatalf("duplicate register = %d (%+v), want 409", code, dup)
	}

	code, ref := postJSON(t, http.HandlerFunc(h.Refresh), "/auth/refresh", map[string]string{
		"refreshToken": login.RefreshToken,
	})
	if code != http.StatusOK || ref.AccessToken == "" || ref.RefreshToken == "" {
		t.Fatalf("refresh = %d, want 200 with rotated pair", code)
	}
	if len(ref.Tenants) != 1 || ref.Tenants[0].ID != reg.Tenants[0].ID {
		t.Fatalf("refresh must re-resolve memberships, got %+v", ref.Tenants)
	}

	code, _ = postJSON(t, http.HandlerFunc(h.Refresh), "/auth/refresh", map[string]string{
		"refreshToken": "bogus",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("bad refresh = %d, want 401", code)
	}
}

func TestRegisterValidation(t *testing.T) {
	pool := testPool(t)
	h := http.HandlerFunc((&AuthHandler{Pool: pool, JWTSecret: testJWTSecret}).Register)

	for name, tc := range map[string]struct {
		body map[string]string
		want int
	}{
		"short username": {map[string]string{"username": "ab", "email": "a@b.co", "password": "SecurePass123"}, http.StatusBadRequest},
		"bad email":      {map[string]string{"username": "validname", "email": "not-an-email", "password": "SecurePass123"}, http.StatusBadRequest},
		"short password": {map[string]string{"username": "validname", "email": "a@b.co", "password": "short"}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := postJSON(t, h, "/auth/register", tc.body)
			if code != tc.want {
				t.Fatalf("= %d, want %d", code, tc.want)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Ada Lovelace": "ada-lovelace",
		"UPPER__x":     "upper-x",
		"a":            "a",
		"---":          "tenant",
	} {
		if got := slugify(in); got != want {
			t.Fatalf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
