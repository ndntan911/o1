// Command worker executes River probe jobs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"golang.org/x/time/rate"

	"o1/internal/app"
	"o1/internal/db/sqlc"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/worker"
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

	box, err := secretbox.New(rt.Config.EncryptionKey)
	if err != nil {
		return fmt.Errorf("create secret box: %w", err)
	}
	guard := probe.NewGuard(probe.PolicyFromConfig(rt.Config))
	prober := probe.NewProber(guard)
	limiter := probeLimiter(rt.Config.ProbeGlobalRPS)

	q := sqlc.New(rt.Pool)
	probeWorker := worker.NewProbeWorker(rt.Pool, q, box, prober, limiter, rt.Logger)

	workers := river.NewWorkers()
	river.AddWorker(workers, probeWorker)

	client, err := river.NewClient(riverpgxv5.New(rt.Pool), &river.Config{
		Logger:  rt.Logger,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			"probe": {MaxWorkers: rt.Config.WorkerConcurrency},
		},
	})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start river client: %w", err)
	}

	health := app.StartHealthServer(rt.Config.WorkerHealthAddr, rt.Logger, rt.Pool.Ping)
	healthErr := make(chan error, 1)
	go func() {
		rt.Logger.Log(ctx, slog.LevelInfo, "starting worker health server", "addr", rt.Config.WorkerHealthAddr)
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			healthErr <- err
		}
	}()

	rt.Logger.Log(ctx, slog.LevelInfo, "worker started", "concurrency", rt.Config.WorkerConcurrency)

	var result error
	select {
	case err := <-healthErr:
		result = fmt.Errorf("health server: %w", err)
	case <-ctx.Done():
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		rt.Logger.Log(context.Background(), slog.LevelError, "river shutdown failed", "error", err)
		if result == nil {
			result = err
		}
	}
	if err := health.Shutdown(stopCtx); err != nil {
		rt.Logger.Log(context.Background(), slog.LevelError, "health shutdown failed", "error", err)
	}
	if result != nil && !errors.Is(result, context.Canceled) {
		return result
	}
	rt.Logger.Log(context.Background(), slog.LevelInfo, "worker stopped")
	return nil
}

// probeLimiter builds the global probe rate limiter. A fractional RPS still
// needs a burst of at least one or every probe would be rejected.
func probeLimiter(rps float64) *rate.Limiter {
	burst := int(math.Ceil(rps))
	if burst < 1 {
		burst = 1
	}
	return rate.NewLimiter(rate.Limit(rps), burst)
}
