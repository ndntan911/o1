// Command scheduler enqueues due probes into River.
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

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"o1/internal/app"
	"o1/internal/db/sqlc"
	"o1/internal/scheduler"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := app.Start(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	riverClient, err := river.NewClient(riverpgxv5.New(rt.Pool), &river.Config{Logger: rt.Logger})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}

	sched := scheduler.New(rt.Pool, sqlc.New(rt.Pool), riverClient, rt.Config, rt.Logger)

	health := app.StartHealthServer(rt.Config.SchedulerHealthAddr, rt.Logger, rt.Pool.Ping)
	healthErr := make(chan error, 1)
	go func() {
		rt.Logger.Log(ctx, slog.LevelInfo, "starting scheduler health server", "addr", rt.Config.SchedulerHealthAddr)
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			healthErr <- err
		}
	}()

	runErr := make(chan error, 1)
	go func() {
		rt.Logger.Log(ctx, slog.LevelInfo, "scheduler started", "tick", rt.Config.SchedulerTick.String())
		runErr <- sched.Run(ctx)
	}()

	var result error
	select {
	case err := <-healthErr:
		result = fmt.Errorf("health server: %w", err)
	case err := <-runErr:
		result = err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := health.Shutdown(shutdownCtx); err != nil {
		rt.Logger.Log(context.Background(), slog.LevelError, "health shutdown failed", "error", err)
	}
	if result != nil && !errors.Is(result, context.Canceled) {
		return result
	}
	rt.Logger.Log(context.Background(), slog.LevelInfo, "scheduler stopped")
	return nil
}
