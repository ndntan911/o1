-- name: GetUserIsAdmin :one
SELECT is_admin FROM users WHERE id = $1;

-- name: ListWorkspacesForAdmin :many
SELECT
  w.id,
  w.name,
  w.slug,
  w.created_by,
  w.created_at,
  w.monitor_limit,
  w.suspended_at,
  w.suspended_by,
  w.suspended_reason,
  (SELECT count(*) FROM workspace_members wm WHERE wm.workspace_id = w.id)::bigint AS member_count,
  (SELECT count(*) FROM monitors m WHERE m.workspace_id = w.id)::bigint AS monitor_count
FROM workspaces w
WHERE (
    sqlc.narg('query')::text IS NULL
    OR w.name ILIKE '%' || sqlc.narg('query')::text || '%'
    OR w.slug::text ILIKE '%' || sqlc.narg('query')::text || '%'
  )
  AND (
    sqlc.narg('suspended')::boolean IS NULL
    OR (w.suspended_at IS NOT NULL) = sqlc.narg('suspended')::boolean
  )
  AND (
    sqlc.narg('after_time')::timestamptz IS NULL
    OR (w.created_at, w.id) < (sqlc.narg('after_time')::timestamptz, sqlc.arg('after_id')::uuid)
  )
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('page_limit')::int;

-- name: SuspendWorkspace :exec
UPDATE workspaces
SET suspended_at = now(),
    suspended_by = $2,
    suspended_reason = $3
WHERE id = $1;

-- name: UnsuspendWorkspace :exec
UPDATE workspaces
SET suspended_at = NULL,
    suspended_by = NULL,
    suspended_reason = NULL
WHERE id = $1;

-- name: ListMonitorsForAdmin :many
SELECT m.*, w.name AS workspace_name, w.slug AS workspace_slug,
       ms.status, ms.last_checked_at, ms.last_status_code,
       ms.last_latency_ms, ms.last_error
FROM monitors m
JOIN workspaces w ON w.id = m.workspace_id
LEFT JOIN monitor_state ms ON ms.monitor_id = m.id
WHERE (
    sqlc.narg('workspace_id')::uuid IS NULL
    OR m.workspace_id = sqlc.narg('workspace_id')::uuid
  )
  AND (
    sqlc.narg('query')::text IS NULL
    OR m.name ILIKE '%' || sqlc.narg('query')::text || '%'
    OR m.url ILIKE '%' || sqlc.narg('query')::text || '%'
  )
  AND (
    sqlc.narg('after_time')::timestamptz IS NULL
    OR (m.created_at, m.id) < (sqlc.narg('after_time')::timestamptz, sqlc.arg('after_id')::uuid)
  )
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg('page_limit')::int;

-- name: CreateSecurityEvent :exec
INSERT INTO security_events (id, workspace_id, actor_user_id, kind, detail, actor_api_key_id)
VALUES ($1, $2, $3, $4, $5, sqlc.arg('actor_api_key_id'));

-- name: ListSecurityEvents :many
SELECT *
FROM security_events
WHERE (
    sqlc.narg('kind')::text IS NULL
    OR kind = sqlc.narg('kind')::text
  )
  AND (
    sqlc.narg('workspace_id')::uuid IS NULL
    OR workspace_id = sqlc.narg('workspace_id')::uuid
  )
  AND (
    sqlc.narg('after_time')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('after_time')::timestamptz, sqlc.arg('after_id')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit')::int;
