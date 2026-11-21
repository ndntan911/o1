-- +goose Up
-- Incident insights: a stored, cited analysis of one incident.
--
-- Rows are append-only. The API reads the latest row per incident, so a rerun
-- keeps its history and the audit trail can explain what was shown when.

CREATE TABLE IF NOT EXISTS incident_insights (
    id             uuid PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    incident_id    uuid NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    model          text NOT NULL,
    prompt_version text NOT NULL,
    payload_hash   text NOT NULL,
    status         text NOT NULL CHECK (status IN ('ok', 'insufficient', 'failed')),
    result         jsonb NOT NULL,
    input_tokens   integer NOT NULL DEFAULT 0,
    output_tokens  integer NOT NULL DEFAULT 0,
    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS incident_insights_incident_created_idx
    ON incident_insights (incident_id, created_at DESC);

CREATE INDEX IF NOT EXISTS incident_insights_workspace_idx
    ON incident_insights (workspace_id);

-- +goose Down
DROP TABLE IF EXISTS incident_insights;
