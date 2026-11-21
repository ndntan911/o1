package insights

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Parse turns model text into a validated Result. It never trusts the model:
// unknown event ids are dropped and mark the hypothesis unsupported, extra
// hypotheses are cut, and an answer that contradicts itself becomes an error.
func Parse(text string, knownEvents map[uuid.UUID]struct{}) (Result, error) {
	body := stripFences(text)

	var raw struct {
		Sufficient bool         `json:"sufficient"`
		Assessment string       `json:"assessment"`
		Hypotheses []Hypothesis `json:"hypotheses"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return Result{}, fmt.Errorf("insights: model returned invalid JSON: %w", err)
	}

	result := Result{Sufficient: raw.Sufficient, Assessment: strings.TrimSpace(raw.Assessment)}
	if result.Sufficient && len(raw.Hypotheses) == 0 {
		// A confident answer with nothing behind it is not an answer.
		result.Sufficient = false
		if result.Assessment == "" {
			result.Assessment = "The model returned no hypotheses."
		}
	}

	if !result.Sufficient {
		return result, nil
	}

	kept := 0
	for _, hypothesis := range raw.Hypotheses {
		if kept == MaxHypotheses {
			break
		}
		cause := strings.TrimSpace(hypothesis.Cause)
		if cause == "" {
			continue
		}
		kept++

		evidence := make([]Evidence, 0, len(hypothesis.Evidence))
		unsupported := false
		for _, item := range hypothesis.Evidence {
			if item.EventID != nil {
				if _, ok := knownEvents[*item.EventID]; !ok {
					unsupported = true
					continue
				}
			} else if strings.TrimSpace(item.Field) == "" {
				unsupported = true
				continue
			}
			evidence = append(evidence, item)
		}
		if len(evidence) == 0 {
			// Nothing verifiable left: the claim is not shown as fact.
			unsupported = true
		}

		result.Hypotheses = append(result.Hypotheses, Hypothesis{
			Rank:        kept,
			Cause:       cause,
			Confidence:  normaliseConfidence(hypothesis.Confidence),
			Evidence:    evidence,
			FalsifiedBy: strings.TrimSpace(hypothesis.FalsifiedBy),
			Unsupported: unsupported,
		})
	}

	if len(result.Hypotheses) == 0 {
		result.Sufficient = false
		if result.Assessment == "" {
			result.Assessment = "The model produced no hypothesis that could be tied to the incident data."
		}
	}
	return result, nil
}

// stripFences removes a ```json wrapper some models add despite JSON mode.
func stripFences(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimPrefix(trimmed, "json")
	if index := strings.LastIndex(trimmed, "```"); index >= 0 {
		trimmed = trimmed[:index]
	}
	return strings.TrimSpace(trimmed)
}

// normaliseConfidence keeps the scale closed; anything unexpected is "low".
func normaliseConfidence(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ConfidenceHigh:
		return ConfidenceHigh
	case ConfidenceMedium:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}
