package copilot

import (
	"encoding/json"
	"testing"
)

func verifyEvidence(toolCallID, pointer, value string) Evidence {
	return Evidence{ToolCallID: toolCallID, Pointer: pointer, Value: json.RawMessage(value)}
}

func TestVerify_CitationRules(t *testing.T) {
	trace := Trace{Calls: []ToolCallRecord{
		{
			ID:     "call_1",
			Result: json.RawMessage(`{"data":{"checks":[{"failure_class":"timeout"}]},"meta":{}}`),
		},
		{
			ID:      "call_failed",
			Result:  json.RawMessage(`{"data":null,"meta":{},"is_error":true,"error_code":"internal"}`),
			IsError: true,
		},
	}}

	cases := []struct {
		name         string
		toolCallID   string
		pointer      string
		value        string
		wantVerified bool
	}{
		{"valid", "call_1", "/data/checks/0/failure_class", `"timeout"`, true},
		{"bad pointer", "call_1", "/data/checks/9/failure_class", `"timeout"`, false},
		{"wrong value", "call_1", "/data/checks/0/failure_class", `"dns"`, false},
		{"unknown call", "call_invented", "/data/checks/0/failure_class", `"timeout"`, false},
		{"failed call", "call_failed", "/data", `null`, false},
		{"pointer without root", "call_1", "data/checks/0/failure_class", `"timeout"`, false},
		{"missing array index", "call_1", "/data/checks/not-a-number", `"timeout"`, false},
		{"escaped slash and tilde", "call_escaped", "/data/a~1b/m~0n", `"yes"`, true},
		{"whole document", "call_1", "", `{"data":{"checks":[{"failure_class":"timeout"}]},"meta":{}}`, true},
		{"null value", "call_null", "/value", `null`, true},
		{"number type mismatch", "call_number", "/value", `"2"`, false},
		{"number value", "call_number", "/value", `2`, true},
	}
	trace.Calls = append(trace.Calls,
		ToolCallRecord{
			ID:     "call_escaped",
			Result: json.RawMessage(`{"data":{"a/b":{"m~n":"yes"}}}`),
		},
		ToolCallRecord{
			ID:     "call_null",
			Result: json.RawMessage(`{"value":null}`),
		},
		ToolCallRecord{
			ID:     "call_number",
			Result: json.RawMessage(`{"value":2}`),
		},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := Result{
				Sufficient: true,
				Assessment: "assessment",
				Hypotheses: []Hypothesis{{
					Rank:       1,
					Cause:      "cause",
					Confidence: "high",
					Evidence:   []Evidence{verifyEvidence(tc.toolCallID, tc.pointer, tc.value)},
				}},
			}

			got, verified := Verify(result, trace)
			if verified != verifyBoolCount(tc.wantVerified) {
				t.Fatalf("verified = %d, want %d", verified, verifyBoolCount(tc.wantVerified))
			}
			if len(got.Hypotheses) != 1 {
				t.Fatalf("hypotheses = %d, want 1", len(got.Hypotheses))
			}
			if got.Hypotheses[0].Unverified == tc.wantVerified {
				t.Fatalf("unverified = %v, want %v", got.Hypotheses[0].Unverified, !tc.wantVerified)
			}
			wantEvidence := 0
			if tc.wantVerified {
				wantEvidence = 1
			}
			if len(got.Hypotheses[0].Evidence) != wantEvidence {
				t.Fatalf("evidence = %d, want %d", len(got.Hypotheses[0].Evidence), wantEvidence)
			}
		})
	}
}

func verifyBoolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestVerify_DropsOnlyFailingEvidence(t *testing.T) {
	trace := Trace{Calls: []ToolCallRecord{{
		ID:     "call_1",
		Result: json.RawMessage(`{"data":{"checks":[{"failure_class":"timeout"},{"failure_class":"dns"}]}}`),
	}}}
	result := Result{
		Sufficient: true,
		Assessment: "assessment",
		Hypotheses: []Hypothesis{{
			Rank:       1,
			Cause:      "cause",
			Confidence: "high",
			Evidence: []Evidence{
				verifyEvidence("call_1", "/data/checks/0/failure_class", `"timeout"`),
				verifyEvidence("call_1", "/data/checks/0/failure_class", `"dns"`),
				verifyEvidence("call_invented", "/data/checks/1/failure_class", `"dns"`),
				verifyEvidence("call_1", "/data/checks/1/failure_class", `"dns"`),
			},
		}},
	}

	got, verified := Verify(result, trace)
	if verified != 1 {
		t.Fatalf("verified = %d, want 1", verified)
	}
	if len(got.Hypotheses[0].Evidence) != 2 {
		t.Fatalf("evidence = %d, want 2", len(got.Hypotheses[0].Evidence))
	}
	if got.Hypotheses[0].Unverified {
		t.Fatal("hypothesis marked unverified, want verified")
	}
	if len(result.Hypotheses[0].Evidence) != 4 {
		t.Fatalf("Verify mutated its input: evidence = %d", len(result.Hypotheses[0].Evidence))
	}
}

func TestVerify_AllUnverifiedIsInsufficient(t *testing.T) {
	trace := Trace{Calls: []ToolCallRecord{{
		ID:     "call_1",
		Result: json.RawMessage(`{"data":{"checks":[]}}`),
	}}}
	result := Result{
		Sufficient: true,
		Assessment: "assessment",
		Hypotheses: []Hypothesis{
			{
				Rank:       1,
				Cause:      "cause one",
				Confidence: "high",
				Evidence:   []Evidence{verifyEvidence("call_1", "/data/checks/0/status", `"down"`)},
			},
			{
				Rank:       2,
				Cause:      "cause two",
				Confidence: "low",
				Evidence:   []Evidence{verifyEvidence("call_invented", "/data", `{}`)},
			},
		},
	}

	got, verified := Verify(result, trace)
	if verified != 0 {
		t.Fatalf("verified = %d, want 0", verified)
	}
	if len(got.Hypotheses) != 2 {
		t.Fatalf("hypotheses = %d, want both preserved", len(got.Hypotheses))
	}
	for i, hypothesis := range got.Hypotheses {
		if !hypothesis.Unverified {
			t.Fatalf("hypothesis[%d] not marked unverified", i)
		}
		if len(hypothesis.Evidence) != 0 {
			t.Fatalf("hypothesis[%d] evidence = %d, want 0", i, len(hypothesis.Evidence))
		}
	}
}

func TestVerify_CountsEachVerifiedHypothesisOnce(t *testing.T) {
	trace := Trace{Calls: []ToolCallRecord{{
		ID:     "call_1",
		Result: json.RawMessage(`{"data":{"status":"down"}}`),
	}}}
	result := Result{Hypotheses: []Hypothesis{
		{Rank: 1, Evidence: []Evidence{
			verifyEvidence("call_1", "/data/status", `"down"`),
			verifyEvidence("call_1", "/data/status", `"down"`),
		}},
		{Rank: 2, Evidence: []Evidence{
			verifyEvidence("call_1", "/data/status", `"up"`),
		}},
		{Rank: 3, Evidence: []Evidence{
			verifyEvidence("call_1", "/data/status", `"down"`),
		}},
	}}

	if _, verified := Verify(result, trace); verified != 2 {
		t.Fatalf("verified = %d, want 2", verified)
	}
}

func TestVerify_EmptyTraceAndEmptyResult(t *testing.T) {
	result := Result{
		Sufficient: true,
		Hypotheses: []Hypothesis{{
			Rank:     1,
			Evidence: []Evidence{verifyEvidence("call_1", "/data", `{}`)},
		}},
	}
	got, verified := Verify(result, Trace{})
	if verified != 0 || !got.Hypotheses[0].Unverified {
		t.Fatalf("verified = %d, unverified = %v", verified, got.Hypotheses[0].Unverified)
	}

	resultWithoutEvidence := Result{Sufficient: true, Hypotheses: []Hypothesis{{Rank: 1}}}
	got, verified = Verify(resultWithoutEvidence, Trace{})
	if verified != 0 || !got.Hypotheses[0].Unverified {
		t.Fatalf("verified = %d, unverified = %v", verified, got.Hypotheses[0].Unverified)
	}
}
