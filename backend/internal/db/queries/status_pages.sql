-- name: GetStatusPage :one
SELECT * FROM status_pages WHERE workspace_id = sqlc.arg('workspace_id')::uuid;

-- name: UpsertStatusPage :one
INSERT INTO status_pages (workspace_id, enabled, title, description)
VALUES (sqlc.arg('workspace_id')::uuid, sqlc.arg('enabled'), sqlc.arg('title'), sqlc.arg('description'))
ON CONFLICT (workspace_id) DO UPDATE
SET enabled = EXCLUDED.enabled,
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    updated_at = now()
RETURNING *;

-- name: DeleteStatusPageMonitors :exec
DELETE FROM status_page_monitors
WHERE status_page_id = sqlc.arg('status_page_id')::uuid;

-- name: InsertStatusPageMonitor :exec
INSERT INTO status_page_monitors (status_page_id, monitor_id)
VALUES (sqlc.arg('status_page_id')::uuid, sqlc.arg('monitor_id')::uuid)
ON CONFLICT DO NOTHING;

-- name: ListStatusPageMonitorIDs :many
SELECT spm.monitor_id
FROM status_page_monitors spm
JOIN monitors m ON m.id = spm.monitor_id
WHERE spm.status_page_id = sqlc.arg('status_page_id')::uuid
ORDER BY m.created_at, m.id;

-- name: ListWorkspaceMonitorIDs :many
SELECT id FROM monitors
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
ORDER BY created_at, id;

-- name: ListPublicStatusMonitors :many
SELECT m.id, m.name, m.paused, ms.status
FROM status_page_monitors spm
JOIN monitors m ON m.id = spm.monitor_id
JOIN monitor_state ms ON ms.monitor_id = m.id
WHERE spm.status_page_id = sqlc.arg('status_page_id')::uuid
ORDER BY m.created_at, m.id;

-- name: ListProbeDayStats :many
SELECT
  monitor_id,
  to_char((checked_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD') AS day,
  count(*)::bigint AS checks,
  count(*) FILTER (WHERE status = 'up')::bigint AS up_count,
  count(*) FILTER (WHERE status = 'down')::bigint AS down_count
FROM probe_results
WHERE monitor_id = ANY(sqlc.arg('monitor_ids')::uuid[])
  AND checked_at >= sqlc.arg('since')::timestamptz
GROUP BY monitor_id, (checked_at AT TIME ZONE 'UTC')::date
ORDER BY monitor_id, day;

-- name: ListPublicActiveIncidents :many
SELECT i.id, i.monitor_id, m.name AS monitor_name, i.started_at
FROM incidents i
JOIN monitors m ON m.id = i.monitor_id
WHERE i.monitor_id = ANY(sqlc.arg('monitor_ids')::uuid[])
  AND i.resolved_at IS NULL
ORDER BY i.started_at DESC, i.id DESC;

-- name: ListPublicRecentIncidents :many
SELECT i.id, i.monitor_id, m.name AS monitor_name, i.started_at, i.resolved_at
FROM incidents i
JOIN monitors m ON m.id = i.monitor_id
WHERE i.monitor_id = ANY(sqlc.arg('monitor_ids')::uuid[])
  AND i.resolved_at >= sqlc.arg('since')::timestamptz
ORDER BY i.resolved_at DESC, i.id DESC
LIMIT sqlc.arg('page_limit')::int;
