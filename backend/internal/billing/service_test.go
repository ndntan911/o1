package billing

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

func newBillingEnv(t *testing.T) (*Service, *sqlc.Queries, uuid.UUID, time.Time) {
	t.Helper()
	pool, _ := testutil.NewPostgres(t)
	userID := testutil.CreateVerifiedUser(t, pool, "svc@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO workspaces (id,name,slug,created_by) VALUES ($1,'Svc',$2,$3)`,
		wsID, "svc", userID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	svc := NewService(pool, sqlc.New(pool), NewSandbox([]byte("sandbox-secret")))
	svc.now = func() time.Time { return now }
	return svc, sqlc.New(pool), wsID, now
}

func startedEvent(wsID uuid.UUID, plan string) Event {
	return Event{
		ID: uuid.NewString(), Kind: "subscription.started", WorkspaceID: wsID.String(),
		PlanKey: plan, ProviderSubscriptionID: "sub_" + plan, ProviderCustomerID: "cus_1",
	}
}

func monitorLimit(t *testing.T, svc *Service, wsID uuid.UUID) int {
	t.Helper()
	var limit int
	if err := svc.pool.QueryRow(context.Background(),
		`SELECT monitor_limit FROM workspaces WHERE id = $1`, wsID).Scan(&limit); err != nil {
		t.Fatalf("read monitor limit: %v", err)
	}
	return limit
}

func TestServiceAppliesStartedEventWithTrial(t *testing.T) {
	svc, q, wsID, now := newBillingEnv(t)
	ctx := context.Background()
	ev := startedEvent(wsID, "pro")

	if err := svc.ApplyEvent(ctx, "sandbox", ev); err != nil {
		t.Fatalf("apply started: %v", err)
	}
	sub, err := q.GetSubscription(ctx, wsID)
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if sub.PlanKey != "pro" || sub.Status != "trialing" {
		t.Fatalf("subscription = %s/%s, want pro/trialing", sub.PlanKey, sub.Status)
	}
	if sub.TrialEnd == nil || !sub.TrialEnd.Equal(now.AddDate(0, 0, 14)) {
		t.Fatalf("trial end = %v, want +14d", sub.TrialEnd)
	}
	if limit := monitorLimit(t, svc, wsID); limit != 50 {
		t.Fatalf("monitor limit = %d, want 50", limit)
	}

	// The same event twice: no error and no second side effect.
	if err := svc.ApplyEvent(ctx, "sandbox", ev); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	var events int
	if err := svc.pool.QueryRow(ctx,
		`SELECT count(*) FROM billing_events WHERE provider_event_id = $1`, ev.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("events = %d (err %v), want 1", events, err)
	}
}

func TestServiceIgnoresOlderPeriodEndButAppliesCancellation(t *testing.T) {
	svc, q, wsID, now := newBillingEnv(t)
	ctx := context.Background()
	if err := svc.ApplyEvent(ctx, "sandbox", startedEvent(wsID, "pro")); err != nil {
		t.Fatalf("started: %v", err)
	}
	newer := now.AddDate(0, 0, 30)
	if _, err := svc.pool.Exec(ctx,
		`UPDATE subscriptions SET status='active', current_period_end=$2 WHERE workspace_id=$1`,
		wsID, newer); err != nil {
		t.Fatalf("seed period: %v", err)
	}

	older := newer.AddDate(0, 0, -10)
	ev := startedEvent(wsID, "team")
	ev.Kind = "subscription.renewed"
	ev.ProviderSubscriptionID = "sub_pro"
	ev.CurrentPeriodEnd = &older
	if err := svc.ApplyEvent(ctx, "sandbox", ev); err != nil {
		t.Fatalf("older event: %v", err)
	}
	sub, _ := q.GetSubscription(ctx, wsID)
	if sub.PlanKey != "pro" {
		t.Fatalf("older event changed the plan to %s", sub.PlanKey)
	}

	cancel := startedEvent(wsID, "pro")
	cancel.Kind = "subscription.canceled"
	if err := svc.ApplyEvent(ctx, "sandbox", cancel); err != nil {
		t.Fatalf("cancel event: %v", err)
	}
	sub, _ = q.GetSubscription(ctx, wsID)
	if sub.Status != "canceled" || !sub.CancelAtPeriodEnd {
		t.Fatalf("cancel state = %s/%v", sub.Status, sub.CancelAtPeriodEnd)
	}
}

func TestServiceRejectsSecondCheckoutAndAdminOverrideWorks(t *testing.T) {
	svc, q, wsID, _ := newBillingEnv(t)
	ctx := context.Background()

	if _, err := svc.Checkout(ctx, wsID, "pro"); err != nil {
		t.Fatalf("checkout on free: %v", err)
	}
	if err := svc.ApplyEvent(ctx, "sandbox", startedEvent(wsID, "pro")); err != nil {
		t.Fatalf("started: %v", err)
	}
	if _, err := svc.Checkout(ctx, wsID, "team"); err != ErrAlreadySubscribed {
		t.Fatalf("second checkout = %v, want ErrAlreadySubscribed", err)
	}

	admin := testutil.CreateVerifiedUser(t, svc.pool, "admin@example.com", "correct horse battery staple")
	if err := svc.AdminSetPlan(ctx, admin, wsID, "team"); err != nil {
		t.Fatalf("admin set plan: %v", err)
	}
	sub, _ := q.GetSubscription(ctx, wsID)
	if sub.PlanKey != "team" || sub.Provider != "internal" {
		t.Fatalf("admin plan = %s/%s", sub.Provider, sub.PlanKey)
	}
	if limit := monitorLimit(t, svc, wsID); limit != 200 {
		t.Fatalf("limit = %d, want 200", limit)
	}
}

func TestSandboxWebhookSignature(t *testing.T) {
	s := NewSandbox([]byte("secret"))
	ev := Event{ID: "evt_1", Kind: "subscription.started", WorkspaceID: uuid.NewString(), PlanKey: "pro"}
	body, sig, err := s.SignEvent(ev)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/webhook/sandbox", strings.NewReader(string(body)))
	req.Header.Set("X-O1-Sandbox-Signature", sig)
	got, err := s.ParseWebhook(req)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.ID != ev.ID || got.PlanKey != "pro" {
		t.Fatalf("parsed = %+v", got)
	}
	req.Header.Set("X-O1-Sandbox-Signature", "sha256=deadbeef")
	if _, err := s.ParseWebhook(req); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func TestFailureAndCancelBeforeCheckoutDoNotGrantThePlan(t *testing.T) {
	svc, q, wsID, _ := newBillingEnv(t)
	ctx := context.Background()

	for _, kind := range []string{"subscription.payment_failed", "subscription.canceled"} {
		ev := Event{
			ID: uuid.NewString(), Kind: kind, WorkspaceID: wsID.String(),
			PlanKey: "pro", ProviderSubscriptionID: "never_created_" + kind,
		}
		if err := svc.ApplyEvent(ctx, "sandbox", ev); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	sub, err := q.GetSubscription(ctx, wsID)
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if sub.PlanKey != "free" || sub.Status != "active" {
		t.Fatalf("unchecked failure granted %s/%s", sub.PlanKey, sub.Status)
	}
	if limit := monitorLimit(t, svc, wsID); limit != 5 {
		t.Fatalf("limit = %d, want 5", limit)
	}
}

func TestTrialIsGrantedOncePerWorkspace(t *testing.T) {
	svc, q, wsID, now := newBillingEnv(t)
	ctx := context.Background()

	first := startedEvent(wsID, "pro")
	if err := svc.ApplyEvent(ctx, "sandbox", first); err != nil {
		t.Fatalf("first start: %v", err)
	}
	sub, _ := q.GetSubscription(ctx, wsID)
	if sub.Status != "trialing" || sub.TrialStartedAt == nil {
		t.Fatalf("first start = %s", sub.Status)
	}

	// A new subscription id (cancel then re-checkout) must not restart the
	// trial; it becomes an active subscription without a trial.
	second := Event{
		ID: uuid.NewString(), Kind: "subscription.started", WorkspaceID: wsID.String(),
		PlanKey: "pro", ProviderSubscriptionID: "sub_second", ProviderCustomerID: "cus_1",
	}
	if err := svc.ApplyEvent(ctx, "sandbox", second); err != nil {
		t.Fatalf("second start: %v", err)
	}
	sub, _ = q.GetSubscription(ctx, wsID)
	if sub.Status != "active" || sub.TrialEnd != nil {
		t.Fatalf("second start = %s/%v, want active/no trial", sub.Status, sub.TrialEnd)
	}
	_ = now
}

func TestPaymentFailureNotifiesWorkspaceAdmins(t *testing.T) {
	svc, _, wsID, _ := newBillingEnv(t)
	ctx := context.Background()
	sender := testutil.NewFakeSender()
	svc.SetEmail(sender, "http://localhost:5173")

	var userID uuid.UUID
	if err := svc.pool.QueryRow(ctx,
		`SELECT created_by FROM workspaces WHERE id = $1`, wsID).Scan(&userID); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if _, err := svc.pool.Exec(ctx,
		`INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,'owner')`,
		wsID, userID); err != nil {
		t.Fatalf("add owner: %v", err)
	}

	start := startedEvent(wsID, "pro")
	if err := svc.ApplyEvent(ctx, "sandbox", start); err != nil {
		t.Fatalf("started: %v", err)
	}
	failed := Event{
		ID: uuid.NewString(), Kind: "subscription.payment_failed", WorkspaceID: wsID.String(),
		PlanKey: "pro", ProviderSubscriptionID: start.ProviderSubscriptionID,
	}
	if err := svc.ApplyEvent(ctx, "sandbox", failed); err != nil {
		t.Fatalf("payment_failed: %v", err)
	}
	if sender.Len() != 1 {
		t.Fatalf("messages = %d, want 1", sender.Len())
	}
	if got := sender.Messages()[0]; got.To != "svc@example.com" {
		t.Fatalf("recipient = %q", got.To)
	}
}
