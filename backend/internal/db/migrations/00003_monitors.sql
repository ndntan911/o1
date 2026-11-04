-- +goose Up
ALTER TABLE users ADD COLUMN is_admin boolean NOT NULL DEFAULT false;

ALTER TABLE workspaces
  ADD COLUMN monitor_limit integer NOT NULL DEFAULT 5 CHECK (monitor_limit > 0),
  ADD COLUMN suspended_at timestamptz,
  ADD COLUMN suspended_by uuid REFERENCES users(id),
  ADD COLUMN suspended_reason text CHECK (length(suspended_reason) <= 500);

CREATE TABLE monitors (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 100),
  url text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
  method text NOT NULL CHECK (method IN ('GET','HEAD','POST','PUT','PATCH','DELETE')),
  headers_enc text,
  body_enc text,
  content_type text CHECK (length(content_type) <= 200),
  expected_status text NOT NULL DEFAULT '2xx',
  interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 60 AND 86400),
  timeout_seconds integer NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 30),
  confirmation_threshold smallint NOT NULL DEFAULT 2 CHECK (confirmation_threshold BETWEEN 1 AND 5),
  recovery_threshold smallint NOT NULL DEFAULT 2 CHECK (recovery_threshold BETWEEN 1 AND 5),
  paused boolean NOT NULL DEFAULT false,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX monitors_workspace_created_idx ON monitors (workspace_id, created_at DESC, id DESC);
CREATE INDEX monitors_created_at_idx ON monitors (created_at);

CREATE TABLE monitor_state (
  monitor_id uuid PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown','up','down')),
  consecutive_failures integer NOT NULL DEFAULT 0,
  consecutive_successes integer NOT NULL DEFAULT 0,
  last_checked_at timestamptz,
  last_status_code integer,
  last_latency_ms integer,
  last_error text,
  next_check_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX monitor_state_due_idx ON monitor_state (next_check_at);

CREATE TABLE probe_results (
  id uuid NOT NULL,
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  scheduled_for timestamptz NOT NULL,
  checked_at timestamptz NOT NULL,
  status text NOT NULL CHECK (status IN ('up','down')),
  status_code integer,
  latency_ms integer NOT NULL CHECK (latency_ms >= 0),
  error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, checked_at)
) PARTITION BY RANGE (checked_at);

CREATE UNIQUE INDEX probe_results_monitor_scheduled_key
  ON probe_results (monitor_id, scheduled_for, checked_at);
CREATE INDEX probe_results_monitor_checked_idx
  ON probe_results (monitor_id, checked_at DESC, id DESC);
CREATE INDEX probe_results_workspace_checked_idx
  ON probe_results (workspace_id, checked_at DESC);

CREATE TABLE security_events (
  id uuid PRIMARY KEY,
  workspace_id uuid REFERENCES workspaces(id) ON DELETE CASCADE,
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  kind text NOT NULL,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_events_kind_created_idx ON security_events (kind, created_at DESC);
CREATE INDEX security_events_workspace_created_idx ON security_events (workspace_id, created_at DESC);

-- +goose Down
DROP TABLE security_events;
DROP TABLE probe_results;
DROP TABLE monitor_state;
DROP TABLE monitors;

ALTER TABLE workspaces
  DROP COLUMN suspended_reason,
  DROP COLUMN suspended_by,
  DROP COLUMN suspended_at,
  DROP COLUMN monitor_limit;

ALTER TABLE users DROP COLUMN is_admin;
