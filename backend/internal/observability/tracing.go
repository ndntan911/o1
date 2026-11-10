package observability

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"o1/internal/config"
)

const (
	// tracerShutdownTimeout bounds the final span flush on shutdown.
	tracerShutdownTimeout = 5 * time.Second
	// instrumentationName labels spans created by this package.
	instrumentationName = "o1"
)

// noopTracer is used when tracing is disabled so callers can start spans
// unconditionally without allocating an SDK provider.
var noopTracer = noop.NewTracerProvider().Tracer(instrumentationName)

// Tracer owns the OTLP trace exporter and SDK tracer provider. A nil or
// disabled Tracer is safe: Start returns a no-op span, Shutdown is a no-op,
// and HTTPHandler passes requests through untouched.
type Tracer struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
	// exporter is kept as the test seam so in-memory tests can read exported
	// spans without a live collector.
	exporter sdktrace.SpanExporter
	enabled  bool
}

// NewTracer builds the OTLP/HTTP tracer from configuration. An empty endpoint
// disables tracing without returning an error.
func NewTracer(ctx context.Context, cfg config.Config, serviceName string) (*Tracer, error) {
	if strings.TrimSpace(cfg.OTELExporterEndpoint) == "" {
		return &Tracer{}, nil
	}

	exporter, err := newOTLPExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	return newTracerWithExporter(cfg, serviceName, exporter)
}

// newOTLPExporter builds the OTLP/HTTP exporter. A full URL keeps its scheme,
// host, and query string, and always targets the traces signal path; a bare
// host:port is treated as a plaintext collector endpoint.
func newOTLPExporter(ctx context.Context, cfg config.Config) (sdktrace.SpanExporter, error) {
	endpoint := strings.TrimSpace(cfg.OTELExporterEndpoint)
	opts := make([]otlptracehttp.Option, 0, 3)
	if strings.Contains(endpoint, "://") {
		endpointURL, err := traceEndpointURL(endpoint)
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlptracehttp.WithEndpointURL(endpointURL.String()))
		// WithEndpointURL keeps the scheme, host, and path but drops the query
		// string. Re-apply it on the request when the operator supplied one.
		if endpointURL.RawQuery != "" {
			opts = append(opts, otlptracehttp.WithHTTPClient(&http.Client{
				Transport: queryTransport{
					base:     http.DefaultTransport,
					rawQuery: endpointURL.RawQuery,
				},
				Timeout: 10 * time.Second,
			}))
		}
	} else {
		opts = append(opts, otlptracehttp.WithEndpoint(endpoint), otlptracehttp.WithInsecure())
	}
	if len(cfg.OTELExporterHeaders) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.OTELExporterHeaders))
	}
	return otlptracehttp.New(ctx, opts...)
}

// traceEndpointURL normalizes a full OTLP endpoint so the exporter always
// posts to /v1/traces. WithEndpointURL normally uses the URL path as-is and
// treats an empty path as the root, so an endpoint such as
// https://collector.example.com would otherwise export to "/" instead of the
// signal path. A base path is preserved: /otlp becomes /otlp/v1/traces.
func traceEndpointURL(endpoint string) (*url.URL, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse OTLP endpoint: %w", err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("parse OTLP endpoint %q: missing host", endpoint)
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/v1/traces") {
		path += "/v1/traces"
	}
	parsed.Path = path
	return parsed, nil
}

// queryTransport re-applies the query string from a full OTLP endpoint URL.
// The OTLP/HTTP exporter builds requests from scheme, host, and path only, so
// a query such as ?tenant=acme would otherwise be silently dropped.
type queryTransport struct {
	base     http.RoundTripper
	rawQuery string
}

func (t queryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.RawQuery = t.rawQuery
	return t.base.RoundTrip(clone)
}

// newTracerWithExporter builds a tracer around an arbitrary exporter. It is the
// injection seam used by tests with tracetest.NewInMemoryExporter.
func newTracerWithExporter(cfg config.Config, serviceName string, exporter sdktrace.SpanExporter) (*Tracer, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(cfg.Release),
			semconv.DeploymentEnvironmentNameKey.String(deploymentEnvironment(cfg)),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(clampSampleRatio(cfg.OTELSampleRatio)))),
		sdktrace.WithBatcher(exporter),
	)

	// Install the global provider and W3C propagators so instrumented
	// libraries and incoming traceparent headers share one trace.
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Tracer{
		provider: provider,
		tracer:   provider.Tracer(instrumentationName),
		exporter: exporter,
		enabled:  true,
	}, nil
}

// Enabled reports whether tracing is active.
func (t *Tracer) Enabled() bool {
	return t != nil && t.enabled
}

// Start begins a span. When tracing is disabled it returns a no-op span and the
// original context, so instrumentation sites never need to branch.
func (t *Tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if t == nil || !t.enabled || t.tracer == nil {
		return noopTracer.Start(ctx, name, opts...)
	}
	return t.tracer.Start(ctx, name, opts...)
}

// Shutdown flushes buffered spans with a five-second timeout. It is idempotent
// and safe on a nil or disabled tracer.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t == nil || t.provider == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, tracerShutdownTimeout)
	defer cancel()
	return t.provider.Shutdown(shutdownCtx)
}

// HTTPHandler wraps next with an otelhttp server span. Health and metrics
// endpoints are excluded. Span names are "METHOD route" using the same route
// resolver as the Prometheus middleware, so raw paths, identifiers, query
// strings, and bodies never reach traces.
func (t *Tracer) HTTPHandler(next http.Handler, resolveRoute func(*http.Request) string) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	if t == nil || !t.enabled || t.provider == nil {
		return next
	}

	return otelhttp.NewHandler(next, "",
		otelhttp.WithTracerProvider(t.provider),
		otelhttp.WithPropagators(otel.GetTextMapPropagator()),
		otelhttp.WithFilter(func(r *http.Request) bool {
			if r.URL == nil {
				return true
			}
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics":
				return false
			default:
				return true
			}
		}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			route := "unmatched"
			if resolveRoute != nil {
				if resolved := resolveRoute(r); resolved != "" {
					route = resolved
				}
			}
			// Go 1.22+ mux patterns already start with the method, so only
			// prepend it when the resolver returned a bare route.
			if strings.HasPrefix(route, r.Method+" ") {
				return route
			}
			return r.Method + " " + route
		}),
	)
}

// clampSampleRatio bounds a trace sampling ratio to [0, 1]. NaN is treated as
// zero so a malformed value never samples everything.
func clampSampleRatio(ratio float64) float64 {
	switch {
	case math.IsNaN(ratio) || ratio < 0:
		return 0
	case ratio > 1:
		return 1
	default:
		return ratio
	}
}

// deploymentEnvironment resolves the resource's deployment environment,
// preferring O1_ENV and falling back to the Sentry environment for configs
// built without it.
func deploymentEnvironment(cfg config.Config) string {
	if cfg.Env != "" {
		return cfg.Env
	}
	if cfg.SentryEnvironment != "" {
		return cfg.SentryEnvironment
	}
	return "development"
}
