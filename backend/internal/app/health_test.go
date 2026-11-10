package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"o1/internal/app"
	"o1/internal/observability"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthServer_HealthAndReadinessUnchanged(t *testing.T) {
	srv := app.StartHealthServer(":0", testLogger(), func(context.Context) error { return nil }, nil)

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", rec.Code)
	}

	notReady := app.StartHealthServer(":0", testLogger(), func(context.Context) error { return errors.New("down") }, nil)
	rec = httptest.NewRecorder()
	notReady.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz with failing probe = %d, want 503", rec.Code)
	}
}

func TestHealthServer_MetricsOnlyWhenHandlerProvided(t *testing.T) {
	srv := app.StartHealthServer(":0", testLogger(), func(context.Context) error { return nil }, nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /metrics without handler = %d, want 404", rec.Code)
	}

	m := observability.NewMetrics("o1-api", "test")
	srv = app.StartHealthServer(":0", testLogger(), func(context.Context) error { return nil }, m.Handler())
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics with handler = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "o1_build_info") {
		t.Fatalf("metrics body missing o1_build_info:\n%s", rec.Body.String())
	}
}
