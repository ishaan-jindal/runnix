package dispatcher

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/ishaan-jindal/runnix/internal/webhooks"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	rnats "github.com/ishaan-jindal/runnix/internal/nats"
)

const intImage = "python:3.12-slim"

func intTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, err := tcpg.Run(ctx, "postgres:16",
		tcpg.WithDatabase("runnix_test"),
		tcpg.WithUsername("runnix"),
		tcpg.WithPassword("runnix"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			wait.ForListeningPort("5432/tcp"),
		),
	)
	if err != nil {
		t.Skipf("docker unavailable, skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		cctx, cc := context.WithTimeout(context.Background(), time.Minute)
		defer cc()
		_ = ctr.Terminate(cctx)
	})
	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func intTestNATS(t *testing.T) *rnats.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, err := tcnats.Run(ctx, "nats:2",
		testcontainers.WithCmd("-js"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("4222/tcp")),
	)
	if err != nil {
		t.Skipf("docker unavailable, skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		cctx, cc := context.WithTimeout(context.Background(), time.Minute)
		defer cc()
		_ = ctr.Terminate(cctx)
	})
	url, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := rnats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	if err := nc.EnsureStreams(ctx); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}
	return nc
}

func intDocker(t *testing.T) *dockerclient.Client {
	t.Helper()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if _, err := cli.Ping(context.Background()); err != nil {
		t.Skipf("docker daemon unavailable, skipping integration test: %v", err)
	}
	return cli
}

func intRunner(t *testing.T) Runner {
	t.Helper()
	r := NewDockerRunner(intDocker(t), intImage, "")
	if err := r.EnsureImage(context.Background()); err != nil {
		t.Skipf("could not pull %s: %v", intImage, err)
	}
	return r
}

// enqueue creates a tenant + queued execution and publishes its submit message.
func enqueue(t *testing.T, pool *pgxpool.Pool, nc *rnats.Client, lang, source, stdin string, timeout int, webhookURL string) (uuid.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	tenant := uuid.New()
	if _, err := storedb.New(pool).CreateTenant(ctx, storedb.CreateTenantParams{
		ID:        store.UUIDToPg(tenant),
		Slug:      "test-" + tenant.String()[:8],
		Namespace: "runnix-tenant-" + tenant.String(),
		Tier:      "free",
	}); err != nil {
		t.Fatal(err)
	}
	webhook := pgtype.Text{String: webhookURL, Valid: webhookURL != ""}
	row, err := storedb.New(pool).CreateExecution(ctx, storedb.CreateExecutionParams{
		TenantID:   store.UUIDToPg(tenant),
		Language:   lang,
		Source:     source,
		Stdin:      stdin,
		TimeoutS:   int32(timeout),
		WebhookUrl: webhook,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.PublishSubmit(ctx, rnats.SubmitMessage{
		ExecutionID: store.PgToString(row.ID),
		TenantID:    tenant.String(),
		Language:    lang,
		Source:      source,
		Stdin:       stdin,
		TimeoutS:    timeout,
		WebhookURL:  webhookURL,
	}); err != nil {
		t.Fatal(err)
	}
	return row.ID.Bytes, row.ID
}

// waitStatus polls until the execution reaches a terminal status or the
// deadline passes.
func waitStatus(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) storedb.GetExecutionRow {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		var got storedb.GetExecutionRow
		err := pool.QueryRow(ctx,
			"SELECT id, tenant_id, language, status, source, stdin, stdout, stderr, exit_code, created_at, updated_at FROM executions WHERE id = $1",
			store.UUIDToPg(id),
		).Scan(&got.ID, &got.TenantID, &got.Language, &got.Status, &got.Source, &got.Stdin, &got.Stdout, &got.Stderr, &got.ExitCode, &got.CreatedAt, &got.UpdatedAt)
		if err == nil && got.Status != "queued" && got.Status != "running" {
			return got
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("execution %s never reached a terminal status", id)
	return storedb.GetExecutionRow{}
}

func intDispatcher(t *testing.T, pool *pgxpool.Pool, nc *rnats.Client, runner Runner) *Dispatcher {
	t.Helper()
	js, err := nc.JS()
	if err != nil {
		t.Fatal(err)
	}
	return &Dispatcher{
		Store:     storedb.New(pool),
		Publisher: nc,
		Runner:    runner,
		JS:        js,
		Workers:   2,
		Logger:    log.New(os.Stderr, "int-dispatcher ", 0),
	}
}

func runDispatcher(t *testing.T, d *Dispatcher) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("dispatcher did not stop cleanly")
		}
	})
	return cancel
}

func TestDispatcherRunsPython(t *testing.T) {
	pool := intTestPool(t)
	nc := intTestNATS(t)
	d := intDispatcher(t, pool, nc, intRunner(t))
	runDispatcher(t, d)

	// Subscribe for the result summary before the run completes.
	sub, err := nc.Conn.SubscribeSync("exec.result.>")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := nc.Conn.Flush(); err != nil {
		t.Fatal(err)
	}

	id, _ := enqueue(t, pool, nc, "python", "print(41+1)", "", 5, "")
	row := waitStatus(t, pool, id)
	if row.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded (stderr=%q)", row.Status, row.Stderr)
	}
	if row.Stdout != "42\n" {
		t.Fatalf("stdout = %q, want %q", row.Stdout, "42\n")
	}
	if !row.ExitCode.Valid || row.ExitCode.Int32 != 0 {
		t.Fatalf("exit_code = %v, want 0", row.ExitCode)
	}

	msg, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("no result message: %v", err)
	}
	var res rnats.ResultMessage
	if err := json.Unmarshal(msg.Data, &res); err != nil {
		t.Fatal(err)
	}
	if res.ExecutionID != id.String() || res.Status != "succeeded" || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestDispatcherTimeout(t *testing.T) {
	pool := intTestPool(t)
	nc := intTestNATS(t)
	d := intDispatcher(t, pool, nc, intRunner(t))
	runDispatcher(t, d)

	id, _ := enqueue(t, pool, nc, "python", "import time; time.sleep(30)", "", 1, "")
	row := waitStatus(t, pool, id)
	if row.Status != "timeout" {
		t.Fatalf("status = %q, want timeout", row.Status)
	}
}

func TestDispatcherNonZeroExit(t *testing.T) {
	pool := intTestPool(t)
	nc := intTestNATS(t)
	d := intDispatcher(t, pool, nc, intRunner(t))
	runDispatcher(t, d)

	id, _ := enqueue(t, pool, nc, "python", "import sys; sys.exit(3)", "", 5, "")
	row := waitStatus(t, pool, id)
	if row.Status != "failed" {
		t.Fatalf("status = %q, want failed", row.Status)
	}
	if !row.ExitCode.Valid || row.ExitCode.Int32 != 3 {
		t.Fatalf("exit_code = %v, want 3", row.ExitCode)
	}
}

func TestDispatcherStdin(t *testing.T) {
	pool := intTestPool(t)
	nc := intTestNATS(t)
	d := intDispatcher(t, pool, nc, intRunner(t))
	runDispatcher(t, d)

	id, _ := enqueue(t, pool, nc, "python", "import sys; print(sys.stdin.read().strip().upper())", "hello world", 5, "")
	row := waitStatus(t, pool, id)
	if row.Status != "succeeded" || row.Stdout != "HELLO WORLD\n" {
		t.Fatalf("status=%q stdout=%q", row.Status, row.Stdout)
	}
}

func TestDispatcherWebhookDelivery(t *testing.T) {
	pool := intTestPool(t)
	nc := intTestNATS(t)

	type delivery struct {
		body      []byte
		event     string
		timestamp string
		signature string
	}
	var mu sync.Mutex
	var got []delivery
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, delivery{
			body:      body,
			event:     r.Header.Get(webhooks.EventHeader),
			timestamp: r.Header.Get(webhooks.TimestampHeader),
			signature: r.Header.Get(webhooks.SignatureHeader),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer recv.Close()

	const secret = "int-test-hook-secret"
	d := intDispatcher(t, pool, nc, intRunner(t))
	d.Webhooks = &webhooks.Deliverer{Secret: secret, AllowPrivate: true, Logger: d.Logger}
	runDispatcher(t, d)

	id, _ := enqueue(t, pool, nc, "python", "print('hooked')", "", 5, recv.URL)
	row := waitStatus(t, pool, id)
	if row.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded (stderr=%q)", row.Status, row.Stderr)
	}

	// The webhook fires just after the result is persisted; poll for it.
	var del delivery
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		if n > 0 {
			del = got[0]
		}
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if del.body == nil {
		t.Fatal("webhook was never delivered")
	}
	if del.event != webhooks.EventExecutionCompleted {
		t.Fatalf("event header = %q", del.event)
	}
	ts, err := strconv.ParseInt(del.timestamp, 10, 64)
	if err != nil {
		t.Fatalf("timestamp header %q: %v", del.timestamp, err)
	}
	if want := webhooks.Sign(secret, ts, del.body); del.signature != want {
		t.Fatalf("signature = %q, want %q", del.signature, want)
	}
	var evt webhooks.ExecutionEvent
	if err := json.Unmarshal(del.body, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.ExecutionID != id.String() || evt.Status != "succeeded" || evt.DurationMS < 0 {
		t.Fatalf("event = %+v", evt)
	}
}

func TestReaperFailsStaleRunning(t *testing.T) {
	pool := intTestPool(t)
	nc := intTestNATS(t)
	ctx := context.Background()

	// No dispatcher is running: the message just sits unconsumed.
	id, _ := enqueue(t, pool, nc, "python", "print(1)", "", 5, "")

	// Simulate a claim whose dispatcher then died: running + stale updated_at.
	if _, err := pool.Exec(ctx, "UPDATE executions SET status = 'running' WHERE id = $1", store.UUIDToPg(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE executions SET updated_at = now() - interval '10 minutes' WHERE id = $1", store.UUIDToPg(id)); err != nil {
		t.Fatal(err)
	}

	r := &Reaper{Store: storedb.New(pool), Interval: time.Minute, StaleAfter: 5 * time.Minute, Logger: log.New(os.Stderr, "int-reaper ", 0)}
	r.reapOnce(ctx)

	row := waitStatus(t, pool, id)
	if row.Status != "failed" || !strings.Contains(row.Stderr, "reaped") {
		t.Fatalf("status = %q stderr = %q, want failed with reap note", row.Status, row.Stderr)
	}
}
