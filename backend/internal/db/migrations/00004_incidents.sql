-- +goose Up
CREATE TABLE incidents (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  status text NOT NULL CHECK (status IN ('open', 'acknowledged', 'resolved')),
  started_at timestamptz NOT NULL DEFAULT now(),
  acknowledged_at timestamptz,
  acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL,
  resolved_at timestamptz,
  last_notified_at timestamptz,
  reminder_count integer NOT NULL DEFAULT 0 CHECK (reminder_count >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (status <> 'resolved' OR resolved_at IS NOT NULL),
  CHECK (status <> 'acknowledged' OR acknowledged_at IS NOT NULL)
);

-- At most one unresolved incident per monitor. This is the database-level
-- guarantee against duplicate opens.
CREATE UNIQUE INDEX incidents_one_active_per_monitor
  ON incidents (monitor_id) WHERE resolved_at IS NULL;

CREATE INDEX incidents_workspace_started_idx
  ON incidents (workspace_id, started_at DESC, id DESC);
CREATE INDEX incidents_monitor_started_idx
  ON incidents (monitor_id, started_at DESC, id DESC);
CREATE INDEX incidents_open_reminder_idx
  ON incidents (last_notified_at)
  WHERE resolved_at IS NULL AND acknowledged_at IS NULL;

CREATE TABLE incident_events (
  id uuid PRIMARY KEY,
  incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('opened', 'acknowledged', 'resolved')),
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incident_events_incident_created_idx
  ON incident_events (incident_id, created_at, id);

CREATE TABLE alert_channels (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('email')),
  name text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 60),
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX alert_channels_workspace_kind_name_key
  ON alert_channels (workspace_id, kind, name);

CREATE TABLE monitor_alert_channels (
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES alert_channels(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (monitor_id, channel_id)
);

CREATE TABLE alert_deliveries (
  id uuid PRIMARY KEY,
  incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES alert_channels(id) ON DELETE CASCADE,
  event text NOT NULL CHECK (event IN ('opened', 'reminder', 'resolved')),
  sequence integer NOT NULL DEFAULT 0 CHECK (sequence >= 0),
  recipient text NOT NULL,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_error text,
  sent_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (incident_id, channel_id, event, sequence, recipient)
);
CREATE INDEX alert_deliveries_incident_created_idx
  ON alert_deliveries (incident_id, created_at, id);
CREATE INDEX alert_deliveries_pending_idx
  ON alert_deliveries (created_at)
  WHERE status = 'pending';

-- +goose Down
DROP TABLE alert_deliveries;
DROP TABLE monitor_alert_channels;
DROP TABLE alert_channels;
DROP TABLE incident_events;
DROP TABLE incidents;
