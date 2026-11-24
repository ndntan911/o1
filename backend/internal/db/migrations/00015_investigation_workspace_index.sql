-- +goose Up
-- The per-workspace daily cap counts investigations by workspace and the
-- read/limit paths order by newest first, so index both columns together.
CREATE INDEX IF NOT EXISTS incident_investigations_workspace_created_idx
    ON incident_investigations (workspace_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS incident_investigations_workspace_created_idx;
