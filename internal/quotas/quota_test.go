package quotas

import (
	"context"
	"testing"
	"time"

	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestLimitsFor(t *testing.T) {
	for name, tc := range map[string]struct {
		tier      string
		hourly    int
		active    int
		unlimited bool
	}{
		"free":         {"free", 60, 2, false},
		"starter":      {"starter", 600, 5, false},
		"professional": {"professional", 6000, 20, false},
		"enterprise":   {"enterprise", -1, -1, true},
		"unknown":      {"platinum", 60, 2, false},
		"empty":        {"", 60, 2, false},
		"case":         {"Free", 60, 2, false},
		"padded":       {"  starter  ", 600, 5, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := limitsFor(tc.tier)
			if got.SubmitsPerHour != tc.hourly || got.MaxActive != tc.active {
				t.Fatalf("limitsFor(%q) = %+v, want hourly=%d active=%d", tc.tier, got, tc.hourly, tc.active)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	free := TierLimits["free"]
	for name, tc := range map[string]struct {
		limits   Limits
		hourly   int64
		active   int64
		allowed  bool
		retryMin int
	}{
		"free under both":                       {free, 59, 1, true, 0},
		"free hourly at limit":                  {free, 60, 0, false, 60},
		"free hourly over limit":                {free, 1000, 0, false, 60},
		"free active at limit":                  {free, 0, 2, false, 30},
		"hourly wins over active":               {free, 60, 2, false, 60},
		"enterprise ignores counts":             {TierLimits["enterprise"], 1 << 30, 1 << 30, true, 0},
		"starter hourly retry scales":           {TierLimits["starter"], 600, 0, false, 6},
		"professional hourly retry floors at 1": {TierLimits["professional"], 6000, 0, false, 1},
	} {
		t.Run(name, func(t *testing.T) {
			got := decide(tc.limits, tc.hourly, tc.active)
			if got.Allowed != tc.allowed {
				t.Fatalf("decide(%+v, %d, %d) allowed = %v, want %v (%+v)", tc.limits, tc.hourly, tc.active, got.Allowed, tc.allowed, got)
			}
			if !tc.allowed && (got.RetryAfterSec < tc.retryMin || got.Reason == "") {
				t.Fatalf("deny should carry Retry-After >= %d and a reason, got %+v", tc.retryMin, got)
			}
			if tc.allowed && got.Reason != "" {
				t.Fatalf("allow should carry no reason, got %+v", got)
			}
		})
	}
}

// fakeQuerier pins tier/counts and records the window start checkCounts used.
type fakeQuerier struct {
	tier     string
	hourly   int64
	active   int64
	tierErr  error
	since    time.Time
	sinceSet bool
}

func (f *fakeQuerier) GetTenant(_ context.Context, _ pgtype.UUID) (storedb.Tenant, error) {
	return storedb.Tenant{Tier: f.tier}, f.tierErr
}

func (f *fakeQuerier) CountExecutionsSince(_ context.Context, arg storedb.CountExecutionsSinceParams) (int64, error) {
	f.since, f.sinceSet = arg.CreatedAt.Time, true
	return f.hourly, nil
}

func (f *fakeQuerier) CountActiveExecutions(_ context.Context, _ pgtype.UUID) (int64, error) {
	return f.active, nil
}

func (f *fakeQuerier) CreateExecution(_ context.Context, _ storedb.CreateExecutionParams) (storedb.CreateExecutionRow, error) {
	return storedb.CreateExecutionRow{}, nil
}

func TestCheckCountsUsesClockWindow(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	fq := &fakeQuerier{tier: "free"}
	d, err := checkCounts(context.Background(), fq, pgtype.UUID{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Fatalf("empty free tenant should be allowed, got %+v", d)
	}
	if !fq.sinceSet || !fq.since.Equal(now.Add(-SubmitWindow)) {
		t.Fatalf("window start = %v, want %v", fq.since, now.Add(-SubmitWindow))
	}
}

func TestCheckCountsDenies(t *testing.T) {
	now := time.Now().UTC()
	for name, fq := range map[string]*fakeQuerier{
		"hourly full":            {tier: "free", hourly: 60},
		"active full":            {tier: "free", hourly: 3, active: 2},
		"unknown tier uses free": {tier: "platinum", hourly: 60},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := checkCounts(context.Background(), fq, pgtype.UUID{}, now)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed || d.RetryAfterSec < 1 || d.Reason == "" {
				t.Fatalf("want deny with retry + reason, got %+v", d)
			}
		})
	}
}

func TestCheckCountsSkipsCountsForEnterprise(t *testing.T) {
	// Enterprise never reaches the count queries; a tier lookup failure is
	// the only error path.
	fq := &fakeQuerier{tier: "enterprise", hourly: 1 << 40, active: 1 << 40}
	d, err := checkCounts(context.Background(), fq, pgtype.UUID{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Fatalf("enterprise should always allow, got %+v", d)
	}
}

func TestNewPostgresCheckerClock(t *testing.T) {
	if c := NewPostgresChecker(nil, nil); c.Clock == nil || c.Clock().IsZero() {
		t.Fatal("nil clock should default to time.Now")
	}
	pinned := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if c := NewPostgresChecker(nil, func() time.Time { return pinned }); !c.Clock().Equal(pinned) {
		t.Fatalf("custom clock not honored, got %v", c.Clock())
	}
}
