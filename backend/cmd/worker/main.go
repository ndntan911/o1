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
	"github.com/riverqueue/river/rivertype"
	"golang.org/x/time/rate"

	"o1/internal/ai"
	"o1/internal/alerts"
	"o1/internal/app"
	"o1/internal/copilot"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/incidents"
	"o1/internal/monitors"
	"o1/internal/observability"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/worker"
	"o1/internal/workspaces"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// investigationModel returns the model the investigation service records and
// sends per request. CopilotModel defaults to AIModel in config, so a missing
// copilot setting keeps the historical behavior.
func investigationModel(copilotModel, aiModel string) string {
	if copilotModel != "" {
		return copilotModel
	}
	return aiModel
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := app.Start(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	obs, err := observability.New(ctx, rt.Config, rt.Logger, "o1-worker")
	if err != nil {
		return err
	}
	defer func() {
		if err := obs.Close(context.Background()); err != nil {
			rt.Logger.Log(context.Background(), slog.LevelError, "observability shutdown failed", "error", err)
		}
	}()

	box, err := secretbox.New(rt.Config.EncryptionKey)
	if err != nil {
		return fmt.Errorf("create secret box: %w", err)
	}
	guard := probe.NewGuard(probe.PolicyFromConfig(rt.Config))
	prober := probe.NewProber(guard)
	limiter := probeLimiter(rt.Config.ProbeGlobalRPS)

	q := sqlc.New(rt.Pool)
	probeWorker := worker.NewProbeWorker(rt.Pool, q, box, prober, limiter, rt.Logger, obs.Metrics)
	sender, err := email.NewSender(rt.Config)
	if err != nil {
		return fmt.Errorf("create email sender: %w", err)
	}
	alertWorker := worker.NewAlertWorker(rt.Pool, q, sender, box, alerts.NewNotifier(guard, ""), rt.Config, rt.Logger, obs.Metrics)

	// The copilot is optional: a misconfigured or absent provider leaves the
	// service nil and the worker registered as a no-op, so the rest of the
	// worker fleet still starts.
	wsSvc := workspaces.NewService(rt.Pool, q)
	var investigationSvc *copilot.Service
	provider, providerErr := ai.New(ai.Settings{
		Provider: rt.Config.AIProvider,
		BaseURL:  rt.Config.AIBaseURL,
		APIKey:   rt.Config.AIAPIKey,
		Model:    rt.Config.AIModel,
		Timeout:  rt.Config.AITimeout,
		Headers:  rt.Config.AIExtraHeaders,
	})
	switch {
	case providerErr != nil:
		rt.Logger.Warn("copilot disabled: provider misconfigured",
			"provider", rt.Config.AIProvider, "error", providerErr)
	case provider == nil:
		rt.Logger.Info("copilot disabled", "provider", rt.Config.AIProvider)
	default:
		toolProvider, ok := provider.(ai.ToolProvider)
		if !ok {
			rt.Logger.Warn("copilot disabled: provider does not support tool calls",
				"provider", rt.Config.AIProvider)
			break
		}
		investigationSvc = copilot.NewService(copilot.Options{
			Store:      copilot.NewPostgresStore(rt.Pool),
			Incidents:  incidents.NewService(rt.Pool, q, wsSvc),
			Monitors:   monitors.NewService(rt.Pool, q, wsSvc, box, guard, rt.Config),
			Workspaces: wsSvc,
			Provider:   toolProvider,
			Pool:       rt.Pool,
			Queries:    q,
			Model:      investigationModel(rt.Config.CopilotModel, rt.Config.AIModel),
			Limits:     copilot.DefaultLimits(),
			Now:        time.Now,
			Logger:     rt.Logger,
			Metrics:    obs.Metrics,
		})
		rt.Logger.Info("copilot enabled", "provider", rt.Config.AIProvider,
			"model", investigationModel(rt.Config.CopilotModel, rt.Config.AIModel))
	}
	investigationWorker := worker.NewInvestigationWorker(investigationSvc, rt.Logger, obs.Metrics)

	// Queue and incident gauges are shared across the environment, so they are
	// refreshed by exactly one process: the worker. Other processes expose
	// their own request, probe, and pool metrics only.
	obs.Metrics.Register(observability.NewPoolCollector(rt.Pool))
	go observability.NewQueueCollector(rt.Pool, obs.Metrics).Start(ctx)
	go observability.NewIncidentCollector(rt.Pool, obs.Metrics).Start(ctx)

	workers := river.NewWorkers()
	river.AddWorker(workers, probeWorker)
	river.AddWorker(workers, alertWorker)
	river.AddWorker(workers, investigationWorker)

	client, err := river.NewClient(riverpgxv5.New(rt.Pool), &river.Config{
		Logger:       rt.Logger,
		Workers:      workers,
		ErrorHandler: observability.SentryErrorHandler{},
		Queues: map[string]river.QueueConfig{
			"probe": {MaxWorkers: rt.Config.WorkerConcurrency},
			"alert": {MaxWorkers: 4},
			"ai":    {MaxWorkers: 2},
		},
		WorkerMiddleware: []rivertype.WorkerMiddleware{
			observability.RiverMiddleware(obs.Metrics, obs.Tracer),
		},
	})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}
	probeWorker.SetJobInserter(client)
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start river client: %w", err)
	}

	health := app.StartHealthServer(rt.Config.WorkerHealthAddr, rt.Logger, rt.Pool.Ping, obs.Metrics.Handler())
	healthErr := make(chan error, 1)
	go func() {
		rt.Logger.Log(ctx, slog.LevelInfo, "starting worker health server", "addr", rt.Config.WorkerHealthAddr)
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			healthErr <- err
		}
	}()

	rt.Logger.Log(ctx, slog.LevelInfo, "worker started",
		"concurrency", rt.Config.WorkerConcurrency,
		"sentry", obs.SentryEnabled,
	)

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
