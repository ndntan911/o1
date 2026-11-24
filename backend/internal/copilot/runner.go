package copilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"o1/internal/ai"
	"o1/internal/copilot/tools"
)

// Runner defaults. They mirror the design's hard caps: five model turns, six
// tool calls, a 90s wall clock, an 8 KB result, 1,200 output tokens per call,
// and a 40,000 input-token budget.
const (
	defaultMaxModelCalls   = 5
	defaultMaxToolCalls    = 6
	defaultMaxResultBytes  = 8192
	defaultMaxOutputTokens = 1200
	defaultMaxInputTokens  = 40000
	defaultTimeout         = 90 * time.Second
	defaultCallTimeout     = 30 * time.Second

	errorCodeLimitExceeded    = "limit_exceeded"
	errorCodeProviderRejected = "provider_rejected"
	errorCodeInvalidOutput    = "invalid_output"
	errorCodeInternal         = "internal"

	maxArgsSummaryRunes = 200
)

// ToolCaller executes one tool for the runner. Registry satisfies it; tests and
// other callers can supply a narrower implementation.
type ToolCaller interface {
	Call(ctx context.Context, workspaceID uuid.UUID, name string, args json.RawMessage) (tools.Result, error)
}

// Limits bounds one investigation run. A zero duration disables that timeout;
// the other caps are required and DefaultLimits supplies the production values.
type Limits struct {
	MaxModelCalls   int
	MaxToolCalls    int
	MaxResultBytes  int
	MaxOutputTokens int
	MaxInputTokens  int
	Timeout         time.Duration
	CallTimeout     time.Duration
}

// DefaultLimits returns the production caps from the design.
func DefaultLimits() Limits {
	return Limits{
		MaxModelCalls:   defaultMaxModelCalls,
		MaxToolCalls:    defaultMaxToolCalls,
		MaxResultBytes:  defaultMaxResultBytes,
		MaxOutputTokens: defaultMaxOutputTokens,
		MaxInputTokens:  defaultMaxInputTokens,
		Timeout:         defaultTimeout,
		CallTimeout:     defaultCallTimeout,
	}
}

// Outcome is one run's terminal state. Result is set only for ok and
// insufficient; Err is set when the provider failed transiently and River
// should retry. Trace and the accounting fields are populated on every path.
type Outcome struct {
	Status       Status
	Result       *Result
	Trace        Trace
	InputTokens  int
	OutputTokens int
	DurationMS   int
	ErrorCode    string
	Err          error
}

// Runner drives the bounded tool-calling loop between a provider and the
// read-only tool registry. It does not persist or enqueue anything.
type Runner struct {
	provider ai.ToolProvider
	caller   ToolCaller
	model    string
	limits   Limits
	now      func() time.Time
}

// NewRunner wires a runner. A nil clock uses time.Now.
func NewRunner(provider ai.ToolProvider, caller ToolCaller, model string, limits Limits, now func() time.Time) *Runner {
	if now == nil {
		now = time.Now
	}
	return &Runner{provider: provider, caller: caller, model: model, limits: limits, now: now}
}

// Run executes one investigation. The seed is the first user message; every
// citable fact must come from a tool result recorded in the returned trace.
func (r *Runner) Run(ctx context.Context, workspaceID uuid.UUID, seed Seed) Outcome {
	started := r.now()
	trace := Trace{
		Steps:      []Step{},
		Calls:      []ToolCallRecord{},
		ModelCalls: []ModelCallRecord{},
	}
	inputTokens := 0
	outputTokens := 0

	duration := func() int { return r.elapsed(started) }
	failed := func(code string) Outcome {
		return Outcome{
			Status:       StatusFailed,
			Trace:        trace,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			DurationMS:   duration(),
			ErrorCode:    code,
		}
	}
	incomplete := func() Outcome {
		return Outcome{
			Status:       StatusIncomplete,
			Trace:        trace,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			DurationMS:   duration(),
			ErrorCode:    errorCodeLimitExceeded,
		}
	}
	stopped := func(runCtx context.Context) (Outcome, bool) {
		err := runCtx.Err()
		if err == nil {
			return Outcome{}, false
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return incomplete(), true
		}
		return Outcome{
			Trace:        trace,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			DurationMS:   duration(),
			Err:          err,
		}, true
	}

	userMessage, err := BuildUserMessage(seed)
	if err != nil {
		return failed(errorCodeInternal)
	}

	runCtx := ctx
	if r.limits.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.limits.Timeout)
		defer cancel()
	}

	definitions := make([]ai.ToolDefinition, 0, len(tools.Specs()))
	for _, spec := range tools.Specs() {
		definitions = append(definitions, ai.ToolDefinition{
			Name:        spec.Name,
			Description: spec.Description,
			Parameters:  spec.InputSchema,
		})
	}

	messages := []ai.ChatMessage{
		{Role: ai.ChatRoleSystem, Content: SystemPrompt},
		{Role: ai.ChatRoleUser, Content: userMessage},
	}
	toolCalls := 0

	for modelIndex := 0; modelIndex < r.limits.MaxModelCalls; modelIndex++ {
		if out, stop := stopped(runCtx); stop {
			return out
		}
		if r.limits.MaxInputTokens > 0 && inputTokens >= r.limits.MaxInputTokens {
			return incomplete()
		}

		request := ai.ChatRequest{
			Model:     r.model,
			Messages:  messages,
			Tools:     definitions,
			MaxTokens: r.limits.MaxOutputTokens,
		}
		callStart := r.now()
		response, chatErr := r.chat(runCtx, request)
		callDuration := r.elapsed(callStart)

		// A failed provider call is still one model call in the audit trail.
		trace.ModelCalls = append(trace.ModelCalls, ModelCallRecord{
			Index:        modelIndex,
			FinishReason: response.FinishReason,
			InputTokens:  response.InputTokens,
			OutputTokens: response.OutputTokens,
			DurationMS:   callDuration,
		})
		inputTokens += response.InputTokens
		outputTokens += response.OutputTokens

		if chatErr != nil {
			if out, stop := stopped(runCtx); stop {
				return out
			}
			// A single call timing out is transient; a permanent provider
			// answer means the run cannot succeed without a new attempt.
			if errors.Is(chatErr, context.Canceled) || ai.IsTransient(chatErr) {
				return Outcome{
					Trace:        trace,
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
					DurationMS:   duration(),
					Err:          chatErr,
				}
			}
			return failed(errorCodeProviderRejected)
		}

		messages = append(messages, ai.ChatMessage{
			Role:      ai.ChatRoleAssistant,
			Content:   response.Text,
			ToolCalls: response.ToolCalls,
		})

		if len(response.ToolCalls) > 0 {
			for _, call := range response.ToolCalls {
				if out, stop := stopped(runCtx); stop {
					return out
				}
				if r.limits.MaxToolCalls <= 0 || toolCalls >= r.limits.MaxToolCalls {
					return incomplete()
				}

				toolStart := r.now()
				result, callErr := r.callTool(runCtx, workspaceID, call)
				toolDuration := r.elapsed(toolStart)
				if out, stop := stopped(runCtx); stop {
					return out
				}
				if callErr != nil {
					// A tool failure is data: the model sees an internal error
					// envelope and may adapt instead of aborting the run.
					result = tools.Result{
						IsError:   true,
						ErrorCode: errorCodeInternal,
						Meta:      tools.Meta{AsOf: r.now().UTC()},
					}
				}

				envelope, isError := r.encodeResult(result)
				summary := summarizeArgs(call.Arguments)
				trace.Calls = append(trace.Calls, ToolCallRecord{
					ID:          call.ID,
					Name:        call.Name,
					ArgsSummary: summary,
					ArgsHash:    hashArgs(call.Arguments),
					Result:      envelope,
					IsError:     isError,
					DurationMS:  toolDuration,
				})
				stepStatus := "ok"
				if isError {
					stepStatus = "error"
				}
				trace.Steps = append(trace.Steps, Step{
					Index:       len(trace.Steps),
					Tool:        call.Name,
					ArgsSummary: summary,
					Status:      stepStatus,
					DurationMS:  toolDuration,
				})
				messages = append(messages, ai.ChatMessage{
					Role:       ai.ChatRoleTool,
					Content:    string(envelope),
					ToolCallID: call.ID,
				})
				toolCalls++
			}
			continue
		}

		if strings.TrimSpace(response.Text) == "" {
			return failed(errorCodeInvalidOutput)
		}
		parsed, parseErr := Parse(response.Text)
		if parseErr != nil {
			return failed(errorCodeInvalidOutput)
		}
		verified, count := Verify(parsed, trace)
		status := StatusInsufficient
		if count > 0 {
			status = StatusOK
		}
		return Outcome{
			Status:       status,
			Result:       &verified,
			Trace:        trace,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			DurationMS:   duration(),
		}
	}

	return incomplete()
}

// chat bounds one provider call with CallTimeout when configured.
func (r *Runner) chat(ctx context.Context, req ai.ChatRequest) (ai.ChatResponse, error) {
	if r.limits.CallTimeout <= 0 {
		return r.provider.Chat(ctx, req)
	}
	callCtx, cancel := context.WithTimeout(ctx, r.limits.CallTimeout)
	defer cancel()
	return r.provider.Chat(callCtx, req)
}

// callTool bounds one tool call with CallTimeout when configured.
func (r *Runner) callTool(ctx context.Context, workspaceID uuid.UUID, call ai.ToolCall) (tools.Result, error) {
	if r.limits.CallTimeout <= 0 {
		return r.caller.Call(ctx, workspaceID, call.Name, call.Arguments)
	}
	callCtx, cancel := context.WithTimeout(ctx, r.limits.CallTimeout)
	defer cancel()
	return r.caller.Call(callCtx, workspaceID, call.Name, call.Arguments)
}

// encodeResult marshals the {data, meta} envelope the model sees and the trace
// stores. A result over MaxResultBytes is replaced by a truncation marker that
// carries the hash of the oversized envelope, because a citation into dropped
// data must not resolve. It also reports whether the call is an error.
func (r *Runner) encodeResult(result tools.Result) (json.RawMessage, bool) {
	raw, err := json.Marshal(result)
	if err != nil {
		return r.errorEnvelope(), true
	}
	limit := r.limits.MaxResultBytes
	if limit <= 0 || len(raw) <= limit {
		return raw, result.IsError
	}

	sum := sha256.Sum256(raw)
	result.Data = truncatedResult{
		Truncated: true,
		SHA256:    hex.EncodeToString(sum[:]),
		Bytes:     len(raw),
	}
	result.Meta.Truncated = true
	raw, err = json.Marshal(result)
	if err != nil || len(raw) > limit {
		return r.errorEnvelope(), true
	}
	return raw, result.IsError
}

// truncatedResult is the bounded payload for an oversized tool result.
type truncatedResult struct {
	Truncated bool   `json:"truncated"`
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
}

// errorEnvelope is the bounded result for a caller or encoding failure.
func (r *Runner) errorEnvelope() json.RawMessage {
	raw, err := json.Marshal(tools.Result{
		IsError:   true,
		ErrorCode: errorCodeInternal,
		Meta:      tools.Meta{AsOf: r.now().UTC()},
	})
	if err != nil {
		// tools.Result contains only JSON-safe values, so this is unreachable.
		return json.RawMessage(`{"data":null,"meta":{},"is_error":true,"error_code":"internal"}`)
	}
	return raw
}

// elapsed converts the runner clock's interval to milliseconds, never negative.
func (r *Runner) elapsed(start time.Time) int {
	elapsed := r.now().Sub(start)
	if elapsed < 0 {
		return 0
	}
	return int(elapsed / time.Millisecond)
}

// summarizeArgs is the short, human-readable argument text kept in the trace.
func summarizeArgs(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	runes := []rune(text)
	if len(runes) > maxArgsSummaryRunes {
		return string(runes[:maxArgsSummaryRunes])
	}
	return text
}

// hashArgs is the hex SHA-256 of the raw arguments.
func hashArgs(raw json.RawMessage) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
