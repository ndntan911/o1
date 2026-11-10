package observability

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"o1/internal/config"
)

// Provider owns the observability integrations for one process. Task 3 adds
// Tracer and Task 4 adds SentryEnabled; an empty configuration leaves every
// integration disabled.
type Provider struct {
	Metrics *Metrics
	Tracer  *Tracer

	logger    *slog.Logger
	closeOnce sync.Once
	closeErr  error
}

// New builds the observability provider for a service. It never fails when
// observability configuration is empty: metrics are always available while
// tracing and Sentry stay disabled until configured.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger, serviceName string) (*Provider, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	tracer, err := NewTracer(ctx, cfg, serviceName)
	if err != nil {
		return nil, err
	}

	return &Provider{
		Metrics: NewMetrics(serviceName, cfg.Release),
		Tracer:  tracer,
		logger:  logger,
	}, nil
}

// Close flushes and shuts down every integration. It is idempotent and safe
// on a nil provider. Trace flush failures are logged, never returned: shutdown
// must not fail a process that is already stopping.
func (p *Provider) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.closeOnce.Do(func() {
		if p.Tracer != nil {
			if err := p.Tracer.Shutdown(ctx); err != nil && p.logger != nil {
				p.logger.Log(ctx, slog.LevelError, "tracing shutdown failed", "error", err)
			}
		}
		p.closeErr = nil
	})
	return p.closeErr
}
