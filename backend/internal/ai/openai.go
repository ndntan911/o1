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

// maxErrorBody caps how much of a provider's error response is kept in logs.
const maxErrorBody = 512

// OpenAIClient speaks the OpenAI-compatible chat completions API, which is also
// what Groq, OpenRouter and most local servers expose. BaseURL selects which.
type OpenAIClient struct {
	HTTP    *http.Client
	BaseURL string
	APIKey  string
	Model   string
	// Headers are added to every request. Authorization and Content-Type are
	// set by the client and cannot be overridden here.
	Headers map[string]string
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature"`
	ResponseFmt *openAIFormat   `json:"response_format,omitempty"`
}

type openAIFormat struct {
	Type string `json:"type"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete sends one chat completion and returns the first choice's text.
func (c *OpenAIClient) Complete(ctx context.Context, req Request) (Response, error) {
	if c == nil || c.APIKey == "" {
		return Response{}, ErrNotConfigured
	}
	model := req.Model
	if model == "" {
		model = c.Model
	}
	body := openAIRequest{
		Model: model,
		Messages: []openAIMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		MaxTokens:   req.MaxTokens,
		Temperature: 0,
		// JSON mode where the provider supports it; models that ignore the
		// field still answer, and the parser protects us either way.
		ResponseFmt: &openAIFormat{Type: "json_object"},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("ai: marshal request: %w", err)
	}

	url := completionURL(c.BaseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("ai: build request: %w", err)
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
		return Response{}, fmt.Errorf("ai: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return Response{}, fmt.Errorf("ai: provider returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var decoded openAIResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&decoded); err != nil {
		return Response{}, fmt.Errorf("ai: decode response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return Response{}, fmt.Errorf("ai: provider returned no choices")
	}
	name := decoded.Model
	if name == "" {
		name = model
	}
	return Response{
		Text:         decoded.Choices[0].Message.Content,
		Model:        name,
		InputTokens:  decoded.Usage.PromptTokens,
		OutputTokens: decoded.Usage.CompletionTokens,
	}, nil
}

// completionURL accepts either the API root (…/v1) or the full completions
// endpoint. A base URL copied from a working curl command usually includes the
// path already, and appending it twice is a 404 that looks like a broken key.
func completionURL(base string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(trimmed, "/chat/completions") {
		return trimmed
	}
	return trimmed + "/chat/completions"
}
