package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
)

func TestAPIKeyFlow(t *testing.T) {
	pool := testPool(t)
	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}
	apiH := NewAPIKeysHandler(pool)
	tenH := NewTenantsHandler(pool)
	execH := &ExecutionsHandler{Store: storedb.New(pool), Publisher: &fakePublisher{}}

	keyAuth := middleware.RequireAuthWithAPIKeys(testJWTSecret, middleware.DBChecker(storedb.New(pool)), apiH.CheckAPIKey)

	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.With(keyAuth).Post("/tenants/{id}/api-keys", apiH.Create)
	m.With(keyAuth).Get("/tenants/{id}/api-keys", apiH.List)
	m.With(keyAuth).Delete("/tenants/{id}/api-keys/{keyId}", apiH.Delete)
	m.With(keyAuth).Get("/tenants/{id}", tenH.Get)
	m.With(keyAuth).Post("/executions", execH.Create)

	sess := registerSession(t, m, "KeyUser", "key@example.com")
	tenant := sess.Tenants[0].ID
	headers := authHeaders(sess, tenant)

	// Create.
	code, raw := doJSON(t, m, http.MethodPost, "/tenants/"+tenant+"/api-keys", headers, map[string]string{"name": "ci key"})
	if code != http.StatusCreated {
		t.Fatalf("create key = %d (%s), want 201", code, raw)
	}
	var created struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Key       string `json:"key"`
		CreatedAt string `json:"created_at"`
	}
	decodeRaw(t, raw, &created)
	if created.ID == "" || created.Name != "ci key" || !strings.HasPrefix(created.Key, "sk_live_") || created.CreatedAt == "" {
		t.Fatalf("created key = %+v", created)
	}

	// Key-authed request.
	keyHeaders := map[string]string{"Authorization": "Bearer " + created.Key, "X-Tenant-ID": tenant}
	code, raw = doJSON(t, m, http.MethodGet, "/tenants/"+tenant, keyHeaders, nil)
	if code != http.StatusOK {
		t.Fatalf("key-authed get = %d (%s), want 200", code, raw)
	}

	// List shape: no secrets.
	code, raw = doJSON(t, m, http.MethodGet, "/tenants/"+tenant+"/api-keys", headers, nil)
	if code != http.StatusOK {
		t.Fatalf("list keys = %d (%s), want 200", code, raw)
	}
	var list struct {
		APIKeys []map[string]any `json:"api_keys"`
	}
	decodeRaw(t, raw, &list)
	if len(list.APIKeys) != 1 {
		t.Fatalf("list = %+v, want one key", list)
	}
	for _, forbidden := range []string{"key", "secret", "secret_hash", "secretHash"} {
		if _, ok := list.APIKeys[0][forbidden]; ok {
			t.Fatalf("list must never expose %q: %v", forbidden, list.APIKeys[0])
		}
	}
	if list.APIKeys[0]["id"] != created.ID || list.APIKeys[0]["name"] != "ci key" {
		t.Fatalf("list item = %v", list.APIKeys[0])
	}

	// Key bearer can manage keys (key-capable auth on key routes).
	code, raw = doJSON(t, m, http.MethodGet, "/tenants/"+tenant+"/api-keys", keyHeaders, nil)
	if code != http.StatusOK {
		t.Fatalf("key-authed list = %d (%s), want 200", code, raw)
	}
	var keyList struct {
		APIKeys []map[string]any `json:"api_keys"`
	}
	decodeRaw(t, raw, &keyList)
	if len(keyList.APIKeys) != 1 || keyList.APIKeys[0]["id"] != created.ID {
		t.Fatalf("key-authed list = %+v, want the created key", keyList)
	}

	// Key-authed write: POST /executions → 202.
	code, raw = doJSON(t, m, http.MethodPost, "/executions", keyHeaders, map[string]any{
		"language": "python",
		"source":   "print('hello')",
	})
	if code != http.StatusAccepted {
		t.Fatalf("key-authed submit = %d (%s), want 202", code, raw)
	}

	// Cross-tenant key use → 403.
	other := registerSession(t, m, "KeyOther", "keyother@example.com")
	otherTenant := other.Tenants[0].ID
	otherHeaders := authHeaders(other, otherTenant)

	// List isolation: tenant B sees none of A's keys.
	code, raw = doJSON(t, m, http.MethodGet, "/tenants/"+otherTenant+"/api-keys", otherHeaders, nil)
	if code != http.StatusOK {
		t.Fatalf("other list = %d (%s), want 200", code, raw)
	}
	var otherList struct {
		APIKeys []map[string]any `json:"api_keys"`
	}
	decodeRaw(t, raw, &otherList)
	if len(otherList.APIKeys) != 0 {
		t.Fatalf("other list = %+v, want empty", otherList)
	}
	crossHeaders := map[string]string{"Authorization": "Bearer " + created.Key, "X-Tenant-ID": otherTenant}
	code, _ = doJSON(t, m, http.MethodGet, "/tenants/"+otherTenant, crossHeaders, nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-tenant key use = %d, want 403", code)
	}

	// Revoke.
	code, _ = doJSON(t, m, http.MethodDelete, "/tenants/"+tenant+"/api-keys/"+created.ID, headers, nil)
	if code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200", code)
	}

	// Revoked key → 401.
	code, _ = doJSON(t, m, http.MethodGet, "/tenants/"+tenant, keyHeaders, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked key use = %d, want 401", code)
	}

	// Revoked-key write → 401.
	code, _ = doJSON(t, m, http.MethodPost, "/executions", keyHeaders, map[string]any{
		"language": "python",
		"source":   "print('hello')",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked key submit = %d, want 401", code)
	}

	// Post-revoke list still shows the key with revoked_at set.
	code, raw = doJSON(t, m, http.MethodGet, "/tenants/"+tenant+"/api-keys", headers, nil)
	if code != http.StatusOK {
		t.Fatalf("post-revoke list = %d (%s), want 200", code, raw)
	}
	var revokedList struct {
		APIKeys []map[string]any `json:"api_keys"`
	}
	decodeRaw(t, raw, &revokedList)
	if len(revokedList.APIKeys) != 1 || revokedList.APIKeys[0]["id"] != created.ID {
		t.Fatalf("post-revoke list = %+v, want the revoked key", revokedList)
	}
	if v, ok := revokedList.APIKeys[0]["revoked_at"]; !ok || v == nil || v == "" {
		t.Fatalf("post-revoke list item must carry revoked_at, got %v", revokedList.APIKeys[0])
	}

	// Idempotent second revoke → 200.
	code, _ = doJSON(t, m, http.MethodDelete, "/tenants/"+tenant+"/api-keys/"+created.ID, headers, nil)
	if code != http.StatusOK {
		t.Fatalf("second revoke = %d, want 200", code)
	}

	// Unknown id → 404.
	code, _ = doJSON(t, m, http.MethodDelete, "/tenants/"+tenant+"/api-keys/does-not-exist", headers, nil)
	if code != http.StatusNotFound {
		t.Fatalf("unknown revoke = %d, want 404", code)
	}

	// Cross-tenant revoke → 404 (no existence leak across tenants).
	code, raw = doJSON(t, m, http.MethodPost, "/tenants/"+tenant+"/api-keys", headers, map[string]string{"name": "second"})
	if code != http.StatusCreated {
		t.Fatalf("second create = %d (%s), want 201", code, raw)
	}
	var second struct {
		ID string `json:"id"`
	}
	decodeRaw(t, raw, &second)
	code, _ = doJSON(t, m, http.MethodDelete, "/tenants/"+otherTenant+"/api-keys/"+second.ID, otherHeaders, nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-tenant revoke = %d, want 404", code)
	}
}

func TestAPIKeyValidation(t *testing.T) {
	pool := testPool(t)
	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}
	apiH := NewAPIKeysHandler(pool)

	keyAuth := middleware.RequireAuthWithAPIKeys(testJWTSecret, middleware.DBChecker(storedb.New(pool)), apiH.CheckAPIKey)
	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.With(keyAuth).Post("/tenants/{id}/api-keys", apiH.Create)
	m.With(keyAuth).Get("/tenants/{id}/api-keys", apiH.List)

	sess := registerSession(t, m, "KeyValidUser", "keyvalid@example.com")
	tenant := sess.Tenants[0].ID
	headers := authHeaders(sess, tenant)

	for name, body := range map[string]map[string]string{
		"empty name": {"name": ""},
		"blank name": {"name": "   "},
		"too long":   {"name": strings.Repeat("x", 61)},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := doJSON(t, m, http.MethodPost, "/tenants/"+tenant+"/api-keys", headers, body)
			if code != http.StatusBadRequest {
				t.Fatalf("= %d, want 400", code)
			}
		})
	}

	// Path id must equal scope.
	code, _ := doJSON(t, m, http.MethodPost, "/tenants/"+otherUUID(tenant)+"/api-keys", headers, map[string]string{"name": "x"})
	if code != http.StatusForbidden {
		t.Fatalf("mismatched create = %d, want 403", code)
	}
	code, _ = doJSON(t, m, http.MethodGet, "/tenants/"+otherUUID(tenant)+"/api-keys", headers, nil)
	if code != http.StatusForbidden {
		t.Fatalf("mismatched list = %d, want 403", code)
	}
}

func otherUUID(id string) string {
	if id == "11111111-1111-1111-1111-111111111111" {
		return "22222222-2222-2222-2222-222222222222"
	}
	return "11111111-1111-1111-1111-111111111111"
}
