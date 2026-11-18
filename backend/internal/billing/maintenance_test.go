package billing

import (
	"context"
	"testing"
	"time"
)

func TestMaintenanceEndsTrialsAndExpiresOldStates(t *testing.T) {
	svc, q, wsID, now := newBillingEnv(t)
	ctx := context.Background()
	if err := svc.ApplyEvent(ctx, "sandbox", startedEvent(wsID, "pro")); err != nil {
		t.Fatalf("started: %v", err)
	}

	// Trial ends after 14 days: back to Free limits, active.
	svc.now = func() time.Time { return now.AddDate(0, 0, 15) }
	res, err := svc.RunMaintenance(ctx)
	if err != nil {
		t.Fatalf("maintenance trial: %v", err)
	}
	if res.TrialsEnded != 1 {
		t.Fatalf("trials ended = %d, want 1", res.TrialsEnded)
	}
	sub, _ := q.GetSubscription(ctx, wsID)
	if sub.PlanKey != "free" || sub.Status != "active" {
		t.Fatalf("after trial = %s/%s, want free/active", sub.PlanKey, sub.Status)
	}
	if limit := monitorLimit(t, svc, wsID); limit != 5 {
		t.Fatalf("limit after trial = %d, want 5", limit)
	}

	// Past due for more than seven days: expired.
	pastDue := now.AddDate(0, 0, -8)
	if _, err := svc.pool.Exec(ctx, `
		UPDATE subscriptions SET status='past_due', past_due_since=$2, plan_key='pro'
		WHERE workspace_id=$1`, wsID, pastDue); err != nil {
		t.Fatalf("seed past_due: %v", err)
	}
	res, err = svc.RunMaintenance(ctx)
	if err != nil {
		t.Fatalf("maintenance past_due: %v", err)
	}
	if res.PastDueExpired != 1 {
		t.Fatalf("past due expired = %d, want 1", res.PastDueExpired)
	}
	sub, _ = q.GetSubscription(ctx, wsID)
	if sub.Status != "expired" || sub.PlanKey != "free" {
		t.Fatalf("after past_due = %s/%s", sub.PlanKey, sub.Status)
	}

	// Cancelled at period end: expired once the period is over.
	yesterday := now.AddDate(0, 0, -1)
	if _, err := svc.pool.Exec(ctx, `
		UPDATE subscriptions SET status='canceled', current_period_end=$2, plan_key='pro'
		WHERE workspace_id=$1`, wsID, yesterday); err != nil {
		t.Fatalf("seed canceled: %v", err)
	}
	res, err = svc.RunMaintenance(ctx)
	if err != nil {
		t.Fatalf("maintenance canceled: %v", err)
	}
	if res.CanceledExpired != 1 {
		t.Fatalf("canceled expired = %d, want 1", res.CanceledExpired)
	}

	var events int
	if err := svc.pool.QueryRow(ctx,
		`SELECT count(*) FROM security_events WHERE kind IN ('trial_ended','subscription_expired')`).Scan(&events); err != nil || events < 3 {
		t.Fatalf("audit events = %d (err %v), want at least 3", events, err)
	}
}
