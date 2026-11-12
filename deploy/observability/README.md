# Observability runbook

This directory holds the deploy-time pieces of the o1 self-monitoring bundle:

| File | Purpose |
|------|---------|
| `alloy.alloy` | Grafana Alloy scrape and remote-write configuration. |
| `prometheus-rules.yml` | Prometheus/Mimir ruler rules loaded into Grafana Cloud. |
| `grafana-dashboard.json` | Grafana dashboard JSON for import. |
| `README.md` | This runbook. |

Nothing here provisions a live Grafana Cloud or Sentry account. All endpoints,
users, and tokens are supplied through the environment.

## How metrics are exposed

Metrics are private by design:

- The API serves `/metrics` on `O1_API_HEALTH_ADDR` (`:8083`). Compose does not
  publish `:8083` to the host.
- The scheduler and worker serve `/metrics` on their health listeners
  (`:8081` and `:8082`). Compose publishes those only on `127.0.0.1`.
- Caddy proxies `/healthz` and `/readyz` to `api:8080` for load balancers and
  the external dead-man's switch. It responds `404` for `/metrics` and never
  proxies it.
- Alloy runs on the Compose network and scrapes the service names directly, so
  no metrics port needs to be public.

Labels are bounded. Raw paths, UUIDs, workspace or monitor IDs, URLs, query
strings, and email addresses never become metric labels.

## Grafana Cloud setup

1. Create a Grafana Cloud stack (the free tier is enough to start) and open
   **Connections → Prometheus**.
2. Copy the **remote write endpoint**, the numeric **user/instance ID**, and
   create an access policy token with `metrics:write` (and `metrics:read` if you
   want to query from the same token). The endpoint looks like:

   ```text
   https://prometheus-prod-XX.grafana.net/api/prom/push
   ```

3. Export the credentials (or put them in `.env` next to `compose.yaml`):

   ```sh
   export GRAFANA_REMOTE_WRITE_URL="https://prometheus-prod-XX.grafana.net/api/prom/push"
   export GRAFANA_REMOTE_WRITE_USER="<numeric-user-id>"
   export GRAFANA_REMOTE_WRITE_TOKEN="<access-policy-token>"
   export O1_ENV="production"
   ```

   Keep the token out of git and out of `alloy.alloy`.

## Start Alloy with the Compose profile

Alloy is optional and is not started by a normal `docker compose up`:

```sh
docker compose --profile observability up -d alloy
docker compose logs -f alloy
```

The compose service refuses to start when any of `GRAFANA_REMOTE_WRITE_URL`,
`GRAFANA_REMOTE_WRITE_USER`, or `GRAFANA_REMOTE_WRITE_TOKEN` is empty, and
prints the name of the missing variable. This prevents a silent remote-write
loop with empty credentials.

Alloy scrapes `api:8083`, `scheduler:8081`, and `worker:8082` every 15 seconds
with the job labels `o1-api`, `o1-scheduler`, and `o1-worker`, adds an `env`
external label from `O1_ENV`, and remote-writes to Grafana Cloud. The Alloy UI
listens on port `12345` inside the container but is not published; inspect the
logs for scrape and remote-write errors:

```sh
docker compose --profile observability logs -f alloy
```

Add a temporary `127.0.0.1:12345:12345` port mapping if you need the UI for
component-level debugging.

In Grafana **Explore**, query `up{job=~"o1-.*"}`. All three jobs should return
`1`.

To stop Alloy without stopping the application stack:

```sh
docker compose --profile observability stop alloy
```

## Logs

Alloy ships the stdout/stderr of `api`, `scheduler`, `worker`, and `web` to
Grafana Cloud Loki. It reads the Docker socket read-only and only collects
containers in the configured Compose project.

1. In Grafana Cloud open **Connections → Loki** and copy the push endpoint
   (`https://logs-prod-XX.grafana.net/loki/api/v1/push`) and the numeric user,
   then create an access policy token with `logs:write`.
2. Set the values next to the metrics credentials:

   ```sh
   export GRAFANA_LOKI_URL="https://logs-prod-XX.grafana.net/loki/api/v1/push"
   export GRAFANA_LOKI_USER="<numeric-user-id>"
   export GRAFANA_LOKI_TOKEN="<access-policy-token>"
   ```

3. Start or restart the profile:

   ```sh
   docker compose --profile observability up -d alloy
   ```

   All six Grafana values are required (three remote-write, three Loki).
   Alloy exits and names the missing variable instead of running degraded.

Labels are bounded to `env`, `service`, `container`, and `level`; application
JSON fields stay in the log body. Query in Grafana **Explore → Loki**:

```logql
{service="api"}
{service="api"} |= "ERROR"
{env="production"} | json
```

Logs survive a deploy because they live in Loki, not in a container that the
deploy replaces. The Compose deployment does not restart Alloy by itself:
re-run the profile command after a release that changes `alloy.alloy` or these
variables.

**Security:** `/var/run/docker.sock` is mounted read-only, but read access to
the Docker socket is effectively root-equivalent on the host. Alloy exposes no
port and runs no application code; keep the socket mount read-only and keep the
Compose-project filter.

## Validate the artifacts locally

Caddy (the Docker image is the fallback when `caddy` is not installed):

```sh
docker run --rm -v "$PWD/web/Caddyfile:/etc/caddy/Caddyfile:ro" \
  caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile
```

Compose (does not start containers or require Grafana credentials):

```sh
docker compose config
docker compose --profile observability config
```

The first command validates the default stack. The second also parses the
optional Alloy service.

Alert rules:

```sh
promtool check rules deploy/observability/prometheus-rules.yml
```

If `promtool` is not installed:

```sh
docker run --rm --entrypoint promtool \
  -v "$PWD/deploy/observability:/rules:ro" \
  prom/prometheus:latest check rules /rules/prometheus-rules.yml
```

Alloy:

```sh
docker run --rm \
  -v "$PWD/deploy/observability/alloy.alloy:/etc/alloy/config.alloy:ro" \
  grafana/alloy:v1.21.3 validate /etc/alloy/config.alloy
```

Dashboard JSON:

```sh
python3 -m json.tool deploy/observability/grafana-dashboard.json >/dev/null
```

## Import the dashboard

1. In Grafana, open **Dashboards → New → Import**.
2. Upload `grafana-dashboard.json` (or paste its contents).
3. Select the Grafana Cloud Prometheus data source when prompted.
4. Import. The dashboard UID is `o1-observability`.

Panels cover HTTP request rate, errors, and latency; queue depth and oldest job
age; probe results and duration; alert deliveries; open incidents; database
pool connections and saturation; scheduler ticks; and process memory/CPU.

## Import the alert rules

The rules file is a Prometheus/Mimir ruler file with three groups:
`o1-availability`, `o1-api`, and `o1-jobs`. Load it into Grafana Cloud with
`mimirtool`:

```sh
mimirtool rules load deploy/observability/prometheus-rules.yml \
  --address="https://prometheus-prod-XX.grafana.net" \
  --id="<tenant-id>" \
  --user="<numeric-user-id>" \
  --key="<access-policy-token>"
```

The address, tenant, and user values are on the same Grafana Cloud Prometheus
details page as the remote-write endpoint. `mimirtool` needs a token with
`rules:write` in addition to `metrics:write` (or an admin token).

### Alert rules

| Alert | Fires when | First checks |
|-------|------------|--------------|
| `O1InstanceDown` | `up == 0` for 2m | Process health, container restarts, scrape target address. |
| `O1MetricsMissing` | any of `o1-api`, `o1-scheduler`, `o1-worker` has no `up` for 10m | Alloy container health, that scrape target, credentials, remote-write errors. |
| `O1HighErrorRate` | 5xx ratio > 2% for 5m | API logs, Sentry, recent deploys, a failing route. |
| `O1HighLatency` | p95 > 1s for 10m | DB pool saturation, slow queries, probe volume, recent deploys. |
| `O1QueueBacklog` | available jobs > 100 for 10m | Worker health, probe throughput, `O1_WORKER_CONCURRENCY`. |
| `O1QueueOldestJob` | oldest runnable job > 5m for 10m | Stuck jobs, worker errors, database locks. |
| `O1JobsDiscarded` | discarded jobs > 0 for 10m | River job errors in logs and Sentry; do not clear blindly. |
| `O1ProbeFailureSpike` | down ratio > 20% for 10m with volume | Affected monitors, worker egress, DNS, target outages. |
| `O1AlertDeliveryFailures` | failed ratio > 10% for 10m with volume | SMTP, webhook/Slack/Telegram endpoints, channel config. |
| `O1DBPoolSaturation` | acquired/max > 0.9 for 10m | Long-running queries, pool size, database capacity. |
| `O1SchedulerStalled` | no `o1-scheduler` tick increase for 5m | Scheduler process, DB connectivity, tick errors. |

`O1MetricsMissing` is the safety net for a lost scrape agent or target: if
Alloy stops scraping, `up` disappears rather than going to zero, so an ordinary
instance-down rule cannot fire. It fires when any one of the three jobs is
absent, not only when all of them are.

## Sentry setup

### Backend

1. Create a Go project in Sentry and copy its DSN.
2. Set these on the API, scheduler, and worker:

   ```sh
   export O1_SENTRY_DSN="https://<key>@<org>.ingest.sentry.io/<project>"
   export O1_SENTRY_ENVIRONMENT="production"   # defaults to O1_ENV
   export O1_RELEASE="<git-sha-or-version>"    # defaults to dev
   ```

3. Recreate the services:

   ```sh
   docker compose up -d --force-recreate api scheduler worker
   ```

Sentry captures API panics, River job errors and panics, and scheduler tick
panics once per event. Performance tracing is disabled in Sentry because OTel
owns traces. Before-send redaction strips request bodies, headers, cookies,
query strings, and user email; breadcrumb `url`, `from`, and `to` values lose
their query strings and fragments as well. The browser SDK also disables URL
query-parameter collection.

### Frontend

The frontend DSN and release are baked into the static bundle at build time:

```sh
export VITE_SENTRY_DSN="https://<key>@<org>.ingest.sentry.io/<project>"
export VITE_RELEASE="<git-sha-or-version>"
docker compose build web
docker compose up -d web
```

`compose.yaml` passes both values as web build args, and CI forwards them the
same way. Leaving `VITE_SENTRY_DSN` empty disables Sentry in the browser. The
React error boundary reports render errors; expected API 4xx responses are not
sent.

## Tracing (optional)

OTLP tracing is enabled only when an endpoint is configured:

```sh
export O1_OTEL_EXPORTER_OTLP_ENDPOINT="https://otlp-gateway-prod-XX.grafana.net/otlp"
export O1_OTEL_EXPORTER_OTLP_HEADERS="Authorization=Basic <base64>"
export O1_OTEL_SAMPLE_RATIO="0.05"
```

`O1_OTEL_EXPORTER_OTLP_HEADERS` is a comma-separated `key=value` list and is
never logged. A full URL endpoint keeps its scheme, host, base path, and query
string, and the traces signal path `/v1/traces` is appended when missing (for
example `https://otlp-gateway-prod-XX.grafana.net/otlp` exports to
`/otlp/v1/traces`). A bare `host:port` endpoint is sent over plaintext HTTP. An
empty endpoint disables tracing with no exporter calls. If you prefer to route
traces through Alloy instead, use the commented OTLP receiver/exporter example
at the bottom of `alloy.alloy`.

## Dead-man's switch

Prometheus alerting cannot report that the whole host, network, or Alloy/Grafana
path is gone. Use an external check:

1. Create a free healthchecks.io check.
2. Set the period to **1 minute** and the grace time to **2 minutes** (alert
   after two missed pings).
3. Point it at the public API health endpoint:

   ```text
   https://<host>/healthz
   ```

Caddy proxies `/healthz` to `api:8080`, so this checks the API process rather
than the SPA. `/readyz` additionally checks database readiness and is intended
for load balancers. Do not point the switch at `/metrics`; it is private and
returns 404 through Caddy.

## Retention and budget

The bundle targets the Grafana Cloud free tier. At the time of writing that
includes roughly 10,000 active series, 14-day retention, and separate log/trace
allowances; verify the current limits before relying on them.

If the stack exceeds the series budget:

- Raise the Alloy scrape interval from `15s` to `30s` or `60s` in
  `alloy.alloy`.
- Drop unused metric families in Alloy with a `prometheus.relabel` rule before
  remote write (for example the default Go runtime metrics if only the `o1_*`
  metrics are needed).
- Reduce retention in the Grafana Cloud stack rather than paying for unused
  history when the data is only used for incident response.
- Keep alert rules and the dead-man's switch even if dashboards are trimmed;
  they are the part that pages a human.

Alloy itself keeps only a small write-ahead queue under `/var/lib/alloy/data`.
There is no log shipping or self-hosted observability stack in this bundle.

## Troubleshooting

- **Alloy exits immediately:** read the printed missing variable; the Compose
  profile requires all three `GRAFANA_REMOTE_WRITE_*` values and all three
  `GRAFANA_LOKI_*` values.
- **Alloy runs but no series arrive:** inspect `docker compose logs alloy` for
  remote-write 401/403 errors, confirm the token has `metrics:write`, and query
  `up{job=~"o1-.*"}` in Grafana Explore.
- **Alloy runs but no logs arrive:** confirm the Loki token has `logs:write`,
  the endpoint ends in `/loki/api/v1/push`, the Docker socket is mounted, and
  `O1_COMPOSE_PROJECT` matches the running Compose project name. Query
  `{env="production"}` in Grafana Explore → Loki.
- **Only some jobs are missing:** check which container is running. API metrics
  are on `:8083`; scheduler and worker metrics are on `:8081` and `:8082`.
- **`/metrics` returns 404 publicly:** expected. Query the internal listeners
  from inside the Compose network or use Alloy.
- **Dashboards empty after import:** select the Prometheus data source in the
  dashboard's `Data source` picker.
