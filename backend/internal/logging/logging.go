// Package logging builds the application logger. In production the log
// output is JSON; otherwise a human-readable text format is used.
package logging

import (
	"io"
	"log/slog"
)

// New returns a slog.Logger writing to w. JSON records are only emitted
// when env is "production".
func New(w io.Writer, env string) *slog.Logger {
	if env == "production" {
		return slog.New(slog.NewJSONHandler(w, nil))
	}
	return slog.New(slog.NewTextHandler(w, nil))
}
