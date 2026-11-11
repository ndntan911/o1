# Plan 13 — Probe Rollups and Data Retention Design

**Status:** Draft for approval
**Date:** 2026-11-11
**Depends on:** Plan 2 (probe results, monthly partitions), Plan 6 (public status page), Plan 9 (metrics), Plan 11 (deploy operations)
**Scope owner:** o1 uptime monitor

## 1. Intent

`probe_results` currently grows without bound. Every probe is stored forever, and
the public status page computes 90 days of daily uptime by scanning raw rows.
Plan 13 adds hourly rollups, backfills them from existing raw data, switches
long-window reads to the rollups, and introduces guarded retention for raw probe
partitions, rollups, and audit events.

The result is bounded storage and stable query cost without changing the public
page or the 7-day monitor results API. This is a data-layer plan: no new
long-range charts, no changes to the 1h/6h/24h/7d results windows, and no
frontend work.

**Important caveat:** Plan 12 (backups/WAL/restore) is skipped. Retention will
delete the only copy of old raw probe rows. Rollups preserve up/down counts and
latency aggregates, but not individual status codes, errors, or exact probe
rows. Retention therefore ships with a dry-run mode, a count-equality safety
gate, and a conservative raw-retention floor.

## 2. Current State

- `probe_results` is range-partitioned monthly on `checked_at`; `db.EnsureProbePartitions`
  creates `[current month − 1, current month + 2]` at startup and once per day.
  Nothing drops old partitions.
- `probe_results` stores `monitor_id`, `workspace_id`, `scheduled_for`,
  `checked_at`, `status` (`up`/`down`), `status_code`, `latency_ms`, and `error`.
- `ListProbeResults` reads raw rows for the 1h/6h/24h/7d windows.
- `MonitorSummary` aggregates raw rows with `count`, `avg`, `percentile_cont(0.95)`,
  `min`, and `max`.
- The public status page's `ListProbeDayStats` groups raw rows into UTC days for
  the 90-day bar chart and overall uptime.
- There are no rollup tables, no retention jobs, and no audit cleanup.
- `security_events` is the audit table (login, API key, suspension, TOTP, monitor
  deletion events). It has no cleanup job.
- The scheduler already owns daily probe-partition maintenance in its tick loop,
  and the River worker owns probe/alert jobs.
- Plan 6 explicitly deferred rollup tables and retention.

## 3. Goals

1. Add `monitor_hourly_stats` with UTC hourly buckets: up/down counts,
   avg/min/max latency, and p95 latency.
2. Backfill rollups from existing raw probe results, oldest to newest, in
   bounded chunks, without blocking probe scheduling.
3. Roll up newly closed hours on an ongoing basis with a lag for late-arriving
   results, idempotently.
4. Switch the public status page to read completed days from rollups and the
   recent frontier from raw, preserving the existing 90-day output.
5. Retain raw probe data for at least 90 days, rollups for 13 months, and audit
   events for 12 months.
6. Drop expired raw partitions only after proving rollups cover their rows.
7. Provide a dry-run retention mode, operational metrics, and a runbook section.
8. Keep the 1h/6h/24h/7d monitor results and summary APIs unchanged.

## 4. Non-Goals

- Backups, WAL archiving, point-in-time recovery, or restore drills (Plan 12,
  skipped).
- Long-range API windows or dashboard charts beyond 7 days (future work).
- Changing the public status page layout, copy, or 90-day window.
- Rollups for incidents, alerts, audit, or workspace-level dashboards.
- TimescaleDB, columnar storage, or a separate analytics database.
- Retention of River job history beyond what River already manages.
- Multi-region or managed Postgres.
- Data export or legal hold.

## 5. Anti-Goals

- Dropping a raw partition before its hours are rolled up.
- Losing probe data silently because a late row arrived after rollup.
- Deleting more than the configured retention window.
- A maintenance failure that stops probe scheduling or the API.
- A status page that shows missing historical days after maintenance runs.
- A retention job that deletes data in tests or on first deploy without an
  explicit, observable configuration.
- Storing per-monitor data without workspace scoping or cascade behavior.

## 6. Approaches Considered

### A. Hourly rollup table plus daily aggregation (chosen)

One `monitor_hourly_stats` table keyed by `(monitor_id, bucket_start)`. Daily
queries aggregate hourly rows. Backfill and ongoing rollups run in the scheduler.
Retention drops raw partitions, deletes old hourly rows, and deletes old audit
events.

- Pros: matches the foundation spec, one source of truth per hour, enough
  resolution for 13 months, simple schema and query model.
- Cons: daily p95 is approximate after raw is dropped; the status page must
  combine rollups and raw at the frontier.

### B. Separate hourly and daily rollup tables

Materialize a `monitor_daily_stats` table from the hourly table.

- Pros: the 90-day status page reads a small daily table directly; daily
  aggregates are precomputed.
- Cons: duplicate writes, backfills, retention, and tests for a cached page that
  runs rarely. Rejected until measurements justify it.

### C. Daily-only rollups with shorter raw retention

Store only daily stats and drop raw after 7 days.

- Pros: simplest and cheapest.
- Cons: contradicts the spec's 90-day raw retention, loses intra-day detail for
  7–90 days, and prevents future latency charts. Rejected.

## 7. Decisions

### 7.1 Schema

Migration `00009_rollups.sql` creates:

```sql
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
CREATE INDEX monitor_hourly_stats_bucket_idx
  ON monitor_hourly_stats (bucket_start);
CREATE INDEX monitor_hourly_stats_workspace_bucket_idx
  ON monitor_hourly_stats (workspace_id, bucket_start);
```

`bucket_start` is always the UTC hour boundary. `checks` is stored because the
status page and queries use total checks; the check constraint prevents drift.
`workspace_id` is redundant with `monitor_id` but keeps tenant scoping and
matches `probe_results`; both foreign keys cascade on delete.

Add a single-row watermark table so backfill and ongoing rollups share one
contiguous frontier:

```sql
CREATE TABLE rollup_state (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  watermark timestamptz, -- last contiguous fully rolled-up hour; null before any run
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO rollup_state (id) VALUES (true) ON CONFLICT DO NOTHING;
```

Also add `CREATE INDEX probe_results_checked_idx ON probe_results (checked_at)`
so the hourly range scan does not scan a whole monthly partition. PostgreSQL
creates the index on every existing and future partition. On a large existing
dataset this takes a lock; run the migration in a maintenance window.

Add `CREATE INDEX security_events_created_idx ON security_events (created_at)`
for audit retention. The existing indexes lead with `kind` or `workspace_id`, so
a created_at range delete would otherwise scan.

Down migration drops the table and the indexes.

### 7.2 Aggregation semantics

`UpsertHourlyStats(start, end)` runs one SQL statement per hour (or a range
grouped by hour):

```sql
INSERT INTO monitor_hourly_stats
  (monitor_id, workspace_id, bucket_start, up_count, down_count, checks,
   avg_latency_ms, min_latency_ms, max_latency_ms, p95_latency_ms, updated_at)
SELECT
  monitor_id,
  workspace_id,
  date_trunc('hour', checked_at) AS bucket_start,
  count(*) FILTER (WHERE status = 'up'),
  count(*) FILTER (WHERE status = 'down'),
  count(*),
  avg(latency_ms),
  min(latency_ms),
  max(latency_ms),
  percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms),
  now()
FROM probe_results
WHERE checked_at >= $1 AND checked_at < $2
GROUP BY monitor_id, workspace_id, date_trunc('hour', checked_at)
ON CONFLICT (monitor_id, bucket_start) DO UPDATE SET
  up_count = EXCLUDED.up_count,
  down_count = EXCLUDED.down_count,
  checks = EXCLUDED.checks,
  avg_latency_ms = EXCLUDED.avg_latency_ms,
  min_latency_ms = EXCLUDED.min_latency_ms,
  max_latency_ms = EXCLUDED.max_latency_ms,
  p95_latency_ms = EXCLUDED.p95_latency_ms,
  updated_at = now();
```

- Uptime is `up_count / (up_count + down_count)`; no-data is excluded exactly
  as the existing contract does.
- Latency values include all rows in the hour, matching the raw summary.
- p95 is exact per hour (`percentile_cont`).
- Daily p95 is not exposed in this plan. When a daily p95 is added later, it must
  be documented as approximate after raw retention: an upper bound from hourly
  p95 values, or a histogram-based exact value. Do not silently average p95s.

### 7.3 Backfill and ongoing rollup

Backfill and ongoing rollups are one catch-up path driven by a contiguous
watermark, not by `max(bucket_start)`:

- `rollup_state.watermark` is the last hour for which every hour from the oldest
  processed hour through that hour is fully rolled up. It is updated in the same
  transaction as the hour's upsert.
- On each pass, read the watermark. The next hour is `watermark + 1h`; when the
  watermark is null, start at the oldest raw hour. The target is the newest hour
  whose end is at least the late-arrival lag in the past:
  `(now - lag - 1h)` truncated to the hour.
- Process at most `O1_ROLLUP_MAX_HOURS_PER_RUN` hours per pass, oldest to newest,
  committing in chunks. Update the watermark after each hour. A crash leaves the
  watermark at the last committed hour, so the next pass resumes there.
- This means the first pass backfills from the oldest raw hour. The status page
  uses rollups below the watermark and raw at and above it, so the 90-day page
  is complete throughout the catch-up. An empty database has no work.
- Once the watermark reaches the target, re-roll the previous 2 hours on each
  pass to pick up late-arriving results. Upserts make this idempotent.
- Use a PostgreSQL advisory lock for each pass. A second scheduler instance
  skips, and a long backfill does not hold the lock between chunks.
- The 15-minute lag plus 2-hour re-roll window is the late-row contract. Rows
  inserted more than 2 hours after their hour are out of contract; retention's
  partition repair (7.4) is the safety net.
- Monitors with no probes in an hour get no row. No-data is represented by
  absence, matching the raw query.

### 7.4 Retention

Retention is one guarded daily job, run in this order: raw partitions, then
rollups, then audit events.

**Safety defaults:** `O1_RETENTION_DRY_RUN` defaults to `true`, so a fresh
deploy only previews deletions. Operators explicitly set it to `false` after
reviewing the dry-run counts. Each run drops at most
`O1_RETENTION_MAX_PARTITION_DROPS` raw partitions (default 1).

**Raw probe partitions (90-day floor):**

- Candidates are partitions whose entire upper bound is at or before
  `now − O1_RAW_RETENTION_DAYS`. Because partitions are monthly, retained raw is
  90–120 days; the promise is at least 90 days, not exactly 90.
- A partition whose end is at or before `now − O1_ROLLUP_RETENTION_MONTHS` may be
  dropped without a rollup gate: no consumer needs rollups that old, and the
  rollup retention would delete them anyway. Emit a distinct metric for this
  path.
- Otherwise, require `watermark >= partition_end` and
  `sum(up_count + down_count)` in rollups to equal `count(*)` in raw over the
  partition range.
- On a mismatch, re-roll the partition's hours from raw once, update the
  watermark where applicable, and re-check. If it still differs, log, emit
  `count_mismatch`, and skip. A human can then inspect or run the repair again.
- When verified, drop with `DROP TABLE probe_results_YYYY_MM`. This is metadata
  plus file unlink; it takes a brief lock on the parent and the partition. Run
  off-peak.
- `EnsureProbePartitions` continues to create future partitions; it does not
  recreate dropped ones.

**Hourly rollups (13 months, minimum 4):**

- Delete rows with `bucket_start < now − O1_ROLLUP_RETENTION_MONTHS` in batches
  of 10,000 using a `ctid` subquery, committing between batches. Cap each run so
  maintenance cannot run unbounded.
- The 4-month floor keeps at least the 90-day public page covered.

**Audit events (12 months):**

- Delete `security_events` rows with `created_at < now − O1_AUDIT_RETENTION_MONTHS`
  in the same batched pattern.
- This is the audit retention promised by the foundation spec and deferred by
  Plan 7.

**Dry run:**

- `O1_RETENTION_DRY_RUN=true` computes candidates and counts, logs them, emits
  metrics, and changes nothing.
- Tests use dry-run or scoped fixtures; production must opt in to deletion.

### 7.5 Scheduling and failure isolation

- The scheduler process owns maintenance; no new binary or Compose service.
- Add a 15-minute rollup ticker and a daily retention ticker alongside the
  existing probe tick and daily partition ticker.
- Run one catch-up pass immediately on startup, then on every rollup tick.
- Each maintenance pass runs behind a recover boundary and logs errors without
  stopping the probe loop. A failed pass is retried on the next tick.
- Use a PostgreSQL advisory lock (`pg_try_advisory_lock`) for the whole
  maintenance pass. If another scheduler instance holds it, skip this tick.
- The existing daily partition tick and retention tick can be combined: create
  missing partitions, then run retention.
- `EnsureProbePartitions` remains at startup and daily; retention runs after it.

### 7.6 Query changes

- **Public status page:** add `ListRollupDayStats` to aggregate hourly rows into
  UTC days (`SUM(up_count)`, `SUM(down_count)`, `SUM(checks)`), and
  `RollupWatermark` to read `rollup_state.watermark`.
  - Determine `splitDay`: if the watermark exists, the UTC day of the watermark;
    otherwise today. Days before `splitDay` come from rollups; days from
    `splitDay` onward come from raw.
  - Merge both result sets into the existing day-stat shape and keep
    `buildDays`/`buildUptime` behavior identical.
  - If rollups are missing for an older day, the page shows `no_data`; the
    maintenance metrics and logs are the signal. The safety gate prevents raw
    loss while rollups are incomplete.
- **Monitor results and summary:** unchanged. They remain raw and capped at
  7 days. Future long-range windows are a separate plan.
- **Admin/API keys/audit:** no query changes; retention only deletes old rows.

### 7.7 Configuration

New environment variables, validated in `config.Load`:

| Variable | Default | Validation |
|---|---|---|
| `O1_RAW_RETENTION_DAYS` | `90` | 7–365 |
| `O1_ROLLUP_RETENTION_MONTHS` | `13` | 4–60 |
| `O1_AUDIT_RETENTION_MONTHS` | `12` | 1–60 |
| `O1_RETENTION_DRY_RUN` | `true` | boolean |
| `O1_RETENTION_MAX_PARTITION_DROPS` | `1` | 1–12 |
| `O1_ROLLUP_INTERVAL` | `15m` | 1m–24h |
| `O1_ROLLUP_MAX_HOURS_PER_RUN` | `24` | 1–720 |

Invalid values fail startup, matching existing config behavior. The maintenance
lag (15m), re-roll window (2h), and retention batch size are internal constants.

### 7.8 Observability

Add to the Plan 9 metrics catalog:

- `o1_rollup_runs_total{result}` — success/error.
- `o1_rollup_hours_total` — hours processed.
- `o1_rollup_rows_total` — upserted hourly rows.
- `o1_rollup_frontier_timestamp_seconds` — the watermark (last contiguous hour).
- `o1_retention_runs_total{result}`.
- `o1_retention_rows_deleted_total{kind}` — `rollup`, `audit`.
- `o1_retention_partitions_dropped_total`.
- `o1_retention_skipped_total{reason}` — `dry_run`, `count_mismatch`, `locked`.
- `o1_maintenance_duration_seconds{job}` — `rollup`, `retention`.

Logs include counts and durations, never row contents. Backup/retention failures
are visible through these metrics and existing Grafana alerts; no new alerting
stack is added.

### 7.9 Documentation

- Root README: add a "Data lifecycle" section with the windows, env vars,
  dry-run default, one-partition-per-run cap, and the Plan 12 caveat.
- `deploy/production/README.md`: replace "Backups are Plan 12" with the current
  state: no backups, retention deletes raw older than ~90 days, rollups are
  aggregates, deletion is off until `O1_RETENTION_DRY_RUN=false`, and at most one
  partition drops per run by default.
- `deploy/CICD.md`: note that deploys do not migrate or delete data; the
  scheduler performs maintenance after startup.
- Document how to verify the frontier and counts:

```sql
SELECT max(bucket_start) FROM monitor_hourly_stats;
SELECT count(*) FROM monitor_hourly_stats;
SELECT relname FROM pg_class WHERE relname LIKE 'probe_results_%' AND relkind = 'r';
```

## 8. Constraints

- UTC everywhere: buckets, days, retention cutoffs.
- Retention deletes are irreversible; Plan 12 is skipped, so there is no
  restore path.
- Maintenance must not block probes, the API, or the worker.
- No new services; the scheduler owns the jobs.
- `probe_results` stays monthly-partitioned; no repartitioning migration.
- Existing status page output (90 bars, uptime, incident sections) is unchanged.
- Backend only; web code and E2E flows are unchanged.
- Tests use testcontainers; no live S3 or backups.

## 9. Deliverables

```text
backend/internal/db/migrations/00009_rollups.sql        # create
backend/internal/db/queries/rollups.sql                 # create
backend/internal/db/queries/status_pages.sql            # modify
backend/internal/db/sqlc/*                              # regenerate
backend/internal/rollup/rollup.go                       # create
backend/internal/rollup/rollup_test.go                  # create
backend/internal/retention/retention.go                 # create
backend/internal/retention/retention_test.go            # create
backend/internal/scheduler/scheduler.go                 # modify: tickers/wiring
backend/internal/scheduler/scheduler_test.go            # modify
backend/internal/statuspage/service.go                  # modify: rollup+raw merge
backend/internal/statuspage/service_test.go             # modify
backend/internal/config/config.go                       # modify: retention config
backend/internal/config/config_test.go                  # modify
backend/internal/observability/metrics.go               # modify: catalog
backend/internal/observability/*_test.go                # modify
README.md                                               # modify
deploy/production/README.md                             # modify
deploy/CICD.md                                          # modify
```

## 10. Verification

- Rollup unit/integration tests:
  - one hour with mixed up/down rows produces exact counts, avg/min/max, p95;
  - re-running the same hour updates rather than duplicates;
  - a late row re-rolled in the window updates the bucket;
  - the watermark advances contiguously across multiple passes and resumes after
    a simulated crash;
  - the ongoing ticker never advances past an unprocessed gap;
  - multi-hour catch-up is ordered and complete;
  - hourly rows for deleted monitors cascade away.
- Retention tests:
  - a partition wholly older than the cutoff with matching counts is dropped;
  - a partition older than the rollup retention window is dropped without a
    rollup gate;
  - a partition with a count mismatch is repaired and re-checked, then kept and
    logged if it still differs;
  - a partition that is not wholly expired is kept;
  - the per-run partition-drop cap is honored;
  - dry-run deletes nothing and is the default;
  - rollup and audit deletes remove only rows older than the cutoff and batch
    correctly;
  - the advisory lock prevents a concurrent pass.
- Status page tests:
  - completed days below the watermark come from rollups, the watermark day and
    today from raw;
  - missing rollup days are no-data, not fabricated;
  - 90-bar output and uptime are unchanged with complete rollups.
- Config tests: defaults (including dry-run true), bounds, invalid values.
- Metrics tests: new series appear in the registry after a pass.
- Schema test: `probe_results_checked_idx` exists on the parent and on each
  partition.
- Full backend suite, `go vet`, `golangci-lint`, and the existing web suite stay
  green.
- Manual check on dev Compose:
  `O1_RETENTION_DRY_RUN=true` on a seeded database logs the counts and changes
  nothing; running without dry-run on a disposable database drops only the
  expected partition.

## 11. Decisions Left to the Builder

- Transaction batch sizes and per-run caps for rollup upserts and batched
  deletes (within the configured maximums).
- The exact SQL shape for partition discovery and count comparison.
- Whether to combine the daily partition and retention tickers or keep them
  separate.
- How much progress logging is emitted per chunk.
- Whether the watermark is cached in the status page cache or queried each
  refresh.
- Whether the migration builds the new `checked_at` index online or accepts a
  maintenance window.

## 12. Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Irreversible raw deletion with no backups | Dry-run default, 90-day floor, watermark + count-equality gate, one-partition-per-run cap, documented caveat |
| Late rows missed after rollup | 15-minute lag, 2-hour re-roll, partition repair before drop |
| Backfill starves ongoing rollups or blocks probes | One watermark-driven catch-up path, bounded hours per pass, advisory lock per pass, raw fallback above the watermark |
| Maintenance failure grows raw storage | Metrics, logs, retry next tick; retention is not required for probes to run |
| Partition drop locks the table | Drop only fully expired verified partitions, metadata operation, off-peak daily run, per-run cap |
| Daily p95 becomes approximate | Not exposed; future work must use an upper bound or histogram and document it |
| Status page gap during backfill | Watermark split uses rollups below it and raw at or above it |
| Audit delete scan is slow | Add `security_events(created_at)` index, batched deletes |
| Rollup range scan is slow | Add `probe_results(checked_at)` index, partition-local |
| Timezone drift | All cutoffs and buckets are UTC; tests pin UTC |

## 13. Rollout

1. Land migration, rollup, retention, query, config, metrics, and tests.
2. Deploy with the default `O1_RETENTION_DRY_RUN=true`; watch the rollup
   watermark advance from the oldest raw hour and confirm the status page still
   shows the full 90-day window.
3. Confirm the status page matches the pre-change output once the watermark
   passes the 7-day boundary.
4. Review dry-run counts for raw partitions, rollups, and audit rows. Check that
   the count gate reports matches and that the per-run cap is 1.
5. Set `O1_RETENTION_DRY_RUN=false`; the next daily run drops at most one fully
   expired, verified partition.
6. Monitor `o1_rollup_frontier_timestamp_seconds`, retention metrics, and raw
   partition count for a week; raise the partition cap only after the first
   drops are clean.
