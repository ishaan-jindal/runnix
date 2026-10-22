package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/ishaan-jindal/runnix/internal/executions"
	rnats "github.com/ishaan-jindal/runnix/internal/nats"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/ishaan-jindal/runnix/internal/webhooks"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nats-io/nats.go/jetstream"
)

// Consumer settings (see also the plan): redelivery is safe because the DB
// claim guard (queued->running) is the dedupe; MaxDeliver bounds poison loops.
// AckWait exceeds the 60s max timeout_s so legit long runs are not
// redelivered mid-flight; a dead worker's message waits out the remainder.
const (
	consumerDurable    = "dispatcher"
	maxDeliver         = 5
	ackWait            = 90 * time.Second
	nakDelay           = 5 * time.Second
	poisonStderrPrefix = "submit message discarded"

	// Result writes retry in place: losing a completed run is worse than a
	// few extra milliseconds of guard on the single UPDATE that persists it.
	finishAttempts = 3
	finishBackoff  = 100 * time.Millisecond
)

// ClaimedExecution is the row the dispatcher claimed. The queue message only
// names it; Postgres is the source of truth for what runs. It aliases the
// sqlc-generated row so *storedb.Queries satisfies Store without an adapter.
type ClaimedExecution = storedb.MarkExecutionRunningRow

// Store is the Postgres surface the dispatcher needs. *storedb.Queries
// satisfies it; unit tests supply fakes.
type Store interface {
	MarkExecutionRunning(ctx context.Context, id pgtype.UUID) (ClaimedExecution, error)
	FinishExecution(ctx context.Context, arg storedb.FinishExecutionParams) error
	RequeueExecution(ctx context.Context, id pgtype.UUID) (int64, error)
}

// ResultPublisher publishes result summaries. *rnats.Client satisfies it.
type ResultPublisher interface {
	PublishResult(ctx context.Context, msg rnats.ResultMessage) error
}

// WebhookSender delivers completion webhooks. *webhooks.Deliverer satisfies
// it; tests supply fakes.
type WebhookSender interface {
	Deliver(ctx context.Context, url string, evt webhooks.ExecutionEvent) error
}

// Dispatcher pulls submit messages and runs each execution exactly once.
// Reaper, when set, reaps executions stranded "running" by a crashed peer.
type Dispatcher struct {
	Store     Store
	Publisher ResultPublisher
	Runner    Runner
	Webhooks  WebhookSender
	Reaper    *Reaper
	JS        jetstream.JetStream
	Workers   int
	Logger    *log.Logger
}

// consumer returns (creating if needed) the durable pull consumer.
func (d *Dispatcher) consumer(ctx context.Context) (jetstream.Consumer, error) {
	return d.JS.CreateOrUpdateConsumer(ctx, rnats.StreamSubmit, jetstream.ConsumerConfig{
		Durable:       consumerDurable,
		Name:          consumerDurable,
		Description:   "runnix execution dispatcher",
		FilterSubject: "exec.submit.>",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxDeliver:    maxDeliver,
	})
}

// Run consumes the submit stream with a worker pool until ctx is cancelled.
// When a Reaper is attached it runs alongside the workers.
func (d *Dispatcher) Run(ctx context.Context) error {
	c, err := d.consumer(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	if d.Reaper != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Reaper.Run(ctx)
		}()
	}
	for i := 0; i < d.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.worker(ctx, c)
		}()
	}
	wg.Wait()
	return nil
}

// worker pulls messages and processes them one at a time.
func (d *Dispatcher) worker(ctx context.Context, c jetstream.Consumer) {
	it, err := c.Messages()
	if err != nil {
		d.Logger.Printf("consumer: %v", err)
		return
	}
	// Unblock a blocked Next() when the run context is cancelled so workers
	// shut down promptly instead of waiting on the next heartbeat.
	go func() {
		<-ctx.Done()
		it.Stop()
	}()
	defer it.Stop()

	for {
		msg, err := it.Next()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				return
			}
			// Transient pull/heartbeat error: back off and retry.
			d.Logger.Printf("consumer next: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		if err := d.handle(ctx, msg.Data()); err != nil {
			d.Logger.Printf("handle execution: %v", err)
			// handle only returns errors for pre-start failures (row requeued)
			// and result-write exhaustion; neither path can double-execute.
			_ = msg.NakWithDelay(nakDelay)
		} else {
			_ = msg.Ack()
		}
	}
}

// handle processes one submit message. It returns an error only for
// conditions worth retrying; terminal outcomes (poison, duplicate, success)
// return nil so the caller acks.
func (d *Dispatcher) handle(ctx context.Context, raw []byte) error {
	var msg rnats.SubmitMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		// Poison message: ack and drop.
		d.Logger.Printf("%s: malformed JSON", poisonStderrPrefix)
		return nil
	}
	if msg.ExecutionID == "" || !executions.ValidLanguage(msg.Language) {
		d.Logger.Printf("%s: bad execution_id/language", poisonStderrPrefix)
		return nil
	}
	id, err := store.ParsePg(msg.ExecutionID)
	if err != nil {
		d.Logger.Printf("%s: bad execution id %q", poisonStderrPrefix, msg.ExecutionID)
		return nil
	}

	// Claim and load the row. A duplicate redelivery gets pgx.ErrNoRows and
	// is acked; the returned row is the source of truth for what runs.
	exec, err := d.Store.MarkExecutionRunning(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already claimed/finished
		}
		return err
	}

	started := time.Now()
	res, err := d.Runner.Run(ctx, exec)
	if err != nil {
		if errors.Is(err, errNotStarted) {
			// Nothing ran, so a retry cannot double-execute. Return the row
			// to the queue and NAK; the redelivery re-claims it. Each attempt
			// burns one of MaxDeliver (5); a permanently broken daemon
			// exhausts those and the stale-queued reaper fails the row once
			// its created_at ages out.
			if _, rerr := d.Store.RequeueExecution(ctx, exec.ID); rerr != nil {
				d.Logger.Printf("requeue %s: %v", msg.ExecutionID, rerr)
			}
			return err
		}
		// The container started: the code may have run, so re-running it
		// could execute it twice. Record the failure and ack.
		if ferr := d.finish(ctx, storedb.FinishExecutionParams{
			ID:     exec.ID,
			Status: "failed",
			Stderr: "runner error: " + err.Error(),
		}); ferr != nil {
			d.Logger.Printf("record runner failure for %s: %v", msg.ExecutionID, ferr)
		}
		d.deliverWebhook(ctx, msg, exec, "failed", nil, time.Since(started))
		return nil
	}

	status, exitCode, stderr := classify(res)
	exitPG := pgtype.Int4{}
	if exitCode != nil {
		exitPG = pgtype.Int4{Int32: int32(*exitCode), Valid: true}
	}
	if err := d.finish(ctx, storedb.FinishExecutionParams{
		ID:       exec.ID,
		Status:   status,
		Stdout:   res.Stdout,
		Stderr:   stderr,
		ExitCode: exitPG,
	}); err != nil {
		return err
	}

	// Result summary on the wire; Postgres already holds the full output, so
	// a publish failure is not worth retrying the run.
	if err := d.Publisher.PublishResult(ctx, rnats.ResultMessage{
		ExecutionID: msg.ExecutionID,
		TenantID:    msg.TenantID,
		Status:      status,
		ExitCode:    exitCode,
	}); err != nil {
		d.Logger.Printf("publish result for %s: %v", msg.ExecutionID, err)
	}

	d.deliverWebhook(ctx, msg, exec, status, exitCode, time.Since(started))
	return nil
}

// finish persists a result, retrying transient failures with short bounded
// backoff. This is the write that must land: a lost result is unrecoverable,
// and a redelivery cannot recover it (the claim guard only claims queued
// rows). The loop honours ctx's deadline instead of outliving it.
func (d *Dispatcher) finish(ctx context.Context, arg storedb.FinishExecutionParams) error {
	var err error
	delay := finishBackoff
	for attempt := 1; attempt <= finishAttempts; attempt++ {
		if err = d.Store.FinishExecution(ctx, arg); err == nil {
			return nil
		}
		if attempt == finishAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
			delay *= 2
		}
	}
	return err
}

// deliverWebhook POSTs the completion event to the webhook_url stored on the
// execution row when one was set. Delivery is best-effort: a failure is
// logged and the persisted result stands.
func (d *Dispatcher) deliverWebhook(ctx context.Context, msg rnats.SubmitMessage, exec ClaimedExecution, status string, exitCode *int, took time.Duration) {
	url := ""
	if exec.WebhookUrl.Valid {
		url = exec.WebhookUrl.String
	}
	if d.Webhooks == nil || url == "" {
		return
	}
	evt := webhooks.ExecutionEvent{
		Event:       webhooks.EventExecutionCompleted,
		ExecutionID: msg.ExecutionID,
		TenantID:    msg.TenantID,
		Status:      status,
		ExitCode:    exitCode,
		DurationMS:  took.Milliseconds(),
		FinishedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := d.Webhooks.Deliver(ctx, url, evt); err != nil {
		d.Logger.Printf("webhook for %s: %v", msg.ExecutionID, err)
	}
}
