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
	for _, index := range []string{"probe_results_checked_idx", "security_events_created_idx"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("%s missing", index)
		}
	}
}
