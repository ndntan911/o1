// Package alerts implements alert channels, validation, secret handling, and
// notification delivery.
package alerts

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"o1/internal/workspaces"
)

// Alert channel kinds. Email keeps its fixed name and is managed through the
// legacy email endpoint; the other kinds are created through the generic
// channel API.
const (
	KindEmail    = "email"
	KindWebhook  = "webhook"
	KindSlack    = "slack"
	KindTelegram = "telegram"

	// EmailChannelName is the fixed name of the per-workspace email channel.
	EmailChannelName = "Email"
)

// Sentinel errors returned by the service. Handlers map them onto HTTP
// responses; errors.Is callers can branch on them.
var (
	// ErrInvalidChannelConfig is returned when a stored channel config is
	// missing, corrupt, or cannot be decrypted.
	ErrInvalidChannelConfig = errors.New("invalid alert channel configuration")
	// ErrEmailChannel is returned when an operation that only supports the
	// generic channel kinds targets the email channel.
	ErrEmailChannel = errors.New("email alert channel is managed separately")
	// ErrNameTaken is returned when an active channel already uses the name
	// within the workspace and kind.
	ErrNameTaken = errors.New("alert channel name already taken")
	// ErrLimit is returned when a workspace has reached the per-kind or total
	// active channel limit.
	ErrLimit = errors.New("alert channel limit reached")
	// ErrSuspended is returned when a mutation targets a suspended workspace.
	ErrSuspended = errors.New("workspace suspended")
	// ErrNotFound is returned for channels or workspaces the caller cannot
	// see. Cross-tenant lookups must not reveal whether a resource exists.
	ErrNotFound = workspaces.ErrNotFound
	// ErrForbidden is returned when the caller is a member but lacks the
	// required role.
	ErrForbidden = workspaces.ErrForbidden
	// ErrValidation is returned for invalid input. Validation failures carry
	// a *ValidationError with per-field messages.
	ErrValidation = workspaces.ErrValidation
)

// ValidationError reports invalid input with one message per JSON field.
type ValidationError struct {
	Fields map[string]string
}

// Error renders the field errors in a stable order.
func (e *ValidationError) Error() string {
	fields := make([]string, 0, len(e.Fields))
	for field := range e.Fields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, field+": "+e.Fields[field])
	}
	return "validation error: " + strings.Join(parts, "; ")
}

// Is reports ValidationError as workspaces.ErrValidation so errors.Is works.
func (e *ValidationError) Is(target error) bool {
	return target == workspaces.ErrValidation
}

// EmailConfig is the stored JSON shape of the email channel.
type EmailConfig struct {
	Recipients []string `json:"recipients"`
}

// WebhookConfig is the stored JSON shape of a webhook channel. URLEnc and
// SigningSecretEnc are secretbox ciphertext and are never returned to clients.
type WebhookConfig struct {
	URLEnc           string `json:"url_enc"`
	URLPreview       string `json:"url_preview"`
	SigningSecretEnc string `json:"signing_secret_enc"`
}

// SlackConfig is the stored JSON shape of a Slack channel. URLEnc is
// secretbox ciphertext and is never returned to clients.
type SlackConfig struct {
	URLEnc     string `json:"url_enc"`
	URLPreview string `json:"url_preview"`
}

// TelegramConfig is the stored JSON shape of a Telegram channel. BotTokenEnc
// is secretbox ciphertext and is never returned to clients.
type TelegramConfig struct {
	BotTokenEnc string `json:"bot_token_enc"`
	ChatID      string `json:"chat_id"`
}

// Channel is the client-facing view of an alert channel. It never carries
// plaintext or sealed secret values.
type Channel struct {
	ID               uuid.UUID `json:"id"`
	Kind             string    `json:"kind"`
	Name             string    `json:"name"`
	Enabled          bool      `json:"enabled"`
	Recipients       []string  `json:"recipients,omitempty"`
	URLPreview       string    `json:"url_preview,omitempty"`
	SigningSecretSet bool      `json:"signing_secret_set,omitempty"`
	BotTokenSet      bool      `json:"bot_token_set,omitempty"`
	ChatID           string    `json:"chat_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// CreateInput is the request body for creating a non-email alert channel.
type CreateInput struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Enabled       *bool  `json:"enabled"`
	URL           string `json:"url"`
	SigningSecret string `json:"signing_secret"`
	BotToken      string `json:"bot_token"`
	ChatID        string `json:"chat_id"`
}

// UpdateInput is the request body for updating a non-email alert channel.
// Nil fields are left unchanged; an empty secret string keeps the stored
// value, except that ClearSigningSecret removes an optional webhook secret.
type UpdateInput struct {
	Name               *string `json:"name"`
	Enabled            *bool   `json:"enabled"`
	URL                *string `json:"url"`
	SigningSecret      *string `json:"signing_secret"`
	ClearSigningSecret bool    `json:"clear_signing_secret"`
	BotToken           *string `json:"bot_token"`
	ChatID             *string `json:"chat_id"`
}

// MonitorChannel is the client-facing view of a channel's switch for one
// monitor.
type MonitorChannel struct {
	ID             uuid.UUID `json:"id"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	ChannelEnabled bool      `json:"channel_enabled"`
	Enabled        bool      `json:"enabled"`
}

// Target is the decrypted delivery target for a non-email channel.
type Target struct {
	Kind          string
	URL           string
	SigningSecret string
	BotToken      string
	ChatID        string
}

const maxNameLen = 60

// ValidateName trims raw and checks that it is 1–60 characters.
func ValidateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return "", &ValidationError{Fields: map[string]string{
			"name": fmt.Sprintf("name must be between 1 and %d characters", maxNameLen),
		}}
	}
	return name, nil
}
