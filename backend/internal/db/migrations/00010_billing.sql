-- +goose Up
CREATE TABLE plans (
  key text PRIMARY KEY,
  name text NOT NULL,
  monitor_limit integer NOT NULL CHECK (monitor_limit > 0),
  channel_limit integer NOT NULL CHECK (channel_limit > 0),
  api_key_limit integer NOT NULL CHECK (api_key_limit > 0),
  price_cents integer NOT NULL CHECK (price_cents >= 0),
  currency text NOT NULL DEFAULT 'usd',
  trial_days integer NOT NULL DEFAULT 0 CHECK (trial_days >= 0)
);

INSERT INTO plans (key, name, monitor_limit, channel_limit, api_key_limit, price_cents, currency, trial_days) VALUES
  ('free', 'Free', 5, 3, 2, 0, 'usd', 0),
  ('pro', 'Pro', 50, 15, 10, 1200, 'usd', 14),
  ('team', 'Team', 200, 30, 20, 3900, 'usd', 14);

CREATE TABLE subscriptions (
  workspace_id uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  provider text NOT NULL DEFAULT 'internal',
  provider_customer_id text,
  provider_subscription_id text UNIQUE,
  plan_key text NOT NULL REFERENCES plans(key),
  status text NOT NULL CHECK (status IN ('trialing','active','past_due','canceled','expired')),
  current_period_end timestamptz,
  trial_end timestamptz,
  past_due_since timestamptz,
  cancel_at_period_end boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO subscriptions (workspace_id, provider, plan_key, status)
SELECT id, 'internal', 'free', 'active' FROM workspaces
ON CONFLICT (workspace_id) DO NOTHING;

-- Every future workspace starts on Free, including ones created by tests or the
-- admin API, so no code path can forget the subscription row.
-- +goose StatementBegin
CREATE FUNCTION create_default_subscription() RETURNS trigger AS $$
BEGIN
  INSERT INTO subscriptions (workspace_id, provider, plan_key, status)
  VALUES (NEW.id, 'internal', 'free', 'active')
  ON CONFLICT (workspace_id) DO NOTHING;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER workspaces_default_subscription
AFTER INSERT ON workspaces
FOR EACH ROW EXECUTE FUNCTION create_default_subscription();

CREATE TABLE billing_events (
  id uuid PRIMARY KEY,
  provider text NOT NULL,
  provider_event_id text NOT NULL,
  kind text NOT NULL,
  payload jsonb NOT NULL,
  processed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_event_id)
);

CREATE INDEX billing_events_unprocessed_idx
  ON billing_events (created_at)
  WHERE processed_at IS NULL;

-- +goose Down
DROP INDEX billing_events_unprocessed_idx;
DROP TABLE billing_events;
DROP TRIGGER workspaces_default_subscription ON workspaces;
DROP FUNCTION create_default_subscription();
DROP TABLE subscriptions;
DROP TABLE plans;
