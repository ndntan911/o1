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
	"o1/internal/app"
	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/observability"
	"o1/internal/workspaces"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	rt, err := app.Start(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	obs, err := observability.New(ctx, rt.Config, rt.Logger, "o1-api")
	if err != nil {
		return err
	}
	defer func() {
		if err := obs.Close(context.Background()); err != nil {
			rt.Logger.Log(context.Background(), slog.LevelError, "observability shutdown failed", "error", err)
		}
	}()

	// Only the Mailpit development sender exists today. Refuse to start in
	// production rather than silently dropping transactional mail.
	if rt.Config.Env == "production" {
		return fmt.Errorf("no production email sender configured: Resend support is not implemented yet")
	}

	sender := email.NewMailpitSender(rt.Config.SMTPAddr, rt.Config.EmailFrom)
	q := sqlc.New(rt.Pool)
	authSvc := auth.NewService(rt.Pool, q, sender, rt.Config)
	// Deferred after rt.Close, so LIFO ordering drains queued password resets
	// before the pool disappears.
	defer authSvc.Close()
	wsSvc := workspaces.NewService(rt.Pool, q)

	handler := api.NewRouter(api.Dependencies{
		Logger:           rt.Logger,
		Pool:             rt.Pool,
		Config:           rt.Config,
		EmailSender:      sender,
		AuthService:      authSvc,
		WorkspaceService: wsSvc,
		Metrics:          obs.Metrics,
		Tracer:           obs.Tracer,
	})

	rt.Logger.Log(ctx, slog.LevelInfo, "starting server",
		"env", rt.Config.Env,
		"addr", rt.Config.HTTPAddr,
		"sentry", obs.SentryEnabled,
	)

	srv := &http.Server{
		Addr:              rt.Config.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			rt.Logger.Log(context.Background(), slog.LevelError, "server error", "error", err)
			errCh <- err
		}
	}()

	// The internal listener serves health checks and Prometheus metrics. It is
	// never exposed through Caddy or the public API listener.
	healthSrv := app.StartHealthServer(rt.Config.APIHealthAddr, rt.Logger, rt.Pool.Ping, obs.Metrics.Handler())
	healthErr := make(chan error, 1)
	go func() {
		rt.Logger.Log(ctx, slog.LevelInfo, "starting api health server", "addr", rt.Config.APIHealthAddr)
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			rt.Logger.Log(context.Background(), slog.LevelError, "health server error", "error", err)
			healthErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	var runErr error
	select {
	case runErr = <-errCh:
	case runErr = <-healthErr:
	case <-stop:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		rt.Logger.Log(context.Background(), slog.LevelError, "graceful shutdown failed", "error", err)
		if runErr == nil {
			runErr = err
		}
	}
	if err := healthSrv.Shutdown(shutdownCtx); err != nil {
		rt.Logger.Log(context.Background(), slog.LevelError, "health shutdown failed", "error", err)
		if runErr == nil {
			runErr = err
		}
	}
	if runErr != nil {
		return runErr
	}
	rt.Logger.Log(context.Background(), slog.LevelInfo, "server stopped")
	return nil
}
