package observability

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/getsentry/sentry-go"

	"o1/internal/config"
)

// Provider owns the observability integrations for one process. Metrics are
// always available; Tracer and SentryEnabled stay off until their endpoint or
// DSN is configured.
type Provider struct {
	Metrics *Metrics
	Tracer  *Tracer
	// SentryEnabled reports whether Sentry was initialized with a DSN.
	SentryEnabled bool

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

	sentryEnabled, err := initSentry(cfg)
	if err != nil {
		return nil, err
	}

	tracer, err := NewTracer(ctx, cfg, serviceName)
	if err != nil {
		return nil, err
	}

	return &Provider{
		Metrics:       NewMetrics(serviceName, cfg.Release),
		Tracer:        tracer,
		SentryEnabled: sentryEnabled,
		logger:        logger,
	}, nil
}

// Close flushes and shuts down every integration. It is idempotent and safe
// on a nil provider. Trace and Sentry flush failures are logged, never
// returned: shutdown must not fail a process that is already stopping.
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
		if p.SentryEnabled {
			if !sentry.Flush(sentryFlushTimeout) && p.logger != nil {
				p.logger.Log(ctx, slog.LevelWarn, "sentry flush timed out",
					"timeout", sentryFlushTimeout.String())
			}
		}
		p.closeErr = nil
	})
	return p.closeErr
}
