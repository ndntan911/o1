package billing

import (
	"context"

	"github.com/google/uuid"

	"o1/internal/db/sqlc"
)

const billingLockID int64 = 0x6f310004

// MaintenanceResult counts the transitions applied in one pass.
type MaintenanceResult struct {
	TrialsEnded     int
	PastDueExpired  int
	CanceledExpired int
}

// RunMaintenance advances time-based subscription states. It runs behind a
// PostgreSQL advisory lock, so overlapping schedulers are safe.
func (s *Service) RunMaintenance(ctx context.Context) (MaintenanceResult, error) {
	var res MaintenanceResult

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return res, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", billingLockID).Scan(&locked); err != nil {
		return res, err
	}
	if !locked {
		return res, nil
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", billingLockID)
	}()

	now := s.now()
	trials, err := s.q.ListTrialsToEnd(ctx, &now)
	if err != nil {
		return res, err
	}
	for _, sub := range trials {
		if err := s.downgrade(ctx, sub, "active", "trial_ended"); err != nil {
			return res, err
		}
		res.TrialsEnded++
	}

	pastDueCutoff := now.AddDate(0, 0, -7)
	pastDue, err := s.q.ListPastDueToExpire(ctx, &pastDueCutoff)
	if err != nil {
		return res, err
	}
	for _, sub := range pastDue {
		if err := s.downgrade(ctx, sub, "expired", "subscription_expired"); err != nil {
			return res, err
		}
		res.PastDueExpired++
	}

	canceled, err := s.q.ListCanceledToExpire(ctx, &now)
	if err != nil {
		return res, err
	}
	for _, sub := range canceled {
		if err := s.downgrade(ctx, sub, "expired", "subscription_expired"); err != nil {
			return res, err
		}
		res.CanceledExpired++
	}
	return res, nil
}

func (s *Service) downgrade(ctx context.Context, sub sqlc.Subscription, status, auditKind string) error {
	free, err := s.q.GetPlan(ctx, "free")
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	sub.PlanKey = free.Key
	sub.Status = status
	sub.TrialEnd = nil
	sub.PastDueSince = nil
	sub.CancelAtPeriodEnd = false
	sub.Provider = "internal"
	sub.ProviderSubscriptionID = nil
	sub.ProviderCustomerID = nil
	sub.UpdatedAt = s.now()
	if err := qtx.UpsertSubscription(ctx, subscriptionParams(sub)); err != nil {
		return err
	}
	if err := qtx.UpdateWorkspaceMonitorLimit(ctx, sqlc.UpdateWorkspaceMonitorLimitParams{
		ID: sub.WorkspaceID, MonitorLimit: free.MonitorLimit,
	}); err != nil {
		return err
	}
	if err := s.audit(ctx, qtx, sub.WorkspaceID, uuid.Nil, auditKind, map[string]any{
		"plan": free.Key, "status": status,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
