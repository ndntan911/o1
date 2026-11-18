package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

func TestLoadLimitsFollowsThePlan(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := testutil.CreateVerifiedUser(t, pool, "limits@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id,name,slug,created_by) VALUES ($1,'Limits',$2,$3)`,
		wsID, "limits", userID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	limits, err := LoadLimits(ctx, q, wsID)
	if err != nil {
		t.Fatalf("LoadLimits free: %v", err)
	}
	if limits != (Limits{Monitors: 5, Channels: 3, APIKeys: 2}) {
		t.Fatalf("free limits = %+v", limits)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE subscriptions SET plan_key = 'pro' WHERE workspace_id = $1`, wsID); err != nil {
		t.Fatalf("update plan: %v", err)
	}
	limits, err = LoadLimits(ctx, q, wsID)
	if err != nil {
		t.Fatalf("LoadLimits pro: %v", err)
	}
	if limits != (Limits{Monitors: 50, Channels: 15, APIKeys: 10}) {
		t.Fatalf("pro limits = %+v", limits)
	}
}

func TestCheckCreateBlocksExpiredAndOldPastDue(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := testutil.CreateVerifiedUser(t, pool, "blocked@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id,name,slug,created_by) VALUES ($1,'Blocked',$2,$3)`,
		wsID, "blocked", userID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	if err := CheckCreate(ctx, q, wsID, now); err != nil {
		t.Fatalf("free should allow creation: %v", err)
	}

	old := now.AddDate(0, 0, -8)
	if _, err := pool.Exec(ctx,
		`UPDATE subscriptions SET status='past_due', past_due_since=$2 WHERE workspace_id=$1`,
		wsID, old); err != nil {
		t.Fatalf("seed past_due: %v", err)
	}
	if err := CheckCreate(ctx, q, wsID, now); !errors.Is(err, ErrPaymentRequired) {
		t.Fatalf("old past_due = %v, want ErrPaymentRequired", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE subscriptions SET status='expired', past_due_since=NULL WHERE workspace_id=$1`,
		wsID); err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	if err := CheckCreate(ctx, q, wsID, now); !errors.Is(err, ErrPaymentRequired) {
		t.Fatalf("expired = %v, want ErrPaymentRequired", err)
	}
}
