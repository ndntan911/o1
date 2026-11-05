// Package app provides the shared startup and health-server plumbing used by
// the api, scheduler, and worker binaries.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/config"
	"o1/internal/db"
	"o1/internal/logging"
)

// Runtime holds the process-wide resources opened by Start.
type Runtime struct {
	Config config.Config
	Logger *slog.Logger
	Pool   *pgxpool.Pool
}

// Start loads configuration, builds the logger, opens the database pool, and
// applies all migrations (goose, River, and probe partitions).
func Start(ctx context.Context) (*Runtime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	logger := logging.New(os.Stdout, cfg.Env)
	slog.SetDefault(logger)

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &Runtime{Config: cfg, Logger: logger, Pool: pool}, nil
}

// Close releases the runtime's resources.
func (r *Runtime) Close() {
	if r != nil && r.Pool != nil {
		r.Pool.Close()
	}
}
