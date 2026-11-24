package main

import (
	"context"
	"testing"
	"time"
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
