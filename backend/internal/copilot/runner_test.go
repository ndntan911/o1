package copilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/ai"
	"o1/internal/copilot/tools"
)

const verifiedAnswer = `{"sufficient":true,"assessment":"timeouts","hypotheses":[{"rank":1,"cause":"timeout","confidence":"high","evidence":[{"tool_call_id":"call_1","pointer":"/data/checks/0/failure_class","value":"timeout"}]}]}`

const insufficientAnswer = `{"sufficient":false,"assessment":"no cause found","hypotheses":[]}`

// scriptedProvider serves one scripted ChatResponse per call and records every
// request so a test can inspect the conversation the runner built.
type scriptedProvider struct {
	mu       sync.Mutex
	script   []ai.ChatResponse
	err      error
	calls    int
	requests []ai.ChatRequest
}

func (p *scriptedProvider) Chat(_ context.Context, req ai.ChatRequest) (ai.ChatResponse, error) {
	p.mu.Lock()
	index := p.calls
	p.calls++
	p.requests = append(p.requests, req)
	script := p.script
	err := p.err
	p.mu.Unlock()

	if err != nil {
		return ai.ChatResponse{}, err
	}
	if index >= len(script) {
		return ai.ChatResponse{}, errors.New("scripted provider exhausted")
	}
	return script[index], nil
}

func (p *scriptedProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *scriptedProvider) Request(index int) ai.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[index]
}

// blockingProvider waits for the context deadline, so timeout paths are
// deterministic without a fixed sleep.
type blockingProvider struct{}

func (*blockingProvider) Chat(ctx context.Context, _ ai.ChatRequest) (ai.ChatResponse, error) {
	<-ctx.Done()
	return ai.ChatResponse{}, ctx.Err()
}

type callerInvocation struct {
	workspaceID uuid.UUID
	name        string
	args        json.RawMessage
}

// fakeCaller returns one fixed result or error and records each invocation.
type fakeCaller struct {
	mu     sync.Mutex
	result tools.Result
	err    error
	calls  []callerInvocation
}

func (f *fakeCaller) Call(_ context.Context, workspaceID uuid.UUID, name string, args json.RawMessage) (tools.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, callerInvocation{workspaceID: workspaceID, name: name, args: args})
	result, err := f.result, f.err
	f.mu.Unlock()

	if err != nil {
		return tools.Result{}, err
	}
	return result, nil
}

func (f *fakeCaller) Calls() []callerInvocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]callerInvocation, len(f.calls))
	copy(out, f.calls)
	return out
}

func toolCall(id, name, args string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func TestRunner_CallsToolThenReturnsVerifiedAnswer(t *testing.T) {
	provider := &scriptedProvider{script: []ai.ChatResponse{
		{
			ToolCalls:    []ai.ToolCall{toolCall("call_1", "o1_get_recent_checks", `{"monitor_id":"m"}`)},
			InputTokens:  100,
			OutputTokens: 10,
			FinishReason: "tool_calls",
		},
		{
			Text:         verifiedAnswer,
			InputTokens:  50,
			OutputTokens: 20,
			FinishReason: "stop",
		},
	}}
	caller := &fakeCaller{result: tools.Result{Data: map[string]any{
		"checks": []any{map[string]any{"failure_class": "timeout"}},
	}}}
	workspaceID := uuid.New()

	out := NewRunner(provider, caller, "m", DefaultLimits(), time.Now).Run(context.Background(), workspaceID, Seed{})

	if out.Err != nil {
		t.Fatalf("out.Err = %v", out.Err)
	}
	if out.Status != StatusOK {
		t.Fatalf("out.Status = %s, want %s", out.Status, StatusOK)
	}
	if out.ErrorCode != "" {
		t.Fatalf("out.ErrorCode = %q, want empty", out.ErrorCode)
	}
	if out.Result == nil || !out.Result.Sufficient || len(out.Result.Hypotheses) != 1 {
		t.Fatalf("out.Result = %+v, want one verified hypothesis", out.Result)
	}
	if len(out.Trace.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(out.Trace.Steps))
	}
	if len(out.Trace.Calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(out.Trace.Calls))
	}
	if len(out.Trace.ModelCalls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(out.Trace.ModelCalls))
	}
	if out.Trace.ModelCalls[0].FinishReason != "tool_calls" {
		t.Fatalf("first finish reason = %q, want tool_calls", out.Trace.ModelCalls[0].FinishReason)
	}
	if out.InputTokens != 150 || out.OutputTokens != 30 {
		t.Fatalf("tokens = %d/%d, want 150/30", out.InputTokens, out.OutputTokens)
	}

	wantHash := sha256.Sum256([]byte(`{"monitor_id":"m"}`))
	if out.Trace.Calls[0].ArgsHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("args hash = %q, want %q", out.Trace.Calls[0].ArgsHash, hex.EncodeToString(wantHash[:]))
	}
	if out.Trace.Steps[0].Tool != "o1_get_recent_checks" || out.Trace.Steps[0].Status != "ok" {
		t.Fatalf("step = %+v", out.Trace.Steps[0])
	}

	invocations := caller.Calls()
	if len(invocations) != 1 {
		t.Fatalf("caller invocations = %d, want 1", len(invocations))
	}
	if invocations[0].workspaceID != workspaceID || invocations[0].name != "o1_get_recent_checks" {
		t.Fatalf("caller invocation = %+v", invocations[0])
	}

	if provider.Calls() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.Calls())
	}
	first := provider.Request(0)
	if len(first.Tools) != len(tools.Specs()) {
		t.Fatalf("first request tools = %d, want %d", len(first.Tools), len(tools.Specs()))
	}
	if len(first.Messages) != 2 || first.Messages[0].Content != SystemPrompt {
		t.Fatalf("first request messages = %+v", first.Messages)
	}

	second := provider.Request(1)
	if len(second.Messages) != 4 {
		t.Fatalf("second request messages = %d, want 4", len(second.Messages))
	}
	if second.Messages[2].Role != ai.ChatRoleAssistant || len(second.Messages[2].ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", second.Messages[2])
	}
	if second.Messages[3].Role != ai.ChatRoleTool || second.Messages[3].ToolCallID != "call_1" {
		t.Fatalf("tool message = %+v", second.Messages[3])
	}
	if !strings.Contains(second.Messages[3].Content, `"failure_class":"timeout"`) {
		t.Fatalf("tool message missing result: %s", second.Messages[3].Content)
	}
	if string(out.Trace.Calls[0].Result) != second.Messages[3].Content {
		t.Fatalf("trace result = %s, tool message = %s", out.Trace.Calls[0].Result, second.Messages[3].Content)
	}
}

func TestRunner_StopsAtCaps(t *testing.T) {
	t.Run("tool calls", func(t *testing.T) {
		script := make([]ai.ChatResponse, 6)
		for i := range script {
			script[i] = ai.ChatResponse{ToolCalls: []ai.ToolCall{
				toolCall(fmt.Sprintf("call_%d_a", i), "o1_get_recent_checks", `{"monitor_id":"m"}`),
				toolCall(fmt.Sprintf("call_%d_b", i), "o1_get_recent_checks", `{"monitor_id":"m"}`),
			}}
		}
		out := NewRunner(&scriptedProvider{script: script}, &fakeCaller{}, "m", DefaultLimits(), time.Now).
			Run(context.Background(), uuid.New(), Seed{})

		if out.Status != StatusIncomplete || out.ErrorCode != "limit_exceeded" {
			t.Fatalf("out = %+v, want incomplete/limit_exceeded", out)
		}
		if out.Result != nil {
			t.Fatalf("out.Result = %+v, want nil", out.Result)
		}
		if len(out.Trace.Steps) != 6 || len(out.Trace.Calls) != 6 {
			t.Fatalf("steps/calls = %d/%d, want 6/6", len(out.Trace.Steps), len(out.Trace.Calls))
		}
	})

	t.Run("model calls", func(t *testing.T) {
		script := make([]ai.ChatResponse, 5)
		for i := range script {
			script[i] = ai.ChatResponse{ToolCalls: []ai.ToolCall{
				toolCall(fmt.Sprintf("call_%d", i), "o1_get_recent_checks", `{"monitor_id":"m"}`),
			}}
		}
		out := NewRunner(&scriptedProvider{script: script}, &fakeCaller{}, "m", DefaultLimits(), time.Now).
			Run(context.Background(), uuid.New(), Seed{})

		if out.Status != StatusIncomplete || out.ErrorCode != "limit_exceeded" {
			t.Fatalf("out = %+v, want incomplete/limit_exceeded", out)
		}
		if len(out.Trace.ModelCalls) != 5 || len(out.Trace.Steps) != 5 {
			t.Fatalf("model calls/steps = %d/%d, want 5/5", len(out.Trace.ModelCalls), len(out.Trace.Steps))
		}
	})

	t.Run("input tokens", func(t *testing.T) {
		provider := &scriptedProvider{script: []ai.ChatResponse{{
			ToolCalls:   []ai.ToolCall{toolCall("call_1", "o1_get_recent_checks", `{"monitor_id":"m"}`)},
			InputTokens: 40_001,
		}}}
		out := NewRunner(provider, &fakeCaller{}, "m", DefaultLimits(), time.Now).
			Run(context.Background(), uuid.New(), Seed{})

		if out.Status != StatusIncomplete || out.ErrorCode != "limit_exceeded" {
			t.Fatalf("out = %+v, want incomplete/limit_exceeded", out)
		}
	})
}

func TestRunner_TransientProviderErrorIsReturned(t *testing.T) {
	providerErr := &ai.HTTPError{StatusCode: 500, Body: "boom"}
	provider := &scriptedProvider{err: providerErr}

	out := NewRunner(provider, &fakeCaller{}, "m", DefaultLimits(), time.Now).
		Run(context.Background(), uuid.New(), Seed{})

	if !errors.Is(out.Err, providerErr) {
		t.Fatalf("out.Err = %v, want %v", out.Err, providerErr)
	}
	if out.Status != "" {
		t.Fatalf("out.Status = %s, want empty", out.Status)
	}
	if out.Result != nil {
		t.Fatalf("out.Result = %+v, want nil", out.Result)
	}
	if len(out.Trace.ModelCalls) != 1 {
		t.Fatalf("model calls = %d, want one recorded failed call", len(out.Trace.ModelCalls))
	}
}

func TestRunner_CallTimeoutIsTransient(t *testing.T) {
	limits := DefaultLimits()
	limits.Timeout = 2 * time.Second
	limits.CallTimeout = 25 * time.Millisecond

	out := NewRunner(&blockingProvider{}, &fakeCaller{}, "m", limits, time.Now).
		Run(context.Background(), uuid.New(), Seed{})

	if !errors.Is(out.Err, context.DeadlineExceeded) {
		t.Fatalf("out.Err = %v, want context.DeadlineExceeded", out.Err)
	}
	if out.Status != "" || out.Result != nil {
		t.Fatalf("out = %+v, want no status and no result", out)
	}
}

func TestRunner_OverallDeadlineIsIncomplete(t *testing.T) {
	limits := DefaultLimits()
	limits.Timeout = 40 * time.Millisecond
	limits.CallTimeout = time.Second

	out := NewRunner(&blockingProvider{}, &fakeCaller{}, "m", limits, time.Now).
		Run(context.Background(), uuid.New(), Seed{})

	if out.Err != nil {
		t.Fatalf("out.Err = %v, want nil", out.Err)
	}
	if out.Status != StatusIncomplete || out.ErrorCode != "limit_exceeded" {
		t.Fatalf("out = %+v, want incomplete/limit_exceeded", out)
	}
	if out.Result != nil {
		t.Fatalf("out.Result = %+v, want nil", out.Result)
	}
	if len(out.Trace.ModelCalls) != 1 {
		t.Fatalf("model calls = %d, want one recorded timed-out call", len(out.Trace.ModelCalls))
	}
}

func TestRunner_ParseFailureIsInvalidOutput(t *testing.T) {
	provider := &scriptedProvider{script: []ai.ChatResponse{{Text: "not json"}}}

	out := NewRunner(provider, &fakeCaller{}, "m", DefaultLimits(), time.Now).
		Run(context.Background(), uuid.New(), Seed{})

	if out.Status != StatusFailed || out.ErrorCode != "invalid_output" {
		t.Fatalf("out = %+v, want failed/invalid_output", out)
	}
	if out.Result != nil {
		t.Fatalf("out.Result = %+v, want nil", out.Result)
	}
}

func TestRunner_ToolErrorIsData(t *testing.T) {
	provider := &scriptedProvider{script: []ai.ChatResponse{
		{ToolCalls: []ai.ToolCall{toolCall("call_1", "o1_get_recent_checks", `{"monitor_id":"m"}`)}},
		{Text: insufficientAnswer},
	}}
	caller := &fakeCaller{err: errors.New("tool exploded")}

	out := NewRunner(provider, caller, "m", DefaultLimits(), time.Now).
		Run(context.Background(), uuid.New(), Seed{})

	if out.Err != nil {
		t.Fatalf("out.Err = %v", out.Err)
	}
	if out.Status != StatusInsufficient {
		t.Fatalf("out.Status = %s, want %s", out.Status, StatusInsufficient)
	}
	if len(out.Trace.Steps) != 1 || out.Trace.Steps[0].Status != "error" {
		t.Fatalf("steps = %+v, want one error step", out.Trace.Steps)
	}
	if len(out.Trace.Calls) != 1 || !out.Trace.Calls[0].IsError {
		t.Fatalf("calls = %+v, want one error call", out.Trace.Calls)
	}
	if provider.Calls() != 2 {
		t.Fatalf("provider calls = %d, want the loop to continue", provider.Calls())
	}

	toolMessage := provider.Request(1).Messages[3]
	if toolMessage.Role != ai.ChatRoleTool || toolMessage.ToolCallID != "call_1" {
		t.Fatalf("tool message = %+v", toolMessage)
	}
	if !strings.Contains(toolMessage.Content, `"is_error":true`) || !strings.Contains(toolMessage.Content, `"error_code":"internal"`) {
		t.Fatalf("tool message = %s, want an internal error envelope", toolMessage.Content)
	}
}

func TestRunner_BoundsToolResult(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxResultBytes = 512

	provider := &scriptedProvider{script: []ai.ChatResponse{
		{ToolCalls: []ai.ToolCall{toolCall("call_1", "o1_get_recent_checks", `{"monitor_id":"m"}`)}},
		{Text: insufficientAnswer},
	}}
	caller := &fakeCaller{result: tools.Result{Data: map[string]any{
		"blob": strings.Repeat("x", 4096),
	}}}

	out := NewRunner(provider, caller, "m", limits, time.Now).
		Run(context.Background(), uuid.New(), Seed{})

	if out.Status != StatusInsufficient {
		t.Fatalf("out.Status = %s, want %s", out.Status, StatusInsufficient)
	}
	if len(out.Trace.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(out.Trace.Calls))
	}
	if len(out.Trace.Calls[0].Result) > limits.MaxResultBytes {
		t.Fatalf("trace result = %d bytes, want <= %d", len(out.Trace.Calls[0].Result), limits.MaxResultBytes)
	}
	if !strings.Contains(string(out.Trace.Calls[0].Result), `"truncated":true`) {
		t.Fatalf("trace result = %s, want a truncation marker", out.Trace.Calls[0].Result)
	}

	toolContent := provider.Request(1).Messages[3].Content
	if len(toolContent) > limits.MaxResultBytes {
		t.Fatalf("tool message = %d bytes, want <= %d", len(toolContent), limits.MaxResultBytes)
	}
	if toolContent != string(out.Trace.Calls[0].Result) {
		t.Fatalf("tool message = %s, trace result = %s", toolContent, out.Trace.Calls[0].Result)
	}
}
