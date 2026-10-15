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

type fakeQuota struct {
	decision  quotas.Decision
	err       error
	creates   int
	createRow storedb.CreateExecutionRow
}

func (f *fakeQuota) CheckAndCreate(_ context.Context, _ string, _ storedb.CreateExecutionParams) (storedb.CreateExecutionRow, quotas.Decision, error) {
	f.creates++
	if f.err != nil {
		return storedb.CreateExecutionRow{}, quotas.Decision{}, f.err
	}
	return f.createRow, f.decision, nil
}

func doQuotaCreate(t *testing.T, h *ExecutionsHandler, quota quotas.Checker) (int, http.Header, []byte) {
	t.Helper()
	h.Quota = quota
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

	for name, tc := range map[string]struct {
		quota          *fakeQuota
		want           int
		wantStoreCalls int
	}{
		"allow proceeds to 202":    {&fakeQuota{decision: allow}, http.StatusAccepted, 0},
		"checker error fails open": {&fakeQuota{err: errors.New("quota db down")}, http.StatusAccepted, 1},
		"nil quota skips checks":   {nil, http.StatusAccepted, 1},
	} {
		t.Run(name, func(t *testing.T) {
			st := &fakeExecutionStore{createRow: queuedRow(t)}
			h := &ExecutionsHandler{Store: st, Publisher: &fakePublisher{}}
			var q quotas.Checker
			if tc.quota != nil {
				tc.quota.createRow = queuedRow(t)
				q = tc.quota
			}
			code, _, raw := doQuotaCreate(t, h, q)
			if code != tc.want {
				t.Fatalf("= %d (%s), want %d", code, raw, tc.want)
			}
			if tc.quota != nil && tc.quota.creates != 1 {
				t.Fatalf("atomic checks = %d, want 1", tc.quota.creates)
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
	code, header, raw := doQuotaCreate(t, h, q)
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
	if st.createCalls != 0 {
		t.Fatalf("store inserts = %d, want 0 on deny", st.createCalls)
	}
	if q.creates != 1 {
		t.Fatalf("atomic checks = %d, want 1", q.creates)
	}
}
