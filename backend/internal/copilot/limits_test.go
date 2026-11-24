package copilot

import (
	"testing"
	"time"

	"o1/internal/config"
)

func TestLimitsFromConfig_MapsEveryCopilotField(t *testing.T) {
	cfg := config.Config{
		CopilotMaxModelCalls:   9,
		CopilotMaxToolCalls:    11,
		CopilotMaxResultBytes:  4096,
		CopilotMaxOutputTokens: 900,
		CopilotMaxInputTokens:  12000,
		CopilotTimeout:         45 * time.Second,
		CopilotCallTimeout:     12 * time.Second,
	}

	got := LimitsFromConfig(cfg)
	want := Limits{
		MaxModelCalls:   9,
		MaxToolCalls:    11,
		MaxResultBytes:  4096,
		MaxOutputTokens: 900,
		MaxInputTokens:  12000,
		Timeout:         45 * time.Second,
		CallTimeout:     12 * time.Second,
	}
	if got != want {
		t.Fatalf("LimitsFromConfig = %+v, want %+v", got, want)
	}
}

func TestLimitsFromConfig_DefaultsForZeroValues(t *testing.T) {
	if got := LimitsFromConfig(config.Config{}); got != DefaultLimits() {
		t.Fatalf("LimitsFromConfig(zero) = %+v, want %+v", got, DefaultLimits())
	}
}
