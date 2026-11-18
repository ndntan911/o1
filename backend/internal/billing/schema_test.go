package billing

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"o1/internal/testutil"
)

func TestBillingSchemaBackfillsFreeSubscriptions(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "billing@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id,name,slug,created_by) VALUES ($1,'Billing',$2,$3)`,
		wsID, "billing", userID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	var plans int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM plans`).Scan(&plans); err != nil {
		t.Fatalf("count plans: %v", err)
	}
	if plans != 3 {
		t.Fatalf("plans = %d, want 3", plans)
	}

	var plan, status string
	if err := pool.QueryRow(ctx,
		`SELECT plan_key, status FROM subscriptions WHERE workspace_id = $1`, wsID).Scan(&plan, &status); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if plan != "free" || status != "active" {
		t.Fatalf("subscription = %s/%s, want free/active", plan, status)
	}

	var limit int
	if err := pool.QueryRow(ctx,
		`SELECT monitor_limit FROM workspaces WHERE id = $1`, wsID).Scan(&limit); err != nil {
		t.Fatalf("read monitor limit: %v", err)
	}
	if limit != 5 {
		t.Fatalf("monitor_limit = %d, want 5", limit)
	}
}
