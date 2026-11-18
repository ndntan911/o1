package api

import (
	"math"
	"testing"

	"o1/internal/config"
)

func TestRateLimitHelpers(t *testing.T) {
	zero := config.Config{}
	if got := publicRPS(zero); math.Abs(got-1.0) > 0.001 {
		t.Fatalf("default public rps = %v, want 1", got)
	}
	if got := publicBurst(zero); got != 20 {
		t.Fatalf("default public burst = %d, want 20", got)
	}
	if got := apiKeyRPS(zero); math.Abs(got-10.0) > 0.001 {
		t.Fatalf("default api key rps = %v, want 10", got)
	}
	if got := apiKeyBurst(zero); got != 60 {
		t.Fatalf("default api key burst = %d, want 60", got)
	}

	cfg := config.Config{
		PublicRatePerMinute: 1200, PublicRateBurst: 200,
		APIKeyRatePerMinute: 6000, APIKeyRateBurst: 600,
	}
	if got := publicRPS(cfg); math.Abs(got-20.0) > 0.001 {
		t.Fatalf("public rps = %v, want 20", got)
	}
	if got := apiKeyRPS(cfg); math.Abs(got-100.0) > 0.001 {
		t.Fatalf("api key rps = %v, want 100", got)
	}
}
