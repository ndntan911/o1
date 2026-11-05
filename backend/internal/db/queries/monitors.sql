-- name: LockWorkspace :one
SELECT id, monitor_limit, suspended_at
FROM workspaces
WHERE id = $1
FOR UPDATE;

-- name: CountMonitors :one
SELECT count(*) FROM monitors WHERE workspace_id = $1;

-- name: CountMonitorCreationsSince :one
SELECT count(*)
FROM monitors
WHERE workspace_id = $1 AND created_at >= $2;

-- name: OldestMonitorCreationSince :one
SELECT created_at
FROM monitors
WHERE workspace_id = $1 AND created_at >= $2
ORDER BY created_at
LIMIT 1;

-- name: CreateMonitor :one
INSERT INTO monitors (
  id, workspace_id, name, url, method, headers_enc, body_enc, content_type,
  expected_status, interval_seconds, timeout_seconds, confirmation_threshold,
  recovery_threshold, paused, created_by
)
VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12,
  $13, $14, $15
)
RETURNING *;

-- name: GetMonitor :one
SELECT * FROM monitors WHERE id = $1 AND workspace_id = $2;

-- name: ListMonitors :many
SELECT m.*, ms.status, ms.last_checked_at, ms.last_status_code,
       ms.last_latency_ms, ms.last_error
FROM monitors m
JOIN monitor_state ms ON ms.monitor_id = m.id
WHERE m.workspace_id = sqlc.arg('workspace_id')::uuid
  AND (
    sqlc.narg('after_time')::timestamptz IS NULL
    OR (m.created_at, m.id) < (sqlc.narg('after_time')::timestamptz, sqlc.arg('after_id')::uuid)
  )
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg('page_limit')::int;

-- name: ListMonitorSummaries :many
SELECT
  monitor_id,
  count(*)::bigint AS checks,
  count(*) FILTER (WHERE status = 'up')::bigint AS up_count,
  count(*) FILTER (WHERE status = 'down')::bigint AS down_count,
  avg(latency_ms)::float8 AS avg_latency_ms,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)::float8 AS p95_latency_ms,
  min(latency_ms)::int AS min_latency_ms,
  max(latency_ms)::int AS max_latency_ms
FROM probe_results
WHERE monitor_id = ANY(sqlc.arg('monitor_ids')::uuid[])
  AND checked_at >= sqlc.arg('since')::timestamptz
GROUP BY monitor_id;

-- name: UpdateMonitor :one
UPDATE monitors
SET name = $3,
    url = $4,
    method = $5,
    headers_enc = $6,
    body_enc = $7,
    content_type = $8,
    expected_status = $9,
    interval_seconds = $10,
    timeout_seconds = $11,
    confirmation_threshold = $12,
    recovery_threshold = $13,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteMonitor :execrows
DELETE FROM monitors WHERE id = $1 AND workspace_id = $2;

-- name: SetMonitorPaused :one
UPDATE monitors
SET paused = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: CreateMonitorState :exec
INSERT INTO monitor_state (monitor_id) VALUES ($1);

-- name: GetMonitorState :one
SELECT * FROM monitor_state WHERE monitor_id = $1;

-- name: GetMonitorForProbe :one
SELECT m.*, ms.status, ms.consecutive_failures, ms.consecutive_successes,
       ms.last_checked_at, ms.last_status_code, ms.last_latency_ms, ms.last_error,
       ms.next_check_at, w.suspended_at
FROM monitors m
JOIN monitor_state ms ON ms.monitor_id = m.id
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.id = $1;

-- name: ListDueMonitors :many
SELECT m.id, m.interval_seconds
FROM monitor_state ms
JOIN monitors m ON m.id = ms.monitor_id
JOIN workspaces w ON w.id = m.workspace_id
WHERE ms.next_check_at <= sqlc.arg('now')::timestamptz
  AND m.paused = false
  AND w.suspended_at IS NULL
ORDER BY ms.next_check_at
LIMIT sqlc.arg('batch')::int
FOR UPDATE OF ms SKIP LOCKED;

-- name: SetNextCheckAt :exec
UPDATE monitor_state
SET next_check_at = $2, updated_at = now()
WHERE monitor_id = $1;

-- name: ResumeMonitor :exec
UPDATE monitors
SET paused = false, updated_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: ResumeMonitorState :exec
UPDATE monitor_state
SET next_check_at = now(), updated_at = now()
WHERE monitor_id = $1;

-- name: InsertProbeResult :execrows
INSERT INTO probe_results (
  id, monitor_id, workspace_id, scheduled_for, checked_at,
  status, status_code, latency_ms, error
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (monitor_id, scheduled_for, checked_at) DO NOTHING;

-- name: ProbeResultExists :one
SELECT EXISTS (
  SELECT 1
  FROM probe_results
  WHERE monitor_id = $1 AND scheduled_for = $2
) AS result_exists;

-- name: GetMonitorStateForUpdate :one
-- Locking the state row serializes concurrent probe results for one monitor so
-- counter updates cannot be lost.
SELECT status, consecutive_failures, consecutive_successes
FROM monitor_state
WHERE monitor_id = $1
FOR UPDATE;

-- name: UpdateMonitorState :exec
UPDATE monitor_state
SET status = $2,
    consecutive_failures = $3,
    consecutive_successes = $4,
    last_checked_at = $5,
    last_status_code = $6,
    last_latency_ms = $7,
    last_error = $8,
    updated_at = now()
WHERE monitor_id = $1;

-- name: ListProbeResults :many
SELECT *
FROM probe_results
WHERE monitor_id = sqlc.arg('monitor_id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND checked_at >= sqlc.arg('since')::timestamptz
  AND (
    sqlc.narg('after_time')::timestamptz IS NULL
    OR (checked_at, id) < (sqlc.narg('after_time')::timestamptz, sqlc.arg('after_id')::uuid)
  )
ORDER BY checked_at DESC, id DESC
LIMIT sqlc.arg('page_limit')::int;

-- name: MonitorSummary :one
-- COALESCE keeps the row scannable for empty windows; callers must consult
-- checks (zero) before treating latency values as meaningful.
SELECT
  count(*)::bigint AS checks,
  count(*) FILTER (WHERE status = 'up')::bigint AS up_count,
  count(*) FILTER (WHERE status = 'down')::bigint AS down_count,
  COALESCE(avg(latency_ms), 0)::float8 AS avg_latency_ms,
  COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::float8 AS p95_latency_ms,
  COALESCE(min(latency_ms), 0)::int AS min_latency_ms,
  COALESCE(max(latency_ms), 0)::int AS max_latency_ms
FROM probe_results
WHERE monitor_id = sqlc.arg('monitor_id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND checked_at >= sqlc.arg('since')::timestamptz;
