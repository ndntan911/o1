-- +goose Up
-- Incident investigations: one row per agent run against an incident.
--
-- Rows are append-only. A retry reuses its row rather than inserting a second
-- one, and the partial unique index allows at most one queued/running run per
-- incident. seed_hash defaults to '' so the row can be written before the
-- seed exists.
CREATE TABLE incident_investigations (
    id             uuid PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    incident_id    uuid NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    status         text NOT NULL CHECK (status IN
                     ('queued', 'running', 'ok', 'insufficient', 'incomplete', 'failed')),
    model          text NOT NULL,
    prompt_version text NOT NULL,
    seed_hash      text NOT NULL DEFAULT '',
    result         jsonb,
    trace          jsonb NOT NULL DEFAULT '{}'::jsonb,
    attempts       integer NOT NULL DEFAULT 0,
    tool_calls     integer NOT NULL DEFAULT 0,
    input_tokens   integer NOT NULL DEFAULT 0,
    output_tokens  integer NOT NULL DEFAULT 0,
    duration_ms    integer NOT NULL DEFAULT 0,
    error_code     text,
    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    completed_at   timestamptz
);

CREATE INDEX incident_investigations_incident_created_idx
    ON incident_investigations (incident_id, created_at DESC);

CREATE UNIQUE INDEX incident_investigations_one_active_idx
    ON incident_investigations (incident_id)
    WHERE status IN ('queued', 'running');

-- +goose Down
DROP TABLE IF EXISTS incident_investigations;
