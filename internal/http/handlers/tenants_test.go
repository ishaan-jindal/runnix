package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ishaan-jindal/runnix/internal/auth"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	testUserID     = "33333333-3333-3333-3333-333333333333"
	testTenantSlug = "acme-org"
)

// fakeTenantStore returns canned tenants.
type fakeTenantStore struct {
	tenant storedb.Tenant
	getErr error
}

func (f *fakeTenantStore) CreateTenant(context.Context, storedb.CreateTenantParams) (storedb.Tenant, error) {
	return storedb.Tenant{}, nil
}

func (f *fakeTenantStore) GetTenant(_ context.Context, _ pgtype.UUID) (storedb.Tenant, error) {
	return f.tenant, f.getErr
}

func (f *fakeTenantStore) AddMembership(context.Context, storedb.AddMembershipParams) error {
	return nil
}

func testTenant(t *testing.T) storedb.Tenant {
	t.Helper()
	return storedb.Tenant{
		ID:        mustPgUUID(t, testTenantID),
		Slug:      testTenantSlug,
		Namespace: "runnix-tenant-" + testTenantID,
		Tier:      "free",
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
}

// authedCreate wraps TenantsHandler.Create with real auth middleware and a
// caller identity of testUserID.
func authedCreate(t *testing.T, h *TenantsHandler, body any) (int, []byte) {
	t.Helper()
	tok, err := auth.SignAccessToken(testJWTSecret, testUserID, []auth.TenantClaim{{ID: testTenantID, Role: "owner"}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(context.Context, string, string) (string, bool) { return "owner", true }
	wrapped := middleware.RequireAuth(testJWTSecret, check)(http.HandlerFunc(h.Create))
	return doJSON(t, wrapped, http.MethodPost, "/tenants", map[string]string{
		"Authorization": "Bearer " + tok,
		"X-Tenant-ID":   testTenantID,
	}, body)
}

func TestTenantGet(t *testing.T) {
	h := &TenantsHandler{Store: &fakeTenantStore{tenant: testTenant(t)}}
	m := chi.NewRouter()
	m.Get("/tenants/{id}", h.Get)
	headers := map[string]string{"X-Tenant-ID": testTenantID}

	t.Run("success", func(t *testing.T) {
		code, raw := doJSON(t, m, http.MethodGet, "/tenants/"+testTenantID, headers, nil)
		if code != http.StatusOK {
			t.Fatalf("= %d (%s), want 200", code, raw)
		}
		var got map[string]any
		decodeRaw(t, raw, &got)
		if got["slug"] != testTenantSlug || got["namespace"] != "runnix-tenant-"+testTenantID {
			t.Fatalf("tenant = %v", got)
		}
	})

	t.Run("id mismatch forbidden", func(t *testing.T) {
		code, _ := doJSON(t, m, http.MethodGet, "/tenants/"+testExecID, headers, nil)
		if code != http.StatusForbidden {
			t.Fatalf("= %d, want 403", code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		h404 := &TenantsHandler{Store: &fakeTenantStore{getErr: pgx.ErrNoRows}}
		m404 := chi.NewRouter()
		m404.Get("/tenants/{id}", h404.Get)
		code, _ := doJSON(t, m404, http.MethodGet, "/tenants/"+testTenantID, headers, nil)
		if code != http.StatusNotFound {
			t.Fatalf("= %d, want 404", code)
		}
	})

	t.Run("missing tenant", func(t *testing.T) {
		code, _ := doJSON(t, m, http.MethodGet, "/tenants/"+testTenantID, nil, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("= %d, want 400", code)
		}
	})
}

func TestTenantCreateValidation(t *testing.T) {
	// Nil pool: validation returns before any DB use.
	h := &TenantsHandler{Store: &fakeTenantStore{}}

	t.Run("no identity forbidden", func(t *testing.T) {
		code, _ := doJSON(t, http.HandlerFunc(h.Create), http.MethodPost, "/tenants", nil,
			map[string]string{"name": "Acme Org"})
		if code != http.StatusForbidden {
			t.Fatalf("= %d, want 403", code)
		}
	})

	for name, tc := range map[string]struct {
		body map[string]string
		want int
	}{
		"short name": {map[string]string{"name": "ab"}, http.StatusBadRequest},
		"blank name": {map[string]string{"name": "   "}, http.StatusBadRequest},
		"bad slug":   {map[string]string{"name": "Acme Org", "slug": "BAD SLUG!!"}, http.StatusBadRequest},
		"short slug": {map[string]string{"name": "Acme Org", "slug": "ab"}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := authedCreate(t, h, tc.body)
			if code != tc.want {
				t.Fatalf("= %d, want %d", code, tc.want)
			}
		})
	}
}
