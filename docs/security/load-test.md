# Load Baseline

**Date:** 2026-11-18 · **Commit:** `2c41bb6` + the harness in this commit ·
**Scenario sources:** `deploy/loadtest/`

This is a measured floor, not a capacity claim. The stack ran on a Docker
Desktop VM (10 vCPU, 8 GB) rather than the 4 vCPU / 8 GB reference host, with
the API, web, and PostgreSQL containers sharing that VM **and** the k6
container. Read the relative result, not the absolute milliseconds.

## Dataset

| | |
|---|---|
| Tenants | 10 workspaces (`load-01` … `load-10`) |
| Monitors | 20 per workspace, 200 total, all paused with `next_check_at` 100 years out |
| Raw results | 2,016,000 rows (7 days at one result per minute) |
| Hourly rollups | 432,000 rows (90 days, 24 buckets per day) |
| Public pages | One published status page per workspace (`/status/load-01` …) |
| Auth | One `member` API key in `load-01` |

No scheduler or worker ran, so no probe traffic and no rollup work interfered.

## Configuration

The API ran with `deploy/loadtest/compose.override.yaml`: public limiter
12,000/min burst 2,000, API-key limiter 60,000/min burst 10,000. Everything
else was the shipped default, including `O1_DB_MAX_CONNS=10`.

## Results

### Public status page (`status.js`)

200 req/s for 5 minutes, 50 VUs preallocated (maximum 300).

| Metric | Value |
|---|---|
| Requests | 59,904 (199.7/s achieved) |
| Failed | 0.00% |
| p95 | 17.0 ms |
| p99 | 243.5 ms (threshold 600 ms) |
| Max | 1.71 s |
| Dropped iterations | 97 (0.16%) |

The response is a 90-day page for 20 monitors, roughly 87 KB each. This path
passes comfortably.

### Authenticated API (`api.js`)

100 iterations/s for 5 minutes; each iteration is one monitor list plus one
summary call, so ~200 requests/s.

| Metric | Value |
|---|---|
| Requests | 59,882 (199.6/s achieved; ~100 iterations/s, two requests each) |
| Failed | 0.00% |
| p95 | 15.6 ms (threshold 300 ms) |
| p99 | 92.1 ms (threshold 600 ms) |
| Max | 739 ms |
| Dropped iterations | 60 (0.1%) |

By call: monitor list p95 22.2 ms, summary p95 3.3 ms. Sequential latency with
no concurrency is 12 ms and 3 ms, so the median under load is unchanged.

**One honest note on variance.** An earlier run in the same session, same
configuration, breached the p99 threshold (p95 561 ms, p99 1.54 s in a
2-minute diagnostic). Re-running later with no configuration change passed
comfortably. The earlier failure was host variance on a shared laptop VM, not a
reproducible server limit, and it is recorded here rather than dropped.

### Limiter behaviour (`limits.js`)

30 seconds at 2 req/s against the shipped defaults (60/min, burst 20):
`rate_limited count>0` passed, so the endpoint returns `429` once the bucket is
empty.

## Resource use and first saturation signal

Ten-second `docker stats` samples during both runs:

| Container | Peak CPU | Average CPU | Peak RSS |
|---|---|---|---|
| api | 73% of one core | 12.7% | 45 MB |
| postgres | 500% (5 of 10 vCPU) | 53% | 260 MB |

PostgreSQL is the first component to approach saturation, at ~5 cores during
the API scenario. No threshold was breached at these rates, so the measured
limit lies above 200 requests/second. Beyond that, the database is what to
watch first, then API CPU; the connection pool (`O1_DB_MAX_CONNS`, default 10)
is a likely next lever but was not the cause of any reproducible failure here.

The earlier pool experiment that appeared to improve p95 was variance: a later
run at the default pool with no changes matched or beat it. Treat pool sizing
as an operator decision, not a measured requirement from this baseline.

## What was not measured

- Probe scheduling and worker throughput (would require outbound traffic to
  third-party targets; deliberately excluded).
- Rollup and retention passes.
- Write paths; the monitor-create limit is 10 per workspace per hour by design.
- TLS termination cost; k6 hit the API directly on the Compose network.

## Re-running

See `deploy/loadtest/README.md`. Record the host shape, the dataset counts, the
raised limits, and the first bottleneck with every new run. If a threshold
fails, record the failure and its cause; do not lower the threshold.
