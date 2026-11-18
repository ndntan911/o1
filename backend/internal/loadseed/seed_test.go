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
}
