-- name: ListPlans :many
SELECT * FROM plans ORDER BY price_cents, key;

-- name: GetPlan :one
SELECT * FROM plans WHERE key = $1;

-- name: GetSubscription :one
SELECT * FROM subscriptions WHERE workspace_id = $1;

-- name: GetSubscriptionByProviderID :one
SELECT * FROM subscriptions WHERE provider_subscription_id = $1;

-- name: UpsertSubscription :exec
INSERT INTO subscriptions (
  workspace_id, provider, provider_customer_id, provider_subscription_id,
  plan_key, status, current_period_end, trial_end, past_due_since,
  cancel_at_period_end, trial_started_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
ON CONFLICT (workspace_id) DO UPDATE SET
  provider = EXCLUDED.provider,
  provider_customer_id = EXCLUDED.provider_customer_id,
  provider_subscription_id = EXCLUDED.provider_subscription_id,
  plan_key = EXCLUDED.plan_key,
  status = EXCLUDED.status,
  current_period_end = EXCLUDED.current_period_end,
  trial_end = EXCLUDED.trial_end,
  past_due_since = EXCLUDED.past_due_since,
  cancel_at_period_end = EXCLUDED.cancel_at_period_end,
  trial_started_at = EXCLUDED.trial_started_at,
  updated_at = now();

-- name: InsertBillingEvent :execrows
INSERT INTO billing_events (id, provider, provider_event_id, kind, payload)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (provider, provider_event_id) DO NOTHING;

-- name: MarkBillingEventProcessed :exec
UPDATE billing_events SET processed_at = now()
WHERE provider = $1 AND provider_event_id = $2;

-- name: GetBillingEvent :one
SELECT * FROM billing_events WHERE provider = $1 AND provider_event_id = $2;

-- name: UpdateWorkspaceMonitorLimit :exec
UPDATE workspaces SET monitor_limit = $2 WHERE id = $1;

-- name: ListTrialsToEnd :many
SELECT * FROM subscriptions
WHERE status = 'trialing' AND trial_end IS NOT NULL AND trial_end <= $1;

-- name: ListPastDueToExpire :many
SELECT * FROM subscriptions
WHERE status = 'past_due' AND past_due_since IS NOT NULL AND past_due_since <= $1;

-- name: ListCanceledToExpire :many
SELECT * FROM subscriptions
WHERE status = 'canceled' AND current_period_end IS NOT NULL AND current_period_end <= $1;

-- name: DowngradeSubscription :execrows
UPDATE subscriptions SET
  provider = 'internal',
  provider_customer_id = NULL,
  provider_subscription_id = NULL,
  plan_key = $2,
  status = $3,
  trial_end = NULL,
  past_due_since = NULL,
  cancel_at_period_end = false,
  current_period_end = NULL,
  updated_at = now()
WHERE workspace_id = $1 AND status = $4;

-- name: CountAlertChannels :one
SELECT count(*)::bigint FROM alert_channels
WHERE workspace_id = $1 AND deleted_at IS NULL;
