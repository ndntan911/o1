// Package ai carries the model transport used by product features. It knows
// how to send a prompt and how to read an answer; it knows nothing about
// incidents, workspaces or storage.
package ai

import (
	"context"
	"errors"
)

// Supported provider names. Off is the default: the feature behaves as if it
// did not exist.
const (
	ProviderOff    = "off"
	ProviderStub   = "stub"
	ProviderOpenAI = "openai"
)

// Request is one completion. The caller owns the prompt and the limits.
type Request struct {
	Model     string
	System    string
	User      string
	MaxTokens int
}

// Response is the model's text plus the accounting the audit trail records.
type Response struct {
	Text         string
	Model        string
	InputTokens  int
	OutputTokens int
}

// Provider is the only surface a feature sees. Implementations must honour the
// context deadline; callers set it.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// ErrNotConfigured is returned when a feature asks for a provider that has not
// been configured. Callers treat it as "hide the feature", not as an error to
// show a user.
var ErrNotConfigured = errors.New("ai: provider not configured")
