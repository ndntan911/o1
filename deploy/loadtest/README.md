# Load Baseline Harness

Three k6 scenarios measure the read paths against a **local** Compose stack. Do
not point them at `o1.work.gd`.

## Reference workload

| | |
|---|---|
| Tenants | 10 workspaces (`load-01` … `load-10`) |
| Monitors | 20 per workspace, 200 total, all paused |
| Raw history | 7 days at one result per minute: 2,016,000 rows |
| Rollups | 90 days at one bucket per hour: 432,000 rows |
| Host | 4 vCPU / 8 GB Linux VM, PostgreSQL 16 in Compose |

The generated monitors are paused and parked 100 years out, so the scheduler
cannot turn the fixture into outbound traffic. The stack therefore runs
`api` and `web` only.

## Setup

```sh
# 1. Start the API with the raised limiters. No scheduler or worker.
docker compose -f compose.yaml -f deploy/loadtest/compose.override.yaml up -d postgres api web

# 2. Build the dataset and print the one load API key.
cd backend
O1_ENV=development \
O1_DATABASE_URL='postgres://o1:o1@localhost:5432/o1?sslmode=disable' \
O1_SECRET_KEY='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=' \
O1_EMAIL_FROM='o1@localhost' \
go run ./cmd/loadseed -tenants 10 -monitors 20
```

Copy the `api key` line. The workspace ID comes from the database:

```sh
docker exec o1-postgres-1 psql -U o1 -d o1 -tAc "SELECT id FROM workspaces WHERE slug = 'load-01'"
```

## Run

k6 runs from Docker so no local install is needed; `host.docker.internal` reaches
the published API port.

```sh
docker run --rm -v "$PWD/deploy/loadtest:/scripts:ro" \
  -e BASE_URL=http://host.docker.internal:8080 \
  grafana/k6 run /scripts/status.js
```

```sh
docker run --rm -v "$PWD/deploy/loadtest:/scripts:ro" \
  -e BASE_URL=http://host.docker.internal:8080 \
  -e WORKSPACE_ID=<uuid> -e API_KEY=o1_... \
  grafana/k6 run /scripts/api.js
```

For the limiter behaviour, recreate the API **without** the override so the
shipped defaults apply, then:

```sh
docker run --rm -v "$PWD/deploy/loadtest:/scripts:ro" \
  -e BASE_URL=http://host.docker.internal:8080 \
  grafana/k6 run /scripts/limits.js
```

## Thresholds

| Scenario | Rate | Threshold |
|---|---|---|
| `status.js` | 200 req/s, 5 min | p95 < 300 ms, p99 < 600 ms, failures < 0.1% |
| `api.js` | 100 req/s, 5 min | p95 < 300 ms, p99 < 600 ms, failures < 0.1% |
| `limits.js` | 2 req/s, 30 s | at least one 429 at the shipped limits |

Record the result, the host shape, and the first bottleneck in
`docs/security/load-test.md`. If a threshold fails, record the failure and the
cause; do not lower the threshold.
