// Package billing owns plans, subscriptions, entitlements, and the payment
// provider boundary.
package billing

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"o1/internal/db/sqlc"
)

// Absolute caps that no plan may exceed, regardless of the plan row.
const (
	AbsoluteChannelCap = 30
	AbsoluteAPIKeyCap  = 20
)

// Limits is the effective entitlement for one workspace.
type Limits struct {
	Monitors int
	Channels int
	APIKeys  int
}

// FreeLimits is the fallback when a workspace has no subscription row. It
// matches the seeded free plan.
func FreeLimits() Limits {
	return Limits{Monitors: 5, Channels: 3, APIKeys: 2}
}

// LimitsForPlan clamps a plan row to the absolute caps.
func LimitsForPlan(plan sqlc.Plan) Limits {
	limits := Limits{
		Monitors: int(plan.MonitorLimit),
		Channels: int(plan.ChannelLimit),
		APIKeys:  int(plan.APIKeyLimit),
	}
	if limits.Channels > AbsoluteChannelCap {
		limits.Channels = AbsoluteChannelCap
	}
	if limits.APIKeys > AbsoluteAPIKeyCap {
		limits.APIKeys = AbsoluteAPIKeyCap
	}
	return limits
}

// LoadLimits reads the workspace plan and returns its effective limits. A
// missing subscription falls back to Free.
func LoadLimits(ctx context.Context, q *sqlc.Queries, workspaceID uuid.UUID) (Limits, error) {
	sub, err := q.GetSubscription(ctx, workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FreeLimits(), nil
	}
	if err != nil {
		return Limits{}, err
	}
	plan, err := q.GetPlan(ctx, sub.PlanKey)
	if err != nil {
		return Limits{}, err
	}
	return LimitsForPlan(plan), nil
}
