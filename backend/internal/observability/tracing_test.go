package observability

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"o1/internal/config"
)

// testTracer builds an enabled Tracer backed by an in-memory exporter so tests
// never need a live OTLP collector.
func testTracer(t *testing.T, ratio float64) (*Tracer, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tr, err := newTracerWithExporter(config.Config{
		Release:           "test-release",
		SentryEnvironment: "test",
		OTELSampleRatio:   ratio,
	}, "o1-test", exporter)
	if err != nil {
		t.Fatalf("newTracerWithExporter: %v", err)
	}
	t.Cleanup(func() {
		_ = tr.Shutdown(context.Background())
	})
	return tr, exporter
}

func flushTracer(t *testing.T, tr *Tracer) []tracetest.SpanStub {
	t.Helper()
	if tr.provider == nil {
		t.Fatal("tracer has no provider")
	}
	if err := tr.provider.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	return exporterSpans(tr, t)
}

func exporterSpans(tr *Tracer, t *testing.T) []tracetest.SpanStub {
	t.Helper()
	// The in-memory exporter is only reachable through the provider's
	// processor in production code, so tests read it through the helper that
	// attached it.
	if tr.exporter == nil {
		t.Fatal("tracer has no test exporter")
	}
	return tr.exporter.(*tracetest.InMemoryExporter).GetSpans()
}

func TestTracerDisabledWithoutEndpoint(t *testing.T) {
	tr, err := NewTracer(context.Background(), config.Config{}, "o1-test")
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	if tr == nil {
		t.Fatal("NewTracer returned nil")
	}
	if tr.Enabled() {
		t.Fatal("Enabled() = true with an empty endpoint")
	}
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}

	// A nil tracer is safe as well.
	var nilTracer *Tracer
	if nilTracer.Enabled() {
		t.Fatal("nil Tracer.Enabled() = true")
	}
	if _, span := nilTracer.Start(context.Background(), "noop"); span == nil {
		t.Fatal("nil Tracer.Start returned a nil span")
	}
	if err := nilTracer.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Tracer.Shutdown: %v", err)
	}
}

func TestTracerSpanAttributesAndResource(t *testing.T) {
	tr, _ := testTracer(t, 1)

	ctx, span := tr.Start(context.Background(), "river.probe", trace.WithAttributes(
		attribute.String("queue", "probe"),
		attribute.Int("attempt", 2),
		attribute.String("kind", "probe"),
	))
	_ = ctx
	span.End()

	spans := flushTracer(t, tr)
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	got := spans[0]
	if got.Name != "river.probe" {
		t.Fatalf("span name = %q, want river.probe", got.Name)
	}
	for key, want := range map[string]string{
		"queue": "probe",
		"kind":  "probe",
	} {
		v, ok := attrValue(got, key)
		if !ok {
			t.Fatalf("span missing attribute %q (attrs: %v)", key, got.Attributes)
		}
		if v.AsString() != want {
			t.Fatalf("%s = %q, want %q", key, v.AsString(), want)
		}
	}
	if v, ok := attrValue(got, "attempt"); !ok || v.AsInt64() != 2 {
		t.Fatalf("attempt = %v (present %v), want 2", v, ok)
	}

	if got.Resource == nil {
		t.Fatal("span has no resource")
	}
	if v, ok := got.Resource.Set().Value("service.name"); !ok || v.AsString() != "o1-test" {
		t.Fatalf("service.name = %v (present %v), want o1-test", v, ok)
	}
	if v, ok := got.Resource.Set().Value("service.version"); !ok || v.AsString() != "test-release" {
		t.Fatalf("service.version = %v (present %v), want test-release", v, ok)
	}
	if v, ok := got.Resource.Set().Value("deployment.environment.name"); !ok || v.AsString() != "test" {
		t.Fatalf("deployment.environment.name = %v (present %v), want test", v, ok)
	}
}

func TestTracerSampleRatioClamping(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want float64
	}{
		{-1, 0},
		{0, 0},
		{0.25, 0.25},
		{math.NaN(), 0},
		{1, 1},
		{2, 1},
	} {
		if got := clampSampleRatio(tc.in); got != tc.want {
			t.Errorf("clampSampleRatio(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTracerHTTPHandler(t *testing.T) {
	tr, _ := testTracer(t, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/monitors/{monitorID}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	resolveRoute := func(r *http.Request) string {
		_, pattern := mux.Handler(r)
		return pattern
	}

	handler := tr.HTTPHandler(mux, resolveRoute)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitors/abc?secret=do-not-record", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	// Health and metrics requests must not create spans.
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	spans := flushTracer(t, tr)
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	got := spans[0]
	if got.Name != "GET /api/v1/monitors/{monitorID}" {
		t.Fatalf("span name = %q, want METHOD route", got.Name)
	}
	for _, kv := range got.Attributes {
		if strings.Contains(kv.Value.String(), "do-not-record") {
			t.Fatalf("span captured query string in attribute %q = %q", kv.Key, kv.Value.String())
		}
	}
}

// attrValue finds an attribute by key in a finished span.
func attrValue(span tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// TestOTLPExporterAppendsSignalPath verifies a full endpoint URL always posts
// spans to the traces signal path, preserving a base path, query string, and
// configured headers.
func TestOTLPExporterAppendsSignalPath(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  func(string) string
		wantPath  string
		wantQuery string
	}{
		{
			name:     "root URL",
			endpoint: func(base string) string { return base },
			wantPath: "/v1/traces",
		},
		{
			name:     "base path",
			endpoint: func(base string) string { return base + "/otlp" },
			wantPath: "/otlp/v1/traces",
		},
		{
			name:     "already signal path",
			endpoint: func(base string) string { return base + "/otlp/v1/traces" },
			wantPath: "/otlp/v1/traces",
		},
		{
			name:      "query preserved",
			endpoint:  func(base string) string { return base + "/otlp?tenant=acme" },
			wantPath:  "/otlp/v1/traces",
			wantQuery: "tenant=acme",
		},
		{
			name:     "trailing slash",
			endpoint: func(base string) string { return base + "/otlp/" },
			wantPath: "/otlp/v1/traces",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			type requestInfo struct {
				path  string
				query string
				auth  string
			}
			requests := make(chan requestInfo, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- requestInfo{
					path:  r.URL.Path,
					query: r.URL.RawQuery,
					auth:  r.Header.Get("Authorization"),
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			tr, err := NewTracer(context.Background(), config.Config{
				OTELExporterEndpoint: tc.endpoint(server.URL),
				OTELExporterHeaders:  map[string]string{"Authorization": "Bearer test-token"},
				OTELSampleRatio:      1,
			}, "o1-test")
			if err != nil {
				t.Fatalf("NewTracer: %v", err)
			}
			_, span := tr.Start(context.Background(), "test")
			span.End()
			if err := tr.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}

			select {
			case got := <-requests:
				if got.path != tc.wantPath {
					t.Fatalf("export path = %q, want %q", got.path, tc.wantPath)
				}
				if got.query != tc.wantQuery {
					t.Fatalf("export query = %q, want %q", got.query, tc.wantQuery)
				}
				if got.auth != "Bearer test-token" {
					t.Fatalf("Authorization header = %q, want Bearer test-token", got.auth)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("exporter did not send a request")
			}
		})
	}
}
