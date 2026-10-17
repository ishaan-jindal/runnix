package dispatcher

import (
	"context"
	"log"
	"time"

	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgtype"
)

// reapStderr is recorded on executions the reaper fails.
const reapStderr = "reaped: dispatcher lost while running"

// staleQueuedStderr is recorded on queued executions the reaper fails.
const staleQueuedStderr = "stale_queued"

// Reaper fails executions stuck "running" because their dispatcher died
// between claiming and finishing, and executions stuck "queued" because their
// submit message never reached a worker (e.g. JetStream gave up at
// MaxDeliver). StaleAfter must comfortably exceed the maximum timeout_s (60s)
// plus sandbox startup; StaleQueuedAfter must exceed the consumer AckWait
// (90s) times MaxDeliver (5) plus a margin, so a message still being
// redelivered is not failed out from under its own retry. Both sweeps guard
// on status, so concurrent reapers and late JetStream redeliveries are
// harmless.
type Reaper struct {
	Store            ReapStore
	Interval         time.Duration
	StaleAfter       time.Duration
	StaleQueuedAfter time.Duration
	Logger           *log.Logger
}

// ReapStore is the store surface the reaper needs. *storedb.Queries
// satisfies it; unit tests supply fakes.
type ReapStore interface {
	ReapStaleExecutions(ctx context.Context, arg storedb.ReapStaleExecutionsParams) (int64, error)
	ListStaleQueuedExecutions(ctx context.Context, staleSecs int32) ([]pgtype.UUID, error)
	MarkStaleQueuedFailed(ctx context.Context, arg storedb.MarkStaleQueuedFailedParams) (int64, error)
}

// Run reaps on every Interval until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reapOnce(ctx)
		}
	}
}

func (r *Reaper) reapOnce(ctx context.Context) {
	n, err := r.Store.ReapStaleExecutions(ctx, storedb.ReapStaleExecutionsParams{
		Stderr:    reapStderr,
		StaleSecs: int32(r.StaleAfter / time.Second),
	})
	if err != nil {
		r.Logger.Printf("reap stale executions: %v", err)
		return
	}
	if n > 0 {
		r.Logger.Printf("reaped %d stale running execution(s)", n)
	}
	r.reapStaleQueued(ctx)
}

// reapStaleQueued fails queued executions whose submit message never reached
// a worker, freeing their quota slot. One SELECT per sweep, then one guarded
// UPDATE per stale row; a row claimed (queued->running) between the two is
// left alone.
func (r *Reaper) reapStaleQueued(ctx context.Context) {
	if r.StaleQueuedAfter <= 0 {
		return // unset means disabled, not "fail every queued row"
	}
	ids, err := r.Store.ListStaleQueuedExecutions(ctx, int32(r.StaleQueuedAfter/time.Second))
	if err != nil {
		r.Logger.Printf("list stale queued executions: %v", err)
		return
	}
	failed := int64(0)
	for _, id := range ids {
		n, err := r.Store.MarkStaleQueuedFailed(ctx, storedb.MarkStaleQueuedFailedParams{
			ID:     id,
			Stderr: staleQueuedStderr,
		})
		if err != nil {
			r.Logger.Printf("fail stale queued execution %x: %v", id.Bytes, err)
			continue
		}
		failed += n
	}
	if failed > 0 {
		r.Logger.Printf("reaped %d stale queued execution(s)", failed)
	}
}
