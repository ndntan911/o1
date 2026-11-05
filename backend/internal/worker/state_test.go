package worker

import "testing"

func TestAdvance(t *testing.T) {
	tests := []struct {
		name    string
		in      State
		success bool
		want    State
	}{
		{"unknown first success", State{Status: "unknown"}, true, State{Status: "up", Successes: 1}},
		{"unknown confirms down", State{Status: "unknown", Failures: 1}, false, State{Status: "down", Failures: 2}},
		{"up confirms down", State{Status: "up", Failures: 1}, false, State{Status: "down", Failures: 2}},
		{"down recovers", State{Status: "down", Successes: 1}, true, State{Status: "up", Successes: 2}},
		{"down stays down", State{Status: "down"}, false, State{Status: "down", Failures: 1}},
		{"up failure below threshold", State{Status: "up"}, false, State{Status: "up", Failures: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Advance(tc.in, tc.success, 2, 2); got != tc.want {
				t.Fatalf("Advance = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAdvance_ThresholdOne(t *testing.T) {
	if got := Advance(State{Status: "unknown"}, false, 1, 1); got.Status != "down" {
		t.Fatalf("threshold 1 = %+v", got)
	}
	if got := Advance(State{Status: "down"}, true, 1, 1); got.Status != "up" {
		t.Fatalf("recovery 1 = %+v", got)
	}
}

func TestAdvance_CapsCounters(t *testing.T) {
	got := Advance(State{Status: "down", Failures: 1000}, false, 2, 2)
	if got.Failures != 1000 {
		t.Fatalf("failures = %d, want capped at 1000", got.Failures)
	}
	got = Advance(State{Status: "up", Successes: 1000}, true, 2, 2)
	if got.Successes != 1000 {
		t.Fatalf("successes = %d, want capped at 1000", got.Successes)
	}
}
