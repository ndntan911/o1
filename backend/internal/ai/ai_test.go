package ai

import (
	"context"
	"io"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIClient_CompleteReturnsTextAndUsage(t *testing.T) {
	var gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "test-model-1",
			"choices": [{"message": {"content": "{\"sufficient\":true}"}}],
			"usage": {"prompt_tokens": 11, "completion_tokens": 7}
		}`))
	}))
	defer server.Close()

	client := &OpenAIClient{HTTP: server.Client(), BaseURL: server.URL, APIKey: "secret-key", Model: "fallback-model"}
	resp, err := client.Complete(context.Background(), Request{System: "sys", User: "user text", MaxTokens: 64})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != `{"sufficient":true}` {
		t.Fatalf("text = %q", resp.Text)
	}
	if resp.Model != "test-model-1" {
		t.Fatalf("model = %q", resp.Model)
	}
	if resp.InputTokens != 11 || resp.OutputTokens != 7 {
		t.Fatalf("usage = %d/%d", resp.InputTokens, resp.OutputTokens)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"max_tokens":64`) {
		t.Fatalf("body missing max_tokens: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"temperature":0`) {
		t.Fatalf("body must pin temperature: %s", gotBody)
	}
}

func TestOpenAIClient_PropagatesProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := &OpenAIClient{HTTP: server.Client(), BaseURL: server.URL, APIKey: "k", Model: "m"}
	_, err := client.Complete(context.Background(), Request{User: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error should carry the status: %v", err)
	}
}

func TestOpenAIClient_EmptyChoicesIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","choices":[]}`))
	}))
	defer server.Close()

	client := &OpenAIClient{HTTP: server.Client(), BaseURL: server.URL, APIKey: "k"}
	if _, err := client.Complete(context.Background(), Request{User: "x"}); err == nil {
		t.Fatal("expected an error when the provider returns no choices")
	}
}

func TestNew_OffByDefaultAndBoundedTimeout(t *testing.T) {
	provider, err := New(Settings{})
	if err != nil || provider != nil {
		t.Fatalf("off should yield nil, nil; got %v, %v", provider, err)
	}

	if _, err := New(Settings{Provider: ProviderOpenAI}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("openai without a key should be ErrNotConfigured, got %v", err)
	}

	provider, err = New(Settings{Provider: ProviderOpenAI, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("openai with a key: %v", err)
	}
	client, ok := provider.(*OpenAIClient)
	if !ok {
		t.Fatalf("provider type = %T", provider)
	}
	if client.HTTP.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v", client.HTTP.Timeout)
	}
}

func TestStub_RecordsRequests(t *testing.T) {
	stub := &Stub{Reply: `{"sufficient":false}`}
	ctx := context.Background()
	if _, err := stub.Complete(ctx, Request{User: "first"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := stub.Complete(ctx, Request{User: "second"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if stub.Calls() != 2 {
		t.Fatalf("calls = %d", stub.Calls())
	}
	if stub.LastRequest().User != "second" {
		t.Fatalf("last request = %q", stub.LastRequest().User)
	}
}

func TestOpenAIClient_SendsExtraHeadersWithoutOverridingAuth(t *testing.T) {
	var gotSession, gotAuth, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = r.Header.Get("X-Session")
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()

	client := &OpenAIClient{
		HTTP:    server.Client(),
		BaseURL: server.URL,
		APIKey:  "real-key",
		Headers: map[string]string{
			"x-session":     "route-123",
			"Authorization": "Bearer forged",
			"Content-Type":  "text/plain",
		},
	}
	if _, err := client.Complete(context.Background(), Request{User: "x"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotSession != "route-123" {
		t.Fatalf("extra header missing, got %q", gotSession)
	}
	if gotAuth != "Bearer real-key" {
		t.Fatalf("Authorization must not be overridden, got %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type must not be overridden, got %q", gotContentType)
	}
}
