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

200 req/s for 5 minutes, 200 VUs preallocated.

| Metric | Value |
|---|---|
| Requests | 59,382 (197.9/s achieved) |
| Failed | 0.00% |
| p95 | 18.8 ms |
| p99 | within the 600 ms threshold |
| Dropped iterations | 619 (1.0%) |
| Data received | 5.2 GB |

The response is a 90-day page for 20 monitors, roughly 87 KB each. This path
passes comfortably.

### Authenticated API (`api.js`)

100 iterations/s for 5 minutes; each iteration is one monitor list plus one
summary call, so ~200 requests/s.

| Metric | Value |
|---|---|
| Requests | 59,106 |
| Failed | 0.00% |
| p95 | 218 ms (threshold 300 ms, passed) |
| p99 | breached the 600 ms threshold |
| Dropped iterations | 448 (1.5%) |

A shorter 2-minute diagnostic on the same configuration separated the two
calls and made the tail worse:

| Call | p95 | p99 |
|---|---|---|
| Monitor list | 687 ms | — |
| Monitor summary | 455 ms | — |
| Combined | 561 ms | 1.54 s |

Sequential latency for the same calls, no concurrency: 12 ms for the list,
3 ms for the summary. The median under load stays at 6–8 ms. The tail is
queueing, not query cost.

### Limiter behaviour (`limits.js`)

30 seconds at 2 req/s against the shipped defaults (60/min, burst 20):
`rate_limited count>0` passed, so the endpoint returns `429` once the bucket is
empty.

## First bottleneck

**The database connection pool.** `O1_DB_MAX_CONNS` defaults to 10. A
simultaneous 5-minute run at the same rate with `O1_DB_MAX_CONNS=25` moved the
combined p95 from 561 ms to **9.8 ms** and p99 to **13 ms**, with no other
change. Every authenticated request performs the key lookup, the workspace
authorization query, and the page query, so a 10-connection pool queues long
before Postgres or the API CPU is busy (API CPU stayed under 1% when idle; the
containers are small).

Recommended follow-up: size `O1_DB_MAX_CONNS` for the expected concurrency and
the managed database's connection limit. The shipped default stays conservative
for a small team; the load override does not change it, so the numbers above
are the default-config numbers.

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
