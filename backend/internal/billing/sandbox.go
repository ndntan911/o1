package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sandbox is an in-process provider for demos and local development. It never
// contacts a payment service; the checkout page posts back a signed event.
type Sandbox struct {
	secret []byte
	now    func() time.Time
}

// NewSandbox builds a sandbox provider with the webhook signing secret.
func NewSandbox(secret []byte) *Sandbox {
	return &Sandbox{secret: secret, now: time.Now}
}

// Name implements Provider.
func (s *Sandbox) Name() string { return "sandbox" }

type sandboxToken struct {
	WorkspaceID string `json:"workspace_id"`
	PlanKey     string `json:"plan_key"`
	Expires     int64  `json:"expires"`
}

// CreateCheckout returns an in-app URL carrying a signed, short-lived token.
func (s *Sandbox) CreateCheckout(_ context.Context, in CheckoutInput) (CheckoutSession, error) {
	raw, err := json.Marshal(sandboxToken{
		WorkspaceID: in.WorkspaceID.String(),
		PlanKey:     in.PlanKey,
		Expires:     s.now().Add(15 * time.Minute).Unix(),
	})
	if err != nil {
		return CheckoutSession{}, fmt.Errorf("encode sandbox token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw) + "." + s.sign(raw)
	return CheckoutSession{
		URL: fmt.Sprintf("/w/%s/settings/billing/sandbox?token=%s", in.Slug, token),
	}, nil
}

// ParseToken verifies and decodes a sandbox checkout token.
func (s *Sandbox) ParseToken(token string) (CheckoutInput, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return CheckoutInput{}, ErrInvalidSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return CheckoutInput{}, ErrInvalidSignature
	}
	if !hmac.Equal([]byte(parts[1]), []byte(s.sign(raw))) {
		return CheckoutInput{}, ErrInvalidSignature
	}
	var t sandboxToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return CheckoutInput{}, ErrInvalidSignature
	}
	if s.now().Unix() > t.Expires {
		return CheckoutInput{}, fmt.Errorf("%w: token expired", ErrInvalidSignature)
	}
	workspaceID, err := uuid.Parse(t.WorkspaceID)
	if err != nil {
		return CheckoutInput{}, ErrInvalidSignature
	}
	return CheckoutInput{WorkspaceID: workspaceID, PlanKey: t.PlanKey}, nil
}

// SignEvent returns the JSON body and the header value for a sandbox event.
func (s *Sandbox) SignEvent(ev Event) ([]byte, string, error) {
	body, err := json.Marshal(ev)
	if err != nil {
		return nil, "", fmt.Errorf("encode sandbox event: %w", err)
	}
	return body, "sha256=" + s.sign(body), nil
}

// ParseWebhook verifies the HMAC header and decodes the event.
func (s *Sandbox) ParseWebhook(r *http.Request) (Event, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		return Event{}, fmt.Errorf("%w: read body", ErrInvalidSignature)
	}
	want := "sha256=" + s.sign(body)
	if !hmac.Equal([]byte(r.Header.Get("X-O1-Sandbox-Signature")), []byte(want)) {
		return Event{}, ErrInvalidSignature
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" || ev.Kind == "" {
		return Event{}, ErrInvalidSignature
	}
	return ev, nil
}

func (s *Sandbox) sign(payload []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
