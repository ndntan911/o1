package copilot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"o1/internal/ai"
)

func TestLoadFixtures(t *testing.T) {
	fs, err := LoadFixtures("testdata/eval")
	if err != nil || len(fs) != 10 {
		t.Fatalf("%d, %v", len(fs), err)
	}
	for _, f := range fs {
		if f.Name == "" || len(f.Want) == 0 {
			t.Fatalf("bad fixture %+v", f)
		}
	}
}

// TestLoadFixtures_CoversSpecCases pins the ten cases from the design's
// Evaluation section so a renamed or deleted fixture does not silently shrink
// the evaluation.
func TestLoadFixtures_CoversSpecCases(t *testing.T) {
	fs, err := LoadFixtures("testdata/eval")
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}
	names := make(map[string]struct{}, len(fs))
	for _, fixture := range fs {
		names[fixture.Name] = struct{}{}
	}
	for _, want := range []string{
		"timeout",
		"dns",
		"one_success_in_failures",
		"no_checks",
		"tool_error",
		"invented_tool_call_id",
		"wrong_pointer_value",
		"loop_limit",
		"injection",
		"oversized_result",
	} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing fixture %q", want)
		}
	}
}

func evalEvidence(pointer, value string) Evidence {
	return Evidence{
		ToolCallID: "call_1",
		Pointer:    pointer,
		Value:      json.RawMessage(value),
	}
}

func evalAnswer(evidence []Evidence, confidence string) ai.ChatResponse {
	result := Result{
		Sufficient: len(evidence) > 0,
		Assessment: "The evidence points at one cause.",
		Hypotheses: []Hypothesis{},
	}
	if len(evidence) > 0 {
		result.Hypotheses = []Hypothesis{{
			Rank:       1,
			Cause:      "a cause supported by the cited result",
			Confidence: confidence,
			Evidence:   evidence,
		}}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return ai.ChatResponse{Text: string(raw), FinishReason: "stop"}
}

func checksCall(id string) ai.ChatResponse {
	return ai.ChatResponse{
		ToolCalls: []ai.ToolCall{{
			ID:        id,
			Name:      "o1_get_recent_checks",
			Arguments: json.RawMessage(`{"monitor_id":"22222222-2222-4222-8222-222222222222"}`),
		}},
		FinishReason: "tool_calls",
	}
}

func findFixture(t *testing.T, fixtures []Fixture, name string) Fixture {
	t.Helper()
	for _, fixture := range fixtures {
		if fixture.Name == name {
			return fixture
		}
	}
	t.Fatalf("fixture %q not found", name)
	return Fixture{}
}

// TestCheckFixture_ScriptedStub runs the ten fixtures through the real agent
// loop with scripted model trajectories. It asserts the checker's verdict, not
// the model's prose: a good trajectory passes, an invented citation or an
// out-of-allowlist call fails.
func TestCheckFixture_ScriptedStub(t *testing.T) {
	fixtures, err := LoadFixtures("testdata/eval")
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}

	loopScript := make([]ai.ChatResponse, 0, 5)
	for i := 0; i < 5; i++ {
		loopScript = append(loopScript, ai.ChatResponse{
			ToolCalls: []ai.ToolCall{
				{ID: "loop-" + string(rune('a'+i*2)), Name: "o1_get_recent_checks", Arguments: json.RawMessage(`{}`)},
				{ID: "loop-" + string(rune('b'+i*2)), Name: "o1_get_recent_checks", Arguments: json.RawMessage(`{}`)},
			},
			FinishReason: "tool_calls",
		})
	}

	cases := []struct {
		name            string
		fixture         string
		script          []ai.ChatResponse
		wantPass        bool
		wantStatus      Status
		wantFailure     string
		wantUnsupported int
	}{
		{
			name:       "timeout cites the failure class",
			fixture:    "timeout",
			script:     []ai.ChatResponse{checksCall("call_1"), evalAnswer([]Evidence{evalEvidence("/data/checks/0/failure_class", `"timeout"`)}, "high")},
			wantPass:   true,
			wantStatus: StatusOK,
		},
		{
			name:        "timeout cites the wrong property",
			fixture:     "timeout",
			script:      []ai.ChatResponse{checksCall("call_1"), evalAnswer([]Evidence{evalEvidence("/data/checks/0/status", `"down"`)}, "high")},
			wantStatus:  StatusOK,
			wantFailure: "cites_failure_class",
		},
		{
			name:        "dns with an invented tool call id",
			fixture:     "invented_tool_call_id",
			script:      []ai.ChatResponse{checksCall("call_1"), inventedAnswer("/data/checks/0/failure_class", `"connection"`)},
			wantStatus:  StatusInsufficient,
			wantFailure: "cites_failure_class",
			// The citation named a call that never happened, so Verify dropped
			// it and the whole answer became insufficient.
			wantUnsupported: 1,
		},
		{
			name:        "wrong pointer value is dropped",
			fixture:     "wrong_pointer_value",
			script:      []ai.ChatResponse{checksCall("call_1"), evalAnswer([]Evidence{evalEvidence("/data/checks/0/failure_class", `"dns"`)}, "high")},
			wantStatus:  StatusInsufficient,
			wantFailure: "cites_failure_class",
			// The pointer resolved to tls, not dns, so the citation was dropped.
			wantUnsupported: 1,
		},
		{
			name:       "no checks refuses",
			fixture:    "no_checks",
			script:     []ai.ChatResponse{checksCall("call_1"), evalAnswer(nil, "")},
			wantPass:   true,
			wantStatus: StatusInsufficient,
		},
		{
			name:       "tool error does not abort the run",
			fixture:    "tool_error",
			script:     []ai.ChatResponse{checksCall("call_1"), evalAnswer(nil, "")},
			wantPass:   true,
			wantStatus: StatusInsufficient,
		},
		{
			name:       "one success is cited without high confidence",
			fixture:    "one_success_in_failures",
			script:     []ai.ChatResponse{checksCall("call_1"), evalAnswer([]Evidence{evalEvidence("/data/checks/2/status", `"up"`)}, "medium")},
			wantPass:   true,
			wantStatus: StatusOK,
		},
		{
			name:        "one success with high confidence fails the property",
			fixture:     "one_success_in_failures",
			script:      []ai.ChatResponse{checksCall("call_1"), evalAnswer([]Evidence{evalEvidence("/data/checks/2/status", `"up"`)}, "high")},
			wantStatus:  StatusOK,
			wantFailure: "not_high_confidence",
		},
		{
			name:       "endless tools stop at the cap",
			fixture:    "loop_limit",
			script:     loopScript,
			wantPass:   true,
			wantStatus: StatusIncomplete,
		},
		{
			name:    "injection cannot name a tool outside the allowlist",
			fixture: "injection",
			script: []ai.ChatResponse{
				{
					ToolCalls: []ai.ToolCall{{
						ID:        "call_1",
						Name:      "o1_delete_monitor",
						Arguments: json.RawMessage(`{"monitor_id":"*"}`),
					}},
					FinishReason: "tool_calls",
				},
				evalAnswer(nil, ""),
			},
			wantStatus:  StatusInsufficient,
			wantFailure: "refuses_injection",
		},
		{
			name:       "oversized result is truncated",
			fixture:    "oversized_result",
			script:     []ai.ChatResponse{checksCall("call_1"), evalAnswer(nil, "")},
			wantPass:   true,
			wantStatus: StatusInsufficient,
		},
		{
			name:        "answer without a checks call fails",
			fixture:     "timeout",
			script:      []ai.ChatResponse{evalAnswer(nil, "")},
			wantStatus:  StatusInsufficient,
			wantFailure: "calls_checks",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := findFixture(t, fixtures, tc.fixture)

			out := NewRunner(&ai.Stub{ChatScript: tc.script}, fixtureCaller{results: fixture.ToolResults},
				"stub", DefaultLimits(), time.Now).Run(context.Background(), evalWorkspaceID, fixture.Seed)

			err := CheckFixture(fixture, out)
			if tc.wantPass && err != nil {
				t.Fatalf("CheckFixture: %v", err)
			}
			if !tc.wantPass {
				if err == nil {
					t.Fatalf("CheckFixture passed, want failure containing %q", tc.wantFailure)
				}
				if !strings.Contains(err.Error(), tc.wantFailure) {
					t.Fatalf("CheckFixture error = %q, want it to contain %q", err, tc.wantFailure)
				}
			}

			rows, runErr := RunLive(context.Background(), &ai.Stub{ChatScript: tc.script}, []Fixture{fixture}, "stub")
			if runErr != nil {
				t.Fatalf("RunLive: %v", runErr)
			}
			if len(rows) != 1 {
				t.Fatalf("RunLive returned %d rows, want 1", len(rows))
			}
			row := rows[0]
			if row.Status != tc.wantStatus {
				t.Errorf("row.Status = %s, want %s", row.Status, tc.wantStatus)
			}
			if row.Passed != tc.wantPass {
				t.Errorf("row.Passed = %v, want %v (failure %q)", row.Passed, tc.wantPass, row.Failure)
			}
			if row.UnsupportedCitations != tc.wantUnsupported {
				t.Errorf("row.UnsupportedCitations = %d, want %d", row.UnsupportedCitations, tc.wantUnsupported)
			}
		})
	}
}

func inventedAnswer(pointer, value string) ai.ChatResponse {
	evidence := evalEvidence(pointer, value)
	evidence.ToolCallID = "call_never_made"
	return evalAnswer([]Evidence{evidence}, "high")
}

// TestRunLive_AllFixtures uses the stub's deterministic demo trajectory for the
// whole set, so the harness covers every fixture in one pass.
func TestRunLive_AllFixtures(t *testing.T) {
	fixtures, err := LoadFixtures("testdata/eval")
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}
	rows, err := RunLive(context.Background(), &ai.Stub{}, fixtures, "stub")
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if len(rows) != len(fixtures) {
		t.Fatalf("RunLive returned %d rows for %d fixtures", len(rows), len(fixtures))
	}
	for _, row := range rows {
		if row.Fixture == "" {
			t.Errorf("row without a fixture name: %+v", row)
		}
		if row.Failure != "" && row.Passed {
			t.Errorf("%s: passed row carries a failure: %s", row.Fixture, row.Failure)
		}
	}

	timeout := rows[0]
	for i, row := range rows {
		if row.Fixture == "timeout" {
			timeout = rows[i]
			break
		}
	}
	if timeout.ToolCalls == 0 {
		t.Errorf("timeout row made no tool calls: %+v", timeout)
	}
}

func TestRunLive_RequiresProvider(t *testing.T) {
	if _, err := RunLive(context.Background(), nil, []Fixture{{Name: "x"}}, "stub"); err == nil {
		t.Fatal("RunLive accepted a nil provider")
	}
}
