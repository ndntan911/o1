// This file owns Sentry initialization, redaction, and error/panic capture.
package observability

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"

	"o1/internal/config"
)

const (
	// sentryFlushTimeout bounds the final event flush on shutdown.
	sentryFlushTimeout = 5 * time.Second
)

// CaptureHook observes a capture instead of sending it to Sentry. Tests install
// one with SetCaptureHookForTest so capture paths never need a DSN or network.
type CaptureHook func(ctx context.Context, err error, tags map[string]string)

var (
	captureHookMu sync.RWMutex
	captureHook   CaptureHook
)

// SetCaptureHookForTest installs a process-wide capture hook and returns a
// function that restores the previous hook. It is intended for tests only.
func SetCaptureHookForTest(hook CaptureHook) (restore func()) {
	captureHookMu.Lock()
	previous := captureHook
	captureHook = hook
	captureHookMu.Unlock()
	return func() {
		captureHookMu.Lock()
		captureHook = previous
		captureHookMu.Unlock()
	}
}

func currentCaptureHook() CaptureHook {
	captureHookMu.RLock()
	defer captureHookMu.RUnlock()
	return captureHook
}

// initSentry configures the Sentry SDK when a DSN is present. An empty DSN
// leaves Sentry disabled and returns false without touching the network.
// Performance tracing stays off because OTel owns traces.
func initSentry(cfg config.Config) (bool, error) {
	dsn := strings.TrimSpace(cfg.SentryDSN)
	if dsn == "" {
		return false, nil
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      sentryEnvironment(cfg),
		Release:          cfg.Release,
		TracesSampleRate: 0,
		AttachStacktrace: true,
		BeforeSend:       BeforeSend,
	})
	if err != nil {
		return false, fmt.Errorf("initialize sentry: %w", err)
	}
	return true, nil
}

// sentryEnvironment resolves the environment tag, preferring the explicit
// Sentry value and falling back to the process environment.
func sentryEnvironment(cfg config.Config) string {
	if cfg.SentryEnvironment != "" {
		return cfg.SentryEnvironment
	}
	if cfg.Env != "" {
		return cfg.Env
	}
	return "development"
}

// BeforeSend strips request payloads and user email from every event. Method
// and route path are preserved so an event stays actionable. It is exported so
// redaction can be tested without a DSN.
func BeforeSend(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}
	if event.Request != nil {
		event.Request.Data = ""
		event.Request.Headers = nil
		event.Request.Cookies = ""
		event.Request.QueryString = ""
		event.Request.URL = stripURLQuery(event.Request.URL)
	}
	event.User.Email = ""
	return event
}

// stripURLQuery removes the query string and fragment from a captured URL.
func stripURLQuery(raw string) string {
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil {
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		// A URL that embeds credentials must not leak them either.
		parsed.User = nil
		return parsed.String()
	}
	// Fall back to string surgery for values the URL parser rejects: a
	// malformed URL must never leak a query string.
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

// CaptureException reports an error to Sentry. Nil errors, canceled requests,
// and server shutdown errors are ignored. It is safe when Sentry is disabled.
func CaptureException(ctx context.Context, err error) {
	if shouldIgnore(err) {
		return
	}
	capture(ctx, err, nil, sentry.LevelError)
}

// CapturePanic reports a recovered panic. The caller decides whether to
// re-panic; this function never does. A nil recover value is a no-op.
func CapturePanic(ctx context.Context, rec any) {
	if rec == nil {
		return
	}
	capture(ctx, panicError(rec), nil, sentry.LevelFatal)
}

// capture sends one event through the test hook when installed, otherwise
// through the Sentry hub in ctx or the current hub.
func capture(ctx context.Context, err error, tags map[string]string, level sentry.Level) {
	if err == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if hook := currentCaptureHook(); hook != nil {
		hook(ctx, err, maps.Clone(tags))
		return
	}

	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		hub = sentry.CurrentHub()
	}
	if hub == nil || hub.Client() == nil {
		return
	}
	// Capture on a clone: a shared hub's scope stack is not safe to mutate
	// from concurrent request or job goroutines.
	local := hub.Clone()
	local.Scope().SetLevel(level)
	for key, value := range tags {
		local.Scope().SetTag(key, value)
	}
	local.CaptureException(err)
}

// shouldIgnore reports whether an error is expected during normal shutdown or
// request cancellation and would only add noise.
func shouldIgnore(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, http.ErrServerClosed)
}

// panicError turns a recover value into an error without losing the original
// error type.
func panicError(rec any) error {
	if err, ok := rec.(error); ok {
		if strings.HasPrefix(err.Error(), "panic:") {
			return err
		}
		return fmt.Errorf("panic: %w", err)
	}
	return fmt.Errorf("panic: %v", rec)
}
