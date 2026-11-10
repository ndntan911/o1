// Package logging builds the application logger. In production the log
// output is JSON; otherwise a human-readable text format is used.
package logging

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// New returns a slog.Logger writing to w. JSON records are only emitted
// when env is "production". Records logged with a context carrying a valid
// span include trace_id and span_id for log/trace correlation.
func New(w io.Writer, env string) *slog.Logger {
	var handler slog.Handler
	if env == "production" {
		handler = slog.NewJSONHandler(w, nil)
	} else {
		handler = slog.NewTextHandler(w, nil)
	}
	return slog.New(&traceHandler{Handler: handler})
}

// traceHandler decorates every record with the active trace and span IDs. The
// IDs are only present when the context carries a valid span, so existing log
// output is unchanged everywhere else.
type traceHandler struct {
	slog.Handler
}

func (h *traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if spanContext := trace.SpanFromContext(ctx).SpanContext(); spanContext.IsValid() {
		record = record.Clone()
		record.AddAttrs(
			slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, record)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithGroup(name)}
}
