package loadseed

import (
	"context"
	"testing"
	"time"

	"o1/internal/testutil"
)

func TestSeedCreatesReferenceDataset(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	res, err := Seed(ctx, pool, Options{Tenants: 2, MonitorsPerTenant: 3, RawDays: 1, RollupDays: 2, Now: now})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if res.Monitors != 6 {
		t.Fatalf("monitors = %d, want 6", res.Monitors)
	}
	// One raw row per minute for the last day, per monitor.
	if want := int64(6 * 24 * 60); res.RawRows != want {
		t.Fatalf("raw rows = %d, want %d", res.RawRows, want)
	}
	// Hourly rollups for the completed days, per monitor.
	if want := int64(6 * 24 * 2); res.RollupRows != want {
		t.Fatalf("rollup rows = %d, want %d", res.RollupRows, want)
	}

	var paused, workspaces, keys int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE paused), count(DISTINCT workspace_id) FROM monitors`).Scan(&paused, &workspaces); err != nil {
		t.Fatalf("count monitors: %v", err)
	}
	if paused != 6 || workspaces != 2 {
		t.Fatalf("paused=%d workspaces=%d", paused, workspaces)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE revoked_at IS NULL`).Scan(&keys); err != nil {
		t.Fatalf("count api keys: %v", err)
	}
	if keys != 1 {
		t.Fatalf("api keys = %d, want 1", keys)
	}
	if res.APIKey == "" || res.APIPrefix == "" {
		t.Fatal("seed did not return the load API key")
	}

	var pages, published int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(spm.n), 0)
		FROM status_pages sp
		LEFT JOIN LATERAL (
			SELECT count(*) AS n FROM status_page_monitors m WHERE m.status_page_id = sp.workspace_id
		) spm ON true
		WHERE sp.enabled`).Scan(&pages, &published); err != nil {
		t.Fatalf("count status pages: %v", err)
	}
	if pages != 2 || published != 6 {
		t.Fatalf("status pages=%d monitors=%d, want 2/6", pages, published)
	}
}

func TestSeedCreatesHistoryBeyondTheStartupWindow(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	// 60 days reaches into a month that startup partition maintenance does not
	// create (it covers now-1 month through now+2 months).
	res, err := Seed(ctx, pool, Options{Tenants: 1, MonitorsPerTenant: 1, RawDays: 60, RollupDays: 2, Now: now})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if want := int64(60 * 24 * 60); res.RawRows != want {
		t.Fatalf("raw rows = %d, want %d", res.RawRows, want)
	}
}
