package ai

import (
	"net/http"
	"strings"
	"time"
)

// Settings are the values a deployment supplies. They mirror the O1_AI_*
// environment variables loaded by package config.
type Settings struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Timeout  time.Duration
	Headers  map[string]string
}

// New builds the provider named by settings. A nil Provider means the feature
// is off; callers check for nil rather than for an error.
func New(settings Settings) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(settings.Provider)) {
	case "", ProviderOff:
		return nil, nil
	case ProviderStub:
		return &Stub{Reply: `{"sufficient":false,"assessment":"No model is configured; this is the stub provider.","hypotheses":[]}`}, nil
	case ProviderOpenAI:
		if settings.APIKey == "" {
			return nil, ErrNotConfigured
		}
		timeout := settings.Timeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		base := settings.BaseURL
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		return &OpenAIClient{
			HTTP:    &http.Client{Timeout: timeout},
			BaseURL: base,
			APIKey:  settings.APIKey,
			Model:   settings.Model,
			Headers: settings.Headers,
		}, nil
	default:
		return nil, ErrNotConfigured
	}
}
