package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	rnats "github.com/ishaan-jindal/runnix/internal/nats"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
)

// failPublisher fails every publish (integration: 502 path against real DB).
type failPublisher struct{ err error }

func (f failPublisher) EnsureStreams(context.Context) error { return f.err }

func (f failPublisher) PublishSubmit(_ context.Context, _ rnats.SubmitMessage) error { return f.err }

// registerSession registers a user and returns the session envelope.
func registerSession(t *testing.T, h http.Handler, username, email string) sessionJSON {
	t.Helper()
	code, sess := postJSON(t, h, "/auth/register", map[string]string{
		"username": username,
		"email":    email,
		"password": "SecurePass123",
	})
	if code != http.StatusCreated {
		t.Fatalf("register %s = %d, want 201", username, code)
	}
	return sess
}

func authHeaders(sess sessionJSON, tenant string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + sess.AccessToken,
		"X-Tenant-ID":   tenant,
	}
}

// TestExecutionSubmitFlow covers register -> submit (202) -> JetStream message
// -> get/list, plus cross-tenant isolation.
func TestExecutionSubmitFlow(t *testing.T) {
	pool := testPool(t)
	nc := testNATS(t)

	execH := &ExecutionsHandler{Store: storedb.New(pool), Publisher: nc}
	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}
	authed := middleware.RequireAuth(testJWTSecret, middleware.DBChecker(storedb.New(pool)))
	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.With(authed).Post("/executions", execH.Create)
	m.With(authed).Get("/executions", execH.List)
	m.With(authed).Get("/executions/{id}", execH.Get)

	sess := registerSession(t, m, "ExecUser", "exec@example.com")
	tenant := sess.Tenants[0].ID
	headers := authHeaders(sess, tenant)

	// Subscribe before submitting so the published message is observed.
	sub, err := nc.Conn.SubscribeSync(rnats.SubjectForSubmit("python"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := nc.Conn.Flush(); err != nil {
		t.Fatal(err)
	}

	code, raw := doJSON(t, m, http.MethodPost, "/executions", headers, map[string]any{
		"language": "python",
		"source":   "print('hello')",
		"stdin":    "",
	})
	if code != http.StatusAccepted {
		t.Fatalf("submit = %d (%s), want 202", code, raw)
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decodeRaw(t, raw, &created)
	if created.Status != "queued" || created.ID == "" {
		t.Fatalf("created = %+v", created)
	}

	msg, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("no JetStream message received: %v", err)
	}
	var submit rnats.SubmitMessage
	if err := json.Unmarshal(msg.Data, &submit); err != nil {
		t.Fatal(err)
	}
	if submit.ExecutionID != created.ID || submit.TenantID != tenant ||
		submit.Language != "python" || submit.Source != "print('hello')" || submit.TimeoutS != DefaultTimeoutS {
		t.Fatalf("submit message = %+v", submit)
	}

	code, raw = doJSON(t, m, http.MethodGet, "/executions/"+created.ID, headers, nil)
	if code != http.StatusOK {
		t.Fatalf("get = %d (%s), want 200", code, raw)
	}
	var got executionJSON
	decodeRaw(t, raw, &got)
	if got.Status != "queued" || got.Source != "print('hello')" {
		t.Fatalf("execution = %+v", got)
	}

	code, raw = doJSON(t, m, http.MethodGet, "/executions", headers, nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d (%s), want 200", code, raw)
	}
	var list struct {
		Executions []executionJSON `json:"executions"`
	}
	decodeRaw(t, raw, &list)
	if len(list.Executions) != 1 || list.Executions[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	// Another tenant cannot see it (404, not 403: no existence leak).
	other := registerSession(t, m, "OtherUser", "other@example.com")
	otherHeaders := authHeaders(other, other.Tenants[0].ID)
	code, _ = doJSON(t, m, http.MethodGet, "/executions/"+created.ID, otherHeaders, nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-tenant get = %d, want 404", code)
	}
	code, raw = doJSON(t, m, http.MethodGet, "/executions", otherHeaders, nil)
	if code != http.StatusOK {
		t.Fatalf("cross-tenant list = %d, want 200", code)
	}
	var otherList struct {
		Executions []executionJSON `json:"executions"`
	}
	decodeRaw(t, raw, &otherList)
	if len(otherList.Executions) != 0 {
		t.Fatalf("cross-tenant list = %+v, want empty", otherList)
	}
}

// TestExecutionPublishFailure covers 502 + failed status against a real DB.
func TestExecutionPublishFailure(t *testing.T) {
	pool := testPool(t)

	execH := &ExecutionsHandler{
		Store:     storedb.New(pool),
		Publisher: failPublisher{err: errors.New("nats down")},
	}
	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}
	authed := middleware.RequireAuth(testJWTSecret, middleware.DBChecker(storedb.New(pool)))
	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.With(authed).Post("/executions", execH.Create)
	m.With(authed).Get("/executions", execH.List)
	m.With(authed).Get("/executions/{id}", execH.Get)

	sess := registerSession(t, m, "FailUser", "fail@example.com")
	headers := authHeaders(sess, sess.Tenants[0].ID)

	code, raw := doJSON(t, m, http.MethodPost, "/executions", headers, map[string]any{
		"language": "python",
		"source":   "print(1)",
	})
	if code != http.StatusBadGateway {
		t.Fatalf("submit = %d (%s), want 502", code, raw)
	}

	// The failed execution is still visible (no ghost-queued row).
	code, raw = doJSON(t, m, http.MethodGet, "/executions", headers, nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d, want 200", code)
	}
	var list struct {
		Executions []executionJSON `json:"executions"`
	}
	decodeRaw(t, raw, &list)
	if len(list.Executions) != 1 || list.Executions[0].Status != "failed" {
		t.Fatalf("list = %+v, want one failed execution", list)
	}
}

// TestTenantCreateGetFlow covers org tenant creation, slug derivation,
// duplicate-name retry, and scope enforcement.
func TestTenantCreateGetFlow(t *testing.T) {
	pool := testPool(t)

	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}
	tenH := NewTenantsHandler(pool)
	authed := middleware.RequireAuth(testJWTSecret, middleware.DBChecker(storedb.New(pool)))
	m := chi.NewRouter()
	m.Post("/auth/register", authH.Register)
	m.With(authed).Post("/tenants", tenH.Create)
	m.With(authed).Get("/tenants/{id}", tenH.Get)

	sess := registerSession(t, m, "OrgUser", "org@example.com")
	personal := sess.Tenants[0].ID
	headers := authHeaders(sess, personal)

	code, raw := doJSON(t, m, http.MethodPost, "/tenants", headers, map[string]string{"name": "Acme Org"})
	if code != http.StatusCreated {
		t.Fatalf("create tenant = %d (%s), want 201", code, raw)
	}
	var created struct {
		ID        string `json:"id"`
		Slug      string `json:"slug"`
		Namespace string `json:"namespace"`
		Role      string `json:"role"`
	}
	decodeRaw(t, raw, &created)
	if created.Slug != "acme-org" || created.Role != "owner" || created.Namespace == "" {
		t.Fatalf("tenant = %+v", created)
	}

	// Same name again: slug conflict retry yields a distinct tenant (201).
	code, raw = doJSON(t, m, http.MethodPost, "/tenants", headers, map[string]string{"name": "Acme Org"})
	if code != http.StatusCreated {
		t.Fatalf("duplicate-name create = %d (%s), want 201", code, raw)
	}
	var created2 struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	decodeRaw(t, raw, &created2)
	if created2.Slug == created.Slug || created2.ID == created.ID {
		t.Fatalf("retry tenant = %+v, want distinct slug/id", created2)
	}

	// The new tenant is resolvable via the DBChecker fallback (token predates it).
	orgHeaders := authHeaders(sess, created.ID)
	code, raw = doJSON(t, m, http.MethodGet, "/tenants/"+created.ID, orgHeaders, nil)
	if code != http.StatusOK {
		t.Fatalf("get tenant = %d (%s), want 200", code, raw)
	}

	// Path id must equal the request scope.
	code, _ = doJSON(t, m, http.MethodGet, "/tenants/"+personal, orgHeaders, nil)
	if code != http.StatusForbidden {
		t.Fatalf("mismatched get = %d, want 403", code)
	}
}
