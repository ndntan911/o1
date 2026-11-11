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
	"o1/internal/observability"
	"o1/internal/retention"
	"o1/internal/rollup"
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

	obs, err := observability.New(ctx, rt.Config, rt.Logger, "o1-scheduler")
	if err != nil {
		return err
	}
	defer func() {
		if err := obs.Close(context.Background()); err != nil {
			rt.Logger.Log(context.Background(), slog.LevelError, "observability shutdown failed", "error", err)
		}
	}()

	riverClient, err := river.NewClient(riverpgxv5.New(rt.Pool), &river.Config{Logger: rt.Logger})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}

	obs.Metrics.Register(observability.NewPoolCollector(rt.Pool))
	sched := scheduler.New(rt.Pool, sqlc.New(rt.Pool), riverClient, rt.Config, rt.Logger, obs.Metrics, obs.Tracer)

	// The scheduler observes each maintenance pass, so the Runner must not
	// report the same pass again: rollup metrics would otherwise double-count.
	rolluper := rollup.New(rt.Pool, sqlc.New(rt.Pool), rt.Logger, nil, rt.Config.RollupMaxHoursPerRun)
	retainer := retention.New(rt.Pool, sqlc.New(rt.Pool), rt.Logger, retention.Config{
		RawRetentionDays:      rt.Config.RawRetentionDays,
		RollupRetentionMonths: rt.Config.RollupRetentionMonths,
		AuditRetentionMonths:  rt.Config.AuditRetentionMonths,
		MaxPartitionDrops:     rt.Config.RetentionMaxPartitionDrops,
		DryRun:                rt.Config.RetentionDryRun,
	}, rolluper)
	sched.SetMaintenance(rolluper, retainer)

	health := app.StartHealthServer(rt.Config.SchedulerHealthAddr, rt.Logger, rt.Pool.Ping, obs.Metrics.Handler())
	healthErr := make(chan error, 1)
	go func() {
		rt.Logger.Log(ctx, slog.LevelInfo, "starting scheduler health server", "addr", rt.Config.SchedulerHealthAddr)
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			healthErr <- err
		}
	}()

	runErr := make(chan error, 1)
	schedCtx, cancelSched := context.WithCancel(ctx)
	defer cancelSched()
	go func() {
		rt.Logger.Log(ctx, slog.LevelInfo, "scheduler started",
			"tick", rt.Config.SchedulerTick.String(),
			"sentry", obs.SentryEnabled,
		)
		runErr <- sched.Run(schedCtx)
	}()

	var result error
	runFinished := false
	select {
	case err := <-healthErr:
		result = fmt.Errorf("health server: %w", err)
	case err := <-runErr:
		result = err
		runFinished = true
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cancelSched()
	if !runFinished {
		// Wait for an in-flight tick to finish before closing the pool, even
		// when the process is stopping because the health server failed.
		select {
		case err := <-runErr:
			if result == nil {
				result = err
			} else if err != nil {
				rt.Logger.Log(context.Background(), slog.LevelError, "scheduler stopped with error", "error", err)
			}
		case <-shutdownCtx.Done():
			rt.Logger.Log(context.Background(), slog.LevelWarn, "scheduler did not stop before shutdown deadline")
		}
	}
	if err := health.Shutdown(shutdownCtx); err != nil {
		rt.Logger.Log(context.Background(), slog.LevelError, "health shutdown failed", "error", err)
	}
	if result != nil && !errors.Is(result, context.Canceled) {
		return result
	}
	rt.Logger.Log(context.Background(), slog.LevelInfo, "scheduler stopped")
	return nil
}
