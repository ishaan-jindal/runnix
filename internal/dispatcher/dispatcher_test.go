package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/ishaan-jindal/runnix/internal/webhooks"
	"github.com/jackc/pgx/v5/pgtype"

	rnats "github.com/ishaan-jindal/runnix/internal/nats"
)

const testExecID = "22222222-2222-2222-2222-222222222222"

type fakeStore struct {
	claimed   int64
	claimErr  error
	finished  []storedb.FinishExecutionParams
	finishErr error
}

func (f *fakeStore) MarkExecutionRunning(context.Context, pgtype.UUID) (int64, error) {
	return f.claimed, f.claimErr
}

func (f *fakeStore) FinishExecution(_ context.Context, arg storedb.FinishExecutionParams) error {
	f.finished = append(f.finished, arg)
	return f.finishErr
}

type fakeRunner struct {
	res  RunResult
	err  error
	runs int
}

func (f *fakeRunner) EnsureImage(context.Context) error { return nil }
func (f *fakeRunner) Sweep(context.Context) error       { return nil }
func (f *fakeRunner) Run(_ context.Context, _ rnats.SubmitMessage) (RunResult, error) {
	f.runs++
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

func submitMsg() rnats.SubmitMessage {
	return rnats.SubmitMessage{
		ExecutionID: testExecID,
		TenantID:    "11111111-1111-1111-1111-111111111111",
		Language:    "python",
		Source:      "print(1)",
		TimeoutS:    2,
	}
}

func TestHandleSuccess(t *testing.T) {
	st := &fakeStore{claimed: 1}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	pub := &fakePub{}
	d := newDispatcher(t, st, run, pub)

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err != nil {
		t.Fatalf("handle = %v", err)
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
	st := &fakeStore{claimed: 0}
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

func TestHandlePoisonMessages(t *testing.T) {
	st := &fakeStore{claimed: 1}
	d := newDispatcher(t, st, &fakeRunner{}, &fakePub{})
	ctx := context.Background()

	for name, raw := range map[string]string{
		"bad json":     "{not json",
		"empty id":     `{"execution_id":"","language":"python","source":"x"}`,
		"bad language": `{"execution_id":"` + testExecID + `","language":"ruby","source":"x"}`,
		"bad uuid":     `{"execution_id":"nope","language":"python","source":"x"}`,
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
	st := &fakeStore{claimed: 1}
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
	st := &fakeStore{claimed: 1}
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
	st := &fakeStore{claimed: 1}
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

func TestHandleRunnerErrorNaks(t *testing.T) {
	st := &fakeStore{claimed: 1}
	run := &fakeRunner{err: context.DeadlineExceeded}
	d := newDispatcher(t, st, run, &fakePub{})

	if err := d.handle(context.Background(), mustJSON(t, submitMsg())); err == nil {
		t.Fatal("handle = nil, want error to trigger nak")
	}
	// Execution must not be left stuck "running".
	if len(st.finished) != 1 || st.finished[0].Status != "failed" {
		t.Fatalf("finished = %+v, want one failed", st.finished)
	}
}

func TestHandleDeliversWebhook(t *testing.T) {
	st := &fakeStore{claimed: 1}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	hooks := &fakeWebhooks{}
	d := newDispatcher(t, st, run, &fakePub{})
	d.Webhooks = hooks

	msg := submitMsg()
	msg.WebhookURL = "https://hooks.example.com/done"
	if err := d.handle(context.Background(), mustJSON(t, msg)); err != nil {
		t.Fatalf("handle = %v", err)
	}
	if len(hooks.events) != 1 || len(hooks.urls) != 1 {
		t.Fatalf("webhook deliveries = %d, want 1", len(hooks.events))
	}
	if hooks.urls[0] != msg.WebhookURL {
		t.Fatalf("url = %q, want %q", hooks.urls[0], msg.WebhookURL)
	}
	evt := hooks.events[0]
	if evt.Event != webhooks.EventExecutionCompleted || evt.Status != "succeeded" ||
		evt.ExecutionID != testExecID || evt.ExitCode == nil || *evt.ExitCode != 0 {
		t.Fatalf("event = %+v", evt)
	}
}

func TestHandleWebhookOnRunnerError(t *testing.T) {
	st := &fakeStore{claimed: 1}
	run := &fakeRunner{err: context.DeadlineExceeded}
	hooks := &fakeWebhooks{}
	d := newDispatcher(t, st, run, &fakePub{})
	d.Webhooks = hooks

	msg := submitMsg()
	msg.WebhookURL = "https://hooks.example.com/done"
	if err := d.handle(context.Background(), mustJSON(t, msg)); err == nil {
		t.Fatal("handle = nil, want error to trigger nak")
	}
	if len(hooks.events) != 1 || hooks.events[0].Status != "failed" || hooks.events[0].ExitCode != nil {
		t.Fatalf("events = %+v, want one failed with null exit_code", hooks.events)
	}
}

func TestHandleSkipsWebhookWithoutURL(t *testing.T) {
	st := &fakeStore{claimed: 1}
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
	st := &fakeStore{claimed: 1}
	run := &fakeRunner{res: RunResult{ExitCode: 0, Stdout: "1\n"}}
	pub := &fakePub{}
	hooks := &fakeWebhooks{err: errors.New("receiver down")}
	d := newDispatcher(t, st, run, pub)
	d.Webhooks = hooks

	msg := submitMsg()
	msg.WebhookURL = "https://hooks.example.com/done"
	if err := d.handle(context.Background(), mustJSON(t, msg)); err != nil {
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
