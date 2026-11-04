// Command api runs the o1 HTTP API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"o1/internal/api"
	"o1/internal/auth"
	"o1/internal/config"
	"o1/internal/db"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/logging"
	"o1/internal/workspaces"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Only the Mailpit development sender exists today. Refuse to start in
	// production rather than silently dropping transactional mail.
	if cfg.Env == "production" {
		return fmt.Errorf("no production email sender configured: Resend support is not implemented yet")
	}

	logger := logging.New(os.Stdout, cfg.Env)
	slog.SetDefault(logger)

	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	sender := email.NewMailpitSender(cfg.SMTPAddr, cfg.EmailFrom)
	q := sqlc.New(pool)
	authSvc := auth.NewService(pool, q, sender, cfg)
	// Deferred after pool.Close, so LIFO ordering drains queued password
	// resets before the pool disappears.
	defer authSvc.Close()
	wsSvc := workspaces.NewService(pool, q)

	handler := api.NewRouter(api.Dependencies{
		Logger:           logger,
		Pool:             pool,
		Config:           cfg,
		EmailSender:      sender,
		AuthService:      authSvc,
		WorkspaceService: wsSvc,
	})

	logger.Log(ctx, slog.LevelInfo, "starting server",
		"env", cfg.Env,
		"addr", cfg.HTTPAddr,
	)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Log(context.Background(), slog.LevelError, "server error", "error", err)
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-stop:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Log(context.Background(), slog.LevelError, "graceful shutdown failed", "error", err)
		return err
	}
	logger.Log(context.Background(), slog.LevelInfo, "server stopped")
	return nil
}
