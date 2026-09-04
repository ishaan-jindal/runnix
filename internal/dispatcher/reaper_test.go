package dispatcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ishaan-jindal/runnix/internal/store/storedb"
)

type fakeReapStore struct {
	calls []storedb.ReapStaleExecutionsParams
	n     int64
	err   error
}

func (f *fakeReapStore) ReapStaleExecutions(_ context.Context, arg storedb.ReapStaleExecutionsParams) (int64, error) {
	f.calls = append(f.calls, arg)
	return f.n, f.err
}

func TestReaperReapOnce(t *testing.T) {
	st := &fakeReapStore{n: 2}
	r := &Reaper{Store: st, StaleAfter: 5 * time.Minute, Logger: testLogger(t)}

	r.reapOnce(context.Background())

	if len(st.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(st.calls))
	}
	arg := st.calls[0]
	if arg.StaleSecs != 300 {
		t.Fatalf("stale_secs = %d, want 300", arg.StaleSecs)
	}
	if arg.Stderr != reapStderr {
		t.Fatalf("stderr = %q, want %q", arg.Stderr, reapStderr)
	}
}

func TestReaperToleratesStoreError(t *testing.T) {
	st := &fakeReapStore{err: errors.New("db down")}
	r := &Reaper{Store: st, StaleAfter: time.Minute, Logger: testLogger(t)}

	r.reapOnce(context.Background()) // must log and keep going, not panic
}

func TestReaperRunStopsOnCancel(t *testing.T) {
	st := &fakeReapStore{}
	r := &Reaper{Store: st, Interval: 10 * time.Millisecond, StaleAfter: time.Minute, Logger: testLogger(t)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Reaper.Run did not stop on cancel")
	}
	if len(st.calls) == 0 {
		t.Fatal("expected at least one reap call while running")
	}
}
