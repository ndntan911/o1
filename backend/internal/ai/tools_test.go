package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChat_EncodesToolsAndParsesToolCalls(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"o1_get_incident","arguments":"{\"incident_id\":\"i\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	}))
	defer srv.Close()

	c := &OpenAIClient{HTTP: srv.Client(), BaseURL: srv.URL, APIKey: "k"}
	res, err := c.Chat(context.Background(), ChatRequest{
		Model:     "m",
		MaxTokens: 100,
		Messages: []ChatMessage{
			{Role: ChatRoleSystem, Content: "sys"},
			{Role: ChatRoleUser, Content: "hi"},
			{
				Role:      ChatRoleAssistant,
				ToolCalls: []ToolCall{{ID: "call_0", Name: "o1_get_recent_checks", Arguments: json.RawMessage(`{"monitor_id":"m"}`)}},
			},
			{Role: ChatRoleTool, ToolCallID: "call_0", Content: `{"data":{},"meta":{}}`},
		},
		Tools: []ToolDefinition{{
			Name:        "o1_get_incident",
			Description: "read an incident",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Name != "o1_get_incident" {
		t.Fatalf("tool name = %q", res.ToolCalls[0].Name)
	}
	if string(res.ToolCalls[0].Arguments) != `{"incident_id":"i"}` {
		t.Fatalf("arguments = %s", res.ToolCalls[0].Arguments)
	}
	if res.Model != "m" || res.FinishReason != "tool_calls" {
		t.Fatalf("model/finish = %q/%q", res.Model, res.FinishReason)
	}
	if res.InputTokens != 5 || res.OutputTokens != 3 {
		t.Fatalf("usage = %d/%d", res.InputTokens, res.OutputTokens)
	}

	// The request must carry OpenAI tool definitions and let the model choose.
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("tool type = %#v", tool["type"])
	}
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "o1_get_incident" || fn["description"] != "read an incident" {
		t.Fatalf("function = %#v", fn)
	}
	if params, _ := fn["parameters"].(map[string]any); params["type"] != "object" {
		t.Fatalf("parameters = %#v", fn["parameters"])
	}
	if body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v", body["tool_choice"])
	}
	if _, present := body["response_format"]; present {
		t.Fatalf("tool-calling turns must not set response_format: %#v", body["response_format"])
	}

	// Assistant tool calls and tool results must round-trip in the wire shape.
	messages, _ := body["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	assistant, _ := messages[2].(map[string]any)
	assistantCalls, _ := assistant["tool_calls"].([]any)
	if len(assistantCalls) != 1 {
		t.Fatalf("assistant tool_calls = %#v", assistant["tool_calls"])
	}
	call, _ := assistantCalls[0].(map[string]any)
	if call["id"] != "call_0" || call["type"] != "function" {
		t.Fatalf("assistant tool call = %#v", assistantCalls[0])
	}
	if _, present := assistant["tool_call_id"]; present {
		t.Fatalf("assistant message must not carry tool_call_id: %#v", assistant)
	}
	toolMsg, _ := messages[3].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_0" {
		t.Fatalf("tool message = %#v", toolMsg)
	}
}

func TestOpenAIChat_Non2xxIsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &OpenAIClient{HTTP: srv.Client(), BaseURL: srv.URL, APIKey: "k"}
	_, err := c.Chat(context.Background(), ChatRequest{Model: "m"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", httpErr.StatusCode)
	}
	if !strings.Contains(httpErr.Error(), "429") {
		t.Fatalf("error text = %q", httpErr.Error())
	}
	if !IsTransient(err) {
		t.Fatalf("429 must be transient")
	}
}

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"500", &HTTPError{StatusCode: 500}, true},
		{"503", &HTTPError{StatusCode: 503}, true},
		{"429", &HTTPError{StatusCode: 429}, true},
		{"408", &HTTPError{StatusCode: 408}, true},
		{"425", &HTTPError{StatusCode: 425}, true},
		{"400", &HTTPError{StatusCode: 400}, false},
		{"401", &HTTPError{StatusCode: 401}, false},
		{"404", &HTTPError{StatusCode: 404}, false},
		{"wrapped 500", fmt.Errorf("ai: request: %w", &HTTPError{StatusCode: 500}), true},
		{"deadline", context.DeadlineExceeded, true},
		{"wrapped deadline", fmt.Errorf("ai: request: %w", context.DeadlineExceeded), true},
		{"canceled", context.Canceled, false},
		{"wrapped canceled", fmt.Errorf("ai: request: %w", context.Canceled), false},
		{"network", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{"wrapped network", fmt.Errorf("ai: request: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), true},
		{"unknown", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransient(tc.err); got != tc.want {
				t.Fatalf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestStubChat_DefaultDemoTrajectory(t *testing.T) {
	stub := &Stub{}
	first, err := stub.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{
		{Role: ChatRoleUser, Content: `{"seed":{"monitor":{"id":"monitor-1"}},"instruction":"investigate"}`},
	}})
	if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "o1_get_recent_checks" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if first.ToolCalls[0].ID != "stub-checks-1" {
		t.Fatalf("tool call id = %q", first.ToolCalls[0].ID)
	}
	var args map[string]any
	if err := json.Unmarshal(first.ToolCalls[0].Arguments, &args); err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if args["monitor_id"] != "monitor-1" || args["hours"] != float64(168) {
		t.Fatalf("arguments = %s", first.ToolCalls[0].Arguments)
	}
	if first.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", first.FinishReason)
	}

	second, err := stub.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{
		{Role: ChatRoleUser, Content: `{"seed":{"monitor":{"id":"monitor-1"}},"instruction":"investigate"}`},
		{Role: ChatRoleAssistant, ToolCalls: first.ToolCalls},
		{Role: ChatRoleTool, ToolCallID: first.ToolCalls[0].ID, Content: `{"data":{"checks":[{"status":"up"},{"status":"down"}]},"meta":{}}`},
	}})
	if err != nil || !strings.Contains(second.Text, `"/data/checks/1/status"`) || !strings.Contains(second.Text, `"down"`) {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if !strings.Contains(second.Text, `"stub-checks-1"`) {
		t.Fatalf("answer must cite the assistant tool-call id: %s", second.Text)
	}
	var answer struct {
		Sufficient bool `json:"sufficient"`
		Hypotheses []struct {
			Evidence []struct {
				ToolCallID string          `json:"tool_call_id"`
				Pointer    string          `json:"pointer"`
				Value      json.RawMessage `json:"value"`
			} `json:"evidence"`
		} `json:"hypotheses"`
	}
	if err := json.Unmarshal([]byte(second.Text), &answer); err != nil {
		t.Fatalf("answer is not JSON: %v", err)
	}
	if !answer.Sufficient || len(answer.Hypotheses) == 0 || len(answer.Hypotheses[0].Evidence) == 0 {
		t.Fatalf("answer = %+v", answer)
	}
	evidence := answer.Hypotheses[0].Evidence[0]
	if evidence.ToolCallID != "stub-checks-1" || evidence.Pointer != "/data/checks/1/status" || string(evidence.Value) != `"down"` {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestStubChat_InsufficientTrajectory(t *testing.T) {
	const insufficient = `{"sufficient":false,"assessment":"No failing check was found in the last 24 hours.","hypotheses":[]}`

	stub := &Stub{}
	noMonitor, err := stub.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{
		{Role: ChatRoleUser, Content: `{"seed":{},"instruction":"investigate"}`},
	}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if noMonitor.Text != insufficient {
		t.Fatalf("no monitor = %q", noMonitor.Text)
	}

	stub = &Stub{}
	noDown, err := stub.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{
		{Role: ChatRoleUser, Content: `{"seed":{"monitor":{"id":"monitor-1"}},"instruction":"investigate"}`},
		{Role: ChatRoleAssistant, ToolCalls: []ToolCall{{ID: "stub-checks-1", Name: "o1_get_recent_checks"}}},
		{Role: ChatRoleTool, ToolCallID: "stub-checks-1", Content: `{"data":{"checks":[{"status":"up"},{"status":"up"}]},"meta":{}}`},
	}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if noDown.Text != insufficient {
		t.Fatalf("no down check = %q", noDown.Text)
	}
}

func TestStubChat_ReturnsScriptAndRecordsRequest(t *testing.T) {
	stub := &Stub{ChatScript: []ChatResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "o1_get_incident"}}},
		{Text: `{"sufficient":false,"assessment":"","hypotheses":[]}`},
	}}
	first, err := stub.Chat(context.Background(), ChatRequest{Model: "m", Messages: []ChatMessage{{Role: ChatRoleUser, Content: "one"}}})
	if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_1" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := stub.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: ChatRoleUser, Content: "two"}}})
	if err != nil || !strings.Contains(second.Text, `"sufficient":false`) {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if stub.ChatCalls() != 2 {
		t.Fatalf("chat calls = %d", stub.ChatCalls())
	}
	if stub.LastChatRequest().Messages[0].Content != "two" {
		t.Fatalf("last request = %+v", stub.LastChatRequest())
	}
}
