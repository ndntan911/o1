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
	})

	rt.Logger.Log(ctx, slog.LevelInfo, "starting server",
		"env", rt.Config.Env,
		"addr", rt.Config.HTTPAddr,
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

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-stop:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		rt.Logger.Log(context.Background(), slog.LevelError, "graceful shutdown failed", "error", err)
		return err
	}
	rt.Logger.Log(context.Background(), slog.LevelInfo, "server stopped")
	return nil
}
