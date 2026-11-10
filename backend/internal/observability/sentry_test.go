package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"o1/internal/config"
)

// captureRecorder installs a test capture hook and records every capture.
type captureRecorder struct {
	mu       sync.Mutex
	errs     []error
	tags     []map[string]string
	contexts []map[string]sentry.Context
	restore  func()
}

func newCaptureRecorder(t *testing.T) *captureRecorder {
	t.Helper()
	r := &captureRecorder{}
	r.restore = SetCaptureHookForTest(func(_ context.Context, err error, tags map[string]string, contexts map[string]sentry.Context) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.errs = append(r.errs, err)
		r.tags = append(r.tags, tags)
		r.contexts = append(r.contexts, contexts)
	})
	t.Cleanup(r.restore)
	return r
}

func (r *captureRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

func (r *captureRecorder) lastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.errs) == 0 {
		return nil
	}
	return r.errs[len(r.errs)-1]
}

func (r *captureRecorder) lastTags() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tags) == 0 {
		return nil
	}
	return r.tags[len(r.tags)-1]
}

func (r *captureRecorder) lastContexts() map[string]sentry.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.contexts) == 0 {
		return nil
	}
	return r.contexts[len(r.contexts)-1]
}

func TestSentryEmptyDSNIsDisabled(t *testing.T) {
	p, err := New(context.Background(), config.Config{Release: "test"}, nil, "o1-api")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.SentryEnabled {
		t.Fatal("Provider.SentryEnabled = true with an empty O1_SENTRY_DSN")
	}
	// Capture must be a safe no-op when Sentry was never initialized.
	CaptureException(context.Background(), errors.New("boom"))
	CapturePanic(context.Background(), "boom")
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSentryInvalidDSNFailsStartup(t *testing.T) {
	if _, err := New(context.Background(), config.Config{SentryDSN: "://not-a-dsn"}, nil, "o1-api"); err == nil {
		t.Fatal("New accepted an invalid Sentry DSN")
	}
}

func TestSentryEnvironmentFallback(t *testing.T) {
	if got := sentryEnvironment(config.Config{SentryEnvironment: "staging", Env: "production"}); got != "staging" {
		t.Fatalf("sentryEnvironment = %q, want staging", got)
	}
	if got := sentryEnvironment(config.Config{Env: "production"}); got != "production" {
		t.Fatalf("sentryEnvironment = %q, want production", got)
	}
	if got := sentryEnvironment(config.Config{}); got != "development" {
		t.Fatalf("sentryEnvironment = %q, want development", got)
	}
}

func TestSentryBeforeSendRedactsSensitiveFields(t *testing.T) {
	event := &sentry.Event{
		Request: &sentry.Request{ //nolint:gosec // fake credentials are the redaction fixture, not real secrets
			URL:         "https://user:pass@api.example.com/api/v1/monitors/123/results?token=secret&email=alice%40example.com",
			Method:      http.MethodGet,
			Data:        `{"email":"alice@example.com","password":"hunter2"}`,
			QueryString: "token=secret&email=alice%40example.com",
			Cookies:     "session=abc123",
			Headers: map[string]string{
				"Authorization": "Bearer secret",
				"Cookie":        "session=abc123",
				"Accept":        "application/json",
			},
		},
		User: sentry.User{Email: "alice@example.com", ID: "user-1"},
	}

	got := BeforeSend(event, nil)
	if got == nil {
		t.Fatal("BeforeSend dropped the event")
	}
	if got.Request == nil {
		t.Fatal("BeforeSend removed the request")
	}
	if got.Request.Data != "" {
		t.Errorf("Request.Data = %q, want empty", got.Request.Data)
	}
	if got.Request.Headers != nil {
		t.Errorf("Request.Headers = %v, want nil", got.Request.Headers)
	}
	if got.Request.Cookies != "" {
		t.Errorf("Request.Cookies = %q, want empty", got.Request.Cookies)
	}
	if got.Request.QueryString != "" {
		t.Errorf("Request.QueryString = %q, want empty", got.Request.QueryString)
	}
	if strings.Contains(got.Request.URL, "?") || strings.Contains(got.Request.URL, "secret") {
		t.Errorf("Request.URL = %q, want query string removed", got.Request.URL)
	}
	if strings.Contains(got.Request.URL, "user") || strings.Contains(got.Request.URL, "pass") {
		t.Errorf("Request.URL = %q, want embedded credentials removed", got.Request.URL)
	}
	if got.Request.Method != http.MethodGet {
		t.Errorf("Request.Method = %q, want %q", got.Request.Method, http.MethodGet)
	}
	if got.User.Email != "" {
		t.Errorf("User.Email = %q, want empty", got.User.Email)
	}
	if got.User.ID != "user-1" {
		t.Errorf("User.ID = %q, want user-1", got.User.ID)
	}
	if !strings.Contains(got.Request.URL, "/api/v1/monitors/123/results") {
		t.Errorf("Request.URL = %q, want route path preserved", got.Request.URL)
	}
}

func TestSentryBeforeSendHandlesNil(t *testing.T) {
	if got := BeforeSend(nil, nil); got != nil {
		t.Fatalf("BeforeSend(nil) = %v, want nil", got)
	}
}

func TestSentryDropsCanceledAndServerClosed(t *testing.T) {
	rec := newCaptureRecorder(t)

	CaptureException(context.Background(), context.Canceled)
	CaptureException(context.Background(), fmt.Errorf("request aborted: %w", context.Canceled))
	CaptureException(context.Background(), http.ErrServerClosed)
	CaptureException(context.Background(), fmt.Errorf("server stopping: %w", http.ErrServerClosed))
	if got := rec.count(); got != 0 {
		t.Fatalf("captured %d ignored errors, want 0", got)
	}

	want := errors.New("probe failed")
	CaptureException(context.Background(), want)
	if got := rec.count(); got != 1 {
		t.Fatalf("captured %d errors, want 1", got)
	}
	if got := rec.lastError(); !errors.Is(got, want) {
		t.Fatalf("captured error = %v, want %v", got, want)
	}
}

func TestSentryCapturePanicUsesPanicValue(t *testing.T) {
	rec := newCaptureRecorder(t)
	CapturePanic(context.Background(), "boom")
	if got := rec.count(); got != 1 {
		t.Fatalf("captured %d panics, want 1", got)
	}
	if got := rec.lastError(); got == nil || !strings.Contains(got.Error(), "boom") {
		t.Fatalf("captured error = %v, want it to mention the panic value", got)
	}

	CapturePanic(context.Background(), errors.New("wrapped panic"))
	if got := rec.count(); got != 2 {
		t.Fatalf("captured %d panics, want 2", got)
	}
	if got := rec.lastError(); got == nil || !strings.Contains(got.Error(), "wrapped panic") {
		t.Fatalf("captured error = %v, want the panic error", got)
	}

	CapturePanic(context.Background(), nil)
	if got := rec.count(); got != 2 {
		t.Fatalf("nil panic captured %d total, want 2", got)
	}
}

func TestSentryDisabledCaptureIsSafe(t *testing.T) {
	if p, err := New(context.Background(), config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "o1-worker"); err != nil {
		t.Fatalf("New: %v", err)
	} else if p.SentryEnabled {
		t.Fatal("SentryEnabled = true without a DSN")
	}
	// These are the paths used with every observability env var empty: they
	// must not panic or touch the network.
	CaptureException(context.Background(), errors.New("boom"))
	CapturePanic(context.Background(), 42)
}

// captureTransport records events in memory. It is bound to a per-test hub so
// tests can assert the assembled event without a real DSN or network.
type captureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *captureTransport) Configure(sentry.ClientOptions) {}

func (t *captureTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *captureTransport) Flush(time.Duration) bool { return true }

func (t *captureTransport) FlushWithContext(context.Context) bool { return true }

func (t *captureTransport) Close() {}

func (t *captureTransport) snapshot() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event(nil), t.events...)
}

func newTestSentryContext(t *testing.T) (context.Context, *captureTransport) {
	t.Helper()
	transport := &captureTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:              "https://public@example.com/1",
		Transport:        transport,
		AttachStacktrace: true,
		BeforeSend:       BeforeSend,
	})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	t.Cleanup(client.Close)
	hub := sentry.NewHub(client, sentry.NewScope())
	return sentry.SetHubOnContext(context.Background(), hub), transport
}

func TestSentryCapturePanicWithTagsReachesEvent(t *testing.T) {
	ctx, transport := newTestSentryContext(t)
	tags := map[string]string{
		"method":     http.MethodGet,
		"route":      "GET /api/v1/monitors",
		"request_id": "req-123",
	}

	CapturePanicWithTags(ctx, "boom", tags)

	events := transport.snapshot()
	if len(events) != 1 {
		t.Fatalf("captured %d events, want 1", len(events))
	}
	event := events[0]
	for key, want := range tags {
		if got := event.Tags[key]; got != want {
			t.Errorf("event tag %q = %q, want %q", key, got, want)
		}
	}
	if event.Level != sentry.LevelFatal {
		t.Errorf("event level = %v, want fatal", event.Level)
	}
	trace, ok := event.Contexts["panic"]["trace"]
	if !ok || strings.TrimSpace(trace.(string)) == "" {
		t.Errorf("event panic context trace = %v (present %v), want a stack trace", trace, ok)
	}
}
