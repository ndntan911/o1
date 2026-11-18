package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
)

// Service owns subscription state and plan transitions.
type Service struct {
	pool     *pgxpool.Pool
	q        *sqlc.Queries
	provider Provider
	now      func() time.Time
}

// NewService wires the billing service to a provider.
func NewService(pool *pgxpool.Pool, q *sqlc.Queries, provider Provider) *Service {
	return &Service{pool: pool, q: q, provider: provider, now: time.Now}
}

// ProviderName reports the configured provider ("sandbox").
func (s *Service) ProviderName() string { return s.provider.Name() }

// View is what the billing endpoint returns to the dashboard.
type View struct {
	Plan              sqlc.Plan
	Status            string
	Provider          string
	CurrentPeriodEnd  *time.Time
	TrialEnd          *time.Time
	CancelAtPeriodEnd bool
	Limits            Limits
}

// Get returns the workspace subscription and its plan.
func (s *Service) Get(ctx context.Context, workspaceID uuid.UUID) (View, error) {
	sub, err := s.q.GetSubscription(ctx, workspaceID)
	if err != nil {
		return View{}, err
	}
	plan, err := s.q.GetPlan(ctx, sub.PlanKey)
	if err != nil {
		return View{}, err
	}
	return View{
		Plan: plan, Status: sub.Status, Provider: sub.Provider,
		CurrentPeriodEnd: sub.CurrentPeriodEnd, TrialEnd: sub.TrialEnd,
		CancelAtPeriodEnd: sub.CancelAtPeriodEnd, Limits: LimitsForPlan(plan),
	}, nil
}

// Checkout starts a hosted sandbox checkout for a paid plan.
func (s *Service) Checkout(ctx context.Context, workspaceID uuid.UUID, planKey string) (CheckoutSession, error) {
	plan, err := s.q.GetPlan(ctx, planKey)
	if err != nil || plan.PriceCents == 0 {
		return CheckoutSession{}, ErrInvalidPlan
	}
	sub, err := s.q.GetSubscription(ctx, workspaceID)
	if err == nil && (sub.PlanKey == "pro" || sub.PlanKey == "team") &&
		(sub.Status == "active" || sub.Status == "trialing" || sub.Status == "past_due") {
		return CheckoutSession{}, ErrAlreadySubscribed
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CheckoutSession{}, err
	}
	return s.provider.CreateCheckout(ctx, CheckoutInput{
		WorkspaceID: workspaceID, PlanKey: planKey,
	})
}

// Cancel marks the subscription to end at the current period.
func (s *Service) Cancel(ctx context.Context, actorID, workspaceID uuid.UUID) error {
	sub, err := s.q.GetSubscription(ctx, workspaceID)
	if err != nil {
		return err
	}
	if sub.Status == "expired" || sub.Status == "canceled" {
		return fmt.Errorf("%w: already ending", ErrInvalidPlan)
	}
	sub.CancelAtPeriodEnd = true
	sub.Status = "canceled"
	sub.UpdatedAt = s.now()
	if err := s.q.UpsertSubscription(ctx, subscriptionParams(sub)); err != nil {
		return err
	}
	return s.audit(ctx, s.q, workspaceID, actorID, "subscription_canceled", map[string]any{
		"plan": sub.PlanKey,
	})
}

// AdminSetPlan is the audited support override.
func (s *Service) AdminSetPlan(ctx context.Context, actorID, workspaceID uuid.UUID, planKey string) error {
	plan, err := s.q.GetPlan(ctx, planKey)
	if err != nil {
		return ErrInvalidPlan
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	sub, err := qtx.GetSubscription(ctx, workspaceID)
	if err != nil {
		return err
	}
	sub.Provider = "internal"
	sub.ProviderSubscriptionID = nil
	sub.ProviderCustomerID = nil
	sub.PlanKey = plan.Key
	sub.Status = "active"
	sub.TrialEnd = nil
	sub.PastDueSince = nil
	sub.CancelAtPeriodEnd = false
	sub.UpdatedAt = s.now()
	if err := qtx.UpsertSubscription(ctx, subscriptionParams(sub)); err != nil {
		return err
	}
	if err := qtx.UpdateWorkspaceMonitorLimit(ctx, sqlc.UpdateWorkspaceMonitorLimitParams{
		ID: workspaceID, MonitorLimit: plan.MonitorLimit,
	}); err != nil {
		return err
	}
	if err := s.audit(ctx, qtx, workspaceID, actorID, "plan_changed", map[string]any{
		"plan": plan.Key, "source": "admin",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ApplyEvent is the single state-changing path for provider events. It is
// idempotent by (provider, event id) and runs in one transaction with the
// limit cache and the audit row.
func (s *Service) ApplyEvent(ctx context.Context, provider string, ev Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	inserted, err := qtx.InsertBillingEvent(ctx, sqlc.InsertBillingEventParams{
		ID: uuid.New(), Provider: provider, ProviderEventID: ev.ID,
		Kind: ev.Kind, Payload: payload,
	})
	if err != nil {
		return err
	}
	if inserted == 0 {
		return tx.Commit(ctx) // duplicate: already applied
	}

	var sub sqlc.Subscription
	existing := true
	if ev.ProviderSubscriptionID != "" {
		sub, err = qtx.GetSubscriptionByProviderID(ctx, &ev.ProviderSubscriptionID)
		if errors.Is(err, pgx.ErrNoRows) {
			existing = false
			if ev.WorkspaceID == "" {
				return fmt.Errorf("billing: new provider subscription without a workspace")
			}
			workspaceID, parseErr := uuid.Parse(ev.WorkspaceID)
			if parseErr != nil {
				return fmt.Errorf("billing: bad workspace id: %w", parseErr)
			}
			sub = sqlc.Subscription{WorkspaceID: workspaceID}
		} else if err != nil {
			return err
		}
	} else if ev.WorkspaceID != "" {
		workspaceID, parseErr := uuid.Parse(ev.WorkspaceID)
		if parseErr != nil {
			return fmt.Errorf("billing: bad workspace id: %w", parseErr)
		}
		sub, err = qtx.GetSubscription(ctx, workspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			existing = false
			sub = sqlc.Subscription{WorkspaceID: workspaceID}
		} else if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("billing: event has no workspace")
	}

	// Ignore a stale renewal, but never drop a cancellation or a failure.
	if existing && ev.CurrentPeriodEnd != nil && sub.CurrentPeriodEnd != nil &&
		ev.CurrentPeriodEnd.Before(*sub.CurrentPeriodEnd) &&
		ev.Kind != "subscription.canceled" && ev.Kind != "subscription.payment_failed" {
		if err := qtx.MarkBillingEventProcessed(ctx, sqlc.MarkBillingEventProcessedParams{
			Provider: provider, ProviderEventID: ev.ID,
		}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	planKey := ev.PlanKey
	if planKey == "" {
		planKey = sub.PlanKey
	}
	plan, err := qtx.GetPlan(ctx, planKey)
	if err != nil {
		return fmt.Errorf("billing: unknown plan %q: %w", planKey, err)
	}

	now := s.now()
	switch ev.Kind {
	case "subscription.started":
		sub.Status = "active"
		sub.TrialEnd = nil
		if plan.TrialDays > 0 {
			sub.Status = "trialing"
			trialEnd := now.AddDate(0, 0, int(plan.TrialDays))
			sub.TrialEnd = &trialEnd
		}
		end := now.AddDate(0, 0, 30)
		if ev.CurrentPeriodEnd != nil {
			end = *ev.CurrentPeriodEnd
		}
		sub.CurrentPeriodEnd = &end
		sub.CancelAtPeriodEnd = false
	case "subscription.renewed":
		sub.Status = "active"
		sub.TrialEnd = nil
		sub.PastDueSince = nil
		if ev.CurrentPeriodEnd != nil {
			sub.CurrentPeriodEnd = ev.CurrentPeriodEnd
		}
	case "subscription.payment_failed":
		sub.Status = "past_due"
		if sub.PastDueSince == nil {
			sub.PastDueSince = &now
		}
	case "subscription.canceled":
		sub.Status = "canceled"
		sub.CancelAtPeriodEnd = true
		if ev.CurrentPeriodEnd != nil {
			sub.CurrentPeriodEnd = ev.CurrentPeriodEnd
		}
	default:
		return fmt.Errorf("billing: unknown event kind %q", ev.Kind)
	}

	sub.Provider = provider
	if ev.ProviderSubscriptionID != "" {
		sub.ProviderSubscriptionID = &ev.ProviderSubscriptionID
	}
	if ev.ProviderCustomerID != "" {
		sub.ProviderCustomerID = &ev.ProviderCustomerID
	}
	sub.PlanKey = plan.Key
	sub.UpdatedAt = now
	if err := qtx.UpsertSubscription(ctx, subscriptionParams(sub)); err != nil {
		return err
	}
	if err := qtx.UpdateWorkspaceMonitorLimit(ctx, sqlc.UpdateWorkspaceMonitorLimitParams{
		ID: sub.WorkspaceID, MonitorLimit: plan.MonitorLimit,
	}); err != nil {
		return err
	}
	auditKind := "plan_changed"
	if ev.Kind == "subscription.payment_failed" {
		auditKind = "payment_failed"
	}
	if err := s.audit(ctx, qtx, sub.WorkspaceID, uuid.Nil, auditKind, map[string]any{
		"plan": plan.Key, "status": sub.Status, "event": ev.Kind,
	}); err != nil {
		return err
	}
	if err := qtx.MarkBillingEventProcessed(ctx, sqlc.MarkBillingEventProcessedParams{
		Provider: provider, ProviderEventID: ev.ID,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func subscriptionParams(sub sqlc.Subscription) sqlc.UpsertSubscriptionParams {
	return sqlc.UpsertSubscriptionParams{
		WorkspaceID:            sub.WorkspaceID,
		Provider:               sub.Provider,
		ProviderCustomerID:     sub.ProviderCustomerID,
		ProviderSubscriptionID: sub.ProviderSubscriptionID,
		PlanKey:                sub.PlanKey,
		Status:                 sub.Status,
		CurrentPeriodEnd:       sub.CurrentPeriodEnd,
		TrialEnd:               sub.TrialEnd,
		PastDueSince:           sub.PastDueSince,
		CancelAtPeriodEnd:      sub.CancelAtPeriodEnd,
	}
}

func optionalUUID(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

func (s *Service) audit(ctx context.Context, q *sqlc.Queries, workspaceID, actorID uuid.UUID, kind string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return q.CreateSecurityEvent(ctx, sqlc.CreateSecurityEventParams{
		ID: uuid.New(), WorkspaceID: optionalUUID(workspaceID),
		ActorUserID: optionalUUID(actorID), Kind: kind, Detail: raw,
	})
}
