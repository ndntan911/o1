-- +goose Up
CREATE TABLE api_keys (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 60),
  prefix text NOT NULL UNIQUE CHECK (length(prefix) = 12),
  token_hash bytea NOT NULL UNIQUE,
  role text NOT NULL CHECK (role IN ('viewer', 'member')),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at timestamptz
);

CREATE UNIQUE INDEX api_keys_workspace_active_name_key
  ON api_keys (workspace_id, name)
  WHERE revoked_at IS NULL;
CREATE INDEX api_keys_workspace_active_idx
  ON api_keys (workspace_id)
  WHERE revoked_at IS NULL;

ALTER TABLE security_events
  ADD COLUMN actor_api_key_id uuid REFERENCES api_keys(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE security_events DROP COLUMN actor_api_key_id;
DROP TABLE api_keys;
