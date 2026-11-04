// Package api wires the HTTP routes and the middleware chain for the API.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"time"

	"o1/internal/httpx"
)

// Dependencies carries the services the router needs. Later tasks add
// more fields (stores, mailer, and so on).
type Dependencies struct {
	Logger *slog.Logger
}

// maxBodyBytes limits the size of request bodies (1 MiB).
const maxBodyBytes int64 = 1 << 20

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withRequestID assigns an X-Request-Id to every request and stores it in
// the request context.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			var buf [16]byte
			_, _ = rand.Read(buf[:])
			id = hex.EncodeToString(buf[:])
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

type requestIDKey struct{}

// RequestID returns the request ID assigned to req, if any.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withSecurityHeaders sets conservative default response headers.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// withBodyLimit rejects request bodies larger than 1 MiB.
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// requestLogging emits one structured log line per request.
func requestLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration", time.Since(start).String(),
				"request_id", RequestID(r.Context()),
			}
			logger.Log(r.Context(), slog.LevelInfo, "http request", attrs...)
		})
	}
}

// recoverPanics converts panics into a 500 problem response instead of
// crashing the server.
func recoverPanics(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Log(r.Context(), slog.LevelError, "panic recovered", "panic", rec, "request_id", RequestID(r.Context()))
					httpx.WriteProblem(w, httpx.Problem{
						Type:   "about:blank",
						Title:  "Internal Server Error",
						Status: http.StatusInternalServerError,
						Code:   "internal_error",
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// NewRouter builds the root HTTP handler.
func NewRouter(deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", httpx.HandleHealthz)

	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	var handler http.Handler = mux
	handler = withBodyLimit(handler)
	handler = withSecurityHeaders(handler)
	handler = requestLogging(logger)(handler)
	handler = recoverPanics(logger)(handler)
	handler = withRequestID(handler)
	return handler
}
