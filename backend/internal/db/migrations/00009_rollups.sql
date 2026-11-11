-- +goose Up
-- rollup_hour marks hour-bucket timestamptz values. The domain lets sqlc map
-- the nullable OldestProbeHour result to *time.Time.
CREATE DOMAIN rollup_hour AS timestamptz;

CREATE TABLE monitor_hourly_stats (
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  bucket_start timestamptz NOT NULL,
  up_count bigint NOT NULL CHECK (up_count >= 0),
  down_count bigint NOT NULL CHECK (down_count >= 0),
  checks bigint NOT NULL CHECK (checks = up_count + down_count),
  avg_latency_ms double precision NOT NULL CHECK (avg_latency_ms >= 0),
  min_latency_ms integer NOT NULL CHECK (min_latency_ms >= 0),
  max_latency_ms integer NOT NULL CHECK (max_latency_ms >= min_latency_ms),
  p95_latency_ms double precision NOT NULL CHECK (p95_latency_ms >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (monitor_id, bucket_start)
);
CREATE INDEX monitor_hourly_stats_bucket_idx ON monitor_hourly_stats (bucket_start);
CREATE INDEX monitor_hourly_stats_workspace_bucket_idx ON monitor_hourly_stats (workspace_id, bucket_start);

CREATE TABLE rollup_state (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  watermark timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO rollup_state (id) VALUES (true);

CREATE INDEX probe_results_checked_idx ON probe_results (checked_at);
CREATE INDEX security_events_created_idx ON security_events (created_at);

-- +goose Down
DROP INDEX IF EXISTS security_events_created_idx;
DROP INDEX IF EXISTS probe_results_checked_idx;
DROP TABLE rollup_state;
DROP TABLE monitor_hourly_stats;
DROP DOMAIN rollup_hour;
