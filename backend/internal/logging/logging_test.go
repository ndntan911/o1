package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"o1/internal/logging"
)

func testSpanContext() trace.SpanContext {
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{
			0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		},
		SpanID: trace.SpanID{
			0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		},
		TraceFlags: trace.FlagsSampled,
	})
}

func decodeRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("logger wrote no record")
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("decode log record %q: %v", line, err)
	}
	return record
}

func TestLoggingTraceCorrelationAddsIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "production")
	sc := testSpanContext()
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "hello", "key", "value")

	record := decodeRecord(t, &buf)
	if got := record["trace_id"]; got != sc.TraceID().String() {
		t.Fatalf("trace_id = %v, want %s", got, sc.TraceID())
	}
	if got := record["span_id"]; got != sc.SpanID().String() {
		t.Fatalf("span_id = %v, want %s", got, sc.SpanID())
	}
	if got := record["msg"]; got != "hello" {
		t.Fatalf("msg = %v, want hello", got)
	}
	if got := record["key"]; got != "value" {
		t.Fatalf("key = %v, want value", got)
	}
}

func TestLoggingTraceCorrelationWithoutSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "production")

	logger.InfoContext(context.Background(), "plain", "key", "value")

	record := decodeRecord(t, &buf)
	if _, ok := record["trace_id"]; ok {
		t.Fatalf("record has trace_id without a span: %v", record)
	}
	if _, ok := record["span_id"]; ok {
		t.Fatalf("record has span_id without a span: %v", record)
	}
	if got := record["msg"]; got != "plain" {
		t.Fatalf("msg = %v, want plain", got)
	}
	if got := record["key"]; got != "value" {
		t.Fatalf("key = %v, want value", got)
	}
}

func TestLoggingTraceCorrelationTextOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "development")
	sc := testSpanContext()
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.With("component", "worker").InfoContext(ctx, "hello")

	out := buf.String()
	for _, want := range []string{
		"trace_id=" + sc.TraceID().String(),
		"span_id=" + sc.SpanID().String(),
		"component=worker",
		"msg=hello",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text output %q missing %q", out, want)
		}
	}
}

func TestLoggingTraceCorrelationKeepsDerivedLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, "production")
	sc := testSpanContext()
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.With("component", "api").InfoContext(ctx, "derived", "id", "123")

	record := decodeRecord(t, &buf)
	if got := record["trace_id"]; got != sc.TraceID().String() {
		t.Fatalf("trace_id = %v, want %s", got, sc.TraceID())
	}
	if got := record["span_id"]; got != sc.SpanID().String() {
		t.Fatalf("span_id = %v, want %s", got, sc.SpanID())
	}
	if got := record["component"]; got != "api" {
		t.Fatalf("component = %v, want api", got)
	}
	if got := record["id"]; got != "123" {
		t.Fatalf("id = %v, want 123", got)
	}
}

// Ensure the wrapper still satisfies slog.Handler.
var _ slog.Handler = logging.New(&bytes.Buffer{}, "test").Handler()
