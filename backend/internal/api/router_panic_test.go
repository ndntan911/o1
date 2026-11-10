package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"o1/internal/observability"
)

// TestAPIPanicCapturedOnce verifies recoverPanics writes a 500, captures the
// panic exactly once, and does not re-panic.
func TestAPIPanicCapturedOnce(t *testing.T) {
	var captures atomic.Int32
	restore := observability.SetCaptureHookForTest(func(context.Context, error, map[string]string) {
		captures.Add(1)
	})
	t.Cleanup(restore)

	handler := recoverPanics(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			panic("boom")
		},
	))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitors", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := captures.Load(); got != 1 {
		t.Fatalf("captures = %d, want 1", got)
	}
}

// TestAPIPanicRecoveryKeepsServing verifies the boundary only fails the
// panicking request: the next request still reaches the handler.
func TestAPIPanicRecoveryKeepsServing(t *testing.T) {
	restore := observability.SetCaptureHookForTest(func(context.Context, error, map[string]string) {})
	t.Cleanup(restore)

	var served atomic.Int32
	handler := recoverPanics(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			if served.Add(1) == 1 {
				panic("boom")
			}
			w.WriteHeader(http.StatusNoContent)
		},
	))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/monitors", nil))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d, want 500", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/monitors", nil))
	if second.Code != http.StatusNoContent {
		t.Fatalf("second status = %d, want 204", second.Code)
	}
}
