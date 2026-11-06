-- name: CreateIncident :one
INSERT INTO incidents (id, workspace_id, monitor_id, status, started_at, last_notified_at)
VALUES (sqlc.arg('id'),sqlc.arg('workspace_id'),sqlc.arg('monitor_id'),'open',sqlc.arg('started_at')::timestamptz,sqlc.arg('started_at')::timestamptz)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetActiveIncidentForMonitor :one
SELECT * FROM incidents
WHERE monitor_id = $1 AND resolved_at IS NULL;

-- name: ResolveIncident :exec
UPDATE incidents SET status='resolved', resolved_at=COALESCE($2, now()), updated_at=now()
WHERE id=$1 AND resolved_at IS NULL;

-- name: AcknowledgeIncident :exec
UPDATE incidents SET status='acknowledged', acknowledged_at=COALESCE($2, now()), acknowledged_by=$3, updated_at=now()
WHERE id=$1 AND resolved_at IS NULL AND acknowledged_at IS NULL;

-- name: CreateIncidentEvent :exec
INSERT INTO incident_events (id, incident_id, kind, actor_user_id, detail)
VALUES ($1,$2,$3,$4,$5);

-- name: ListIncidents :many
SELECT i.*, m.name AS monitor_name
FROM incidents i
JOIN monitors m ON m.id = i.monitor_id
WHERE i.workspace_id = sqlc.arg('workspace_id')::uuid
  AND (sqlc.arg('active_only')::bool = false OR i.resolved_at IS NULL)
  AND (sqlc.narg('status')::text IS NULL OR i.status = sqlc.narg('status')::text)
  AND (sqlc.narg('monitor_id')::uuid IS NULL OR i.monitor_id = sqlc.narg('monitor_id')::uuid)
  AND (
    sqlc.narg('after_time')::timestamptz IS NULL
    OR (i.started_at, i.id) < (sqlc.narg('after_time')::timestamptz, sqlc.arg('after_id')::uuid)
  )
ORDER BY i.started_at DESC, i.id DESC
LIMIT sqlc.arg('page_limit')::int;

-- name: GetIncident :one
SELECT i.*, m.name AS monitor_name
FROM incidents i JOIN monitors m ON m.id = i.monitor_id
WHERE i.id=$1 AND i.workspace_id=$2;

-- name: ListIncidentEvents :many
SELECT * FROM incident_events
WHERE incident_id=$1
ORDER BY created_at, id;

-- name: ListIncidentsNeedingReminder :many
SELECT i.id, i.workspace_id, i.monitor_id, i.reminder_count
FROM incidents i
JOIN monitors m ON m.id = i.monitor_id
JOIN workspaces w ON w.id = i.workspace_id
WHERE i.resolved_at IS NULL
  AND i.acknowledged_at IS NULL
  AND i.reminder_count < sqlc.arg('max_reminders')::int
  AND i.last_notified_at <= sqlc.arg('before')::timestamptz
  AND m.paused = false
  AND w.suspended_at IS NULL
ORDER BY i.last_notified_at
LIMIT sqlc.arg('batch')::int
FOR UPDATE OF i SKIP LOCKED;

-- name: UpdateIncidentReminder :exec
UPDATE incidents
SET last_notified_at=$2, reminder_count=reminder_count+1, updated_at=now()
WHERE id=$1;

-- name: ListIncidentDeliveries :many
SELECT * FROM alert_deliveries
WHERE incident_id=$1
ORDER BY created_at, id;
