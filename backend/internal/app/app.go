// Package app provides the shared startup and health-server plumbing used by
// the api, scheduler, and worker binaries.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
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
	if cfg.EncryptionKeyDerived {
		logger.Log(ctx, slog.LevelWarn, "O1_ENCRYPTION_KEY is not set; deriving tenant encryption key from O1_SECRET_KEY (development only)")
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err := bootstrapAdmin(ctx, pool, logger, cfg.BootstrapAdminEmail); err != nil {
		pool.Close()
		return nil, err
	}
	return &Runtime{Config: cfg, Logger: logger, Pool: pool}, nil
}

// bootstrapAdmin promotes an existing verified account when
// O1_BOOTSTRAP_ADMIN_EMAIL is configured. It never errors when the account
// does not exist yet, so operators can set the variable before signing up.
func bootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, emailAddr string) error {
	emailAddr = auth.NormalizeEmail(emailAddr)
	if emailAddr == "" {
		return nil
	}
	tag, err := pool.Exec(ctx, `
		UPDATE users
		SET is_admin = true
		WHERE email = $1 AND email_verified_at IS NOT NULL AND is_admin = false`,
		emailAddr)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	logger.Log(ctx, slog.LevelInfo, "bootstrap admin check",
		"email", emailAddr,
		"promoted", tag.RowsAffected(),
	)
	return nil
}

// Close releases the runtime's resources.
func (r *Runtime) Close() {
	if r != nil && r.Pool != nil {
		r.Pool.Close()
	}
}
