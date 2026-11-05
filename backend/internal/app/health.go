package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"o1/internal/httpx"
)

// StartHealthServer builds an HTTP server exposing /healthz and /readyz. The
// caller starts it and shuts it down; ready is probed with a 2s timeout.
func StartHealthServer(addr string, logger *slog.Logger, ready func(context.Context) error) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			httpx.WriteProblem(w, httpx.Problem{
				Type:   "about:blank",
				Title:  "Service Unavailable",
				Status: http.StatusServiceUnavailable,
				Code:   "not_ready",
			})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}
