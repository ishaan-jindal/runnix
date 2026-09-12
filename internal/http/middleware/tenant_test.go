package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishaan-jindal/runnix/internal/auth"
)

func tokenFor(t *testing.T, secret string, tenants []auth.TenantClaim) string {
	t.Helper()
	tok, err := auth.SignAccessToken(secret, "u1", tenants)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestRequireAuth(t *testing.T) {
	secret := "s3cret"
	ok := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	check := func(_ context.Context, _, tenantID string) (string, bool) {
		if tenantID == "t-store" {
			return "member", true
		}
		return "", false
	}
	h := RequireAuth(secret, check)(http.HandlerFunc(ok))

	cases := []struct {
		name       string
		token      string
		tenant     string
		want       int
		useChecker bool
	}{
		{"missing token", "", "t1", http.StatusUnauthorized, false},
		{"bad token", "Bearer nope", "t1", http.StatusUnauthorized, false},
		{"missing tenant", "valid", "", http.StatusBadRequest, false},
		{"member via claim", "valid", "t1", http.StatusOK, false},
		{"non-member", "valid", "t9", http.StatusForbidden, false},
		{"member via store", "valid-empty", "t-store", http.StatusOK, true},
	}

	valid := tokenFor(t, secret, []auth.TenantClaim{{ID: "t1", Role: "owner"}})
	validEmpty := tokenFor(t, secret, nil)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := tc.token
			if tok == "valid" {
				tok = "Bearer " + valid
			} else if tok == "valid-empty" {
				tok = "Bearer " + validEmpty
			} else if tok != "" && len(tok) > 7 && tok[:7] != "Bearer " {
				tok = "Bearer " + tok
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tok != "" {
				req.Header.Set("Authorization", tok)
			}
			if tc.tenant != "" {
				req.Header.Set("X-Tenant-ID", tc.tenant)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("= %d, want %d", rec.Code, tc.want)
			}
			if rec.Code == http.StatusOK && TenantIDFrom(req.Context()) != "" {
				t.Fatal("middleware must not mutate incoming request context")
			}
		})
	}
}

func TestRequireUser(t *testing.T) {
	secret := "s3cret"
	var gotUser, gotTenant string
	ok := func(w http.ResponseWriter, r *http.Request) {
		gotUser = UserIDFrom(r.Context())
		gotTenant = TenantIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}
	h := RequireUser(secret)(http.HandlerFunc(ok))
	valid := tokenFor(t, secret, []auth.TenantClaim{{ID: "t1", Role: "owner"}})

	cases := []struct {
		name       string
		token      string
		want       int
		wantUser   string
		wantTenant string
	}{
		{"missing token", "", http.StatusUnauthorized, "", ""},
		{"bad token", "Bearer nope", http.StatusUnauthorized, "", ""},
		{"api key rejected", "Bearer sk_live_abc.def", http.StatusUnauthorized, "", ""},
		{"valid jwt sets only user", "Bearer " + valid, http.StatusOK, "u1", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotUser, gotTenant = "", ""
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("= %d, want %d", rec.Code, tc.want)
			}
			if gotUser != tc.wantUser || gotTenant != tc.wantTenant {
				t.Fatalf("ctx = (%q, %q), want (%q, %q)",
					gotUser, gotTenant, tc.wantUser, tc.wantTenant)
			}
		})
	}
}

func TestRequireAuthWithAPIKeys(t *testing.T) {
	secret := "s3cret"
	keyID, keySecret, fullKey, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	storedHash := auth.HashAPIKeySecret(keySecret)
	otherID, _, otherFull, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if otherID == keyID {
		t.Fatal("test keys collided")
	}

	keys := func(_ context.Context, id, hash string) (string, string, bool) {
		if id != keyID || !auth.EqualAPIKeyHash(hash, storedHash) {
			return "", "", false
		}
		return "u-key", "t-key", true
	}
	revoked := func(_ context.Context, _, _ string) (string, string, bool) {
		return "", "", false
	}
	member := func(_ context.Context, userID, tenantID string) (string, bool) {
		if userID == "u-key" && tenantID == "t-key" {
			return "admin", true
		}
		return "", false
	}
	gone := func(_ context.Context, _, _ string) (string, bool) {
		return "", false
	}

	var gotUser, gotTenant string
	ok := func(w http.ResponseWriter, r *http.Request) {
		gotUser = UserIDFrom(r.Context())
		gotTenant = TenantIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}
	validJWT := tokenFor(t, secret, []auth.TenantClaim{{ID: "t1", Role: "owner"}})

	cases := []struct {
		name       string
		token      string
		tenant     string
		check      MembershipChecker
		keyCheck   APIKeyChecker
		want       int
		wantUser   string
		wantTenant string
	}{
		{"jwt delegates member", "Bearer " + validJWT, "t1", nil, keys, http.StatusOK, "u1", "t1"},
		{"jwt delegates bad token", "Bearer nope", "t1", nil, keys, http.StatusUnauthorized, "", ""},
		{"jwt delegates missing token", "", "t1", nil, keys, http.StatusUnauthorized, "", ""},
		{"valid key sets ctx", "Bearer " + fullKey, "t-key", member, keys, http.StatusOK, "u-key", "t-key"},
		{"bad secret", "Bearer sk_live_" + keyID + ".wrongsecret", "t-key", member, keys, http.StatusUnauthorized, "", ""},
		{"unknown key", "Bearer " + otherFull, "t-key", member, keys, http.StatusUnauthorized, "", ""},
		{"revoked key", "Bearer " + fullKey, "t-key", member, revoked, http.StatusUnauthorized, "", ""},
		{"malformed key no secret", "Bearer sk_live_abc", "t-key", member, keys, http.StatusUnauthorized, "", ""},
		{"malformed key empty", "Bearer sk_live_", "t-key", member, keys, http.StatusUnauthorized, "", ""},
		{"key wrong tenant", "Bearer " + fullKey, "t-other", member, keys, http.StatusForbidden, "", ""},
		{"membership gone", "Bearer " + fullKey, "t-key", gone, keys, http.StatusForbidden, "", ""},
		{"nil membership denies", "Bearer " + fullKey, "t-key", nil, keys, http.StatusForbidden, "", ""},
		{"missing tenant", "Bearer " + fullKey, "", member, keys, http.StatusBadRequest, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotUser, gotTenant = "", ""
			h := RequireAuthWithAPIKeys(secret, tc.check, tc.keyCheck)(http.HandlerFunc(ok))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", tc.token)
			}
			if tc.tenant != "" {
				req.Header.Set("X-Tenant-ID", tc.tenant)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("= %d, want %d", rec.Code, tc.want)
			}
			if gotUser != tc.wantUser || gotTenant != tc.wantTenant {
				t.Fatalf("ctx = (%q, %q), want (%q, %q)",
					gotUser, gotTenant, tc.wantUser, tc.wantTenant)
			}
			if rec.Code != http.StatusOK {
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("error body is not JSON: %v", err)
				}
				if _, ok := body["error"]; !ok {
					t.Fatalf("error body %q missing error key", rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "phase") {
					t.Fatalf("error body %q must not use phase wording", rec.Body.String())
				}
			}
		})
	}
}

func refreshFor(t *testing.T, secret, userID string) string {
	t.Helper()
	tok, _, err := auth.SignRefreshToken(secret, userID)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestRefreshTokenRejectedAsBearer proves token-type separation at the
// middleware layer: a refresh token must never authorize API access, even
// though it is a valid JWT signed with the same secret. The auth package
// enforces this via audience separation (access vs refresh).
func TestRefreshTokenRejectedAsBearer(t *testing.T) {
	secret := "s3cret"
	refresh := refreshFor(t, secret, "u1")
	ok := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}

	t.Run("RequireUser rejects refresh token", func(t *testing.T) {
		h := RequireUser(secret)(http.HandlerFunc(ok))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+refresh)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("= %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		assertErrEnvelope(t, rec.Body.String())
	})

	t.Run("RequireAuthWithAPIKeys rejects refresh token", func(t *testing.T) {
		allowAll := func(_ context.Context, userID, _ string) (string, bool) {
			return "member", userID != ""
		}
		denyKeys := func(_ context.Context, _, _ string) (string, string, bool) {
			return "", "", false
		}
		h := RequireAuthWithAPIKeys(secret, allowAll, denyKeys)(http.HandlerFunc(ok))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+refresh)
		req.Header.Set("X-Tenant-ID", "t1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("= %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		assertErrEnvelope(t, rec.Body.String())
	})
}

func assertErrEnvelope(t *testing.T, body string) {
	t.Helper()
	var decoded map[string]string
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	if _, ok := decoded["error"]; !ok {
		t.Fatalf("error body %q missing error key", body)
	}
	if strings.Contains(body, "phase") {
		t.Fatalf("error body %q must not use phase wording", body)
	}
}
