package main

import (
	"context"
	"testing"
	"time"

	"o1/internal/config"
)

func TestProbeLimiterFractionalRPSAllowsProbes(t *testing.T) {
	limiter := probeLimiter(0.5)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := limiter.Wait(ctx); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if limiter.Burst() != 1 {
		t.Fatalf("burst = %d, want 1", limiter.Burst())
	}
}

func TestInvestigationModelUsesCopilotModel(t *testing.T) {
	if got := investigationModel("copilot", "insights"); got != "copilot" {
		t.Fatalf("investigationModel with copilot model = %q, want copilot", got)
	}
	// Config normally fills CopilotModel from AIModel; the fallback keeps a
	// hand-built config from recording an empty model.
	if got := investigationModel("", "insights"); got != "insights" {
		t.Fatalf("investigationModel fallback = %q, want insights", got)
	}
}

func TestCopilotProviderSettingsUseCopilotTimeoutAndModel(t *testing.T) {
	settings := copilotProviderSettings(config.Config{
		AIProvider:         "openai",
		AIBaseURL:          "https://example.test/v1",
		AIAPIKey:           "key",
		AIModel:            "insights-model",
		AITimeout:          15 * time.Second,
		CopilotModel:       "copilot-model",
		CopilotCallTimeout: 30 * time.Second,
	})
	if settings.Timeout != 30*time.Second {
		t.Fatalf("timeout = %s, want the copilot call timeout", settings.Timeout)
	}
	if settings.Model != "copilot-model" {
		t.Fatalf("model = %q, want copilot-model", settings.Model)
	}

	fallback := copilotProviderSettings(config.Config{
		AIModel:            "insights-model",
		AITimeout:          15 * time.Second,
		CopilotCallTimeout: 30 * time.Second,
	})
	if fallback.Model != "insights-model" {
		t.Fatalf("fallback model = %q, want insights-model", fallback.Model)
	}
	if fallback.Timeout != 30*time.Second {
		t.Fatalf("fallback timeout = %s, want the copilot call timeout", fallback.Timeout)
	}
}
