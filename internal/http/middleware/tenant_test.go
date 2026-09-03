package middleware

import (
	"net/http"
	"net/http/httptest"
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
	check := func(userID, tenantID string) (string, bool) {
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
