-- name: RollupHourlyStats :execrows
INSERT INTO monitor_hourly_stats (
  monitor_id, workspace_id, bucket_start, up_count, down_count, checks,
  avg_latency_ms, min_latency_ms, max_latency_ms, p95_latency_ms, updated_at
)
SELECT
  monitor_id, workspace_id, date_trunc('hour', checked_at),
  count(*) FILTER (WHERE status = 'up'),
  count(*) FILTER (WHERE status = 'down'),
  count(*),
  avg(latency_ms),
  min(latency_ms),
  max(latency_ms),
  percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms),
  now()
FROM probe_results
WHERE checked_at >= sqlc.arg('start') AND checked_at < sqlc.arg('end')
GROUP BY monitor_id, workspace_id, date_trunc('hour', checked_at)
ON CONFLICT (monitor_id, bucket_start) DO UPDATE SET
  up_count = EXCLUDED.up_count, down_count = EXCLUDED.down_count,
  checks = EXCLUDED.checks, avg_latency_ms = EXCLUDED.avg_latency_ms,
  min_latency_ms = EXCLUDED.min_latency_ms, max_latency_ms = EXCLUDED.max_latency_ms,
  p95_latency_ms = EXCLUDED.p95_latency_ms, updated_at = now();

-- name: GetRollupWatermark :one
SELECT watermark FROM rollup_state WHERE id;

-- name: SetRollupWatermark :exec
UPDATE rollup_state SET watermark = sqlc.arg('watermark')::timestamptz, updated_at = now() WHERE id;

-- name: OldestProbeHour :one
-- Returns the oldest probe check time; callers truncate it to the hour. The
-- LEFT JOIN keeps the result nullable for an empty table because sqlc cannot
-- infer nullability through min()/date_trunc() expressions.
SELECT p.checked_at
FROM (VALUES (1)) AS seed(x)
LEFT JOIN probe_results p
  ON p.checked_at = (SELECT min(checked_at) FROM probe_results)
LIMIT 1;

-- name: CountProbeChecks :one
SELECT count(*)::bigint FROM probe_results
WHERE checked_at >= sqlc.arg('start') AND checked_at < sqlc.arg('end');

-- name: CountRollupChecks :one
SELECT COALESCE(sum(checks), 0)::bigint FROM monitor_hourly_stats
WHERE bucket_start >= sqlc.arg('start') AND bucket_start < sqlc.arg('end');

-- name: ListRollupDayStats :many
SELECT
  monitor_id,
  to_char((bucket_start AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD') AS day,
  sum(checks)::bigint AS checks,
  sum(up_count)::bigint AS up_count,
  sum(down_count)::bigint AS down_count
FROM monitor_hourly_stats
WHERE monitor_id = ANY(sqlc.arg('monitor_ids')::uuid[])
  AND bucket_start >= sqlc.arg('since')::timestamptz
  AND bucket_start < sqlc.arg('until')::timestamptz
GROUP BY monitor_id, (bucket_start AT TIME ZONE 'UTC')::date
ORDER BY monitor_id, day;

-- name: DeleteOldHourlyStats :execrows
DELETE FROM monitor_hourly_stats
WHERE ctid IN (
  SELECT ctid FROM monitor_hourly_stats
  WHERE bucket_start < sqlc.arg('before')::timestamptz
  LIMIT 10000
);

-- name: DeleteOldSecurityEvents :execrows
DELETE FROM security_events
WHERE ctid IN (
  SELECT ctid FROM security_events
  WHERE created_at < sqlc.arg('before')::timestamptz
  LIMIT 10000
);
