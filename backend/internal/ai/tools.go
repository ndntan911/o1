package ai

import (
	"context"
	"encoding/json"
)

// ChatRole names the author of one message in a tool-calling conversation.
type ChatRole string

const (
	ChatRoleSystem    ChatRole = "system"
	ChatRoleUser      ChatRole = "user"
	ChatRoleAssistant ChatRole = "assistant"
	ChatRoleTool      ChatRole = "tool"
)

// ToolDefinition describes one function the model may call.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall is one function call the model asked for. Arguments are the raw
// JSON the model produced; callers validate them against the matching schema.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ChatMessage is one turn. Assistant messages may carry ToolCalls, and tool
// messages carry the ToolCallID of the call they answer.
type ChatMessage struct {
	Role       ChatRole
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

// ChatRequest is one tool-calling completion. The caller owns the messages and
// the limits.
type ChatRequest struct {
	Model     string
	Messages  []ChatMessage
	Tools     []ToolDefinition
	MaxTokens int
}

// ChatResponse is the model's text and/or tool calls plus the accounting the
// audit trail records.
type ChatResponse struct {
	Text         string
	ToolCalls    []ToolCall
	Model        string
	InputTokens  int
	OutputTokens int
	FinishReason string
}

// ToolProvider is the transport the agent runner sees. Implementations must
// honour the context deadline; callers set it.
type ToolProvider interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
