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
