package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// probePartitionLockID serializes partition maintenance across processes.
const probePartitionLockID int64 = 0x6f310003

// EnsureProbePartitions creates the monthly probe_results partitions covering
// [now-1 month, now+2 months], inclusive, in UTC. It is idempotent: existing
// partitions are left untouched. The work is serialized with a PostgreSQL
// advisory lock so concurrent callers (several binaries starting at once)
// cannot race on CREATE TABLE.
func EnsureProbePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire partition connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", probePartitionLockID); err != nil {
		return fmt.Errorf("acquire partition advisory lock: %w", err)
	}
	defer func() {
		// The caller's context may already be canceled; unlocking must still
		// happen on the same connection before it returns to the pool.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", probePartitionLockID); err != nil {
			slog.Warn("release partition advisory lock", "err", err)
		}
	}()

	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 3, 0)

	for month := first; month.Before(last); month = month.AddDate(0, 1, 0) {
		next := month.AddDate(0, 1, 0)
		// The identifier and bounds are derived only from internally computed
		// times, never from user input. PostgreSQL utility statements (DDL) do
		// not accept bind parameters, so the RFC 3339 timestamps are inlined.
		// Formatting a time.Time cannot introduce SQL metacharacters.
		name := "probe_results_" + month.Format("2006_01")
		stmt := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s PARTITION OF probe_results FOR VALUES FROM ('%s') TO ('%s')",
			pgx.Identifier{name}.Sanitize(),
			month.Format(time.RFC3339),
			next.Format(time.RFC3339),
		)
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("create probe partition %s: %w", name, err)
		}
	}
	return nil
}
