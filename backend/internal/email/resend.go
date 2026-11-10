package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultResendBaseURL = "https://api.resend.com"
	resendRequestTimeout = 10 * time.Second
	resendMaxDrainBytes  = 64 << 10
	resendUserAgent      = "o1-email/1.0"
)

// DeliveryError is a sanitized email delivery failure. Message never contains
// credentials, recipients, request bodies, or provider response bodies.
// Permanent errors must not be retried.
type DeliveryError struct {
	Message   string
	Permanent bool
	Cause     error
}

// Error returns the sanitized message only.
func (e *DeliveryError) Error() string {
	return e.Message
}

// Unwrap exposes the underlying cause for errors.Is/errors.As.
func (e *DeliveryError) Unwrap() error {
	return e.Cause
}

// IsPermanent reports whether err is a permanent delivery failure that should
// not be retried. Unknown errors are treated as transient.
func IsPermanent(err error) bool {
	var deliveryErr *DeliveryError
	if errors.As(err, &deliveryErr) {
		return deliveryErr.Permanent
	}
	return false
}

// ResendSender delivers mail through the Resend HTTP API.
type ResendSender struct {
	apiKey  string
	from    string
	replyTo string
	baseURL string
	client  *http.Client
	limiter *rate.Limiter
}

// NewResendSender returns a sender that posts messages to the Resend API at
// baseURL (default https://api.resend.com) using apiKey. Request rate is
// bounded per sender by rps and burst; rps <= 0 disables limiting.
func NewResendSender(apiKey, from, replyTo, baseURL string, rps float64, burst int) *ResendSender {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultResendBaseURL
	}
	if burst < 1 {
		burst = 1
	}
	limiter := rate.NewLimiter(rate.Inf, burst)
	if rps > 0 {
		limiter = rate.NewLimiter(rate.Limit(rps), burst)
	}

	return &ResendSender{
		apiKey:  apiKey,
		from:    from,
		replyTo: replyTo,
		baseURL: baseURL,
		client: &http.Client{
			Timeout: resendRequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				// A redirect must never move the bearer token to another host.
				return http.ErrUseLastResponse
			},
		},
		limiter: limiter,
	}
}

type resendPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
	ReplyTo string   `json:"reply_to,omitempty"`
}

// Send delivers msg through Resend. It validates the message and waits on the
// per-sender rate limiter before any network I/O. Transport failures, timeouts,
// 408, 425, 429, and 5xx responses are transient; every other non-2xx response
// is permanent.
func (s *ResendSender) Send(ctx context.Context, msg Message) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMessage(s.from, msg); err != nil {
		return err
	}
	if err := s.limiter.Wait(ctx); err != nil {
		return &DeliveryError{
			Message: "resend rate limit wait failed",
			Cause:   err,
		}
	}

	payload, err := json.Marshal(resendPayload{
		From:    s.from,
		To:      []string{msg.To},
		Subject: msg.Subject,
		Text:    msg.Text,
		HTML:    msg.HTML,
		ReplyTo: s.replyTo,
	})
	if err != nil {
		return &DeliveryError{
			Message: "resend request failed",
			Cause:   err,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/emails", bytes.NewReader(payload))
	if err != nil {
		return &DeliveryError{
			Message: "resend request failed",
			Cause:   err,
		}
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", resendUserAgent)
	if msg.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", msg.IdempotencyKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return &DeliveryError{
			Message: "resend request failed",
			Cause:   err,
		}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	// Drain a bounded amount so the connection can be reused without trusting
	// an arbitrarily large provider response.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, resendMaxDrainBytes))

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	return &DeliveryError{
		Message:   fmt.Sprintf("resend returned HTTP %d", resp.StatusCode),
		Permanent: isPermanentStatus(resp.StatusCode),
	}
}

// isPermanentStatus classifies a non-2xx HTTP status.
func isPermanentStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return false
	}
	return status < http.StatusInternalServerError
}
