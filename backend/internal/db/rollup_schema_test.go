package db_test

import (
	"context"
	"testing"

	"o1/internal/testutil"
)

func TestRollupSchema(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	for _, name := range []string{"monitor_hourly_stats", "rollup_state"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("%s missing", name)
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rollup_state WHERE id`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rollup_state rows = %d, want 1", rows)
	}
	for _, index := range []string{
		"probe_results_checked_idx", "security_events_created_idx",
		"monitor_hourly_stats_bucket_idx", "monitor_hourly_stats_workspace_bucket_idx",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("%s missing", index)
		}
	}
	var watermarkNullable bool
	if err := pool.QueryRow(ctx, `
		SELECT is_nullable = 'YES' FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'rollup_state' AND column_name = 'watermark'`).Scan(&watermarkNullable); err != nil {
		t.Fatal(err)
	}
	if !watermarkNullable {
		t.Fatal("rollup_state.watermark is not nullable")
	}
}
