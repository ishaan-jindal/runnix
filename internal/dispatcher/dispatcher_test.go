package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/ishaan-jindal/runnix/internal/webhooks"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	rnats "github.com/ishaan-jindal/runnix/internal/nats"
)

const (
	testExecID   = "22222222-2222-2222-2222-222222222222"
	testTenantID = "11111111-1111-1111-1111-111111111111"
)

func mustPgUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := store.ParsePg(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// claimedRow is what MarkExecutionRunning returns: the DB row the dispatcher
// runs, independent of what the submit message carried.
func claimedRow(t *testing.T) ClaimedExecution {
	t.Helper()
	return ClaimedExecution{
		ID:       mustPgUUID(t, testExecID),
		TenantID: mustPgUUID(t, testTenantID),
		Language: "python",
		Source:   "print(1)",
		TimeoutS: 2,
	}
}

type fakeStore struct {
	claim      ClaimedExecution
	claimErr   error
	finished   []storedb.FinishExecutionParams
	finishErrs []error // consumed one per FinishExecution call; empty = success
	requeued   []pgtype.UUID
	requeueErr error
}

func (f *fakeStore) MarkExecutionRunning(context.Context, pgtype.UUID) (ClaimedExecution, error) {
	return f.claim, f.claimErr
}

func (f *fakeStore) FinishExecution(_ context.Context, arg storedb.FinishExecutionParams) error {
	f.finished = append(f.finished, arg)
	if len(f.finishErrs) > 0 {
		err := f.finishErrs[0]
		f.finishErrs = f.finishErrs[1:]
		return err
	}
	return nil
}

func (f *fakeStore) RequeueExecution(_ context.Context, id pgtype.UUID) (int64, error) {
	f.requeued = append(f.requeued, id)
	return 1, f.requeueErr
}

type fakeRunner struct {
	res  RunResult
	err  error
	runs int
	last ClaimedExecution
}

func (f *fakeRunner) Run(_ context.Context, exec ClaimedExecution) (RunResult, error) {
	f.runs++
	f.last = exec
	return f.res, f.err
}

type fakePub struct {
	msgs []rnats.ResultMessage
	err  error
}

func (f *fakePub) PublishResult(_ context.Context, m rnats.ResultMessage) error {
	f.msgs = append(f.msgs, m)
	return f.err
}

type fakeWebhooks struct {
	events []webhooks.ExecutionEvent
	urls   []string
	err    error
}

func (f *fakeWebhooks) Deliver(_ context.Context, url string, evt webhooks.ExecutionEvent) error {
	f.urls = append(f.urls, url)
	f.events = append(f.events, evt)
	return f.err
}

func newDispatcher(t *testing.T, st *fakeStore, run *fakeRunner, pub *fakePub) *Dispatcher {
	t.Helper()
	return &Dispatcher{Store: st, Runner: run, Publisher: pub, Logger: testLogger(t)}
}

func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(&testWriter{t: t}, "test ", 0)
}

type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// submitMsg carries references only; source/stdin/timeout/webhook live in the
// row claimed from Postgres.
func submitMsg() rnats.SubmitMessage {
	return rnats.SubmitMessage{
		ExecutionID: testExecID,
		TenantID:    testTenantID,
		Language:    "python",
	}
}

func TestHandleSuccess(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	pub := &fakePub{}
	d := newDispatcher(t, st, run, pub)

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v", err)
	}
	if run.last.Source != "print(1)" || run.last.TimeoutS != 2 {
		t.Fatalf("runner got %+v, want the claimed row", run.last)
	}
	if len(st.finished) != 1 {
		t.Fatalf("finished = %d, want 1", len(st.finished))
	}
	f := st.finished[0]
	if f.Status != "succeeded" || f.Stdout != "1\n" || !f.ExitCode.Valid || f.ExitCode.Int32 != 0 {
		t.Fatalf("finish = %+v", f)
	}
	if len(pub.msgs) != 1 || pub.msgs[0].Status != "succeeded" {
		t.Fatalf("publish = %+v", pub.msgs)
	}
}

func TestHandleDuplicateClaimSkipped(t *testing.T) {
	st := &fakeStore{claimErr: pgx.ErrNoRows}
	run := &fakeRunner{}
	d := newDispatcher(t, st, run, &fakePub{})

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v", err)
	}
	if run.runs != 0 {
		t.Fatalf("runner ran %d times, want 0", run.runs)
	}
	if len(st.finished) != 0 {
		t.Fatalf("finished = %d, want 0", len(st.finished))
	}
}

func TestHandleClaimErrorNaks(t *testing.T) {
	st := &fakeStore{claimErr: errors.New("db down")}
	run := &fakeRunner{}
	d := newDispatcher(t, st, run, &fakePub{})

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err == nil {
		t.Fatal("handle = nil, want error to trigger nak")
	}
	if run.runs != 0 {
		t.Fatalf("runner ran %d times, want 0", run.runs)
	}
}

func TestHandlePoisonMessages(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	d := newDispatcher(t, st, &fakeRunner{}, &fakePub{})
	ctx := context.Background()

	for name, raw := range map[string]string{
		"bad json":     "{not json",
		"empty id":     `{"execution_id":"","language":"python"}`,
		"bad language": `{"execution_id":"` + testExecID + `","language":"haskell"}`,
		"bad uuid":     `{"execution_id":"nope","language":"python"}`,
		"no language":  `{"execution_id":"` + testExecID + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := d.handle(ctx, []byte(raw)); err != nil {
				t.Fatalf("handle = %v, want nil (ack+drop)", err)
			}
			if len(st.finished) != 0 {
				t.Fatalf("finished = %d, want 0", len(st.finished))
			}
		})
	}
}

func TestHandleTimeout(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	run := &fakeRunner{res: RunResult{ExitCode: 137, TimedOut: true, Stdout: "partial"}}
	pub := &fakePub{}
	d := newDispatcher(t, st, run, pub)

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v", err)
	}
	f := st.finished[0]
	if f.Status != "timeout" {
		t.Fatalf("status = %q, want timeout", f.Status)
	}
	if f.ExitCode.Valid {
		t.Fatalf("exit_code should be null on timeout, got %v", f.ExitCode)
	}
}

func TestHandleOOM(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	run := &fakeRunner{res: RunResult{ExitCode: 137, OOMKilled: true, Stderr: ""}}
	d := newDispatcher(t, st, run, &fakePub{})

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v", err)
	}
	f := st.finished[0]
	if f.Status != "failed" || !strings.Contains(f.Stderr, "out of memory") {
		t.Fatalf("finish = %+v", f)
	}
}

func TestHandleNonZeroExit(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	run := &fakeRunner{res: RunResult{ExitCode: 1, Stderr: "boom"}}
	d := newDispatcher(t, st, run, &fakePub{})

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v", err)
	}
	f := st.finished[0]
	if f.Status != "failed" || f.ExitCode.Int32 != 1 || f.Stderr != "boom" {
		t.Fatalf("finish = %+v", f)
	}
}

// TestHandleRunnerErrors covers the two error classes: a pre-start failure
// never ran code and is requeued for redelivery; a post-start failure may
// have run and must be recorded and acked instead of re-run.
func TestHandleRunnerErrors(t *testing.T) {
	tests := []struct {
		name         string
		runErr       error
		wantHandle   bool // handle returns an error (NAK)
		wantRequeued int
		wantFinished int
		wantStatus   string
		wantWebhooks int
	}{
		{
			name:         "pre-start failure is requeued",
			runErr:       fmt.Errorf("%w: start container: daemon down", errNotStarted),
			wantHandle:   true,
			wantRequeued: 1,
		},
		{
			name:         "post-start failure is recorded and acked",
			runErr:       errors.New("read logs: broken pipe"),
			wantFinished: 1,
			wantStatus:   "failed",
			wantWebhooks: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{claim: claimedRow(t)}
			st.claim.WebhookUrl = pgtype.Text{String: "https://hooks.example.com/done", Valid: true}
			run := &fakeRunner{err: tc.runErr}
			hooks := &fakeWebhooks{}
			d := newDispatcher(t, st, run, &fakePub{})
			d.Webhooks = hooks

			err := d.handle(context.Background(), mustJSON(t, submitMsg()))
			if (err != nil) != tc.wantHandle {
				t.Fatalf("handle = %v, want error %v", err, tc.wantHandle)
			}
			if run.runs != 1 {
				t.Fatalf("runner ran %d times, want 1", run.runs)
			}
			if len(st.requeued) != tc.wantRequeued {
				t.Fatalf("requeued = %d, want %d", len(st.requeued), tc.wantRequeued)
			}
			if tc.wantRequeued > 0 && st.requeued[0] != st.claim.ID {
				t.Fatalf("requeued id = %x, want %x", st.requeued[0].Bytes, st.claim.ID.Bytes)
			}
			if len(st.finished) != tc.wantFinished {
				t.Fatalf("finished = %d, want %d", len(st.finished), tc.wantFinished)
			}
			if tc.wantFinished > 0 {
				f := st.finished[0]
				if f.Status != tc.wantStatus || !strings.Contains(f.Stderr, "runner error") {
					t.Fatalf("finish = %+v", f)
				}
			}
			if len(hooks.events) != tc.wantWebhooks {
				t.Fatalf("webhook deliveries = %d, want %d", len(hooks.events), tc.wantWebhooks)
			}
		})
	}
}

func TestHandleFinishRetries(t *testing.T) {
	st := &fakeStore{
		claim:      claimedRow(t),
		finishErrs: []error{errors.New("tx reset"), errors.New("tx reset")},
	}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	pub := &fakePub{}
	d := newDispatcher(t, st, run, pub)

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v, want the retried write to land", err)
	}
	if len(st.finished) != finishAttempts {
		t.Fatalf("finish attempts = %d, want %d", len(st.finished), finishAttempts)
	}
	if len(pub.msgs) != 1 {
		t.Fatalf("result publishes = %d, want 1", len(pub.msgs))
	}
}

func TestHandleFinishGivesUp(t *testing.T) {
	st := &fakeStore{
		claim:      claimedRow(t),
		finishErrs: []error{errors.New("tx reset"), errors.New("tx reset"), errors.New("tx reset")},
	}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	pub := &fakePub{}
	d := newDispatcher(t, st, run, pub)

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err == nil {
		t.Fatal("handle = nil, want the write failure surfaced")
	}
	if len(st.finished) != finishAttempts {
		t.Fatalf("finish attempts = %d, want %d", len(st.finished), finishAttempts)
	}
	if len(pub.msgs) != 0 {
		t.Fatalf("result publishes = %d, want 0 after a failed write", len(pub.msgs))
	}
}

func TestHandleFinishStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := &fakeStore{
		claim:      claimedRow(t),
		finishErrs: []error{errors.New("tx reset"), errors.New("tx reset"), errors.New("tx reset")},
	}
	d := newDispatcher(t, st, &fakeRunner{res: RunResult{ExitCode: 0}}, &fakePub{})

	start := time.Now()
	if err := d.handle(ctx, mustJSON(t, submitMsg())); err == nil {
		t.Fatal("handle = nil, want the write failure surfaced")
	}
	// The loop must not sleep out its backoff once ctx is done.
	if took := time.Since(start); took > finishBackoff {
		t.Fatalf("handle took %s after ctx cancellation, want <= %s", took, finishBackoff)
	}
	if len(st.finished) != 1 {
		t.Fatalf("finish attempts = %d, want 1", len(st.finished))
	}
}

func TestHandleDeliversWebhook(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	st.claim.WebhookUrl = pgtype.Text{String: "https://hooks.example.com/done", Valid: true}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	hooks := &fakeWebhooks{}
	d := newDispatcher(t, st, run, &fakePub{})
	d.Webhooks = hooks

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v", err)
	}
	if len(hooks.events) != 1 || len(hooks.urls) != 1 {
		t.Fatalf("webhook deliveries = %d, want 1", len(hooks.events))
	}
	if hooks.urls[0] != st.claim.WebhookUrl.String {
		t.Fatalf("url = %q, want %q", hooks.urls[0], st.claim.WebhookUrl.String)
	}
	evt := hooks.events[0]
	if evt.Event != webhooks.EventExecutionCompleted || evt.Status != "succeeded" ||
		evt.ExecutionID != testExecID || evt.ExitCode == nil || *evt.ExitCode != 0 {
		t.Fatalf("event = %+v", evt)
	}
}

func TestHandleWebhookOnPostStartRunnerError(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	st.claim.WebhookUrl = pgtype.Text{String: "https://hooks.example.com/done", Valid: true}
	run := &fakeRunner{err: errors.New("read logs: broken pipe")}
	hooks := &fakeWebhooks{}
	d := newDispatcher(t, st, run, &fakePub{})
	d.Webhooks = hooks

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v, want nil (post-start error is terminal)", err)
	}
	if len(hooks.events) != 1 || hooks.events[0].Status != "failed" || hooks.events[0].ExitCode != nil {
		t.Fatalf("events = %+v, want one failed with null exit_code", hooks.events)
	}
}

func TestHandleSkipsWebhookWithoutURL(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	hooks := &fakeWebhooks{}
	d := newDispatcher(t, st, run, &fakePub{})
	d.Webhooks = hooks

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v", err)
	}
	if len(hooks.events) != 0 {
		t.Fatalf("webhook deliveries = %d, want 0 (no webhook_url)", len(hooks.events))
	}
}

func TestHandleWebhookFailureTolerated(t *testing.T) {
	st := &fakeStore{claim: claimedRow(t)}
	st.claim.WebhookUrl = pgtype.Text{String: "https://hooks.example.com/done", Valid: true}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	pub := &fakePub{}
	hooks := &fakeWebhooks{err: errors.New("receiver down")}
	d := newDispatcher(t, st, run, pub)
	d.Webhooks = hooks

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v, want nil (delivery is best-effort)", err)
	}
	if len(st.finished) != 1 || st.finished[0].Status != "succeeded" {
		t.Fatalf("finished = %+v, want the run to stand", st.finished)
	}
	if len(pub.msgs) != 1 {
		t.Fatalf("result publishes = %d, want 1", len(pub.msgs))
	}
}

func mustJSON(t *testing.T, m rnats.SubmitMessage) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
