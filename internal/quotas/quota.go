// Package quotas enforces per-tenant submit quotas (v1).
//
// Each tier gets a trailing-hour submit cap plus a cap on concurrent
// (queued + running) executions. The atomic check-and-insert path runs the
// count check and the execution insert inside one transaction holding a
// per-tenant advisory lock, so concurrent gateways serialize check AND
// insert and a burst cannot over-admit.
package quotas

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubmitWindow is the trailing window for the hourly submit cap.
const SubmitWindow = time.Hour

// Limits caps submits per SubmitWindow and concurrent active executions.
// -1 means unlimited (enterprise).
type Limits struct {
	SubmitsPerHour int
	MaxActive      int
}

// tierLimits maps tenant tier to submit quotas. Unknown tiers fall back to
// the free tier.
var tierLimits = map[string]Limits{
	"free":         {SubmitsPerHour: 60, MaxActive: 2},
	"starter":      {SubmitsPerHour: 600, MaxActive: 5},
	"professional": {SubmitsPerHour: 6000, MaxActive: 20},
	"enterprise":   {SubmitsPerHour: -1, MaxActive: -1},
}

// Decision is the outcome of a submit-quota check.
type Decision struct {
	Allowed       bool
	RetryAfterSec int    // hint for the Retry-After header when denied
	Reason        string // short deny reason ("" when allowed)
}

// Checker gates execution submits per tenant.
type Checker interface {
	// CheckAndCreate atomically checks quotas and inserts the execution in
	// the same locked transaction. On allow it returns the inserted row and
	// an allowed Decision; on deny it inserts nothing and returns the deny
	// Decision with a zero row.
	CheckAndCreate(ctx context.Context, tenantID string, params storedb.CreateExecutionParams) (storedb.CreateExecutionRow, Decision, error)
}

// PostgresChecker is the Postgres-backed Checker.
type PostgresChecker struct {
	Pool *pgxpool.Pool
	// Clock reports now; defaults to time.Now.
	Clock func() time.Time
}

// NewPostgresChecker builds a Checker over pool. A nil clock means time.Now.
func NewPostgresChecker(pool *pgxpool.Pool, clock func() time.Time) *PostgresChecker {
	if clock == nil {
		clock = time.Now
	}
	return &PostgresChecker{Pool: pool, Clock: clock}
}

func limitsFor(tier string) Limits {
	if l, ok := tierLimits[strings.ToLower(strings.TrimSpace(tier))]; ok {
		return l
	}
	return tierLimits["free"]
}

// decide maps counts to a decision. Hourly denies hint a retry after one
// fair-share slot (window/limit, at least a second); active denies hint a
// short fixed wait since a slot frees on completion, not on time.
func decide(l Limits, hourly, active int64) Decision {
	if l.SubmitsPerHour >= 0 && hourly >= int64(l.SubmitsPerHour) {
		retry := 3600
		if l.SubmitsPerHour > 0 {
			retry = 3600 / l.SubmitsPerHour
		}
		if retry < 1 {
			retry = 1
		}
		return Decision{RetryAfterSec: retry, Reason: "hourly submit limit exceeded"}
	}
	if l.MaxActive >= 0 && active >= int64(l.MaxActive) {
		return Decision{RetryAfterSec: 30, Reason: "too many active executions"}
	}
	return Decision{Allowed: true}
}

// advisoryLockSQL serializes one tenant's quota checks (and locked
// check-and-insert callers) without blocking other tenants. Callers pass
// the canonical UUID string so alternate notations share one lock.
const advisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

// querier is the storedb surface quota checks need. *storedb.Queries
// satisfies it; unit tests supply fakes.
type querier interface {
	GetTenant(ctx context.Context, id pgtype.UUID) (storedb.Tenant, error)
	CountExecutionsSince(ctx context.Context, arg storedb.CountExecutionsSinceParams) (int64, error)
	CountActiveExecutions(ctx context.Context, tenantID pgtype.UUID) (int64, error)
	CreateExecution(ctx context.Context, arg storedb.CreateExecutionParams) (storedb.CreateExecutionRow, error)
}

// CheckSubmit reports whether the tenant may submit now.
func (c *PostgresChecker) CheckSubmit(ctx context.Context, tenantID string) (Decision, error) {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("quota tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tenantUUID, err := store.ParsePg(tenantID)
	if err != nil {
		return Decision{}, fmt.Errorf("quota tenant: %w", err)
	}
	if _, err := tx.Exec(ctx, advisoryLockSQL, store.PgToString(tenantUUID)); err != nil {
		return Decision{}, fmt.Errorf("quota lock: %w", err)
	}
	return checkCounts(ctx, storedb.New(tx), tenantUUID, c.Clock())
}

// CheckAndCreate admits and inserts in one locked transaction, so concurrent
// submits for the same tenant serialize. On deny it rolls back (inserting
// nothing) and returns the deny Decision with a zero row.
func (c *PostgresChecker) CheckAndCreate(ctx context.Context, tenantID string, params storedb.CreateExecutionParams) (storedb.CreateExecutionRow, Decision, error) {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return storedb.CreateExecutionRow{}, Decision{}, fmt.Errorf("quota tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tenantUUID, err := store.ParsePg(tenantID)
	if err != nil {
		return storedb.CreateExecutionRow{}, Decision{}, fmt.Errorf("quota tenant: %w", err)
	}
	if _, err := tx.Exec(ctx, advisoryLockSQL, store.PgToString(tenantUUID)); err != nil {
		return storedb.CreateExecutionRow{}, Decision{}, fmt.Errorf("quota lock: %w", err)
	}
	q := storedb.New(tx)
	d, err := checkCounts(ctx, q, tenantUUID, c.Clock())
	if err != nil {
		return storedb.CreateExecutionRow{}, Decision{}, err
	}
	if !d.Allowed {
		return storedb.CreateExecutionRow{}, d, nil
	}
	row, err := q.CreateExecution(ctx, params)
	if err != nil {
		return storedb.CreateExecutionRow{}, Decision{}, fmt.Errorf("quota insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storedb.CreateExecutionRow{}, Decision{}, fmt.Errorf("quota commit: %w", err)
	}
	return row, Decision{Allowed: true}, nil
}

// checkCounts reads the tier and counts inside the caller's locked
// transaction and maps them to a decision. Shared by CheckSubmit and
// CheckAndCreate so both enforce identical limits.
func checkCounts(ctx context.Context, q querier, tenantUUID pgtype.UUID, now time.Time) (Decision, error) {
	tenant, err := q.GetTenant(ctx, tenantUUID)
	if err != nil {
		return Decision{}, fmt.Errorf("quota tier: %w", err)
	}
	l := limitsFor(tenant.Tier)
	if l.SubmitsPerHour < 0 && l.MaxActive < 0 {
		return Decision{Allowed: true}, nil
	}
	var hourly, active int64
	if l.SubmitsPerHour >= 0 {
		hourly, err = q.CountExecutionsSince(ctx, storedb.CountExecutionsSinceParams{
			TenantID:  tenantUUID,
			CreatedAt: pgtype.Timestamptz{Time: now.Add(-SubmitWindow), Valid: true},
		})
		if err != nil {
			return Decision{}, fmt.Errorf("quota hourly count: %w", err)
		}
	}
	if l.MaxActive >= 0 {
		active, err = q.CountActiveExecutions(ctx, tenantUUID)
		if err != nil {
			return Decision{}, fmt.Errorf("quota active count: %w", err)
		}
	}
	return decide(l, hourly, active), nil
}
