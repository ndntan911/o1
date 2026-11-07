package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"o1/internal/probe"
)

const (
	notifierTimeout        = 10 * time.Second
	notifierDrainLimit     = 64 << 10
	notifierUserAgent      = "o1-alerts/1.0"
	defaultTelegramBaseURL = "https://api.telegram.org"
)

// NotificationSender delivers one notification to a decrypted target.
type NotificationSender interface {
	Send(ctx context.Context, target Target, event Event) error
}

// Notifier sends webhook, Slack, and Telegram notifications. Webhook and Slack
// requests use a guarded HTTP client that never follows redirects; Telegram
// always uses the base URL fixed at construction.
type Notifier struct {
	guard           *probe.Guard
	client          *http.Client
	telegramClient  *http.Client
	telegramBaseURL string
	now             func() time.Time
}

// NewNotifier builds a Notifier. An empty telegramBaseURL uses the public
// Telegram API.
func NewNotifier(guard *probe.Guard, telegramBaseURL string) *Notifier {
	if guard == nil {
		guard = probe.NewGuard(probe.Policy{})
	}
	if telegramBaseURL == "" {
		telegramBaseURL = defaultTelegramBaseURL
	}
	return &Notifier{
		guard:  guard,
		client: guard.HTTPClient(false),
		telegramClient: &http.Client{
			Timeout: notifierTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		telegramBaseURL: strings.TrimRight(telegramBaseURL, "/"),
		now:             time.Now,
	}
}

// DeliveryError is a sanitized delivery failure. Its message never contains the
// destination URL, path, query, token, or signing secret.
type DeliveryError struct {
	Message   string
	Permanent bool
	Cause     error
}

// Error returns only the sanitized message.
func (e *DeliveryError) Error() string {
	return e.Message
}

// Unwrap exposes the underlying cause to errors.Is/errors.As callers.
func (e *DeliveryError) Unwrap() error {
	return e.Cause
}

// IsPermanent reports whether err is a non-retryable delivery failure.
func IsPermanent(err error) bool {
	var deliveryErr *DeliveryError
	return errors.As(err, &deliveryErr) && deliveryErr.Permanent
}

// Send delivers one event. The target is re-validated for webhook and Slack
// deliveries so DNS changes cannot bypass the SSRF policy.
func (n *Notifier) Send(ctx context.Context, target Target, event Event) error {
	if n == nil {
		return &DeliveryError{Message: "alert notifier is not configured", Permanent: true, Cause: ErrInvalidChannelConfig}
	}
	ctx, cancel := context.WithTimeout(ctx, notifierTimeout)
	defer cancel()

	switch target.Kind {
	case KindWebhook:
		if err := n.validateTarget(ctx, target); err != nil {
			return err
		}
		body, err := marshalJSON(webhookPayload(event))
		if err != nil {
			return &DeliveryError{Message: "webhook payload could not be encoded", Permanent: true, Cause: err}
		}
		headers := map[string]string{
			"Content-Type": "application/json",
			"User-Agent":   notifierUserAgent,
			"X-O1-Event":   event.Kind,
		}
		if target.SigningSecret != "" {
			timestamp := strconv.FormatInt(n.now().UTC().Unix(), 10)
			headers["X-O1-Timestamp"] = timestamp
			headers["X-O1-Signature"] = webhookSignature(target.SigningSecret, timestamp, body)
		}
		return n.post(ctx, n.client, target.URL, body, headers, "webhook")

	case KindSlack:
		if err := n.validateTarget(ctx, target); err != nil {
			return err
		}
		return n.post(ctx, n.client, target.URL, slackPayload(event), map[string]string{
			"Content-Type": "application/json",
			"User-Agent":   notifierUserAgent,
		}, "slack")

	case KindTelegram:
		destination := n.telegramBaseURL + "/bot" + target.BotToken + "/sendMessage"
		return n.post(ctx, n.telegramClient, destination, telegramPayload(event, target.ChatID), map[string]string{
			"Content-Type": "application/json",
			"User-Agent":   notifierUserAgent,
		}, "telegram")

	default:
		return &DeliveryError{Message: "unsupported alert channel kind", Permanent: true, Cause: ErrInvalidChannelConfig}
	}
}

func (n *Notifier) validateTarget(ctx context.Context, target Target) error {
	u, err := n.guard.ValidateURL(ctx, target.URL)
	if err != nil {
		return &DeliveryError{Message: "alert target is not allowed", Permanent: true, Cause: err}
	}
	if target.Kind == KindSlack {
		if message := validateSlackURL(u); message != "" {
			return &DeliveryError{Message: "slack target is not allowed", Permanent: true}
		}
	}
	return nil
}

func (n *Notifier) post(ctx context.Context, client *http.Client, destination string, body []byte, headers map[string]string, label string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, bytes.NewReader(body))
	if err != nil {
		return &DeliveryError{Message: label + " request could not be built", Permanent: true, Cause: err}
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return &DeliveryError{Message: label + " request failed", Cause: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, notifierDrainLimit))

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	permanent := !(resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooEarly ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= http.StatusInternalServerError)
	return &DeliveryError{
		Message:   fmt.Sprintf("%s returned HTTP %d", label, resp.StatusCode),
		Permanent: permanent,
	}
}

type webhookWorkspace struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type webhookMonitor struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	URL    string    `json:"url"`
	Status string    `json:"status"`
}

type webhookIncident struct {
	ID             uuid.UUID  `json:"id"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
	ResolvedAt     *time.Time `json:"resolved_at"`
}

type webhookBody struct {
	Event     string           `json:"event"`
	Sequence  int              `json:"sequence"`
	Workspace webhookWorkspace `json:"workspace"`
	Monitor   *webhookMonitor  `json:"monitor"`
	Incident  *webhookIncident `json:"incident"`
	Link      string           `json:"link"`
}

func webhookPayload(event Event) webhookBody {
	body := webhookBody{
		Event:    event.Kind,
		Sequence: event.Sequence,
		Workspace: webhookWorkspace{
			Name: event.WorkspaceName,
			Slug: event.WorkspaceSlug,
		},
		Link: event.Link,
	}
	switch event.Kind {
	case "opened", "reminder", "resolved":
		monitorStatus := "down"
		incidentStatus := "open"
		if event.Kind == "resolved" {
			monitorStatus = "up"
			incidentStatus = "resolved"
		}
		body.Monitor = &webhookMonitor{
			ID:     event.MonitorID,
			Name:   event.MonitorName,
			URL:    event.MonitorURL,
			Status: monitorStatus,
		}
		body.Incident = &webhookIncident{
			ID:             event.IncidentID,
			Status:         incidentStatus,
			StartedAt:      event.StartedAt,
			AcknowledgedAt: event.AcknowledgedAt,
			ResolvedAt:     event.ResolvedAt,
		}
	}
	return body
}

func webhookSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func slackPayload(event Event) []byte {
	var text string
	if event.Kind == "test" {
		text = fmt.Sprintf("Test notification from o1 for %s. %s", event.WorkspaceName, event.Link)
	} else {
		emoji, wording := "🔴", "is down"
		switch event.Kind {
		case "reminder":
			wording = "is still down"
		case "resolved":
			emoji, wording = "🟢", "recovered"
		}
		text = fmt.Sprintf("%s %s: %s %s", emoji, event.WorkspaceName, event.MonitorName, wording)
		if event.MonitorURL != "" {
			text += "\n" + event.MonitorURL
		}
		text += "\n" + event.Link
	}
	body, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	return body
}

func telegramPayload(event Event, chatID string) []byte {
	var text string
	if event.Kind == "test" {
		text = fmt.Sprintf("Test notification from o1 for %s. %s", event.WorkspaceName, event.Link)
	} else {
		emoji, wording := "🔴", "is down"
		switch event.Kind {
		case "reminder":
			wording = "is still down"
		case "resolved":
			emoji, wording = "🟢", "recovered"
		}
		text = fmt.Sprintf("%s %s: %s %s\n%s", emoji, event.WorkspaceName, event.MonitorName, wording, event.Link)
	}
	body, _ := json.Marshal(struct {
		ChatID                string `json:"chat_id"`
		Text                  string `json:"text"`
		DisableWebPagePreview bool   `json:"disable_web_page_preview"`
	}{ChatID: chatID, Text: text, DisableWebPagePreview: true})
	return body
}

func marshalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}
