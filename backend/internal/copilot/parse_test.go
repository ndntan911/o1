package copilot

import (
	"encoding/json"
	"strings"
	"testing"
)

func parseAnswerJSON(t *testing.T, answer map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}
	return string(raw)
}

func TestParse_AcceptsRawAndFencedJSON(t *testing.T) {
	body := `{"sufficient":true,"assessment":"  the target timed out  ","hypotheses":[]}`

	for name, raw := range map[string]string{
		"raw":            body,
		"raw whitespace": "\n\t " + body + " \n",
		"fenced json":    "```json\n" + body + "\n```",
		"fenced upper":   "```JSON\r\n" + body + "\r\n```",
		"fenced bare":    "```\n" + body + "\n```",
		"fenced spaced":  "  ``` json\n" + body + "\n```  \n",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !result.Sufficient {
				t.Fatal("sufficient = false, want true")
			}
			if result.Assessment != "the target timed out" {
				t.Fatalf("assessment = %q", result.Assessment)
			}
			if result.Hypotheses == nil || len(result.Hypotheses) != 0 {
				t.Fatalf("hypotheses = %#v, want an empty slice", result.Hypotheses)
			}
		})
	}
}

func TestParse_RejectsProseAndNonObjects(t *testing.T) {
	cases := map[string]string{
		"prose":                 "I think the target is timing out.",
		"empty":                 "",
		"array":                 `[{"sufficient":true}]`,
		"null":                  `null`,
		"string":                `"sufficient"`,
		"trailing data":         `{"sufficient":true} and then some prose`,
		"fenced trailing prose": "```json\n{\"sufficient\":true}\n```\nI hope that helps!",
		"unclosed fence":        "```json\n{\"sufficient\":true}\n",
		"wrong fence language":  "```yaml\nsufficient: true\n```",
		"malformed object":      `{"sufficient": true,`,
		"wrong field type":      `{"sufficient": true, "assessment": 42}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatalf("Parse(%q) succeeded, want error", raw)
			}
		})
	}
}

func TestParse_CapsHypothesesAndRenumbersRanks(t *testing.T) {
	hypotheses := make([]map[string]any, 0, 6)
	for i := 0; i < 6; i++ {
		hypotheses = append(hypotheses, map[string]any{
			"rank":       10 + i,
			"cause":      "cause-" + string(rune('a'+i)),
			"confidence": "high",
			"evidence":   []any{},
		})
	}
	raw := parseAnswerJSON(t, map[string]any{
		"sufficient": true,
		"assessment": "assessment",
		"hypotheses": hypotheses,
	})

	result, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Hypotheses) != 3 {
		t.Fatalf("hypotheses = %d, want 3", len(result.Hypotheses))
	}
	for i, hypothesis := range result.Hypotheses {
		if hypothesis.Rank != i+1 {
			t.Fatalf("hypothesis[%d].rank = %d, want %d", i, hypothesis.Rank, i+1)
		}
		wantCause := "cause-" + string(rune('a'+i))
		if hypothesis.Cause != wantCause {
			t.Fatalf("hypothesis[%d].cause = %q, want %q", i, hypothesis.Cause, wantCause)
		}
	}
}

func TestParse_NormalizesConfidence(t *testing.T) {
	cases := map[string]string{
		"high":   "high",
		"medium": "medium",
		"low":    "low",
		" HIGH ": "low",
		"urgent": "low",
		"":       "low",
	}
	for value, want := range cases {
		raw := parseAnswerJSON(t, map[string]any{
			"sufficient": true,
			"assessment": "assessment",
			"hypotheses": []any{map[string]any{
				"rank":       1,
				"cause":      "cause",
				"confidence": value,
				"evidence":   []any{},
			}},
		})

		result, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", value, err)
		}
		if got := result.Hypotheses[0].Confidence; got != want {
			t.Fatalf("confidence %q = %q, want %q", value, got, want)
		}
	}
}

func TestParse_CapsEvidenceAndStrings(t *testing.T) {
	evidenceItems := make([]map[string]any, 0, 7)
	for i := 0; i < 7; i++ {
		evidenceItems = append(evidenceItems, map[string]any{
			"tool_call_id": " call_1 ",
			"pointer":      " /data/checks/0/status ",
			"value":        "down",
			"note":         strings.Repeat("n", 400),
		})
	}
	raw := parseAnswerJSON(t, map[string]any{
		"sufficient": true,
		"assessment": "  " + strings.Repeat("a", 1500) + "  ",
		"hypotheses": []any{map[string]any{
			"rank":         1,
			"cause":        "  " + strings.Repeat("c", 400) + "  ",
			"confidence":   "high",
			"evidence":     evidenceItems,
			"falsified_by": "  " + strings.Repeat("f", 400) + "  ",
		}},
		"next_checks": []any{"  one  ", "two", "three", "four", "five"},
	})

	result, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len([]rune(result.Assessment)); got != 1200 {
		t.Fatalf("assessment length = %d, want 1200", got)
	}
	if len(result.Hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(result.Hypotheses))
	}
	hypothesis := result.Hypotheses[0]
	if got := len([]rune(hypothesis.Cause)); got != 300 {
		t.Fatalf("cause length = %d, want 300", got)
	}
	if got := len([]rune(hypothesis.FalsifiedBy)); got != 300 {
		t.Fatalf("falsified_by length = %d, want 300", got)
	}
	if len(hypothesis.Evidence) != 5 {
		t.Fatalf("evidence = %d, want 5", len(hypothesis.Evidence))
	}
	for i, item := range hypothesis.Evidence {
		if item.ToolCallID != "call_1" {
			t.Fatalf("evidence[%d].tool_call_id = %q", i, item.ToolCallID)
		}
		if item.Pointer != "/data/checks/0/status" {
			t.Fatalf("evidence[%d].pointer = %q", i, item.Pointer)
		}
		if string(item.Value) != `"down"` {
			t.Fatalf("evidence[%d].value = %s", i, item.Value)
		}
		if got := len([]rune(item.Note)); got != 300 {
			t.Fatalf("evidence[%d].note length = %d, want 300", i, got)
		}
	}
	if len(result.NextChecks) != 3 || result.NextChecks[0] != "one" {
		t.Fatalf("next_checks = %#v", result.NextChecks)
	}
}

func TestParse_KeepsEvidenceValuesAndFalsifiedBy(t *testing.T) {
	raw := `{
		"sufficient": true,
		"assessment": "assessment",
		"hypotheses": [{
			"rank": 1,
			"cause": "cause",
			"confidence": "medium",
			"falsified_by": "a successful check would rule this out",
			"evidence": [{
				"tool_call_id": "call_1",
				"pointer": "/data/checks/0/status",
				"value": {"nested": [1, 2, null]},
				"note": "note"
			}]
		}],
		"next_checks": ["advice"]
	}`

	result, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Hypotheses) != 1 || len(result.Hypotheses[0].Evidence) != 1 {
		t.Fatalf("result = %+v", result)
	}
	item := result.Hypotheses[0].Evidence[0]
	if string(item.Value) != `{"nested": [1, 2, null]}` {
		t.Fatalf("value = %s", item.Value)
	}
	if result.Hypotheses[0].FalsifiedBy != "a successful check would rule this out" {
		t.Fatalf("falsified_by = %q", result.Hypotheses[0].FalsifiedBy)
	}
}

func TestParse_ExplicitInsufficientDropsHypotheses(t *testing.T) {
	raw := `{
		"sufficient": false,
		"assessment": "not enough data",
		"hypotheses": [{
			"rank": 1,
			"cause": "cause",
			"confidence": "high",
			"evidence": [{"tool_call_id": "call_1", "pointer": "/data", "value": {}}]
		}]
	}`

	result, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.Sufficient {
		t.Fatal("sufficient = true, want false")
	}
	if len(result.Hypotheses) != 0 {
		t.Fatalf("hypotheses = %#v, want none", result.Hypotheses)
	}
}
