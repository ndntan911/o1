package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/getsentry/sentry-go"

	"o1/internal/observability"
)

// TestAPIPanicCapturedOnce verifies recoverPanics writes a 500, captures the
// panic exactly once, and does not re-panic.
func TestAPIPanicCapturedOnce(t *testing.T) {
	var captures atomic.Int32
	restore := observability.SetCaptureHookForTest(func(context.Context, error, map[string]string, map[string]sentry.Context) {
		captures.Add(1)
	})
	t.Cleanup(restore)

	handler := recoverPanics(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)(http.HandlerFunc(
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

// TestAPIPanicTagsAreBounded verifies the event receives method, matched route
// pattern, and request ID without any raw path or query string.
func TestAPIPanicTagsAreBounded(t *testing.T) {
	var mu sync.Mutex
	var gotTags map[string]string
	restore := observability.SetCaptureHookForTest(func(_ context.Context, _ error, tags map[string]string, _ map[string]sentry.Context) {
		mu.Lock()
		defer mu.Unlock()
		gotTags = tags
	})
	t.Cleanup(restore)

	resolveRoute := func(r *http.Request) string {
		if r.URL.Path == "/api/v1/monitors" {
			return "GET /api/v1/monitors"
		}
		return ""
	}
	handler := withRequestID(recoverPanics(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		resolveRoute,
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitors?token=secret&email=alice%40example.com", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=abc123")
	req.Header.Set("X-Request-Id", "req-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	mu.Lock()
	tags := gotTags
	mu.Unlock()

	if tags["method"] != http.MethodGet {
		t.Fatalf("method tag = %q, want GET", tags["method"])
	}
	if tags["route"] != "GET /api/v1/monitors" {
		t.Fatalf("route tag = %q, want the matched pattern", tags["route"])
	}
	if tags["request_id"] != "req-123" {
		t.Fatalf("request_id tag = %q, want req-123", tags["request_id"])
	}
	for key, value := range tags {
		switch key {
		case "method", "route", "request_id":
		default:
			t.Errorf("unexpected tag %q = %q", key, value)
		}
	}
}

// TestAPIPanicRouteFallsBackToUnmatched verifies a nil or empty resolver never
// leaks the raw path.
func TestAPIPanicRouteFallsBackToUnmatched(t *testing.T) {
	for name, resolveRoute := range map[string]func(*http.Request) string{
		"nil":   nil,
		"empty": func(*http.Request) string { return "" },
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var gotTags map[string]string
			restore := observability.SetCaptureHookForTest(func(_ context.Context, _ error, tags map[string]string, _ map[string]sentry.Context) {
				mu.Lock()
				defer mu.Unlock()
				gotTags = tags
			})
			t.Cleanup(restore)

			handler := recoverPanics(slog.New(slog.NewTextHandler(io.Discard, nil)), resolveRoute)(
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
			)
			handler.ServeHTTP(httptest.NewRecorder(),
				httptest.NewRequest(http.MethodGet, "/api/v1/secret/raw/path", nil))

			mu.Lock()
			tags := gotTags
			mu.Unlock()
			if tags["route"] != "unmatched" {
				t.Fatalf("route tag = %q, want unmatched", tags["route"])
			}
			if tags["request_id"] != "" {
				t.Fatalf("request_id tag = %q, want absent", tags["request_id"])
			}
		})
	}
}

// TestAPIPanicRecoveryKeepsServing verifies the boundary only fails the
// panicking request: the next request still reaches the handler.
func TestAPIPanicRecoveryKeepsServing(t *testing.T) {
	restore := observability.SetCaptureHookForTest(func(context.Context, error, map[string]string, map[string]sentry.Context) {})
	t.Cleanup(restore)

	var served atomic.Int32
	handler := recoverPanics(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)(http.HandlerFunc(
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
