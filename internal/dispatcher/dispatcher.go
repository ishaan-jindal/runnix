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
)

// Store is the Postgres surface the dispatcher needs. *storedb.Queries
// satisfies it; unit tests supply fakes.
type Store interface {
	MarkExecutionRunning(ctx context.Context, id pgtype.UUID) (int64, error)
	FinishExecution(ctx context.Context, arg storedb.FinishExecutionParams) error
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
			_ = msg.NakWithDelay(nakDelay) // claim guard makes redelivery safe
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

	// Claim. A duplicate redelivery sees 0 rows and is acked.
	n, err := d.Store.MarkExecutionRunning(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil // already claimed/finished
	}

	started := time.Now()
	res, err := d.Runner.Run(ctx, msg)
	if err != nil {
		// Runner failed before producing an outcome: record it failed so it
		// is not stuck "running", then let redelivery see the claim guard.
		_ = d.Store.FinishExecution(ctx, storedb.FinishExecutionParams{
			ID:     id,
			Status: "failed",
			Stderr: "runner error: " + err.Error(),
		})
		d.deliverWebhook(ctx, msg, "failed", nil, time.Since(started))
		return err
	}

	status, exitCode, stderr := classify(res)
	exitPG := pgtype.Int4{}
	if exitCode != nil {
		exitPG = pgtype.Int4{Int32: int32(*exitCode), Valid: true}
	}
	if err := d.Store.FinishExecution(ctx, storedb.FinishExecutionParams{
		ID:       id,
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

	d.deliverWebhook(ctx, msg, status, exitCode, time.Since(started))
	return nil
}

// deliverWebhook POSTs the completion event to the submission's webhook_url
// when one was set. Delivery is best-effort: a failure is logged and the
// persisted result stands.
func (d *Dispatcher) deliverWebhook(ctx context.Context, msg rnats.SubmitMessage, status string, exitCode *int, took time.Duration) {
	if d.Webhooks == nil || msg.WebhookURL == "" {
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
	if err := d.Webhooks.Deliver(ctx, msg.WebhookURL, evt); err != nil {
		d.Logger.Printf("webhook for %s: %v", msg.ExecutionID, err)
	}
}
