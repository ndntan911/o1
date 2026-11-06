package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"o1/internal/db/sqlc"
	"o1/internal/probe"
	"o1/internal/secretbox"
)

const (
	maxURLLen           = 2048
	minSigningSecretLen = 16
	maxSigningSecretLen = 128
	maxBotTokenLen      = 128
	maxChatIDLen        = 64
)

var (
	telegramTokenPattern    = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	telegramChatPattern     = regexp.MustCompile(`^-?[0-9]+$`)
	telegramUsernamePattern = regexp.MustCompile(`^@[A-Za-z][A-Za-z0-9_]{3,}$`)
	slackHosts              = map[string]struct{}{
		"hooks.slack.com":     {},
		"hooks.slack-gov.com": {},
	}
)

const invalidKindMessage = "kind must be one of webhook, slack, or telegram"

// SealCreateConfig validates in for kind and returns the sealed JSON config to
// store. It never includes plaintext secrets in the returned bytes.
func SealCreateConfig(box *secretbox.Box, guard *probe.Guard, kind string, in CreateInput) ([]byte, error) {
	switch kind {
	case KindEmail:
		return nil, ErrEmailChannel
	case KindWebhook:
		return sealWebhookConfig(box, guard, in)
	case KindSlack:
		return sealSlackConfig(box, guard, in)
	case KindTelegram:
		return sealTelegramConfig(box, in)
	default:
		return nil, &ValidationError{Fields: map[string]string{"kind": invalidKindMessage}}
	}
}

func sealWebhookConfig(box *secretbox.Box, guard *probe.Guard, in CreateInput) ([]byte, error) {
	fields := make(map[string]string)

	rawURL := strings.TrimSpace(in.URL)
	u, message := validateHTTPURL(guard, rawURL)
	if message != "" {
		fields["url"] = message
	}
	if message := validateSigningSecret(in.SigningSecret); message != "" {
		fields["signing_secret"] = message
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}

	sealedURL, err := sealValue(box, rawURL)
	if err != nil {
		return nil, err
	}
	sealedSecret, err := sealValue(box, in.SigningSecret)
	if err != nil {
		return nil, err
	}
	return marshalConfig(WebhookConfig{
		URLEnc:           sealedURL,
		URLPreview:       urlPreview(u),
		SigningSecretEnc: sealedSecret,
	})
}

func sealSlackConfig(box *secretbox.Box, guard *probe.Guard, in CreateInput) ([]byte, error) {
	rawURL := strings.TrimSpace(in.URL)
	u, message := validateHTTPURL(guard, rawURL)
	if message == "" {
		message = validateSlackURL(u)
	}
	if message != "" {
		return nil, &ValidationError{Fields: map[string]string{"url": message}}
	}

	sealedURL, err := sealValue(box, rawURL)
	if err != nil {
		return nil, err
	}
	return marshalConfig(SlackConfig{URLEnc: sealedURL, URLPreview: urlPreview(u)})
}

func sealTelegramConfig(box *secretbox.Box, in CreateInput) ([]byte, error) {
	token := strings.TrimSpace(in.BotToken)
	chatID := strings.TrimSpace(in.ChatID)
	fields := make(map[string]string)
	if message := validateBotToken(token); message != "" {
		fields["bot_token"] = message
	}
	if message := validateChatID(chatID); message != "" {
		fields["chat_id"] = message
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}

	sealedToken, err := sealValue(box, token)
	if err != nil {
		return nil, err
	}
	return marshalConfig(TelegramConfig{BotTokenEnc: sealedToken, ChatID: chatID})
}

// MergeUpdateConfig decodes the existing config, applies the provided update,
// and returns the sealed JSON config to store. Omitted or empty secrets keep
// their stored value; ClearSigningSecret removes an optional webhook secret.
func MergeUpdateConfig(box *secretbox.Box, guard *probe.Guard, kind string, existing []byte, in UpdateInput) ([]byte, error) {
	switch kind {
	case KindEmail:
		return nil, ErrEmailChannel
	case KindWebhook:
		return mergeWebhookConfig(box, guard, existing, in)
	case KindSlack:
		return mergeSlackConfig(box, guard, existing, in)
	case KindTelegram:
		return mergeTelegramConfig(box, existing, in)
	default:
		return nil, &ValidationError{Fields: map[string]string{"kind": invalidKindMessage}}
	}
}

func mergeWebhookConfig(box *secretbox.Box, guard *probe.Guard, existing []byte, in UpdateInput) ([]byte, error) {
	var cfg WebhookConfig
	if err := decodeConfig(existing, &cfg); err != nil || cfg.URLEnc == "" {
		return nil, ErrInvalidChannelConfig
	}

	fields := make(map[string]string)
	if in.URL != nil {
		rawURL := strings.TrimSpace(*in.URL)
		u, message := validateHTTPURL(guard, rawURL)
		if message != "" {
			fields["url"] = message
		} else {
			sealedURL, err := sealValue(box, rawURL)
			if err != nil {
				return nil, err
			}
			cfg.URLEnc = sealedURL
			cfg.URLPreview = urlPreview(u)
		}
	}

	switch {
	case in.ClearSigningSecret:
		cfg.SigningSecretEnc = ""
	case in.SigningSecret != nil && *in.SigningSecret != "":
		if message := validateSigningSecret(*in.SigningSecret); message != "" {
			fields["signing_secret"] = message
		} else {
			sealedSecret, err := sealValue(box, *in.SigningSecret)
			if err != nil {
				return nil, err
			}
			cfg.SigningSecretEnc = sealedSecret
		}
	}

	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	return marshalConfig(cfg)
}

func mergeSlackConfig(box *secretbox.Box, guard *probe.Guard, existing []byte, in UpdateInput) ([]byte, error) {
	var cfg SlackConfig
	if err := decodeConfig(existing, &cfg); err != nil || cfg.URLEnc == "" {
		return nil, ErrInvalidChannelConfig
	}
	if in.URL == nil {
		return marshalConfig(cfg)
	}

	rawURL := strings.TrimSpace(*in.URL)
	u, message := validateHTTPURL(guard, rawURL)
	if message == "" {
		message = validateSlackURL(u)
	}
	if message != "" {
		return nil, &ValidationError{Fields: map[string]string{"url": message}}
	}
	sealedURL, err := sealValue(box, rawURL)
	if err != nil {
		return nil, err
	}
	cfg.URLEnc = sealedURL
	cfg.URLPreview = urlPreview(u)
	return marshalConfig(cfg)
}

func mergeTelegramConfig(box *secretbox.Box, existing []byte, in UpdateInput) ([]byte, error) {
	var cfg TelegramConfig
	if err := decodeConfig(existing, &cfg); err != nil || cfg.BotTokenEnc == "" || cfg.ChatID == "" {
		return nil, ErrInvalidChannelConfig
	}

	fields := make(map[string]string)
	if in.BotToken != nil {
		token := strings.TrimSpace(*in.BotToken)
		if message := validateBotToken(token); message != "" {
			fields["bot_token"] = message
		} else {
			sealedToken, err := sealValue(box, token)
			if err != nil {
				return nil, err
			}
			cfg.BotTokenEnc = sealedToken
		}
	}
	if in.ChatID != nil {
		chatID := strings.TrimSpace(*in.ChatID)
		if message := validateChatID(chatID); message != "" {
			fields["chat_id"] = message
		} else {
			cfg.ChatID = chatID
		}
	}

	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	return marshalConfig(cfg)
}

// ChannelView maps a stored channel row to the client-facing view. It decodes
// the config without decrypting any secret value.
func ChannelView(row sqlc.AlertChannel) (Channel, error) {
	view := Channel{
		ID:        row.ID,
		Kind:      row.Kind,
		Name:      row.Name,
		Enabled:   row.Enabled,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}

	switch row.Kind {
	case KindEmail:
		var cfg EmailConfig
		if err := decodeConfig(row.Config, &cfg); err != nil {
			return Channel{}, err
		}
		if cfg.Recipients == nil {
			cfg.Recipients = []string{}
		}
		view.Recipients = cfg.Recipients
	case KindWebhook:
		var cfg WebhookConfig
		if err := decodeConfig(row.Config, &cfg); err != nil {
			return Channel{}, err
		}
		view.URLPreview = cfg.URLPreview
		view.SigningSecretSet = cfg.SigningSecretEnc != ""
	case KindSlack:
		var cfg SlackConfig
		if err := decodeConfig(row.Config, &cfg); err != nil {
			return Channel{}, err
		}
		view.URLPreview = cfg.URLPreview
	case KindTelegram:
		var cfg TelegramConfig
		if err := decodeConfig(row.Config, &cfg); err != nil {
			return Channel{}, err
		}
		view.ChatID = cfg.ChatID
		view.BotTokenSet = cfg.BotTokenEnc != ""
	default:
		return Channel{}, ErrInvalidChannelConfig
	}
	return view, nil
}

// OpenTarget decrypts a stored config and returns the delivery target. It
// rejects unknown kinds and the email channel.
func OpenTarget(box *secretbox.Box, kind string, raw []byte) (Target, error) {
	target := Target{Kind: kind}

	switch kind {
	case KindWebhook:
		var cfg WebhookConfig
		if err := decodeConfig(raw, &cfg); err != nil {
			return Target{}, ErrInvalidChannelConfig
		}
		rawURL, err := openRequired(box, cfg.URLEnc)
		if err != nil {
			return Target{}, err
		}
		signingSecret := ""
		if cfg.SigningSecretEnc != "" {
			plaintext, err := box.Open(cfg.SigningSecretEnc)
			if err != nil {
				return Target{}, ErrInvalidChannelConfig
			}
			signingSecret = string(plaintext)
		}
		target.URL = rawURL
		target.SigningSecret = signingSecret
	case KindSlack:
		var cfg SlackConfig
		if err := decodeConfig(raw, &cfg); err != nil {
			return Target{}, ErrInvalidChannelConfig
		}
		rawURL, err := openRequired(box, cfg.URLEnc)
		if err != nil {
			return Target{}, err
		}
		target.URL = rawURL
	case KindTelegram:
		var cfg TelegramConfig
		if err := decodeConfig(raw, &cfg); err != nil {
			return Target{}, ErrInvalidChannelConfig
		}
		if validateChatID(cfg.ChatID) != "" {
			return Target{}, ErrInvalidChannelConfig
		}
		token, err := openRequired(box, cfg.BotTokenEnc)
		if err != nil {
			return Target{}, err
		}
		if validateBotToken(token) != "" {
			return Target{}, ErrInvalidChannelConfig
		}
		target.BotToken = token
		target.ChatID = cfg.ChatID
	default:
		return Target{}, ErrInvalidChannelConfig
	}
	return target, nil
}

// validateHTTPURL enforces the shared URL length and SSRF policy. The caller
// supplies the already-trimmed URL. It returns a field message on failure.
func validateHTTPURL(guard *probe.Guard, raw string) (*url.URL, string) {
	if raw == "" {
		return nil, "url is required"
	}
	if utf8.RuneCountInString(raw) > maxURLLen {
		return nil, fmt.Sprintf("url must be at most %d characters", maxURLLen)
	}
	u, err := guard.ValidateURL(context.Background(), raw)
	if err != nil {
		if errors.Is(err, probe.ErrBlocked) {
			return nil, "url is not allowed"
		}
		return nil, "url is invalid"
	}
	return u, ""
}

// validateSlackURL enforces the Slack-specific scheme, port, and host rules.
func validateSlackURL(u *url.URL) string {
	if u == nil || !strings.EqualFold(u.Scheme, "https") {
		return "url must use https"
	}
	if u.Port() != "" {
		return "url must not include an explicit port"
	}
	if _, ok := slackHosts[strings.ToLower(u.Hostname())]; !ok {
		return "url must be a Slack webhook URL"
	}
	return ""
}

func validateSigningSecret(secret string) string {
	if secret == "" {
		return ""
	}
	if n := utf8.RuneCountInString(secret); n < minSigningSecretLen || n > maxSigningSecretLen {
		return fmt.Sprintf(
			"signing secret must be between %d and %d characters",
			minSigningSecretLen, maxSigningSecretLen,
		)
	}
	return ""
}

func validateBotToken(token string) string {
	switch {
	case token == "":
		return "bot token is required"
	case utf8.RuneCountInString(token) > maxBotTokenLen:
		return fmt.Sprintf("bot token must be at most %d characters", maxBotTokenLen)
	case !telegramTokenPattern.MatchString(token):
		return "bot token is invalid"
	default:
		return ""
	}
}

func validateChatID(chatID string) string {
	switch {
	case chatID == "":
		return "chat id is required"
	case utf8.RuneCountInString(chatID) > maxChatIDLen:
		return fmt.Sprintf("chat id must be at most %d characters", maxChatIDLen)
	case !telegramChatPattern.MatchString(chatID) && !telegramUsernamePattern.MatchString(chatID):
		return "chat id is invalid"
	default:
		return ""
	}
}

// urlPreview returns scheme://host/… without path, query, fragment, or
// userinfo. The actual port is kept when present.
func urlPreview(u *url.URL) string {
	return fmt.Sprintf("%s://%s/…", strings.ToLower(u.Scheme), u.Host)
}

func sealValue(box *secretbox.Box, plaintext string) (string, error) {
	sealed, err := box.Seal([]byte(plaintext))
	if err != nil {
		return "", fmt.Errorf("alerts: seal config: %w", err)
	}
	return sealed, nil
}

func openRequired(box *secretbox.Box, value string) (string, error) {
	if value == "" {
		return "", ErrInvalidChannelConfig
	}
	plaintext, err := box.Open(value)
	if err != nil || len(plaintext) == 0 {
		return "", ErrInvalidChannelConfig
	}
	return string(plaintext), nil
}

func decodeConfig(raw []byte, dest any) error {
	if len(raw) == 0 {
		return ErrInvalidChannelConfig
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return ErrInvalidChannelConfig
	}
	return nil
}

func marshalConfig(cfg any) ([]byte, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("alerts: marshal config: %w", err)
	}
	return raw, nil
}
