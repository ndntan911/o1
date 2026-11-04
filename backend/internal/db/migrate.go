package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationLockID serializes the whole migration run across processes. It must
// be held for goose, River migrations (added in a later task), and probe
// partition maintenance so simultaneously starting binaries cannot race.
const migrationLockID int64 = 0x6f310002

// Migrate applies all pending embedded goose migrations and ensures the probe
// result partitions exist. It is idempotent: migrations that already ran are
// recorded in goose's version table and are skipped. Concurrent callers are
// serialized with a PostgreSQL advisory lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		// Run the unlock on a fresh context so cancellation of ctx cannot
		// leave the lock held for the lifetime of the pooled connection.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
			slog.Warn("release migration advisory lock", "err", err)
		}
	}()

	d, err := sql.Open("pgx", pool.Config().ConnConfig.ConnString())
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer func() {
		if cerr := d.Close(); cerr != nil {
			slog.Warn("close migration connection", "err", cerr)
		}
	}()

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, d, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	if err := EnsureProbePartitions(ctx, pool, time.Now().UTC()); err != nil {
		return fmt.Errorf("ensure probe partitions: %w", err)
	}
	return nil
}
