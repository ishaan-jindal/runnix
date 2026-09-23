package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ishaan-jindal/runnix/internal/quotas"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
)

// fakeQuota returns canned decisions/errors for both quota paths and counts
// calls. The handler's submit path uses CheckAndCreate; CheckSubmit stays
// for the standalone read-path tests.
type fakeQuota struct {
	decision  quotas.Decision
	err       error
	checks    int
	creates   int
	createRow storedb.CreateExecutionRow
}

func (f *fakeQuota) CheckSubmit(_ context.Context, _ string) (quotas.Decision, error) {
	f.checks++
	return f.decision, f.err
}

func (f *fakeQuota) CheckAndCreate(_ context.Context, _ string, _ storedb.CreateExecutionParams) (storedb.CreateExecutionRow, quotas.Decision, error) {
	f.creates++
	if f.err != nil {
		return storedb.CreateExecutionRow{}, quotas.Decision{}, f.err
	}
	return f.createRow, f.decision, nil
}

// doQuotaCreate posts a minimal submit and returns status, headers, body.
func doQuotaCreate(t *testing.T, h *ExecutionsHandler, quota quotas.Checker, enabled bool) (int, http.Header, []byte) {
	t.Helper()
	h.Quota, h.QuotasEnabled = quota, enabled
	body := strings.NewReader(`{"language":"python","source":"print(1)"}`)
	req := httptest.NewRequest(http.MethodPost, "/executions", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", testTenantID)
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	return rec.Code, rec.Header(), rec.Body.Bytes()
}

func TestCreateQuota(t *testing.T) {
	allow := quotas.Decision{Allowed: true}
	deny := quotas.Decision{RetryAfterSec: 60, Reason: "hourly submit limit exceeded"}

	for name, tc := range map[string]struct {
		quota          *fakeQuota
		enabled        bool
		want           int
		wantStoreCalls int
	}{
		// Allow inserts inside the quota transaction: the store fallback
		// must not run a second insert.
		"allow proceeds to 202": {&fakeQuota{decision: allow}, true, http.StatusAccepted, 0},
		// Checker errors fail open through the plain store path.
		"checker error fails open":     {&fakeQuota{err: errors.New("quota db down")}, true, http.StatusAccepted, 1},
		"disabled skips denying quota": {&fakeQuota{decision: deny}, false, http.StatusAccepted, 1},
		"nil quota skips checks":       {nil, true, http.StatusAccepted, 1},
	} {
		t.Run(name, func(t *testing.T) {
			st := &fakeExecutionStore{createRow: queuedRow(t)}
			h := &ExecutionsHandler{Store: st, Publisher: &fakePublisher{}}
			var q quotas.Checker
			if tc.quota != nil {
				tc.quota.createRow = queuedRow(t)
				q = tc.quota
			}
			code, _, raw := doQuotaCreate(t, h, q, tc.enabled)
			if code != tc.want {
				t.Fatalf("= %d (%s), want %d", code, raw, tc.want)
			}
			if tc.enabled && tc.quota != nil && tc.quota.creates != 1 {
				t.Fatalf("atomic checks = %d, want 1", tc.quota.creates)
			}
			if !tc.enabled && tc.quota != nil && (tc.quota.creates != 0 || tc.quota.checks != 0) {
				t.Fatalf("disabled handler must not check quotas, got creates=%d checks=%d", tc.quota.creates, tc.quota.checks)
			}
			if st.createCalls != tc.wantStoreCalls {
				t.Fatalf("store inserts = %d, want %d", st.createCalls, tc.wantStoreCalls)
			}
		})
	}
}

func TestCreateQuotaDeny(t *testing.T) {
	st := &fakeExecutionStore{createRow: queuedRow(t)}
	h := &ExecutionsHandler{Store: st, Publisher: &fakePublisher{}}
	q := &fakeQuota{decision: quotas.Decision{RetryAfterSec: 60, Reason: "hourly submit limit exceeded"}}
	code, header, raw := doQuotaCreate(t, h, q, true)
	if code != http.StatusTooManyRequests {
		t.Fatalf("= %d (%s), want 429", code, raw)
	}
	if got := header.Get("Retry-After"); got != strconv.Itoa(60) {
		t.Fatalf("Retry-After = %q, want %q", got, "60")
	}
	var envelope map[string]string
	decodeRaw(t, raw, &envelope)
	if envelope["error"] != "hourly submit limit exceeded" {
		t.Fatalf("error envelope = %v, want quota reason", envelope)
	}
	// A deny must not insert: the atomic path rolls back before the write.
	if st.createCalls != 0 {
		t.Fatalf("store inserts = %d, want 0 on deny", st.createCalls)
	}
	if q.creates != 1 {
		t.Fatalf("atomic checks = %d, want 1", q.creates)
	}
}
