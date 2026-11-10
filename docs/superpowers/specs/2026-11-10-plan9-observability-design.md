# Plan 9 — Observability and Self-Monitoring Design

**Status:** Draft for approval
**Date:** 2026-11-10
**Depends on:** Plans 1–8, all merged into `main`.
**Out of scope:** log shipping/Loki, self-hosted Prometheus/Grafana, live cloud
account provisioning, alert routing/paging integrations, on-call schedules,
SLO error budgets, DB-query tracing, profiling, and load testing.

## 1. Intent

Plan 9 makes the running system observable and able to alert its operator. It
adds Prometheus metrics on private endpoints, OpenTelemetry tracing with
trace-correlated logs, Sentry error capture for the Go binaries and React app,
and a deployable observability bundle: Grafana Alloy scrape/remote-write
config, dashboard JSON, Prometheus alert rules, and a dead-man's-switch
runbook.

The application stays self-contained: no metrics or traces are public, no
hosted account is provisioned by this plan, and every integration is disabled
when its configuration is empty.

## 2. Current state

- All three binaries share `app.Start`, structured `slog` logging, and a
  health-server helper with `/healthz` and `/readyz`. The API serves health
  checks on its public listener; scheduler and worker use `:8081`/`:8082`.
- Logs include `request_id`, method, path, status, and duration, but no trace
  IDs and no per-route metrics.
- There are no Prometheus, Sentry, or OTel dependencies in use. OTel packages
  are present only indirectly through River.
- The API router wraps handlers with request IDs, security headers, CSRF, body
  limits, panic recovery, and request logging.
- Probe and alert workers own their job execution and return errors to River.
  The scheduler runs ticks that enqueue probes.
- River stores jobs in `river_job`; the incidents table has `resolved_at` and
  status indexes; `pgxpool.Stat()` exposes pool counters.
- The React app has no error boundary or Sentry initialization.
- There are no deploy/observability artifacts.

## 3. Goals, non-goals, anti-goals

**Goals**

1. Expose Prometheus metrics for HTTP traffic, database pool health, River
   queue depth/age, probe results/duration, alert delivery results/duration,
   open incidents, and scheduler activity.
2. Keep metrics endpoints private: API metrics on a new internal health
   listener, scheduler/worker metrics on their existing health listeners.
3. Emit OTLP traces for API requests, River jobs, and probe/alert work when an
   OTLP endpoint is configured; correlate logs with `trace_id`/`span_id`.
4. Capture backend panics/worker errors and frontend errors in Sentry with
   redacted request data.
5. Ship a Grafana Alloy config that scrapes the three internal endpoints and
   remote-writes to Grafana Cloud, plus dashboard JSON, alert rules, and a
   dead-man's-switch runbook.
6. Keep cardinality bounded: no workspace IDs, monitor IDs, URLs, user emails,
   or raw paths in metric labels.
7. Every integration is optional: empty DSN/OTLP config disables it without
   affecting startup or existing behavior.

**Non-goals**

- Log aggregation, Loki, or shipping stdout logs.
- Self-hosting Prometheus/Grafana/Alertmanager.
- Paging integrations, on-call rotations, or SLO/error-budget policy.
- DB-query tracing, continuous profiling, or eBPF.
- Synthetic checks, real-user monitoring, or session replay.
- Live Grafana/Sentry account setup or secret distribution.
- Load/performance testing (a later plan).

**Anti-goals**

- A public `/metrics` endpoint reachable through Caddy.
- Metric labels containing tenant identifiers, URLs, query strings, emails, or
  other unbounded/PII values.
- Sentry payloads containing cookies, Authorization headers, request bodies,
  query strings, passwords, TOTP/recovery codes, or API keys.
- Observability configuration that can prevent the API, workers, or scheduler
  from starting when no endpoint is configured.
- Blocking request handling on trace export or metrics collection.
- Duplicating trace sampling through both Sentry performance and OTel.

## 4. Approaches

**A. Prometheus exposition on private health ports, scraped by Grafana Alloy
with remote_write to Grafana Cloud; OTLP traces direct; Sentry direct
(chosen).**
Each binary exposes `/metrics` on its internal health listener. One Alloy
sidecar scrapes all three and remote-writes to Grafana Cloud. Traces go
directly from each binary over OTLP/HTTP. Sentry captures errors directly.

*Why:* it satisfies the spec's Prometheus requirement, keeps metrics off the
public listener, uses one standard sidecar for scraping/remote-write, and lets
each process export traces without local storage. The cost is one Alloy
container and a scrape configuration.

**B. Push OTel metrics directly over OTLP.**
No scrape agent, but metrics are no longer Prometheus exposition and the
spec's metrics contract becomes approximate. Rejected.

**C. Self-host Prometheus/Grafana in Compose.**
Full control but adds storage, retention, and alerting operations on the same
single VPS, against the spec's hosted-free-tier guidance. Rejected.

**Decision:** A.

## 5. Constraints

- Metric names use the `o1_` prefix; labels are bounded to method, route
  pattern, status class/code, queue, state, channel kind, result, and similar
  enums.
- The HTTP `route` label is the `http.ServeMux` pattern (for example
  `GET /api/v1/workspaces/{workspaceID}/monitors/{monitorID}`) or
  `unmatched`; health and metrics endpoints are excluded. Raw paths never
  appear.
- `/metrics` is unavailable on the public API listener and on the public
  Caddy site. API metrics use a new internal listener (`O1_API_HEALTH_ADDR`,
  default `:8083`); scheduler/worker metrics use their existing health
  listeners. Scheduler/worker health ports are published to loopback only in
  Compose, and the API metrics port is not published.
- Caddy gains explicit `/healthz` and `/readyz` reverse-proxy routes to
  `api:8080` so the external dead-man's switch reaches the API rather than the
  SPA shell. `/metrics` is never routed by Caddy.
- An optional Alloy service runs under the Compose `observability` profile and
  on the Compose network, where it can reach `api:8083`, `scheduler:8081`, and
  `worker:8082`. Normal `docker compose up` and CI do not start it; the scrape
  config's remote-write credentials are environment-provided.
- Default Go/process collectors and an `o1_build_info` gauge are registered.
- Queue and incident gauges refresh in the background (15s for queue, 30s for
  incidents) rather than querying per scrape. Refresh errors are logged at
  warn level and keep the last values.
- `o1_queue_jobs{queue,state}` includes every non-terminal state, including
  `discarded` as a failure signal, but backlog/oldest-age alerts count only
  runnable states (`available`, `running`, `retryable`, `pending`) so one
  exhausted retry does not page for days. A separate discarded-jobs alert
  covers terminal failures.
- OTLP tracing is enabled only when `O1_OTEL_EXPORTER_OTLP_ENDPOINT` is set.
  Sampling ratio defaults to `0.05`, clamped to `[0, 1]`; parent-based sampler.
  Export failures never fail requests or jobs.
- Logs add `trace_id` and `span_id` when the context carries a valid span.
- Sentry is enabled only when `O1_SENTRY_DSN` (backend) or `VITE_SENTRY_DSN`
  (frontend) is set. Frontend DSN/release are Docker build args baked into the
  static bundle and forwarded through Compose/CI; performance/tracing is
  disabled in Sentry and OTel owns traces.
- Sentry `BeforeSend` removes request headers, cookies, body, query string, and
  user email; it also drops expected control-flow errors (`context.Canceled`,
  `http.ErrServerClosed`). Worker errors are captured once by the River
  middleware, not again by each worker.
- Dashboard/alert artifacts assume Grafana Cloud free-tier limits and document
  how to reduce retention before paying.
- No secrets are committed; OTLP auth headers and DSNs come from environment
  or deploy secrets.
- UI copy is English. Every task ends with formatting, lint/typecheck where
  applicable, tests, and a commit.

## 6. Architecture and configuration

New package `internal/observability`:

- `metrics.go` — registry, metric definitions, HTTP middleware, pool/queue/
  incident collectors, nil-safe helpers.
- `tracing.go` — OTLP setup, resource attributes, sampler, shutdown.
- `sentry.go` — init, redaction, capture helpers, River middleware.
- `river.go` — job metrics + trace span + Sentry middleware.
- `server.go` — metrics handler and `app.StartHealthServer` integration.
- Tests alongside.

Startup:

- `app.Start` continues to load config, logger, pool, and migrations.
- Each `cmd/*/main.go` calls `observability.New(ctx, rt.Config, rt.Logger,
  serviceName)` after `app.Start` and defers `Close`. It returns a `Provider`
  with `Metrics`, `Tracer`, `SentryEnabled`, and a shutdown function.
- The API passes metrics/tracing to `api.NewRouter` and starts an internal
  health server on `O1_API_HEALTH_ADDR` with `/healthz`, `/readyz`, `/metrics`.
  The existing public `/healthz` and `/readyz` stay unchanged.
- Scheduler/worker pass `Metrics` to their jobs and start their existing health
  server with `/metrics` added.
- `StartHealthServer` takes an optional `*prometheus.Registry` or
  `http.Handler`; nil means no `/metrics` route.

Configuration additions:

| Variable | Default | Meaning |
|----------|---------|---------|
| `O1_API_HEALTH_ADDR` | `:8083` | Internal API health/metrics listener |
| `O1_OTEL_EXPORTER_OTLP_ENDPOINT` | empty | OTLP/HTTP traces endpoint; empty disables |
| `O1_OTEL_EXPORTER_OTLP_HEADERS` | empty | Comma-separated `key=value` auth headers, never logged |
| `O1_OTEL_SAMPLE_RATIO` | `0.05` | Trace sampler ratio, clamped 0–1 |
| `O1_SENTRY_DSN` | empty | Backend Sentry DSN; empty disables |
| `O1_SENTRY_ENVIRONMENT` | `O1_ENV` | Sentry environment |
| `O1_RELEASE` | `dev` | Service version for OTel/Sentry/build info |
| `VITE_SENTRY_DSN` | empty | Frontend build-time Sentry DSN |
| `VITE_RELEASE` | `dev` | Frontend release |

`compose.yaml` forwards the backend variables into containers, publishes the
scheduler/worker health ports on `127.0.0.1` only, and passes the Vite variables
as web build args. The API health port is not published to the host. The deploy
bundle documents Grafana Cloud endpoints/headers and Sentry project setup.

## 7. Metrics catalog

All metrics use the `o1_` prefix.

**HTTP**

- `o1_http_requests_total{method,route,status}` counter.
- `o1_http_request_duration_seconds{method,route}` histogram, buckets
  `0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10`.
- `o1_http_in_flight_requests` gauge.
- `o1_http_response_bytes{method,route}` histogram? Not required; omit.

**Database pool**

- `o1_db_pool_connections{state}` gauge with `state` in
  `acquired|idle|total|max`.
- `o1_db_pool_acquires_total`, `o1_db_pool_acquire_duration_seconds_total`,
  `o1_db_pool_empty_acquires_total`, `o1_db_pool_canceled_acquires_total`
  counters from `pgxpool.Stat()`.

**Queue**

- `o1_queue_jobs{queue,state}` gauge for non-terminal states
  (`available`, `running`, `retryable`, `scheduled`, `pending`, `discarded`).
- `o1_queue_oldest_job_age_seconds{queue}` gauge for runnable states only
  (`available`, `running`, `retryable`, `pending`), so discarded/scheduled rows
  do not trigger backlog pages.

**Probes**

- `o1_probe_results_total{result}` counter with `up|down`.
- `o1_probe_duration_seconds{result}` histogram, buckets
  `0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10,30`.
- `o1_probe_skipped_total{reason}` counter with bounded reasons
  (`replay`, `paused`, `suspended`, `missing`).

**Alert deliveries**

- `o1_alert_deliveries_total{channel_kind,status}` counter with
  `channel_kind` in `email|webhook|slack|telegram` and
  `status` in `sent|failed|skipped`.
- `o1_alert_delivery_duration_seconds{channel_kind}` histogram, buckets
  `0.05,0.1,0.25,0.5,1,2.5,5,10,30,60`.

**Incidents**

- `o1_incidents_open` gauge refreshed every 30 seconds.

**Scheduler**

- `o1_scheduler_ticks_total` counter.
- `o1_scheduler_enqueued_total` counter.
- `o1_scheduler_tick_duration_seconds` histogram, buckets
  `0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5`.

**Build/process**

- `o1_build_info{service,version}` gauge set to 1.
- Default Go and process collectors.

Queue collector query:

```sql
SELECT queue, state, count(*)
FROM river_job
WHERE state NOT IN ('completed', 'cancelled')
GROUP BY queue, state;

SELECT queue, min(created_at)
FROM river_job
WHERE state IN ('available', 'running', 'retryable', 'pending')
GROUP BY queue;
```

Open incidents: `SELECT count(*) FROM incidents WHERE resolved_at IS NULL`.

## 8. Tracing

- OTel SDK with OTLP/HTTP exporter (`otlptracehttp`), batch processor,
  resource `service.name` (`o1-api`/`o1-worker`/`o1-scheduler`),
  `service.version` from `O1_RELEASE`, deployment environment from `O1_ENV`.
- Parent-based trace-ID-ratio sampler with `O1_OTEL_SAMPLE_RATIO`.
- API: `otelhttp.NewHandler` around the router with filters excluding
  `/healthz`, `/readyz`, `/metrics`; span names are `METHOD route` using the
  same route pattern as metrics. No query strings or bodies are recorded.
- River: middleware starts a span `river.<kind>` with `queue`, `attempt`, and
  `job_id`-free attributes; records the error and sets status on failure.
- Probe worker adds `probe.result` (`up|down`) and `probe.duration_ms`.
- Alert worker adds `alert.channel_kind` and `alert.status`.
- Scheduler tick span `scheduler.tick` with enqueued count.
- Logs: `logging.New` wraps its handler with a trace-context handler that adds
  `trace_id`/`span_id` when valid. Existing log fields stay unchanged.
- Shutdown flushes with a 5-second timeout; failures are logged, not fatal.

## 9. Sentry

Backend:

- `sentry.Init` when a DSN is set; `AttachStacktrace`, environment, release,
  `TracesSampleRate: 0`.
- `BeforeSend` clears `Request.Data`, `Request.Headers`, `Request.Cookies`,
  `Request.QueryString`, and `User.Email`; keeps method and route if present.
  Drops `context.Canceled` and `http.ErrServerClosed`.
- Existing panic recovery calls `observability.CaptureException` before
  writing the 500 response; the recover middleware remains the only API panic
  boundary.
- River's `ErrorHandler` is wired to a Sentry-aware handler that captures
  returned errors **and** `HandlePanic` job panics once, with job kind/queue
  tags and no payload args.
- The scheduler wraps each tick in a recover boundary that captures the panic,
  logs it, and continues with the next tick instead of crashing the process.
- `log.Fatal`/startup errors are not Sentry events (process not running).

Frontend:

- Add `@sentry/react`; initialize in `main.tsx` when `VITE_SENTRY_DSN` is set,
  with `environment: import.meta.env.MODE`, `release: VITE_RELEASE`,
  `tracesSampleRate: 0`, and a `beforeSend` that strips request data and URL
  query strings.
- Wrap the router in `Sentry.ErrorBoundary` with a simple fallback; existing
  route error handling stays.
- Ignore expected API problems (4xx) already rendered by the app.

## 10. Self-monitoring artifacts

Add `deploy/observability/`:

- `alloy.alloy` — scrape `api:8083`, `scheduler:8081`, `worker:8082` every
  15s, add `env`/`service` labels, remote_write to Grafana Cloud. Include a
  commented example for OTLP trace forwarding if direct export is undesirable.
  Run Alloy as an optional Compose service under the `observability` profile;
  it joins the Compose network, so it reaches the unpublised ports. Remote-write
  URL/basic-auth come from environment/secret files and are not committed.
- `prometheus-rules.yml` — a Prometheus/Mimir ruler file (validated with
  `promtool check rules`, loaded into Grafana Cloud with `mimirtool`), with:
  - `O1InstanceDown` (`up == 0` for 2m) and `O1MetricsMissing`
    (`absent(up{job=~"o1-.*"})` for 10m) so losing the scrape agent is visible.
  - `O1HighErrorRate` (5xx ratio > 2% for 5m).
  - `O1HighLatency` (p95 > 1s for 10m).
  - `O1QueueBacklog` (`o1_queue_jobs{state="available"} > 100` for 10m) and
    `O1QueueOldestJob` (> 5m for 10m, runnable states only).
  - `O1JobsDiscarded` (`o1_queue_jobs{state="discarded"} > 0` for 10m).
  - `O1ProbeFailureSpike` (down ratio > 20% for 10m, excluding low-volume).
  - `O1AlertDeliveryFailures` (failed ratio > 10% for 10m).
  - `O1DBPoolSaturation` (acquired/max > 0.9 for 10m).
  - `O1SchedulerStalled` (no tick increase for 5m).
- `grafana-dashboard.json` — overview panels for request rate/errors/latency,
  queue depth/age, probe results/duration, alert deliveries, incidents, DB
  pool, scheduler ticks, process memory/CPU.
- `README.md` — Grafana Cloud setup, Alloy deployment with the Compose
  `observability` profile, dashboard import, alert-rule import with `mimirtool`,
  retention/budget guidance, Sentry project setup (backend env and frontend
  build args), and the external dead-man's switch: a free healthchecks.io check
  pinging `https://<host>/healthz` every minute, alerting after two misses. The
  switch is intentionally outside the monitored system; `/healthz` is routed
  by Caddy to the API, while `/readyz` reflects database readiness for load
  balancers.

No live account, token, or dashboard is provisioned by the plan.

## 11. Testing

**Backend**

- Metrics: registry exposition contains every catalog name; HTTP middleware
  records method/route/status and excludes health/metrics; raw paths and query
  strings never appear as labels; unmatched requests use `unmatched`.
- Route-pattern resolution: parameterized routes produce the template, not the
  UUID.
- Pool collector: real `pgxpool` stats expose acquired/idle/total/max.
- Queue collector: insert River jobs in multiple queues/states, refresh, assert
  counts and oldest age; terminal jobs are excluded; refresh failure keeps
  last values.
- Incident gauge: open/acknowledged counted, resolved excluded.
- Probe/alert/scheduler instrumentation: workers and scheduler increment the
  expected counters/histograms; nil metrics are safe.
- Tracing: empty endpoint is a no-op; with an in-memory/test exporter, API and
  River spans exist with route/kind attributes and errors set; sampling ratio
  is clamped; shutdown flushes; logs include trace/span IDs when a span is
  active and omit them otherwise.
- Sentry: empty DSN disables capture; `BeforeSend` removes headers/cookies/
  body/query/email and drops canceled errors; panic recovery captures once;
  River middleware captures job errors once.
- Health server: `/metrics` is served only when a registry is passed; API
  public listener does not expose it; internal listener serves health+metrics.
- Caddy config check: `/healthz` and `/readyz` route to the API, `/metrics`
  does not; Compose does not publish the API metrics port and binds
  scheduler/worker health ports to loopback.
- Alert artifacts: `promtool check rules deploy/observability/prometheus-rules.yml`
  succeeds (documented; run in CI if promtool is available), and the discarded/
  metrics-missing alerts are present.
- Frontend build args: a build with `VITE_SENTRY_DSN` set embeds initialization;
  a build without it does not; Compose forwards the args.
- Existing suites stay green.

**Web**

- `@sentry/react` init is skipped without DSN and called with the expected
  environment/release/sample settings with a DSN.
- `beforeSend` strips request data and URL query.
- ErrorBoundary fallback renders on a thrown child.
- Existing router/auth tests stay green.

**E2E**

- Existing smoke flow stays green; optionally assert through `compose exec`
  that the worker/scheduler internal `/metrics` contains `o1_queue_jobs` and
  that the API internal endpoint contains `o1_http_requests_total` after the
  UI flow. Public UI must not expose `/metrics`.

## 12. Acceptance

1. Scraping the three internal endpoints yields the catalog metrics with
   bounded labels and no tenant/PII values.
2. The public API listener and Caddy site do not expose `/metrics`; Caddy routes
   `/healthz` and `/readyz` to the API for the external switch.
3. A configured OTLP endpoint produces API/River/probe/alert spans; an empty
   endpoint leaves the app fully functional with no export attempts.
4. Logs carry trace/span IDs when tracing is active.
5. A configured Sentry DSN captures backend panics (API, River job panics, and
   scheduler ticks) and frontend errors without headers, cookies, bodies, query
   strings, or emails.
6. The Alloy config scrapes all three services and remote-writes to Grafana
   Cloud; the dashboard and Prometheus-rules file import cleanly.
7. The dead-man's-switch runbook pings the public `/healthz` (API, not the SPA)
   and alerts after two misses; `O1MetricsMissing` covers a lost scrape agent.
8. Existing behavior, routes, auth, and tests are unchanged with all
   observability env vars empty.

## 13. Calls made by the builder

- Prometheus exposition on private listeners + Grafana Alloy scrape/remote_write
  (approach A); traces via OTLP direct; Sentry direct.
- API gets a separate internal health/metrics listener (`:8083`); scheduler
  and worker reuse `:8081`/`:8082`, published on loopback only. Caddy routes
  `/healthz`/`/readyz` to the API for the external switch.
- Alloy is optional via the Compose `observability` profile; no credentials are
  required for normal startup or CI.
- Queue and incident gauges are background-refreshed; backlog/oldest-age counts
  only runnable states, with a separate discarded-jobs alert.
- Sentry performance is disabled; OTel owns tracing. Sentry captures API
  panics, River returned errors and job panics, and scheduler tick panics.
- Alert rules use the Prometheus/Mimir ruler format.
- Frontend Sentry DSN/release are build args, forwarded by Compose/CI.
- No log shipping, self-hosted observability stack, or live account
  provisioning.
- Dashboards/alert rules/runbook are committed as config artifacts.
- `o1_` metric prefix and the catalog names above are fixed.
- `nil` metrics/tracing/sentry are safe no-ops.

## 14. Left to the builder

- Exact metric helper function names beyond the catalog.
- Prometheus collector structure and whether queue refresh runs in the worker,
  scheduler, or both (it must run in exactly one place per environment).
- Sentry SDK wiring details and River middleware ordering.
- Dashboard panel layout and alert thresholds within the documented intent.
- Test exporter choice for OTel traces.
