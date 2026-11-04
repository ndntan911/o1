package db_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"o1/internal/db"
	"o1/internal/testutil"
)

func TestEnsureProbePartitions_CreatesWindowAndIsIdempotent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	// Start from a database without probe partitions so the assertion sees
	// exactly the window created below (testutil's Migrate already created
	// partitions for the real current month).
	if _, err := pool.Exec(ctx, `
		DO $$
		DECLARE r record;
		BEGIN
			FOR r IN
				SELECT relname FROM pg_class
				WHERE relname LIKE 'probe_results_%' AND relkind = 'r'
			LOOP
				EXECUTE format('DROP TABLE %I', r.relname);
			END LOOP;
		END
		$$`); err != nil {
		t.Fatalf("drop existing partitions: %v", err)
	}

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if err := db.EnsureProbePartitions(ctx, pool, now); err != nil {
		t.Fatalf("EnsureProbePartitions: %v", err)
	}
	if err := db.EnsureProbePartitions(ctx, pool, now); err != nil {
		t.Fatalf("second EnsureProbePartitions: %v", err)
	}

	var names []string
	if err := pool.QueryRow(ctx, `
		SELECT array_agg(relname::text ORDER BY relname)
		FROM pg_class
		WHERE relname LIKE 'probe_results_%' AND relkind = 'r'`).Scan(&names); err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	want := []string{"probe_results_2026_10", "probe_results_2026_11", "probe_results_2026_12", "probe_results_2027_01"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("partitions = %v, want %v", names, want)
	}
}
