package dispatcher

import (
	"context"
	"log"
	"time"

	"github.com/ishaan-jindal/runnix/internal/store/storedb"
)

// reapStderr is recorded on executions the reaper fails.
const reapStderr = "reaped: dispatcher lost while running"

// Reaper fails executions stuck "running" because their dispatcher died
// between claiming and finishing. StaleAfter must comfortably exceed the
// maximum timeout_s (60s) plus sandbox startup; the guarded UPDATE makes
// concurrent reapers and late JetStream redeliveries harmless.
type Reaper struct {
	Store      ReapStore
	Interval   time.Duration
	StaleAfter time.Duration
	Logger     *log.Logger
}

// ReapStore is the store surface the reaper needs. *storedb.Queries
// satisfies it; unit tests supply fakes.
type ReapStore interface {
	ReapStaleExecutions(ctx context.Context, arg storedb.ReapStaleExecutionsParams) (int64, error)
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
}
