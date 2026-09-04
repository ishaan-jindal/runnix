package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	rnats "github.com/ishaan-jindal/runnix/internal/nats"
)

const (
	testTenantID = "11111111-1111-1111-1111-111111111111"
	testExecID   = "22222222-2222-2222-2222-222222222222"
)

func mustPgUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := store.ParsePg(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// fakeExecutionStore records calls and returns canned results.
type fakeExecutionStore struct {
	createRow  storedb.CreateExecutionRow
	createErr  error
	lastCreate storedb.CreateExecutionParams
	getRow     storedb.GetExecutionRow
	getErr     error
	listRows   []storedb.ListExecutionsRow
	listErr    error
	lastList   storedb.ListExecutionsParams
	failed     []storedb.SetExecutionFailedParams
}

func (f *fakeExecutionStore) CreateExecution(_ context.Context, arg storedb.CreateExecutionParams) (storedb.CreateExecutionRow, error) {
	f.lastCreate = arg
	return f.createRow, f.createErr
}

func (f *fakeExecutionStore) GetExecution(_ context.Context, _ storedb.GetExecutionParams) (storedb.GetExecutionRow, error) {
	return f.getRow, f.getErr
}

func (f *fakeExecutionStore) ListExecutions(_ context.Context, arg storedb.ListExecutionsParams) ([]storedb.ListExecutionsRow, error) {
	f.lastList = arg
	return f.listRows, f.listErr
}

func (f *fakeExecutionStore) SetExecutionFailed(_ context.Context, arg storedb.SetExecutionFailedParams) error {
	f.failed = append(f.failed, arg)
	return nil
}

// fakePublisher records submit messages or fails on demand.
type fakePublisher struct {
	msgs []rnats.SubmitMessage
	err  error
}

func (f *fakePublisher) EnsureStreams(context.Context) error { return f.err }

func (f *fakePublisher) PublishSubmit(_ context.Context, msg rnats.SubmitMessage) error {
	f.msgs = append(f.msgs, msg)
	return f.err
}

func queuedRow(t *testing.T) storedb.CreateExecutionRow {
	t.Helper()
	return storedb.CreateExecutionRow{
		ID:        mustPgUUID(t, testExecID),
		TenantID:  mustPgUUID(t, testTenantID),
		Language:  "python",
		Status:    "queued",
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
}

func TestCreateValidation(t *testing.T) {
	h := &ExecutionsHandler{Store: &fakeExecutionStore{}, Publisher: &fakePublisher{}, WebhooksEnabled: true}
	big := strings.Repeat("x", MaxSourceBytes+1)

	for name, tc := range map[string]struct {
		tenant string
		body   map[string]any
		want   int
	}{
		"missing tenant":    {"", map[string]any{"language": "python", "source": "print(1)"}, http.StatusBadRequest},
		"bad tenant uuid":   {"nope", map[string]any{"language": "python", "source": "print(1)"}, http.StatusBadRequest},
		"tenant mismatch":   {testTenantID, map[string]any{"tenant_id": testExecID, "language": "python", "source": "print(1)"}, http.StatusForbidden},
		"bad language":      {testTenantID, map[string]any{"language": "ruby", "source": "puts 1"}, http.StatusBadRequest},
		"missing language":  {testTenantID, map[string]any{"source": "print(1)"}, http.StatusBadRequest},
		"empty source":      {testTenantID, map[string]any{"language": "python", "source": "  "}, http.StatusBadRequest},
		"source too large":  {testTenantID, map[string]any{"language": "python", "source": big}, http.StatusBadRequest},
		"timeout too small": {testTenantID, map[string]any{"language": "python", "source": "print(1)", "timeout_s": -1}, http.StatusBadRequest},
		"timeout too large": {testTenantID, map[string]any{"language": "python", "source": "print(1)", "timeout_s": 61}, http.StatusBadRequest},
		"bad webhook":       {testTenantID, map[string]any{"language": "python", "source": "print(1)", "webhook_url": "ftp://x"}, http.StatusBadRequest},
		"webhook no host":   {testTenantID, map[string]any{"language": "python", "source": "print(1)", "webhook_url": "https://"}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.tenant != "" {
				headers["X-Tenant-ID"] = tc.tenant
			}
			code, _ := doJSON(t, http.HandlerFunc(h.Create), http.MethodPost, "/executions", headers, tc.body)
			if code != tc.want {
				t.Fatalf("= %d, want %d", code, tc.want)
			}
		})
	}
}

func TestCreateWebhookNotConfigured(t *testing.T) {
	// No signing secret configured: webhook submissions are refused outright.
	h := &ExecutionsHandler{Store: &fakeExecutionStore{}, Publisher: &fakePublisher{}}
	code, raw := doJSON(t, http.HandlerFunc(h.Create), http.MethodPost, "/executions",
		map[string]string{"X-Tenant-ID": testTenantID},
		map[string]any{"language": "python", "source": "print(1)", "webhook_url": "https://hooks.example.com/done"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("= %d (%s), want 503", code, raw)
	}
}

func TestCreateSuccessPublishes(t *testing.T) {
	st := &fakeExecutionStore{createRow: queuedRow(t)}
	pub := &fakePublisher{}
	h := &ExecutionsHandler{Store: st, Publisher: pub, WebhooksEnabled: true}

	body := map[string]any{"language": "python", "source": "print('hi')", "stdin": "in", "webhook_url": "https://93.184.216.34/done"}
	code, raw := doJSON(t, http.HandlerFunc(h.Create), http.MethodPost, "/executions",
		map[string]string{"X-Tenant-ID": testTenantID}, body)
	if code != http.StatusAccepted {
		t.Fatalf("= %d (%s), want 202", code, raw)
	}
	var got struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decodeRaw(t, raw, &got)
	if got.ID != testExecID || got.Status != "queued" {
		t.Fatalf("envelope = %+v, want id=%s status=queued", got, testExecID)
	}
	// Timeout defaults to 2 when omitted.
	if st.lastCreate.TimeoutS != DefaultTimeoutS {
		t.Fatalf("timeout = %d, want default %d", st.lastCreate.TimeoutS, DefaultTimeoutS)
	}
	if len(pub.msgs) != 1 {
		t.Fatalf("published %d messages, want 1", len(pub.msgs))
	}
	msg := pub.msgs[0]
	if msg.ExecutionID != testExecID || msg.TenantID != testTenantID ||
		msg.Language != "python" || msg.Source != "print('hi')" ||
		msg.Stdin != "in" || msg.TimeoutS != DefaultTimeoutS {
		t.Fatalf("submit message = %+v", msg)
	}
	if msg.WebhookURL != "https://93.184.216.34/done" {
		t.Fatalf("webhook_url = %q, want it forwarded", msg.WebhookURL)
	}
}

func TestCreatePublishFailureMarksFailed(t *testing.T) {
	st := &fakeExecutionStore{createRow: queuedRow(t)}
	pub := &fakePublisher{err: errors.New("nats down")}
	h := &ExecutionsHandler{Store: st, Publisher: pub}

	code, _ := doJSON(t, http.HandlerFunc(h.Create), http.MethodPost, "/executions",
		map[string]string{"X-Tenant-ID": testTenantID},
		map[string]any{"language": "python", "source": "print(1)"})
	if code != http.StatusBadGateway {
		t.Fatalf("= %d, want 502", code)
	}
	if len(st.failed) != 1 || store.PgToString(st.failed[0].ID) != testExecID {
		t.Fatalf("SetExecutionFailed calls = %+v, want one for %s", st.failed, testExecID)
	}
}

func TestGetExecution(t *testing.T) {
	row := storedb.GetExecutionRow{
		ID:        mustPgUUID(t, testExecID),
		TenantID:  mustPgUUID(t, testTenantID),
		Language:  "python",
		Status:    "succeeded",
		Source:    "print(1)",
		Stdout:    "1\n",
		ExitCode:  pgtype.Int4{Int32: 0, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
	newHandler := func() (*ExecutionsHandler, *fakeExecutionStore) {
		st := &fakeExecutionStore{getRow: row}
		return &ExecutionsHandler{Store: st, Publisher: &fakePublisher{}}, st
	}
	mux := func(h *ExecutionsHandler) http.Handler {
		m := chi.NewRouter()
		m.Get("/executions/{id}", h.Get)
		return m
	}
	headers := map[string]string{"X-Tenant-ID": testTenantID}

	t.Run("success maps exit code", func(t *testing.T) {
		h, _ := newHandler()
		code, raw := doJSON(t, mux(h), http.MethodGet, "/executions/"+testExecID, headers, nil)
		if code != http.StatusOK {
			t.Fatalf("= %d (%s), want 200", code, raw)
		}
		var got executionJSON
		decodeRaw(t, raw, &got)
		if got.Status != "succeeded" || got.Stdout != "1\n" || got.ExitCode == nil || *got.ExitCode != 0 {
			t.Fatalf("execution = %+v", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		st := &fakeExecutionStore{getErr: pgx.ErrNoRows}
		h := &ExecutionsHandler{Store: st, Publisher: &fakePublisher{}}
		code, _ := doJSON(t, mux(h), http.MethodGet, "/executions/"+testExecID, headers, nil)
		if code != http.StatusNotFound {
			t.Fatalf("= %d, want 404", code)
		}
	})

	t.Run("bad id", func(t *testing.T) {
		h, _ := newHandler()
		code, _ := doJSON(t, mux(h), http.MethodGet, "/executions/nope", headers, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("= %d, want 400", code)
		}
	})
}

func TestListPagination(t *testing.T) {
	st := &fakeExecutionStore{}
	h := &ExecutionsHandler{Store: st, Publisher: &fakePublisher{}}
	headers := map[string]string{"X-Tenant-ID": testTenantID}

	t.Run("clamps limit", func(t *testing.T) {
		code, raw := doJSON(t, http.HandlerFunc(h.List), http.MethodGet, "/executions?limit=5000", headers, nil)
		if code != http.StatusOK {
			t.Fatalf("= %d (%s), want 200", code, raw)
		}
		if st.lastList.Limit != MaxListSize {
			t.Fatalf("limit = %d, want clamp %d", st.lastList.Limit, MaxListSize)
		}
	})

	for name, target := range map[string]string{
		"bad limit":  "/executions?limit=abc",
		"zero limit": "/executions?limit=0",
		"bad offset": "/executions?offset=-1",
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := doJSON(t, http.HandlerFunc(h.List), http.MethodGet, target, headers, nil)
			if code != http.StatusBadRequest {
				t.Fatalf("= %d, want 400", code)
			}
		})
	}
}
