-- +goose Up
ALTER TABLE alert_channels
  DROP CONSTRAINT alert_channels_kind_check;
ALTER TABLE alert_channels
  ADD CONSTRAINT alert_channels_kind_check
  CHECK (kind IN ('email', 'webhook', 'slack', 'telegram'));

ALTER TABLE alert_channels ADD COLUMN deleted_at timestamptz;

DROP INDEX alert_channels_workspace_kind_name_key;
CREATE UNIQUE INDEX alert_channels_workspace_kind_name_key
  ON alert_channels (workspace_id, kind, name)
  WHERE deleted_at IS NULL;

CREATE INDEX alert_channels_active_idx
  ON alert_channels (workspace_id)
  WHERE deleted_at IS NULL;

-- +goose Down
DELETE FROM alert_channels WHERE kind <> 'email';

DROP INDEX alert_channels_active_idx;
DROP INDEX alert_channels_workspace_kind_name_key;
CREATE UNIQUE INDEX alert_channels_workspace_kind_name_key
  ON alert_channels (workspace_id, kind, name);

ALTER TABLE alert_channels DROP COLUMN deleted_at;

ALTER TABLE alert_channels
  DROP CONSTRAINT alert_channels_kind_check;
ALTER TABLE alert_channels
  ADD CONSTRAINT alert_channels_kind_check
  CHECK (kind IN ('email'));
