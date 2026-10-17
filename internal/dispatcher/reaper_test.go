package dispatcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeReapStore struct {
	calls []storedb.ReapStaleExecutionsParams
	n     int64
	err   error

	rows      []*fakeQueuedRow
	promote   map[pgtype.UUID]bool // ids a racing claim flips queued->running before the UPDATE
	listSecs  []int32
	markCalls []storedb.MarkStaleQueuedFailedParams
}

// fakeQueuedRow models a row for the stale-queued sweep; age is how long ago
// created_at was.
type fakeQueuedRow struct {
	id     pgtype.UUID
	status string
	age    time.Duration
	stderr string
}

func (f *fakeReapStore) ReapStaleExecutions(_ context.Context, arg storedb.ReapStaleExecutionsParams) (int64, error) {
	f.calls = append(f.calls, arg)
	return f.n, f.err
}

// ListStaleQueuedExecutions mirrors the SQL: queued rows older than staleSecs.
func (f *fakeReapStore) ListStaleQueuedExecutions(_ context.Context, staleSecs int32) ([]pgtype.UUID, error) {
	f.listSecs = append(f.listSecs, staleSecs)
	var ids []pgtype.UUID
	for _, row := range f.rows {
		if row.status == "queued" && row.age > time.Duration(staleSecs)*time.Second {
			ids = append(ids, row.id)
		}
	}
	return ids, nil
}

// MarkStaleQueuedFailed mirrors the SQL status guard; promote simulates a
// claim (queued->running) racing in between the SELECT and the UPDATE.
func (f *fakeReapStore) MarkStaleQueuedFailed(_ context.Context, arg storedb.MarkStaleQueuedFailedParams) (int64, error) {
	f.markCalls = append(f.markCalls, arg)
	for _, row := range f.rows {
		if row.id != arg.ID {
			continue
		}
		if f.promote[arg.ID] {
			row.status = "running"
		}
		if row.status != "queued" {
			return 0, nil
		}
		row.status = "failed"
		row.stderr = arg.Stderr
		return 1, nil
	}
	return 0, nil
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

func TestReaperMarksStaleQueuedFailed(t *testing.T) {
	ids := []pgtype.UUID{
		{Bytes: [16]byte{1}, Valid: true},
		{Bytes: [16]byte{2}, Valid: true},
		{Bytes: [16]byte{3}, Valid: true},
		{Bytes: [16]byte{4}, Valid: true},
	}
	tests := []struct {
		name    string
		rows    []*fakeQueuedRow
		promote map[pgtype.UUID]bool
		want    map[pgtype.UUID]string
	}{
		{
			name: "stale queued is failed",
			rows: []*fakeQueuedRow{{id: ids[0], status: "queued", age: 11 * time.Minute}},
			want: map[pgtype.UUID]string{ids[0]: "failed"},
		},
		{
			name: "fresh queued is left alone",
			rows: []*fakeQueuedRow{{id: ids[1], status: "queued", age: time.Minute}},
			want: map[pgtype.UUID]string{ids[1]: "queued"},
		},
		{
			name: "running is left alone",
			rows: []*fakeQueuedRow{{id: ids[2], status: "running", age: 11 * time.Minute}},
			want: map[pgtype.UUID]string{ids[2]: "running"},
		},
		{
			name:    "row claimed between select and update is left alone",
			rows:    []*fakeQueuedRow{{id: ids[3], status: "queued", age: 11 * time.Minute}},
			promote: map[pgtype.UUID]bool{ids[3]: true},
			want:    map[pgtype.UUID]string{ids[3]: "running"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeReapStore{rows: tc.rows, promote: tc.promote}
			r := &Reaper{Store: st, StaleAfter: 5 * time.Minute, StaleQueuedAfter: 10 * time.Minute, Logger: testLogger(t)}

			r.reapOnce(context.Background())

			if len(st.listSecs) != 1 || st.listSecs[0] != 600 {
				t.Fatalf("list stale_secs = %v, want [600]", st.listSecs)
			}
			for _, row := range tc.rows {
				if row.status != tc.want[row.id] {
					t.Fatalf("row %x status = %q, want %q", row.id.Bytes, row.status, tc.want[row.id])
				}
				if tc.want[row.id] == "failed" && row.stderr != staleQueuedStderr {
					t.Fatalf("stderr = %q, want %q", row.stderr, staleQueuedStderr)
				}
			}
		})
	}
}

func TestReaperStaleQueuedDisabledWhenUnset(t *testing.T) {
	st := &fakeReapStore{rows: []*fakeQueuedRow{{id: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, status: "queued", age: time.Hour}}}
	r := &Reaper{Store: st, StaleAfter: time.Minute, Logger: testLogger(t)} // StaleQueuedAfter unset

	r.reapOnce(context.Background())

	if len(st.listSecs) != 0 {
		t.Fatalf("queued sweep ran with threshold 0: %v", st.listSecs)
	}
	if st.rows[0].status != "queued" {
		t.Fatalf("queued row was failed with threshold unset")
	}
}
