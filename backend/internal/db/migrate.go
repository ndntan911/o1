package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies all pending embedded goose migrations. It is idempotent:
// migrations that already ran recorded in goose's version table are skipped.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
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
	return nil
}
