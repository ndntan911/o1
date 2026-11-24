package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAIToolRequest mirrors the chat completions body for tool-calling turns.
// Chat never sets response_format: a tool-calling turn needs tool calls, not a
// JSON document.
type openAIToolRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIToolMessage `json:"messages"`
	Tools       []openAITool        `json:"tools,omitempty"`
	ToolChoice  string              `json:"tool_choice,omitempty"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Temperature float64             `json:"temperature"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIToolMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIToolResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat sends one tool-calling completion. Tools are advertised as OpenAI
// functions and the model chooses among them; the response's tool calls keep
// their raw JSON arguments.
func (c *OpenAIClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if c == nil || c.APIKey == "" {
		return ChatResponse{}, ErrNotConfigured
	}
	model := req.Model
	if model == "" {
		model = c.Model
	}

	body := openAIToolRequest{
		Model:       model,
		Messages:    make([]openAIToolMessage, 0, len(req.Messages)),
		MaxTokens:   req.MaxTokens,
		Temperature: 0,
	}
	for _, msg := range req.Messages {
		wire := openAIToolMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		for _, call := range msg.ToolCalls {
			arguments := strings.TrimSpace(string(call.Arguments))
			if arguments == "" {
				arguments = "{}"
			}
			var wireCall openAIToolCall
			wireCall.ID = call.ID
			wireCall.Type = "function"
			wireCall.Function.Name = call.Name
			wireCall.Function.Arguments = arguments
			wire.ToolCalls = append(wire.ToolCalls, wireCall)
		}
		body.Messages = append(body.Messages, wire)
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]openAITool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			parameters := tool.Parameters
			if len(parameters) == 0 {
				parameters = json.RawMessage(`{"type":"object"}`)
			}
			body.Tools = append(body.Tools, openAITool{
				Type: "function",
				Function: openAIToolFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  parameters,
				},
			})
		}
		body.ToolChoice = "auto"
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(c.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	for name, value := range c.Headers {
		switch strings.ToLower(name) {
		case "authorization", "content-type":
			// Reserved: the client owns these.
			continue
		}
		httpReq.Header.Set(name, value)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ai: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ChatResponse{}, providerHTTPError(resp)
	}

	var decoded openAIToolResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&decoded); err != nil {
		return ChatResponse{}, fmt.Errorf("ai: decode response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("ai: provider returned no choices")
	}

	choice := decoded.Choices[0]
	calls := make([]ToolCall, 0, len(choice.Message.ToolCalls))
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "" && call.Type != "function" {
			continue
		}
		arguments := strings.TrimSpace(call.Function.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		calls = append(calls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(arguments),
		})
	}

	name := decoded.Model
	if name == "" {
		name = model
	}
	return ChatResponse{
		Text:         choice.Message.Content,
		ToolCalls:    calls,
		Model:        name,
		InputTokens:  decoded.Usage.PromptTokens,
		OutputTokens: decoded.Usage.CompletionTokens,
		FinishReason: choice.FinishReason,
	}, nil
}
