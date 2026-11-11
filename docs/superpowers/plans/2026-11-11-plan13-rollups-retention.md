# Plan 13 — Probe Rollups and Data Retention Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add hourly probe rollups, a watermark-driven catch-up that serves the 90-day status page, and guarded retention for raw probe partitions, hourly rollups, and audit events.

**Architecture:** A new `monitor_hourly_stats` table stores one UTC hour per monitor. A single-row `rollup_state.watermark` tracks the contiguous rolled-up frontier; the scheduler runs one catch-up path from the oldest raw hour and a daily retention pass. The status page reads rollups below the watermark and raw at or above it. Retention defaults to dry-run and drops at most one verified partition per run.

**Tech Stack:** Go 1.26, PostgreSQL 16 (range partitions), pgx/v5, sqlc 1.31.1, goose, River, Prometheus metrics, testcontainers-go, Vitest/Playwright (unchanged).

**Spec:** `docs/superpowers/specs/2026-11-11-plan13-rollups-retention-design.md`

## Global Constraints

- All buckets, days, and retention cutoffs are UTC.
- Raw probe retention is at least 90 days; because partitions are monthly, retained raw is 90–120 days.
- Hourly rollups are retained 13 months by default; the configured floor is 4 months so the 90-day public page is always covered.
- Audit (`security_events`) retention is 12 months by default.
- `O1_RETENTION_DRY_RUN` defaults to `true`; deletion requires an explicit `false`.
- At most `O1_RETENTION_MAX_PARTITION_DROPS` raw partitions (default 1) drop per run.
- A raw partition drops only when it is wholly expired and either beyond the rollup retention window or fully rolled up with matching check counts.
- The rollup watermark is contiguous; never advance it past an unprocessed hour.
- Hourly p95 is exact. Daily p95 is not exposed in this plan.
- The 1h/6h/24h/7d results and summary APIs stay raw and unchanged.
- The public status page keeps its 90 daily bars, uptime contract, incidents, and cache behavior.
- No new service or binary; the scheduler owns maintenance. No backups (Plan 12 skipped).
- Tests use testcontainers for integration; `-short` must remain Docker-free.
- Backend only; web code is unchanged.

## Review Focus

1. **Partition drop safety.** A partition with a missing rollup, a count mismatch, an unadvanced watermark, or an unexpired range must not be dropped; the old-beyond-rollup-window path drops without a gate. Tested in Task 3.
2. **Watermark continuity.** A crash, error, or max-hours cap must leave the watermark at the last committed hour and resume without skipping. Tested in Task 2.
3. **Late-arriving probes.** A row inserted within the 2-hour re-roll window updates its bucket; rows outside it are not re-read. Tested in Task 2.
4. **Deletion defaults.** Dry-run is the default, the per-run cap is honored, and no partition or row is deleted when dry-run is on. Tested in Tasks 1, 3, and 4.
5. **Status page parity.** Completed days below the watermark come from rollups, the watermark day and today from raw, and the 90-bar output matches the pre-change result. Tested in Task 5.

---

## File Structure

```text
backend/internal/db/migrations/00009_rollups.sql      # create: tables, state row, indexes
backend/internal/db/queries/rollups.sql               # create: rollup/state/retention queries
backend/internal/db/queries/status_pages.sql          # modify: no change if queries live in rollups.sql
backend/internal/db/sqlc/*                            # regenerate
backend/internal/db/rollup_schema_test.go             # create: migration/schema assertions
backend/internal/config/config.go                     # modify: retention config
backend/internal/config/config_test.go                # modify
backend/internal/rollup/rollup.go                     # create: catch-up engine
backend/internal/rollup/rollup_test.go                # create
backend/internal/retention/retention.go               # create: shared Retainer/Result/Config
backend/internal/retention/partitions.go              # create: raw partition retention
backend/internal/retention/partitions_test.go         # create
backend/internal/retention/prune.go                   # create: rollup/audit pruning
backend/internal/retention/prune_test.go              # create
backend/internal/statuspage/service.go                # modify: rollup+raw merge
backend/internal/statuspage/service_test.go           # modify
backend/internal/scheduler/scheduler.go               # modify: maintenance loop/lock
backend/internal/scheduler/scheduler_test.go          # modify
backend/internal/observability/metrics.go             # modify: maintenance metrics
backend/internal/observability/metrics_test.go        # modify
backend/cmd/scheduler/main.go                         # modify: wire rollup/retention
README.md                                             # modify: data lifecycle
deploy/production/README.md                           # modify: retention caveat
deploy/CICD.md                                        # modify: maintenance note
```

`rollup` owns hourly aggregation; `retention` owns all deletes; the scheduler owns timing and the maintenance lock; `statuspage` owns the read split.

---

### Task 1: Rollup schema, queries, and retention config

**Files:**
- Create: `backend/internal/db/migrations/00009_rollups.sql`
- Create: `backend/internal/db/queries/rollups.sql`
- Create: `backend/internal/db/rollup_schema_test.go`
- Regenerate: `backend/internal/db/sqlc/*`
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`

**Interfaces:**
- Produces config fields: `RawRetentionDays int`, `RollupRetentionMonths int`, `AuditRetentionMonths int`, `RetentionDryRun bool`, `RetentionMaxPartitionDrops int`, `RollupInterval time.Duration`, `RollupMaxHoursPerRun int`.
- Produces queries (sqlc):
  - `RollupHourlyStats(ctx, RollupHourlyStatsParams{Start, End time.Time}) (int64, error)`
  - `GetRollupWatermark(ctx) (*time.Time, error)`
  - `SetRollupWatermark(ctx, watermark time.Time) error`
  - `OldestProbeHour(ctx) (*time.Time, error)`
  - `CountProbeChecks(ctx, CountProbeChecksParams{Start, End time.Time}) (int64, error)`
  - `CountRollupChecks(ctx, CountRollupChecksParams{Start, End time.Time}) (int64, error)`
  - `ListRollupDayStats(ctx, ListRollupDayStatsParams{MonitorIds []uuid.UUID, Since, Until time.Time}) ([]ListRollupDayStatsRow, error)` where a row has `MonitorID uuid.UUID`, `Day string`, `Checks int64`, `UpCount int64`, `DownCount int64`.
  - `DeleteOldHourlyStats(ctx, before time.Time) (int64, error)`
  - `DeleteOldSecurityEvents(ctx, before time.Time) (int64, error)`
  - `CountOldHourlyStats(ctx, before time.Time) (int64, error)`
  - `CountOldSecurityEvents(ctx, before time.Time) (int64, error)`
- Produces schema: `monitor_hourly_stats`, `rollup_state`, `probe_results_checked_idx`, `security_events_created_idx`.

- [ ] **Step 1: Write failing config tests**

Add to `config_test.go`:

```go
func TestRetentionConfig_Defaults(t *testing.T) {
	setEmailEnv(t, "development")
	for _, key := range []string{
		"O1_RAW_RETENTION_DAYS", "O1_ROLLUP_RETENTION_MONTHS",
		"O1_AUDIT_RETENTION_MONTHS", "O1_RETENTION_DRY_RUN",
		"O1_RETENTION_MAX_PARTITION_DROPS", "O1_ROLLUP_INTERVAL",
		"O1_ROLLUP_MAX_HOURS_PER_RUN",
	} {
		t.Setenv(key, "")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RawRetentionDays != 90 || cfg.RollupRetentionMonths != 13 ||
		cfg.AuditRetentionMonths != 12 || !cfg.RetentionDryRun ||
		cfg.RetentionMaxPartitionDrops != 1 || cfg.RollupInterval != 15*time.Minute ||
		cfg.RollupMaxHoursPerRun != 24 {
		t.Fatalf("retention defaults = %+v", cfg)
	}
}

func TestRetentionConfig_RejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"O1_RAW_RETENTION_DAYS":            "6",
		"O1_ROLLUP_RETENTION_MONTHS":       "3",
		"O1_AUDIT_RETENTION_MONTHS":        "0",
		"O1_RETENTION_DRY_RUN":             "sometimes",
		"O1_RETENTION_MAX_PARTITION_DROPS": "0",
		"O1_ROLLUP_INTERVAL":               "500ms",
		"O1_ROLLUP_MAX_HOURS_PER_RUN":      "0",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			setEmailEnv(t, "development")
			t.Setenv(key, value)
			if _, err := config.Load(); err == nil {
				t.Fatalf("Load accepted %s=%s", key, value)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/config -run TestRetentionConfig -v`
Expected: FAIL because the fields and env parsing do not exist.

- [ ] **Step 3: Add config fields, parsing, and validation**

Add `envBool` beside the other `env*` helpers (add `strconv` to the imports if absent):

```go
func envBool(key string, fallback bool) (bool, error) {
	v := env(key, "")
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s: must be true or false", key)
	}
	return b, nil
}
```

Load:

```go
if cfg.RawRetentionDays, err = envInt("O1_RAW_RETENTION_DAYS", 90); err != nil { return cfg, err }
if cfg.RollupRetentionMonths, err = envInt("O1_ROLLUP_RETENTION_MONTHS", 13); err != nil { return cfg, err }
if cfg.AuditRetentionMonths, err = envInt("O1_AUDIT_RETENTION_MONTHS", 12); err != nil { return cfg, err }
if cfg.RetentionDryRun, err = envBool("O1_RETENTION_DRY_RUN", true); err != nil { return cfg, err }
if cfg.RetentionMaxPartitionDrops, err = envInt("O1_RETENTION_MAX_PARTITION_DROPS", 1); err != nil { return cfg, err }
if cfg.RollupInterval, err = envDuration("O1_ROLLUP_INTERVAL", 15*time.Minute); err != nil { return cfg, err }
if cfg.RollupMaxHoursPerRun, err = envInt("O1_ROLLUP_MAX_HOURS_PER_RUN", 24); err != nil { return cfg, err }
```

Validate in `validate()`: raw days 7–365; rollup months 4–60; audit months 1–60; max drops 1–12; rollup interval 1m–1h; max hours 1–720. Error messages name the env variable and the bound.

- [ ] **Step 4: Write the failing schema test**

Create `backend/internal/db/rollup_schema_test.go` (package `db_test`):

```go
func TestRollupSchema(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	for _, name := range []string{"monitor_hourly_stats", "rollup_state"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("%s missing", name)
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rollup_state WHERE id`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rollup_state rows = %d, want 1", rows)
	}
	for _, index := range []string{"probe_results_checked_idx", "security_events_created_idx"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("%s missing", index)
		}
	}
}
```

Add `context` and `o1/internal/testutil` imports.

- [ ] **Step 5: Run the schema test to verify it fails**

Run: `cd backend && go test ./internal/db -run TestRollupSchema -v`
Expected: FAIL because the migration and tables do not exist.

- [ ] **Step 6: Create the migration**

`00009_rollups.sql`:

```sql
-- +goose Up
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
```

- [ ] **Step 7: Create the queries and regenerate sqlc**

`backend/internal/db/queries/rollups.sql`:

```sql
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
UPDATE rollup_state SET watermark = $1, updated_at = now() WHERE id;

-- name: OldestProbeHour :one
SELECT date_trunc('hour', min(checked_at)) FROM probe_results;

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
```

Run: `cd backend && sqlc generate`
Expected: generated files compile; no unrelated diff.

- [ ] **Step 8: Run the tests to verify they pass**

Run:

```bash
cd backend
go test ./internal/config -run TestRetentionConfig -v
go test ./internal/db -run TestRollupSchema -v
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/db/migrations/00009_rollups.sql \
  backend/internal/db/queries/rollups.sql backend/internal/db/sqlc \
  backend/internal/db/rollup_schema_test.go \
  backend/internal/config/config.go backend/internal/config/config_test.go
git commit -m "feat: add rollup schema, queries, and retention config"
```

---

### Task 2: Hourly rollup engine

**Files:**
- Create: `backend/internal/rollup/rollup.go`
- Create: `backend/internal/rollup/rollup_test.go`

**Interfaces:**
- Consumes: Task 1 queries.
- Produces:
  - `type Result struct { Hours int; Rows int64; From, To time.Time }`
  - `type Metrics interface { ObserveRollup(hours int, rows int64, d time.Duration, err error); SetRollupWatermark(t time.Time) }`
  - `func New(pool *pgxpool.Pool, q *sqlc.Queries, logger *slog.Logger, metrics Metrics, maxHoursPerRun int) *Rolluper`
  - `func (r *Rolluper) Run(ctx context.Context) (Result, error)`
  - `func (r *Rolluper) RollupHour(ctx context.Context, hour time.Time) (int64, error)`

- [ ] **Step 1: Write the failing rollup tests**

Create `rollup_test.go` with a testcontainers helper that creates a user, workspace, and monitor, then inserts raw rows. Tests:

```go
func TestRun_RollsUpOneHour(t *testing.T) {
	// Seed three up rows (100, 200, 300ms) and one down row (400ms) in
	// 2026-10-01 10:00 UTC. Run with now = 12:00 UTC and maxHours = 24.
	// Assert one row: up=3, down=1, checks=4, avg/min/max match, p95
	// equals the reference percentile_cont over the same raw rows.
}

func TestRun_IsIdempotentAndUpdatesLateRows(t *testing.T) {
	// Roll up an hour, insert another up row in the same hour, run again.
	// Assert one row with updated counts, not two rows.
}

func TestRun_ResumesFromWatermark(t *testing.T) {
	// Seed rows in two hours. First run with maxHours=1; assert the
	// watermark is the first hour. Second run; assert the second hour.
}

func TestRun_NeverSkipsAGap(t *testing.T) {
	// Set the watermark to an hour with no raw rows, seed a later hour.
	// Assert Run advances through the gap hour without writing a row and
	// stops at the max-hours cap or target.
}

func TestRun_ReRollsRecentHoursOnlyAtTheFrontier(t *testing.T) {
	// When the watermark is behind the target, recent hours above the
	// watermark are not written. When caught up, the previous 2 hours are
	// re-read.
}

func TestRun_DeletedMonitorsCascade(t *testing.T) {
	// Roll up, delete the monitor, assert hourly rows are gone.
}
```

Use `testutil.NewPostgres(t)` and direct inserts. For p95, compare against:

```go
var wantP95 float64
err := pool.QueryRow(ctx, `
  SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)
  FROM probe_results WHERE monitor_id = $1 AND checked_at >= $2 AND checked_at < $3`,
	monitorID, start, end).Scan(&wantP95)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/rollup -v`
Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the rollup engine**

`rollup.go`:

- Constants: `lateArrivalLag = 15 * time.Minute`, `replayWindow = 2 * time.Hour`.
- `Rolluper` fields: pool, q, logger, metrics, maxHours, now func() time.Time.
- `New`: default maxHours to 24 when <= 0; `now = time.Now`.
- `Run`:
  1. `watermark, err := q.GetRollupWatermark(ctx)`.
  2. If `watermark == nil`, `oldest, err := q.OldestProbeHour(ctx)`; if nil, return zero Result.
  3. `start := oldest.Truncate(time.Hour)`; if watermark != nil, `start = watermark.Truncate(time.Hour).Add(time.Hour)`.
  4. `target := r.now().UTC().Add(-lateArrivalLag).Add(-time.Hour).Truncate(time.Hour)`.
  5. If `start.After(target)`, skip the catch-up and go to replay if `watermark != nil && !watermark.Before(target)`.
  6. Process hours from `start` to `target` inclusive, at most `maxHours`. For each:
     - `rows, err := r.RollupHour(ctx, hour)`.
     - `q.SetRollupWatermark(ctx, hour)` on success.
     - accumulate `Result.Hours`, `Rows`, set `From` on first, `To` on last.
  7. When the watermark is at or past `target`, re-roll the previous `replayWindow` hours ending at `target` (do not move the watermark). Do this only for hours >= the oldest raw hour to avoid scanning empty far-past hours; the query returns 0 otherwise.
  8. On error, return the partial Result and the wrapped error.
- `RollupHour`: `return q.RollupHourlyStats(ctx, sqlc.RollupHourlyStatsParams{Start: hour.UTC().Truncate(time.Hour), End: hour.UTC().Truncate(time.Hour).Add(time.Hour)})`.
- Metrics: if `r.metrics != nil`, call `ObserveRollup(result.Hours, result.Rows, duration, err)` and `SetRollupWatermark(result.To)`. When a pass processes no hours but a watermark exists, call `SetRollupWatermark` with that existing watermark so the gauge stays current.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/rollup -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/rollup
git commit -m "feat: add hourly probe rollup engine"
```

---

### Task 3: Guarded raw probe retention

**Files:**
- Create: `backend/internal/retention/retention.go`
- Create: `backend/internal/retention/partitions.go`
- Create: `backend/internal/retention/partitions_test.go`

**Interfaces:**
- Consumes: Task 1 queries; Task 2 `Rolluper.RollupHour`.
- Produces:
  - `type Config struct { RawRetentionDays, RollupRetentionMonths, AuditRetentionMonths, MaxPartitionDrops int; DryRun bool }`
  - `type HourRolluper interface { RollupHour(ctx context.Context, hour time.Time) (int64, error) }`
  - `type Result struct { Candidates, PartitionsDropped, SkippedDryRun, SkippedWatermark, SkippedMismatch int; RollupRows, AuditRows int64 }`
  - `func New(pool *pgxpool.Pool, q *sqlc.Queries, logger *slog.Logger, cfg Config, repairer HourRolluper) *Retainer`
  - `func (r *Retainer) Run(ctx context.Context) (Result, error)`

- [ ] **Step 1: Write the failing partition tests**

Create `partitions_test.go` with `testutil.NewPostgres(t)`. Each test creates a partition with an explicit name and bounds, seeds raw rows and rollups, sets the watermark, and calls `Run`.

```go
func TestRun_DropsVerifiedExpiredPartition(t *testing.T) {
	// Partition probe_results_2026_08 (Aug 2026), now = 2026-12-01.
	// Raw count == rollup checks, watermark >= partition end.
	// Assert the partition is gone and PartitionsDropped == 1.
}

func TestRun_KeepsPartitionWithCountMismatch(t *testing.T) {
	// Same but one raw row has no rollup. The fake repairer does not fix it.
	// Assert the partition remains and the result reports a skip.
}

func TestRun_RepairsThenDrops(t *testing.T) {
	// Same mismatch, but the repairer inserts the missing rollup. Assert a
	// drop after one repair attempt.
}

func TestRun_KeepsUnexpiredPartition(t *testing.T) {
	// Partition end is within 90 days. Assert it remains.
}

func TestRun_DropsBeyondRollupWindowWithoutGate(t *testing.T) {
	// Partition end is older than the 13-month rollup retention. No
	// watermark and no rollups. Assert it drops.
}

func TestRun_DryRunChangesNothing(t *testing.T) {
	// DryRun=true with a verified expired partition. Assert it remains and
	// Candidates > 0.
}

func TestRun_HonorsPartitionDropCap(t *testing.T) {
	// Two verified expired partitions, MaxPartitionDrops=1. Assert exactly
	// one is dropped.
}
```

Partition helper:

```go
func createPartition(t *testing.T, pool *pgxpool.Pool, name string, start, end time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF probe_results FOR VALUES FROM ('%s') TO ('%s')",
		pgx.Identifier{name}.Sanitize(),
		start.Format(time.RFC3339), end.Format(time.RFC3339)))
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/retention -run TestRun -v`
Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the shared retainer and partition path**

`retention.go`:

- `Config`, `Result`, `HourRolluper`, `Retainer` fields (pool, q, logger, cfg, repairer, now func() time.Time, batchSize int default 10000).
- `New` validates nothing (config is already validated) and sets `now = time.Now`, `batchSize` to 10000, and `MaxPartitionDrops` to 1 when it is not positive.
- `Run` calls `runPartitions` (Task 3) and later `pruneRollups`/`pruneAudit` (Task 4). In Task 3, implement `Run` to call only `runPartitions`; Task 4 adds the other two calls.

`partitions.go`:

- `probePartitionRE = regexp.MustCompile(`^probe_results_(\d{4})_(\d{2})$`)`.
- `listProbePartitions(ctx) ([]probePartition, error)` queries:

```sql
SELECT c.relname
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class p ON p.oid = i.inhparent
WHERE p.relname = 'probe_results' AND c.relkind = 'r'
ORDER BY c.relname;
```

- `partitionStartEnd(name)`: parse year/month; start is the first of the month UTC, end is the first of the next month. Skip names that do not match, with a warn log.
- `runPartitions`:
  1. `now := r.now().UTC()`.
  2. `rawCutoff := now.AddDate(0, 0, -r.cfg.RawRetentionDays)`.
  3. `rollupCutoff := now.AddDate(0, -r.cfg.RollupRetentionMonths, 0)`.
  4. List partitions; filter `end <= rawCutoff`; increment `Candidates`.
  5. For each candidate (oldest first), stop when `PartitionsDropped == MaxPartitionDrops`:
     - If `DryRun`: read the raw/rollup counts for logging, increment
       `SkippedDryRun`, and continue. Do not repair and do not drop; dry-run
       must not mutate rollups.
     - If `end <= rollupCutoff`: drop without a rollup gate.
     - Else read the watermark; if nil or `watermark.Before(end)`, increment
       `SkippedWatermark` and continue.
     - Else compare `CountProbeChecks(start,end)` and
       `CountRollupChecks(start,end)`.
     - If unequal: call `repairPartition(start,end)` once (loop hours,
       `repairer.RollupHour`), re-read the counts, and increment
       `SkippedMismatch` if they still differ.
     - Else `DROP TABLE` with `pgx.Identifier{name}.Sanitize()`; increment
       `PartitionsDropped`.
- `repairPartition` returns the first error; log the hour count.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/retention -run TestRun -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/retention
git commit -m "feat: add guarded raw probe retention"
```

---

### Task 4: Rollup and audit retention

**Files:**
- Create: `backend/internal/retention/prune.go`
- Create: `backend/internal/retention/prune_test.go`
- Modify: `backend/internal/retention/retention.go` (call the two pruners from `Run`)

**Interfaces:**
- Consumes: Task 1 `DeleteOldHourlyStats`, `DeleteOldSecurityEvents`.
- Produces: `func (r *Retainer) pruneRollups(ctx context.Context, now time.Time) (int64, error)` and `func (r *Retainer) pruneAudit(ctx context.Context, now time.Time) (int64, error)`.

- [ ] **Step 1: Write the failing prune tests**

```go
func TestRun_PrunesOldRollupsAndKeepsNew(t *testing.T) {
	// Insert rollups at now-14 months and now-2 months. Run with
	// RollupRetentionMonths=13. Assert only the old row is gone and
	// Result.RollupRows == 1.
}

func TestRun_PrunesOldAuditEventsAndKeepsNew(t *testing.T) {
	// Insert security_events at now-13 months and now-1 month. Run with
	// AuditRetentionMonths=12. Assert only the old row is gone and
	// Result.AuditRows == 1.
}

func TestRun_PruneBatchesUseConfiguredBatchSize(t *testing.T) {
	// Set r.batchSize = 2 and insert 5 old rows. Assert all are deleted
	// across batches and no newer row is touched.
}

func TestRun_DryRunPrunesNothing(t *testing.T) {
	// DryRun=true with old rollups and audit rows. Assert both remain.
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/retention -run 'TestRun_Prune|TestRun_DryRunPrunes' -v`
Expected: FAIL because the prune paths do not exist.

- [ ] **Step 3: Implement batched pruning**

`prune.go`:

- `pruneRollups`: `cutoff := now.AddDate(0, -r.cfg.RollupRetentionMonths, 0)`; loop `DeleteOldHourlyStats(ctx, cutoff)` while rows == `batchSize` and a per-run cap (e.g., 100 batches) is not reached; return total. In dry-run, count rows with a `SELECT count(*)` before the delete? To keep it simple and testable, dry-run returns 0 and logs candidates. If the design requires dry-run counts, add `CountOldHourlyStats`/`CountOldSecurityEvents` queries in Task 1. Add them to Task 1's interface list and SQL:
  - `CountOldHourlyStats(ctx, before) (int64, error)`
  - `CountOldSecurityEvents(ctx, before) (int64, error)`
  Dry-run sets `Result.RollupRows`/`AuditRows` to those counts but deletes nothing.
- `pruneAudit`: same with `created_at`.
- `Run` order: `runPartitions`, then `pruneRollups`, then `pruneAudit`. Accumulate results. A prune error returns the partial result and the error.

Add the two count queries to Task 1 Step 7:

```sql
-- name: CountOldHourlyStats :one
SELECT count(*)::bigint FROM monitor_hourly_stats WHERE bucket_start < sqlc.arg('before');

-- name: CountOldSecurityEvents :one
SELECT count(*)::bigint FROM security_events WHERE created_at < sqlc.arg('before');
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/retention -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/retention
git commit -m "feat: prune expired rollups and audit events"
```

---

### Task 5: Status page rollup read

**Files:**
- Modify: `backend/internal/statuspage/service.go`
- Modify: `backend/internal/statuspage/service_test.go`

**Interfaces:**
- Consumes: `GetRollupWatermark`, `ListRollupDayStats` from Task 1.
- Produces: a local `dayStat` type and a merge helper used by `buildDays`/`buildUptime`.

- [ ] **Step 1: Write the failing status page tests**

Add tests:

```go
func TestPublicPage_UsesRollupsBelowWatermarkAndRawAtTheFrontier(t *testing.T) {
	// now = 2026-11-11 12:00 UTC. Watermark = 2026-11-09 23:00 UTC.
	// Rollups exist for 2026-11-08 and 2026-11-09; raw exists for
	// 2026-11-10 and 2026-11-11. Assert the day bars and 90-day uptime
	// combine both sources without double counting.
}

func TestPublicPage_NoWatermarkUsesRawOnly(t *testing.T) {
	// No rollups, watermark null, raw for today. Assert existing behavior.
}

func TestPublicPage_MissingRollupDayIsNoData(t *testing.T) {
	// Watermark covers a day with no rollup row and no raw fallback.
	// Assert that day is no_data and does not affect uptime.
}

func TestPublicPage_NinetyBarsRemainUnchanged(t *testing.T) {
	// Compare the 90 emitted dates and statuses with the pre-change
	// expectation when rollups fully cover the window.
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/statuspage -run TestPublicPage_ -v`
Expected: FAIL because the service still reads only raw rows.

- [ ] **Step 3: Implement the split and merge**

In `service.go`:

```go
type dayStat struct {
	MonitorID uuid.UUID
	Day       string
	Checks    int64
	UpCount   int64
	DownCount int64
}
```

In `publicPage` (or the existing function name):

1. `watermark, err := s.q.GetRollupWatermark(ctx)`.
2. `split := startOfUTCDay(now)`; if watermark != nil, `split = startOfUTCDay(*watermark)`.
3. If `split.After(since)`: query `ListRollupDayStats` with `Since: since, Until: split`; convert rows to `dayStat`.
4. Raw query: `rawSince := since`; if `split.After(rawSince)`, `rawSince = split`; query `ListProbeDayStats` with `Since: rawSince`; convert.
5. Merge both slices into one `[]dayStat`; the ranges do not overlap.
6. Keep the `allowedDays` filter and `buildDays`/`buildUptime`; change their parameter type to `[]dayStat`.

Do not change the cache key or TTL.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/statuspage -v`
Expected: PASS, including the existing 90-day and incident tests.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/statuspage
git commit -m "feat: serve completed status days from rollups"
```

---

### Task 6: Scheduler maintenance, lock, and metrics

**Files:**
- Modify: `backend/internal/scheduler/scheduler.go`
- Modify: `backend/internal/scheduler/scheduler_test.go`
- Modify: `backend/internal/observability/metrics.go`
- Modify: `backend/internal/observability/metrics_test.go`
- Modify: `backend/cmd/scheduler/main.go`
- Modify: `compose.yaml`
- Modify: `deploy/production/compose.yaml`
- Modify: `deploy/production/env.example`

**Interfaces:**
- Consumes: `rollup.Rolluper.Run`, `retention.Retainer.Run`.
- Produces:
  - `type RollupRunner interface { Run(ctx context.Context) (rollup.Result, error) }`
  - `type RetentionRunner interface { Run(ctx context.Context) (retention.Result, error) }`
  - `func (s *Scheduler) SetMaintenance(rollup RollupRunner, retention RetentionRunner)`
  - Metrics methods: `ObserveRollup(hours int, rows int64, d time.Duration, err error)`, `SetRollupWatermark(t time.Time)`, `ObserveRetention(d time.Duration, err error)`, `AddRetentionRows(kind string, n int64)`, `AddPartitionsDropped(n int)`, `ObserveRetentionSkip(reason string)`.

- [ ] **Step 1: Write the failing scheduler and metrics tests**

Scheduler tests:

```go
func TestMaintenance_RunsRollupThenRetention(t *testing.T) {
	// Set fake runners; call s.runMaintenance(ctx, true, true) directly.
	// Assert rollup ran before retention.
}

func TestMaintenance_FailureDoesNotStopProbeTick(t *testing.T) {
	// Fake rollup returns an error. Call runMaintenance(ctx, true, false),
	// then Tick; assert Tick still enqueues probes and the error is logged.
}

func TestMaintenance_SkipsWhenLockHeld(t *testing.T) {
	// Acquire pg_advisory_lock(maintenanceLockID) on one connection,
	// call runMaintenance(ctx, true, true), assert the fakes were not called.
}

func TestMaintenance_StartupPassRuns(t *testing.T) {
	// Start Run in a context with a very short rollup interval (or invoke
	// the startup path directly) and assert both runners are called once.
}
```

Metrics test: after `ObserveRollup(3, 12, time.Second, nil)`, `SetRollupWatermark(t)`, `ObserveRetention(time.Second, nil)`, `AddRetentionRows("rollup", 4)`, `AddRetentionRows("audit", 2)`, `AddPartitionsDropped(1)`, and `ObserveRetentionSkip("dry_run")`, the gathered text contains `o1_rollup_runs_total{result="success"}`, `o1_rollup_hours_total`, `o1_rollup_rows_total`, `o1_rollup_frontier_timestamp_seconds`, `o1_retention_runs_total{result="success"}`, `o1_retention_rows_deleted_total{kind="rollup"}`, `o1_retention_rows_deleted_total{kind="audit"}`, `o1_retention_partitions_dropped_total`, `o1_retention_skipped_total{reason="dry_run"}`, and `o1_maintenance_duration_seconds{job="rollup"}`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/scheduler ./internal/observability -run 'TestMaintenance|TestMetrics_Maintenance' -v`
Expected: FAIL because the interfaces and metrics do not exist.

- [ ] **Step 3: Add metrics**

In `metrics.go`, add to `NewMetrics` and register:

- `rollupRuns *prometheus.CounterVec` label `result`.
- `rollupHours prometheus.Counter`.
- `rollupRows prometheus.Counter`.
- `rollupWatermark prometheus.Gauge`.
- `retentionRuns *prometheus.CounterVec` label `result`.
- `retentionRows *prometheus.CounterVec` label `kind`.
- `retentionPartitionsDropped prometheus.Counter`.
- `retentionSkipped *prometheus.CounterVec` label `reason`.
- `maintenanceDuration *prometheus.HistogramVec` label `job`.

Add methods with nil-receiver guards:

```go
func (m *Metrics) ObserveRollup(hours int, rows int64, d time.Duration, err error) {
	if m == nil { return }
	result := "success"
	if err != nil { result = "error" }
	m.rollupRuns.WithLabelValues(result).Inc()
	m.rollupHours.Add(float64(hours))
	m.rollupRows.Add(float64(rows))
	m.maintenanceDuration.WithLabelValues("rollup").Observe(d.Seconds())
}
func (m *Metrics) SetRollupWatermark(t time.Time) {
	if m == nil { return }
	m.rollupWatermark.Set(float64(t.Unix()))
}
func (m *Metrics) ObserveRetention(d time.Duration, err error) {
	if m == nil { return }
	result := "success"
	if err != nil { result = "error" }
	m.retentionRuns.WithLabelValues(result).Inc()
	m.maintenanceDuration.WithLabelValues("retention").Observe(d.Seconds())
}
func (m *Metrics) AddRetentionRows(kind string, n int64) {
	if m == nil || n == 0 { return }
	m.retentionRows.WithLabelValues(kind).Add(float64(n))
}
func (m *Metrics) AddPartitionsDropped(n int) {
	if m == nil || n == 0 { return }
	m.retentionPartitionsDropped.Add(float64(n))
}
func (m *Metrics) ObserveRetentionSkip(reason string) {
	if m == nil { return }
	m.retentionSkipped.WithLabelValues(reason).Inc()
}
```

- [ ] **Step 4: Wire the scheduler**

In `scheduler.go`:

- Add `maintenanceLockID int64 = 0x6f310013`.
- Add fields `rollup RollupRunner`, `retention RetentionRunner`.
- `SetMaintenance` assigns them.
- `Run` starts `go s.maintenanceLoop(ctx)` before `tickLoop`.
- `maintenanceLoop`:
  - If both runners are nil, return.
  - `s.runMaintenance(ctx, true, true)` once at startup.
  - `rollupTicker := time.NewTicker(s.cfg.RollupInterval)`; `retentionTicker := time.NewTicker(24 * time.Hour)`.
  - Loop: on a rollup tick call `s.runMaintenance(ctx, true, false)`; on the retention tick call `s.runMaintenance(ctx, false, true)`. Retention must not run on the rollup ticker.
  - Stop on `ctx.Done()`.
- `runMaintenance(ctx context.Context, doRollup, doRetention bool)`:
  1. Acquire a connection from the pool; `SELECT pg_try_advisory_lock($1)`; unlock deferred.
  2. If not locked, log debug and return.
  3. If `doRollup` and `s.rollup != nil`: run it, time it, call `ObserveRollup(result.Hours, result.Rows, d, err)`; on success call `SetRollupWatermark(result.To)`; on error log and continue to retention.
  4. If `doRetention` and `s.retention != nil`: run it, time it, call `ObserveRetention(d, err)`; on success call `AddRetentionRows("rollup", result.RollupRows)`, `AddRetentionRows("audit", result.AuditRows)`, `AddPartitionsDropped(result.PartitionsDropped)`, and `ObserveRetentionSkip` for each nonzero reason; log counts.
  5. Never return an error to the tick loop; log failures.
- `cmd/scheduler/main.go`:
  ```go
  rolluper := rollup.New(rt.Pool, sqlc.New(rt.Pool), rt.Logger, obs.Metrics, rt.Config.RollupMaxHoursPerRun)
  retainer := retention.New(rt.Pool, sqlc.New(rt.Pool), rt.Logger, retention.Config{
      RawRetentionDays: rt.Config.RawRetentionDays,
      RollupRetentionMonths: rt.Config.RollupRetentionMonths,
      AuditRetentionMonths: rt.Config.AuditRetentionMonths,
      MaxPartitionDrops: rt.Config.RetentionMaxPartitionDrops,
      DryRun: rt.Config.RetentionDryRun,
  }, rolluper)
  sched.SetMaintenance(rolluper, retainer)
  ```
  Add imports.

- [ ] **Step 5: Forward retention config in Compose**

Add these to the shared backend environment in both `compose.yaml` and
`deploy/production/compose.yaml`:

```yaml
O1_RAW_RETENTION_DAYS: ${O1_RAW_RETENTION_DAYS:-90}
O1_ROLLUP_RETENTION_MONTHS: ${O1_ROLLUP_RETENTION_MONTHS:-13}
O1_AUDIT_RETENTION_MONTHS: ${O1_AUDIT_RETENTION_MONTHS:-12}
O1_RETENTION_DRY_RUN: ${O1_RETENTION_DRY_RUN:-true}
O1_RETENTION_MAX_PARTITION_DROPS: ${O1_RETENTION_MAX_PARTITION_DROPS:-1}
O1_ROLLUP_INTERVAL: ${O1_ROLLUP_INTERVAL:-15m}
O1_ROLLUP_MAX_HOURS_PER_RUN: ${O1_ROLLUP_MAX_HOURS_PER_RUN:-24}
```

Add the same keys, commented and with defaults, to
`deploy/production/env.example` under a "Data lifecycle" section. Verify:

```bash
docker compose --env-file deploy/production/env.example \
  -f deploy/production/compose.yaml config | grep -E 'O1_(RAW|ROLLUP|RETENTION|AUDIT)_'
```

Expected: all seven keys appear in the scheduler/backend environment.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/scheduler ./internal/observability -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/scheduler backend/internal/observability backend/cmd/scheduler \
  compose.yaml deploy/production/compose.yaml deploy/production/env.example
git commit -m "feat: schedule rollups and retention"
```

---

### Task 7: Data lifecycle docs and final verification

**Files:**
- Modify: `README.md`
- Modify: `deploy/production/README.md`
- Modify: `deploy/CICD.md`

**Interfaces:**
- Consumes everything from Tasks 1–6.

- [ ] **Step 1: Document the data lifecycle**

In root `README.md`, add a "Data lifecycle" section:

- Raw probe results: at least 90 days, 90–120 because partitions are monthly.
- Hourly rollups: 13 months (minimum 4), UTC buckets, up/down counts and latency aggregates.
- Audit events: 12 months.
- Retention is dry-run by default; set `O1_RETENTION_DRY_RUN=false` to delete.
- At most one raw partition drops per run by default.
- Plan 12 is skipped: there are no backups; retention deletes the only raw copy; rollups are aggregates, not a backup.
- The env vars and defaults from the spec table.
- How to inspect the watermark and partitions:

```sql
SELECT max(bucket_start) FROM monitor_hourly_stats;
SELECT watermark FROM rollup_state;
SELECT relname FROM pg_class WHERE relname LIKE 'probe_results_%' AND relkind = 'r';
```

- [ ] **Step 2: Update the production runbook**

In `deploy/production/README.md`:

- Replace the "Backups are Plan 12" paragraph with the current state: no backups, retention deletes raw after 90+ days, rollups are aggregates.
- Add a "Retention" section: dry-run default, opt-in deletion, partition cap, how to preview, what is irreversibly lost.
- State that the scheduler runs maintenance after startup and that deploys do not delete data.

Add a **Rollout** subsection: deploy with the dry-run default, watch
`rollup_state.watermark` advance from the oldest raw hour, confirm the 90-day
page still matches, review the dry-run counts, then set
`O1_RETENTION_DRY_RUN=false` and expect at most one partition drop per day.

In `deploy/CICD.md`, add one line: deploys and rollbacks do not run retention; the scheduler does, and `O1_RETENTION_DRY_RUN` controls deletion.

- [ ] **Step 3: Run the complete verification**

```bash
cd backend
gofmt -l .
go vet ./...
golangci-lint run
go test -short ./...
go test -p 3 -count=1 ./...
cd ../web
npm test -- --run
```

Expected: all exit 0. The integration suite uses testcontainers and needs Docker.

Manual dry-run check on dev Compose:

```bash
docker compose up -d --build
# seed or use existing dev data, then run the scheduler with dry-run on
O1_RETENTION_DRY_RUN=true docker compose up -d scheduler
docker compose logs --tail=100 scheduler
```

Expected: logs show candidate/rollup/audit counts and no deletions.

- [ ] **Step 4: Commit**

```bash
git add README.md deploy/production/README.md deploy/CICD.md
git commit -m "docs: document rollups and data retention"
```

---

## Self-Review Checklist

- [ ] Spec coverage: schema/queries/config → Task 1; rollup engine → Task 2; raw partition retention → Task 3; rollup/audit pruning → Task 4; status page → Task 5; scheduling/lock/metrics → Task 6; docs/verification → Task 7.
- [ ] Review Focus tests: partition safety → Task 3; watermark continuity → Task 2; late rows → Task 2; deletion defaults/cap → Tasks 1/3/4; status parity → Task 5.
- [ ] Names/signatures match: `Rolluper.Run`, `Rolluper.RollupHour`, `Retainer.Run`, `Retainer.pruneRollups`, `Retainer.pruneAudit`, `rollup_state.watermark`, `monitor_hourly_stats`, `ListRollupDayStats`, `SetMaintenance`, `ObserveRollup`, `ObserveRetention`.
- [ ] No task depends on a later task without saying so; each ends with a passing test cycle and a commit.
- [ ] The plan adds no backups, no UI, no new service, and no change to the 1h/6h/24h/7d APIs.
