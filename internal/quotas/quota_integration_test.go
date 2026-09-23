package quotas

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// testQuotaPool boots postgres:16-alpine and applies migrations via
// store.Migrate. Skips when Docker is unavailable.
func testQuotaPool(t *testing.T) *pgxpool.Pool {
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = ctr.Terminate(ctx)
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
		t.Fatalf("migrate failed: %v", err)
	}
	return pool
}

// seedTenant creates a tenant at the given tier and returns its string id.
func seedTenant(t *testing.T, pool *pgxpool.Pool, tier string) string {
	t.Helper()
	id := uuid.New()
	slug := fmt.Sprintf("quota-%s", id.String()[:8])
	ctx := context.Background()
	if _, err := storedb.New(pool).CreateTenant(ctx, storedb.CreateTenantParams{
		ID:        store.UUIDToPg(id),
		Slug:      slug,
		Namespace: "runnix-tenant-" + id.String(),
		Tier:      tier,
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id.String()
}

// TestQuotaConcurrency hammers the REAL atomic CheckAndCreate path with
// concurrent racers, proving the advisory lock covers check AND insert so a
// burst cannot over-admit.
func TestQuotaConcurrency(t *testing.T) {
	pool := testQuotaPool(t)
	tenant := seedTenant(t, pool, "free")
	checker := NewPostgresChecker(pool, nil)
	tenantUUID, err := store.ParsePg(tenant)
	if err != nil {
		t.Fatal(err)
	}
	params := storedb.CreateExecutionParams{
		TenantID: tenantUUID,
		Language: "python",
		Source:   "print(1)",
		TimeoutS: 2,
	}
	const workers = 10

	start := make(chan struct{})
	var admitted, denied atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, d, err := checker.CheckAndCreate(ctx, tenant, params)
			if err != nil {
				t.Errorf("CheckAndCreate: %v", err)
				return
			}
			if d.Allowed {
				admitted.Add(1)
			} else {
				denied.Add(1)
				if d.Reason != "too many active executions" || d.RetryAfterSec < 1 {
					t.Errorf("deny should carry the active reason with a retry hint, got %+v", d)
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := admitted.Load(); got != int64(TierLimits["free"].MaxActive) {
		t.Fatalf("admitted = %d, want exactly the active limit %d", got, TierLimits["free"].MaxActive)
	}
	if got := admitted.Load() + denied.Load(); got != workers {
		t.Fatalf("admitted + denied = %d, want %d racers accounted for", got, workers)
	}

	// The gateway read path now denies with the active reason.
	d, err := checker.CheckSubmit(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Reason != "too many active executions" || d.RetryAfterSec < 1 {
		t.Fatalf("want active deny with retry hint, got %+v", d)
	}
}

// finishExecution moves one queued row to succeeded so it counts toward the
// hourly window but frees the active slot.
func finishExecution(t *testing.T, pool *pgxpool.Pool, tenant string) {
	t.Helper()
	ctx := context.Background()
	q := storedb.New(pool)
	tenantUUID, err := store.ParsePg(tenant)
	if err != nil {
		t.Fatal(err)
	}
	row, err := q.CreateExecution(ctx, storedb.CreateExecutionParams{
		TenantID: tenantUUID,
		Language: "python",
		Source:   "print(1)",
		TimeoutS: 2,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n, err := q.MarkExecutionRunning(ctx, row.ID); err != nil || n != 1 {
		t.Fatalf("claim = %d, %v; want 1, nil", n, err)
	}
	if err := q.FinishExecution(ctx, storedb.FinishExecutionParams{
		ID:       row.ID,
		Status:   "succeeded",
		ExitCode: pgtype.Int4{Int32: 0, Valid: true},
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestQuotaHourlyLimit fills the free hourly window with finished executions
// and proves the next submit is denied with a Retry-After hint.
func TestQuotaHourlyLimit(t *testing.T) {
	pool := testQuotaPool(t)
	tenant := seedTenant(t, pool, "free")
	for i := 0; i < TierLimits["free"].SubmitsPerHour; i++ {
		finishExecution(t, pool, tenant)
	}
	d, err := NewPostgresChecker(pool, nil).CheckSubmit(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Reason != "hourly submit limit exceeded" || d.RetryAfterSec < 1 {
		t.Fatalf("want hourly deny with retry hint, got %+v", d)
	}
}

// TestQuotaEnterpriseUnlimited allows submits regardless of existing rows.
func TestQuotaEnterpriseUnlimited(t *testing.T) {
	pool := testQuotaPool(t)
	tenant := seedTenant(t, pool, "enterprise")
	checker := NewPostgresChecker(pool, nil)
	tenantUUID, err := store.ParsePg(tenant)
	if err != nil {
		t.Fatal(err)
	}
	params := storedb.CreateExecutionParams{
		TenantID: tenantUUID,
		Language: "python",
		Source:   "print(1)",
		TimeoutS: 2,
	}
	for i := 0; i < TierLimits["free"].MaxActive+1; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, d, err := checker.CheckAndCreate(ctx, tenant, params)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if !d.Allowed {
			t.Fatalf("enterprise attempt %d denied, want allow", i)
		}
	}
	d, err := checker.CheckSubmit(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Fatalf("enterprise should allow, got %+v", d)
	}
}
