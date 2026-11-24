package copilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Parser limits. The server enforces them so a model cannot widen the stored
// answer, whatever the prompt says.
const (
	maxHypotheses            = 3
	maxEvidencePerHypothesis = 5
	maxAssessmentRunes       = 1200
	maxCauseRunes            = 300
	maxNoteRunes             = 300
	maxFalsifiedByRunes      = 300
	maxNextChecks            = 3
)

type answerWire struct {
	Sufficient *bool            `json:"sufficient"`
	Assessment string           `json:"assessment"`
	Hypotheses []hypothesisWire `json:"hypotheses"`
	NextChecks []string         `json:"next_checks"`
}

type hypothesisWire struct {
	Rank        int            `json:"rank"`
	Cause       string         `json:"cause"`
	Confidence  string         `json:"confidence"`
	Evidence    []evidenceWire `json:"evidence"`
	FalsifiedBy string         `json:"falsified_by"`
}

type evidenceWire struct {
	ToolCallID string          `json:"tool_call_id"`
	Pointer    string          `json:"pointer"`
	Value      json.RawMessage `json:"value"`
	Note       string          `json:"note"`
}

// Parse accepts a raw JSON object or one fenced ```json block and normalizes it
// into the stored result. Prose, non-object JSON, and trailing text are errors.
func Parse(raw string) (Result, error) {
	body, err := jsonObject(raw)
	if err != nil {
		return Result{}, err
	}

	decoder := json.NewDecoder(strings.NewReader(body))
	var wire answerWire
	if err := decoder.Decode(&wire); err != nil {
		return Result{}, fmt.Errorf("copilot: parse answer: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Result{}, errors.New("copilot: trailing data after answer object")
	}

	result := Result{
		Sufficient: wire.Sufficient != nil && *wire.Sufficient,
		Assessment: cleanText(wire.Assessment, maxAssessmentRunes),
		Hypotheses: []Hypothesis{},
		NextChecks: make([]string, 0, maxNextChecks),
	}

	// An explicit sufficient:false is a refusal; the output contract requires
	// it to carry no hypotheses. A missing field is left to verification.
	if wire.Sufficient == nil || *wire.Sufficient {
		result.Hypotheses = normalizeHypotheses(wire.Hypotheses)
	}

	for _, check := range wire.NextChecks {
		if len(result.NextChecks) == maxNextChecks {
			break
		}
		if cleaned := strings.TrimSpace(check); cleaned != "" {
			result.NextChecks = append(result.NextChecks, cleaned)
		}
	}

	return result, nil
}

// jsonObject extracts the object text from a raw answer or a fenced block.
func jsonObject(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("copilot: empty model output")
	}

	if strings.HasPrefix(text, "```") {
		rest := text[len("```"):]
		newline := strings.IndexByte(rest, '\n')
		if newline < 0 {
			return "", errors.New("copilot: malformed JSON fence")
		}
		language := strings.TrimSpace(strings.TrimSuffix(rest[:newline], "\r"))
		if language != "" && !strings.EqualFold(language, "json") {
			return "", fmt.Errorf("copilot: unsupported fence language %q", language)
		}
		rest = rest[newline+1:]
		end := strings.Index(rest, "```")
		if end < 0 {
			return "", errors.New("copilot: unterminated JSON fence")
		}
		if trailing := strings.TrimSpace(rest[end+len("```"):]); trailing != "" {
			return "", errors.New("copilot: text after JSON fence")
		}
		text = strings.TrimSpace(rest[:end])
	}

	if !strings.HasPrefix(text, "{") {
		return "", errors.New("copilot: model output is not a JSON object")
	}
	return text, nil
}

func normalizeHypotheses(wire []hypothesisWire) []Hypothesis {
	limit := len(wire)
	if limit > maxHypotheses {
		limit = maxHypotheses
	}

	hypotheses := make([]Hypothesis, 0, limit)
	for index := 0; index < limit; index++ {
		source := wire[index]
		hypothesis := Hypothesis{
			Rank:        index + 1,
			Cause:       cleanText(source.Cause, maxCauseRunes),
			Confidence:  normalizeConfidence(source.Confidence),
			Evidence:    []Evidence{},
			FalsifiedBy: cleanText(source.FalsifiedBy, maxFalsifiedByRunes),
		}

		count := len(source.Evidence)
		if count > maxEvidencePerHypothesis {
			count = maxEvidencePerHypothesis
		}
		for item := 0; item < count; item++ {
			evidence := source.Evidence[item]
			hypothesis.Evidence = append(hypothesis.Evidence, Evidence{
				ToolCallID: strings.TrimSpace(evidence.ToolCallID),
				Pointer:    strings.TrimSpace(evidence.Pointer),
				Value:      evidence.Value,
				Note:       cleanText(evidence.Note, maxNoteRunes),
			})
		}
		hypotheses = append(hypotheses, hypothesis)
	}
	return hypotheses
}

// normalizeConfidence accepts exactly the contract's values; anything else,
// including different casing, becomes low.
func normalizeConfidence(value string) string {
	switch strings.TrimSpace(value) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	case "low":
		return "low"
	default:
		return "low"
	}
}

// cleanText trims whitespace and caps the value by runes.
func cleanText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
