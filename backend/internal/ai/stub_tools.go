package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// stubInsufficient is the deterministic refusal the demo trajectory returns
// when it cannot cite a failing check.
const stubInsufficient = `{"sufficient":false,"assessment":"No failing check was found in the last 24 hours.","hypotheses":[]}`

// Chat records the request and returns the next ChatScript entry. With no
// script it runs a fixed two-call demo: ask for the monitor's recent checks,
// then cite the first failing check.
func (s *Stub) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	s.mu.Lock()
	s.lastChatReq = req
	s.chatCalls++
	index := s.chatIndex
	s.chatIndex++
	script := s.ChatScript
	s.mu.Unlock()

	if s.Err != nil {
		return ChatResponse{}, s.Err
	}
	if len(script) > 0 {
		if index >= len(script) {
			return ChatResponse{}, fmt.Errorf("ai: stub chat script exhausted after %d calls", len(script))
		}
		return script[index], nil
	}
	return s.chatDemo(req), nil
}

// ChatCalls reports how many tool-calling turns the stub served.
func (s *Stub) ChatCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatCalls
}

// LastChatRequest returns the most recent tool-calling request, for assertions
// in tests.
func (s *Stub) LastChatRequest() ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastChatReq
}

// chatDemo is the default script: one tool call, then a verified-looking answer
// built only from that tool's result.
func (s *Stub) chatDemo(req ChatRequest) ChatResponse {
	monitorID := stubMonitorID(req.Messages)
	toolID, toolContent, hasTool := stubLastTool(req.Messages)

	if !hasTool {
		if monitorID == "" {
			return stubChatText(stubInsufficient)
		}
		arguments, err := json.Marshal(map[string]any{
			"monitor_id": monitorID,
			"hours":      168,
		})
		if err != nil {
			return stubChatText(stubInsufficient)
		}
		return ChatResponse{
			ToolCalls: []ToolCall{{
				ID:        "stub-checks-1",
				Name:      "o1_get_recent_checks",
				Arguments: json.RawMessage(arguments),
			}},
			Model:        "stub",
			InputTokens:  stubTokens(req),
			FinishReason: "tool_calls",
		}
	}

	index, ok := stubFirstDownCheck(toolContent)
	if !ok {
		return stubChatText(stubInsufficient)
	}
	if toolID == "" {
		toolID = "stub-checks-1"
	}

	answer := stubAnswer{
		Sufficient: true,
		Assessment: "The monitor returned failing checks in the queried window, so the incident has a concrete cause to investigate.",
		Hypotheses: []stubHypothesis{{
			Rank:       1,
			Cause:      "The monitor's probe failed in the queried window.",
			Confidence: "high",
			Evidence: []stubEvidence{{
				ToolCallID: toolID,
				Pointer:    fmt.Sprintf("/data/checks/%d/status", index),
				Value:      json.RawMessage(`"down"`),
				Note:       "the first failing check in the returned window",
			}},
			FalsifiedBy: "A run of successful checks in the same window would rule this out.",
		}},
		NextChecks: []string{"Compare the first failing check with the deployment timeline."},
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		return stubChatText(stubInsufficient)
	}
	return ChatResponse{
		Text:         string(raw),
		Model:        "stub",
		InputTokens:  stubTokens(req),
		OutputTokens: len(raw) / 4,
		FinishReason: "stop",
	}
}

type stubAnswer struct {
	Sufficient bool             `json:"sufficient"`
	Assessment string           `json:"assessment"`
	Hypotheses []stubHypothesis `json:"hypotheses"`
	NextChecks []string         `json:"next_checks,omitempty"`
}

type stubHypothesis struct {
	Rank        int            `json:"rank"`
	Cause       string         `json:"cause"`
	Confidence  string         `json:"confidence"`
	Evidence    []stubEvidence `json:"evidence"`
	FalsifiedBy string         `json:"falsified_by,omitempty"`
}

type stubEvidence struct {
	ToolCallID string          `json:"tool_call_id"`
	Pointer    string          `json:"pointer"`
	Value      json.RawMessage `json:"value"`
	Note       string          `json:"note,omitempty"`
}

func stubChatText(text string) ChatResponse {
	return ChatResponse{
		Text:         text,
		Model:        "stub",
		OutputTokens: len(text) / 4,
		FinishReason: "stop",
	}
}

// stubMonitorID reads the monitor id from the most recent user message, which
// carries {"seed":{"monitor":{"id":"..."}},"instruction":"..."}.
func stubMonitorID(messages []ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != ChatRoleUser {
			continue
		}
		var payload struct {
			Seed struct {
				Monitor struct {
					ID string `json:"id"`
				} `json:"monitor"`
			} `json:"seed"`
		}
		if err := json.Unmarshal([]byte(messages[i].Content), &payload); err != nil {
			return ""
		}
		return payload.Seed.Monitor.ID
	}
	return ""
}

// stubLastTool returns the most recent tool result and the id of the assistant
// call it answers.
func stubLastTool(messages []ChatMessage) (id string, content string, ok bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != ChatRoleTool {
			continue
		}
		id = messages[i].ToolCallID
		if id == "" {
			id = stubLastToolCallID(messages[:i])
		}
		return id, messages[i].Content, true
	}
	return "", "", false
}

func stubLastToolCallID(messages []ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != ChatRoleAssistant || len(messages[i].ToolCalls) == 0 {
			continue
		}
		return messages[i].ToolCalls[len(messages[i].ToolCalls)-1].ID
	}
	return ""
}

// stubFirstDownCheck finds the first check whose status is "down" in a tool
// envelope shaped like {"data":{"checks":[...]},"meta":{}}.
func stubFirstDownCheck(content string) (int, bool) {
	var envelope struct {
		Data struct {
			Checks []struct {
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return 0, false
	}
	for i, check := range envelope.Data.Checks {
		if strings.EqualFold(strings.TrimSpace(check.Status), "down") {
			return i, true
		}
	}
	return 0, false
}

// stubTokens is a rough length-based estimate, like Complete's.
func stubTokens(req ChatRequest) int {
	total := 0
	for _, msg := range req.Messages {
		total += len(msg.Content)
	}
	return total / 4
}
