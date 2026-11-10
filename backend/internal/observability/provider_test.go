package observability_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"o1/internal/config"
	"o1/internal/observability"
)

func TestObservabilityProvider_EmptyConfigIsNoop(t *testing.T) {
	p, err := observability.New(context.Background(), config.Config{Release: "dev"}, nil, "o1-api")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p == nil {
		t.Fatal("New returned a nil provider")
	}
	if p.Metrics == nil {
		t.Fatal("Provider.Metrics is nil")
	}
	if body := gatherText(t, p.Metrics.Registry()); !strings.Contains(body, "o1_build_info") {
		t.Fatalf("registry missing o1_build_info:\n%s", body)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestObservabilityProvider_NilLoggerIsSafe(t *testing.T) {
	logger := (*slog.Logger)(nil)
	p, err := observability.New(context.Background(), config.Config{}, logger, "o1-worker")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Metrics == nil {
		t.Fatal("Provider.Metrics is nil")
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
