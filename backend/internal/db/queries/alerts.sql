-- name: UpsertEmailAlertChannel :one
INSERT INTO alert_channels (id, workspace_id, kind, name, config, enabled)
VALUES ($1,$2,'email','Email',$3,$4)
ON CONFLICT (workspace_id, kind, name)
DO UPDATE SET config=EXCLUDED.config, enabled=EXCLUDED.enabled, updated_at=now()
RETURNING *;

-- name: GetEmailAlertChannel :one
SELECT * FROM alert_channels
WHERE workspace_id=$1 AND kind='email' AND name='Email';

-- name: ListAlertChannels :many
SELECT * FROM alert_channels WHERE workspace_id=$1 ORDER BY created_at, id;

-- name: AttachMonitorsToEmailChannel :exec
INSERT INTO monitor_alert_channels (monitor_id, channel_id)
SELECT m.id, sqlc.arg('channel_id')::uuid
FROM monitors m
WHERE m.workspace_id = sqlc.arg('workspace_id')::uuid
ON CONFLICT DO NOTHING;

-- name: UpsertMonitorAlertChannel :exec
INSERT INTO monitor_alert_channels (monitor_id, channel_id, enabled)
VALUES ($1,$2,$3)
ON CONFLICT (monitor_id, channel_id)
DO UPDATE SET enabled=EXCLUDED.enabled, updated_at=now();

-- name: GetMonitorAlertState :one
SELECT m.id AS monitor_id,
       c.id AS channel_id,
       COALESCE(c.enabled, false) AS channel_enabled,
       COALESCE(mac.enabled, false) AS monitor_enabled
FROM monitors m
LEFT JOIN alert_channels c
  ON c.workspace_id = m.workspace_id AND c.kind='email' AND c.name='Email'
LEFT JOIN monitor_alert_channels mac
  ON mac.monitor_id = m.id AND mac.channel_id = c.id
WHERE m.id=$1 AND m.workspace_id=$2;

-- name: ListEnabledAlertChannelsForMonitor :many
SELECT c.* FROM alert_channels c
JOIN monitor_alert_channels mac ON mac.channel_id = c.id
WHERE mac.monitor_id=$1 AND mac.enabled=true AND c.enabled=true;

-- name: CreateAlertDelivery :one
INSERT INTO alert_deliveries (id, incident_id, channel_id, event, sequence, recipient)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetAlertDelivery :one
SELECT d.*, i.status AS incident_status, i.started_at, i.resolved_at, i.acknowledged_at,
       m.name AS monitor_name, m.paused AS monitor_paused, m.url AS monitor_url,
       w.name AS workspace_name, w.slug AS workspace_slug, w.suspended_at,
       c.kind AS channel_kind, c.config AS channel_config, c.enabled AS channel_enabled
FROM alert_deliveries d
JOIN incidents i ON i.id = d.incident_id
JOIN monitors m ON m.id = i.monitor_id
JOIN workspaces w ON w.id = i.workspace_id
JOIN alert_channels c ON c.id = d.channel_id
WHERE d.id=$1;

-- name: MarkAlertDeliverySent :exec
UPDATE alert_deliveries SET status='sent', sent_at=now(), attempts=attempts+1, last_error=NULL, updated_at=now()
WHERE id=$1;

-- name: MarkAlertDeliveryFailed :exec
UPDATE alert_deliveries SET status=$2, attempts=attempts+1, last_error=$3, updated_at=now()
WHERE id=$1;
