package billing

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrAlreadySubscribed is returned when a workspace on a paid plan tries to
	// start another checkout. Cancelling first is the v1 path.
	ErrAlreadySubscribed = errors.New("billing: workspace already has a paid plan")
	// ErrInvalidPlan is returned for an unknown, free, or malformed plan key.
	ErrInvalidPlan = errors.New("billing: invalid plan")
	// ErrNoSubscription means the workspace has no subscription row.
	ErrNoSubscription = errors.New("billing: subscription not found")
	// ErrInvalidSignature is returned when a webhook cannot be authenticated.
	ErrInvalidSignature = errors.New("billing: invalid signature")
)

// CheckoutInput describes one checkout request.
type CheckoutInput struct {
	WorkspaceID uuid.UUID
	PlanKey     string
	AdminEmail  string
}

// CheckoutSession is the URL the browser should visit.
type CheckoutSession struct {
	URL string
}

// Event is the provider-neutral webhook payload. Adapters translate their own
// payload into this shape.
type Event struct {
	ID                     string     `json:"id"`
	Kind                   string     `json:"kind"`
	WorkspaceID            string     `json:"workspace_id,omitempty"`
	PlanKey                string     `json:"plan_key,omitempty"`
	Status                 string     `json:"status,omitempty"`
	ProviderSubscriptionID string     `json:"provider_subscription_id,omitempty"`
	ProviderCustomerID     string     `json:"provider_customer_id,omitempty"`
	CurrentPeriodEnd       *time.Time `json:"current_period_end,omitempty"`
	TrialEnd               *time.Time `json:"trial_end,omitempty"`
	CancelAtPeriodEnd      *bool      `json:"cancel_at_period_end,omitempty"`
}

// Provider is the payment boundary. Plan 15 ships Sandbox only; a real
// merchant-of-record adapter implements the same three methods.
type Provider interface {
	Name() string
	CreateCheckout(ctx context.Context, in CheckoutInput) (CheckoutSession, error)
	ParseWebhook(r *http.Request) (Event, error)
}
