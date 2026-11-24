package copilot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// Verify confirms every citation against the recorded tool results. Evidence
// for an unknown or failed call, an unresolvable pointer, or a value that does
// not match is dropped. A hypothesis with no surviving evidence is marked
// Unverified. The returned count is the number of hypotheses with at least one
// verified evidence item. Sufficient is set from that count, and when it is
// false the hypotheses are cleared so the stored result cannot contradict
// itself.
func Verify(result Result, trace Trace) (Result, int) {
	calls := make(map[string]ToolCallRecord, len(trace.Calls))
	for _, call := range trace.Calls {
		calls[call.ID] = call
	}

	verified := 0
	sanitized := Result{
		Sufficient: result.Sufficient,
		Assessment: result.Assessment,
		Hypotheses: make([]Hypothesis, 0, len(result.Hypotheses)),
		NextChecks: result.NextChecks,
	}

	for _, hypothesis := range result.Hypotheses {
		kept := make([]Evidence, 0, len(hypothesis.Evidence))
		for _, item := range hypothesis.Evidence {
			if evidenceResolves(item, calls) {
				kept = append(kept, item)
			}
		}
		hypothesis.Evidence = kept
		hypothesis.Unverified = len(kept) == 0
		if !hypothesis.Unverified {
			verified++
		}
		sanitized.Hypotheses = append(sanitized.Hypotheses, hypothesis)
	}

	// Sufficient describes what survived verification, not what the model
	// claimed. A result with no verified cause cannot list causes.
	sanitized.Sufficient = verified > 0
	if !sanitized.Sufficient {
		sanitized.Hypotheses = []Hypothesis{}
	}

	return sanitized, verified
}

func evidenceResolves(item Evidence, calls map[string]ToolCallRecord) bool {
	call, ok := calls[item.ToolCallID]
	if !ok || call.IsError || len(call.Result) == 0 {
		return false
	}

	document, ok := decodeJSONValue(call.Result)
	if !ok {
		return false
	}
	cited, ok := decodeJSONValue(item.Value)
	if !ok {
		return false
	}
	resolved, ok := resolvePointer(document, item.Pointer)
	if !ok {
		return false
	}
	return reflect.DeepEqual(resolved, cited)
}

func decodeJSONValue(raw []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Keep numbers as their literal text so a citation must match exactly
	// instead of collapsing large integers through float64.
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return value, true
}

// resolvePointer implements RFC 6901 against decoded JSON values.
func resolvePointer(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}

	tokens, ok := pointerTokens(pointer)
	if !ok {
		return nil, false
	}
	current := document
	for _, token := range tokens {
		switch node := current.(type) {
		case map[string]any:
			next, exists := node[token]
			if !exists {
				return nil, false
			}
			current = next
		case []any:
			index, valid := arrayIndex(token)
			if !valid || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// pointerTokens splits a pointer and resolves ~1 then ~0, the RFC 6901 order.
func pointerTokens(pointer string) ([]string, bool) {
	parts := strings.Split(pointer[1:], "/")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		var token strings.Builder
		for index := 0; index < len(part); index++ {
			if part[index] != '~' {
				token.WriteByte(part[index])
				continue
			}
			if index+1 >= len(part) {
				return nil, false
			}
			index++
			switch part[index] {
			case '0':
				token.WriteByte('~')
			case '1':
				token.WriteByte('/')
			default:
				return nil, false
			}
		}
		tokens = append(tokens, token.String())
	}
	return tokens, true
}

// arrayIndex accepts only the canonical array index form: 0 or a non-zero digit
// followed by digits. "-" never names an existing element.
func arrayIndex(token string) (int, bool) {
	if token == "" {
		return 0, false
	}
	if token == "0" {
		return 0, true
	}
	if token[0] < '1' || token[0] > '9' {
		return 0, false
	}
	for index := 1; index < len(token); index++ {
		if token[index] < '0' || token[index] > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(token)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
