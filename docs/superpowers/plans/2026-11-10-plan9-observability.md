# Plan 9 — Observability and Self-Monitoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Prometheus metrics on private listeners, OTLP tracing with trace-correlated logs, Sentry error capture for Go and React, and a Grafana Cloud/Alloy dashboard, alert-rule, and dead-man's-switch bundle.

**Architecture:** A new `internal/observability` provider owns a Prometheus registry, OTLP tracer, and Sentry client. The API exposes metrics on a new internal listener; scheduler/worker expose them on their health listeners. An optional Compose `observability` profile runs Grafana Alloy to scrape and remote-write; Caddy routes public health checks to the API.

**Tech Stack:** Go 1.26, PostgreSQL 16, `prometheus/client_golang`, OpenTelemetry Go SDK + OTLP/HTTP, `getsentry/sentry-go`, River v0.51, React 19, `@sentry/react`, Grafana Alloy, Grafana Cloud, Vitest/MSW, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-10-plan9-observability-design.md`

## Global Constraints

- Plan 9 scope only. No log shipping/Loki, self-hosted Prometheus/Grafana, live cloud/Grafana provisioning, paging/on-call, SLOs, DB-query tracing, profiling, RUM/session replay, or load testing.
- Metric names use the `o1_` prefix and the catalog in the spec; labels stay bounded (method, route pattern, status, queue, state, channel kind, result) and never contain tenant IDs, URLs, query strings, emails, or raw paths.
- HTTP `route` label is the `http.ServeMux` pattern or `unmatched`; health/metrics endpoints are excluded.
- `/metrics` is never public: API metrics on `O1_API_HEALTH_ADDR` (`:8083`, unpublished); scheduler/worker metrics on existing health ports, published on loopback only. Caddy proxies `/healthz` and `/readyz` to `api:8080` and never `/metrics`.
- Alloy runs only under the Compose `observability` profile on the Compose network; normal startup and CI need no Grafana credentials.
- Queue/incident gauges refresh in the background (15s/30s), not per scrape. `o1_queue_jobs` includes all non-terminal states; backlog/oldest-age counts only `available`, `running`, `retryable`, `pending`.
- OTLP tracing is enabled only with `O1_OTEL_EXPORTER_OTLP_ENDPOINT`; default sample ratio `0.05`, clamped 0–1, parent-based. Export failures never fail requests/jobs. Sentry performance is disabled; OTel owns traces.
- Logs add `trace_id`/`span_id` only when a valid span is in context.
- Sentry is enabled only with `O1_SENTRY_DSN` (backend) or `VITE_SENTRY_DSN` (frontend build arg). `BeforeSend` strips headers, cookies, body, query strings, and email; drops `context.Canceled` and `http.ErrServerClosed`.
- Sentry captures API panics, River returned errors and job panics, scheduler tick panics, and frontend errors.
- Frontend DSN/release are Vite build args forwarded by Compose/CI.
- Empty observability configuration is a safe no-op; existing behavior and tests must not change.
- UI copy is English. Every task ends with formatting, lint/typecheck, tests, and a commit.

## Review Focus

1. **Metrics privacy and cardinality.** Scraping must never expose a raw path, UUID, workspace/monitor ID, URL, query string, or email as a label; `/metrics` must be unreachable through Caddy or the public API listener.
2. **No-op safety.** With every observability env var empty, all three binaries start, serve requests/jobs, and pass the existing suite; no exporter or Sentry call is attempted.
3. **Trace/log correlation.** API and River spans carry route/kind/result attributes; logs carry trace/span IDs only when a span is active; shutdown flushes within the timeout.
4. **Redaction.** Sentry payloads never contain cookies, Authorization headers, bodies, query strings, emails, passwords, TOTP/recovery codes, or API keys; worker errors are captured exactly once.
5. **Alert correctness.** Queue backlog/oldest-age ignore discarded/scheduled rows; a lost scrape agent fires `absent()`; Prometheus ruler YAML validates; Alloy/Caddy/Compose changes do not expose metrics or require credentials by default.

---

## File Structure

```text
backend/
  internal/config/config.go                           # modify: observability config
  internal/observability/metrics.go                   # create
  internal/observability/metrics_test.go              # create
  internal/observability/collectors.go                # create
  internal/observability/collectors_test.go           # create
  internal/observability/tracing.go                   # create
  internal/observability/tracing_test.go              # create
  internal/observability/sentry.go                    # create
  internal/observability/sentry_test.go               # create
  internal/observability/river.go                     # create
  internal/observability/river_test.go                # create
  internal/observability/provider.go                  # create
  internal/observability/provider_test.go             # create
  internal/logging/logging.go                         # modify: trace-aware handler
  internal/logging/logging_test.go                    # modify
  internal/app/health.go                              # modify: optional metrics handler
  internal/api/router.go                              # modify: metrics/tracing/Sentry wiring
  internal/api/router_test.go                         # modify
  internal/scheduler/scheduler.go                     # modify: metrics + tick span/recover
  internal/scheduler/scheduler_test.go                # modify
  internal/worker/probe_worker.go                     # modify: metrics + span attrs
  internal/worker/alert_worker.go                     # modify: metrics + span attrs
  cmd/api/main.go                                     # modify: provider + internal health server
  cmd/worker/main.go                                  # modify: provider, metrics, River middleware/error handler
  cmd/scheduler/main.go                               # modify: provider, metrics, recover
  compose.yaml                                        # modify: loopback ports, observability profile, web args
  web/Caddyfile                                       # modify: /healthz,/readyz proxy
web/
  src/main.tsx                                        # modify: Sentry init/boundary
  src/lib/sentry.ts                                   # create
  src/lib/sentry.test.ts                              # create
  src/components/feedback/AppErrorFallback.tsx        # create (or reuse)
  Dockerfile                                          # modify: VITE build args
deploy/observability/
  alloy.alloy                                         # create
  prometheus-rules.yml                                # create
  grafana-dashboard.json                              # create
  README.md                                           # create
web/tests/e2e/smoke.spec.ts                           # modify: metrics smoke (optional)
README.md                                             # modify: observability section
```

`internal/observability` owns metric definitions, collectors, tracing, and Sentry. The API wires HTTP metrics/tracing/Sentry; workers/scheduler wire River and job metrics. Deploy artifacts are config-only.

---

### Task 1: Observability provider, config, metrics core, and private listeners

**Files:**
- Modify: `backend/internal/config/config.go`
- Create: `backend/internal/observability/provider.go`
- Create: `backend/internal/observability/metrics.go`
- Create: `backend/internal/observability/metrics_test.go`
- Create: `backend/internal/observability/provider_test.go`
- Modify: `backend/internal/app/health.go`
- Modify: `backend/internal/api/router.go`
- Modify: `backend/internal/api/router_test.go`
- Modify: `backend/cmd/api/main.go`, `backend/cmd/worker/main.go`, `backend/cmd/scheduler/main.go`

**Interfaces:**
- Consumes: existing config/app/router/health code.
- Produces:
  - `config.Config` fields: `APIHealthAddr`, `OTELExporterEndpoint`, `OTELExporterHeaders`, `OTELSampleRatio`, `SentryDSN`, `SentryEnvironment`, `Release`.
  - `observability.Provider` with `Metrics *Metrics` and `Close(ctx) error`; Task 3 adds `Tracer`, Task 4 adds `SentryEnabled`.
  - `observability.New(ctx, cfg config.Config, logger *slog.Logger, serviceName string) (*Provider, error)`.
  - `observability.NewMetrics(service, version string) *Metrics`.
  - `Metrics.Registry() *prometheus.Registry`, `Metrics.Handler() http.Handler`.
  - `Metrics.HTTPMiddleware(next http.Handler, resolveRoute func(*http.Request) string) http.Handler`.
  - `app.StartHealthServer(addr string, logger *slog.Logger, ready func(context.Context) error, metrics http.Handler) *http.Server`; nil metrics omits `/metrics`.

- [ ] **Step 1: Write the failing config/provider/metrics tests**

Config test additions:
- Defaults: `APIHealthAddr=:8083`, empty OTLP endpoint, sample ratio `0.05`, empty DSN, `SentryEnvironment` defaults to Env, `Release=dev`.
- `O1_OTEL_SAMPLE_RATIO=2` clamps to 1, `-1` clamps to 0, invalid fails loading.
- `O1_API_HEALTH_ADDR`/`O1_RELEASE` read from env.

Metrics test:

```go
func TestMetrics_HTTPMiddlewareBoundedLabels(t *testing.T) {
	m := observability.NewMetrics("o1-api", "test")
	route := func(r *http.Request) string {
		if strings.HasPrefix(r.URL.Path, "/api/v1/workspaces/") {
			return "GET /api/v1/workspaces/{workspaceID}"
		}
		return "unmatched"
	}
	h := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), route)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/123e4567-e89b-12d3-a456-426614174000", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := gatherText(t, m.Registry())
	if !strings.Contains(body, `o1_http_requests_total{method="GET",route="GET /api/v1/workspaces/{workspaceID}",status="418"} 1`) {
		t.Fatalf("metrics body missing bounded labels:\n%s", body)
	}
	if strings.Contains(body, "123e4567") {
		t.Fatal("raw UUID leaked into metrics")
	}
}

func TestMetrics_HealthAndMetricsExcluded(t *testing.T) {
	m := observability.NewMetrics("o1-api", "test")
	called := false
	h := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}), func(*http.Request) string { return "GET /healthz" })
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("handler did not run")
	}
	if strings.Contains(gatherText(t, m.Registry()), "o1_http_requests_total") {
		t.Fatal("health request was recorded")
	}
}

func TestMetrics_NilIsSafe(t *testing.T) {
	var m *observability.Metrics
	m.ObserveProbe("up", time.Millisecond)
	m.ObserveAlert("email", "sent", time.Millisecond)
	m.ObserveSchedulerTick(3, time.Millisecond)
	m.ObserveProbeSkipped("replay")
}
```

`gatherText` uses `promhttp.HandlerFor` into a `bytes.Buffer`; define it once in `metrics_test.go` and reuse in later observability tests.

Provider test:
- `New` with empty observability config returns non-nil provider, a registry containing `o1_build_info`, nil/disabled tracer/Sentry, and a no-op close.
- Close is idempotent and returns nil.

Health server test:
- `/metrics` returns 200 and exposition only when a handler is passed; nil gives 404.
- `/healthz` and `/readyz` unchanged.

API router test:
- The public router does **not** serve `/metrics` (405/404).
- With a metrics instance, requests record bounded route labels using `mux.Handler` resolution.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/config ./internal/observability ./internal/app ./internal/api -run 'TestObservability|TestMetrics|TestHealthServer|TestRouterMetrics' -v`
Expected: FAIL because the package/config/listener do not exist.

- [ ] **Step 3: Add config fields and parsing**

Run: `cd backend && go get github.com/prometheus/client_golang`.

Pin:
- `APIHealthAddr` default `:8083`.
- `OTELExporterEndpoint`, `SentryDSN` default empty.
- `OTELExporterHeaders` parsed from comma-separated `key=value` into `map[string]string`; reject malformed pairs; never log values.
- `OTELSampleRatio` default `0.05`, parse float, reject NaN/Inf, clamp to `[0,1]`.
- `SentryEnvironment` default `Env`; `Release` default `dev`.

- [ ] **Step 4: Implement the provider and metrics core**

Pin:
- `NewMetrics(service, version)` creates a registry with Go/process collectors, `o1_build_info{service,version}=1`, and the HTTP/probe/alert/scheduler/incident metric vectors/histograms from the catalog. Nil receivers are no-ops.
- `HTTPMiddleware` records in-flight, counts, duration, and status; it skips `/healthz`, `/readyz`, `/metrics` and uses the supplied route resolver; empty/unknown route → `unmatched`.
- `Provider.New` builds metrics, calls the (later) tracing/Sentry initializers, and returns a provider whose Close flushes them; with empty config both are no-ops.
- Logging helpers never include request bodies or headers.

- [ ] **Step 5: Wire health servers and API internal listener**

- Change `StartHealthServer` signature; add `/metrics` only when `metrics != nil`.
- `cmd/api` starts a second health server on `APIHealthAddr` with pool readiness and metrics, and shuts it down with the main server.
- Scheduler/worker pass their metrics handler.
- API router accepts a `*observability.Metrics` dependency, wraps the mux with `HTTPMiddleware` using `mux.Handler(r)` for the pattern, and excludes the public `/healthz`/`/readyz` from metrics. The public mux never registers `/metrics`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/config ./internal/observability ./internal/app ./internal/api -run 'TestObservability|TestMetrics|TestHealthServer|TestRouterMetrics' -v`
Expected: PASS.

- [ ] **Step 7: Quality gate and commit**

Run: `cd backend && gofmt -w internal/config internal/observability internal/app internal/api cmd && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/config backend/internal/observability backend/internal/app backend/internal/api backend/cmd
git commit -m "feat: add observability provider, metrics, and private listeners"
```

---

### Task 2: Collectors and worker/scheduler instrumentation

**Files:**
- Create: `backend/internal/observability/collectors.go`
- Create: `backend/internal/observability/collectors_test.go`
- Modify: `backend/internal/observability/metrics.go`
- Modify: `backend/internal/scheduler/scheduler.go`
- Modify: `backend/internal/scheduler/scheduler_test.go`
- Modify: `backend/internal/worker/probe_worker.go`
- Modify: `backend/internal/worker/alert_worker.go`
- Modify: `backend/internal/worker/*_test.go`
- Modify: `backend/cmd/worker/main.go`, `backend/cmd/scheduler/main.go`

**Interfaces:**
- Consumes: Task 1 metrics.
- Produces:
  - `NewPoolCollector(pool *pgxpool.Pool) prometheus.Collector`.
  - `NewQueueCollector(pool *pgxpool.Pool, metrics *Metrics) *QueueCollector` with `Refresh(ctx) error`, `Start(ctx)`.
  - `NewIncidentCollector(pool *pgxpool.Pool, metrics *Metrics) *IncidentCollector` with `Refresh(ctx) error`, `Start(ctx)`.
  - Worker/scheduler constructors accept `*Metrics` (nil-safe).

- [ ] **Step 1: Write failing collector tests**

Use `testutil.NewPostgres`:

```go
func TestQueueCollector_CountsAndOldestRunnable(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, row := range []struct {
		queue, state string
		age          time.Duration
		finalized    bool
	}{
		{"probe", "available", 2 * time.Minute, false},
		{"probe", "running", time.Minute, false},
		{"probe", "discarded", 24 * time.Hour, true},
		{"alert", "completed", time.Hour, true},
	} {
		var finalized any
		if row.finalized {
			finalized = now
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO river_job (kind, args, queue, state, created_at, finalized_at)
			VALUES ('test','{}',$1,$2,$3,$4)`,
			row.queue, row.state, now.Add(-row.age), finalized); err != nil {
			t.Fatal(err)
		}
	}
	m := observability.NewMetrics("o1-worker", "test")
	c := observability.NewQueueCollector(pool, m)
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	body := gatherText(t, m.Registry())
	if !strings.Contains(body, `o1_queue_jobs{queue="probe",state="available"} 1`) {
		t.Fatalf("missing queue gauge:\n%s", body)
	}
	if !strings.Contains(body, `o1_queue_jobs{queue="probe",state="discarded"} 1`) {
		t.Fatalf("missing discarded gauge:\n%s", body)
	}
	if strings.Contains(body, `state="completed"`) {
		t.Fatal("terminal job counted")
	}
	// Oldest age ignores discarded: ~2 minutes, not 24 hours.
	if !strings.Contains(body, `o1_queue_oldest_job_age_seconds{queue="probe"}`) {
		t.Fatalf("missing oldest age:\n%s", body)
	}
}
```

Add:
- `TestIncidentCollector_CountsUnresolved`: insert open/acknowledged/resolved incidents, assert gauge = 2.
- `TestPoolCollector_ExposesStates`: real pool, gather contains acquired/idle/total/max.
- `TestCollector_RefreshFailureKeepsLastValue`: cancel context after a successful refresh, second refresh fails silently (returns error), gauge unchanged.
- Worker tests: probe success/failure increments `o1_probe_results_total{result}` and duration histogram; skipped reasons; alert sent/failed/skipped increments channel/status; nil metrics safe.
- Scheduler test: a tick increments ticks/enqueued and records duration; a panicking tick is recovered by Task 4's boundary (leave a TODO until then) or test the metric only.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/observability ./internal/worker ./internal/scheduler -run 'TestQueueCollector|TestIncidentCollector|TestPoolCollector|TestWorkerMetrics|TestSchedulerMetrics' -v`
Expected: FAIL because collectors/instrumentation do not exist.

- [ ] **Step 3: Implement collectors**

Pin:
- Queue queries exactly as the design; gauge includes non-terminal states, oldest age only runnable states; `Refresh` logs and returns errors, `Start` ticks every 15s and stops on context cancellation.
- Incident gauge queries `resolved_at IS NULL` every 30s.
- Pool collector reads `pool.Stat()` on scrape and exposes acquired/idle/total/max and acquire counters.
- `Start` runs in exactly one process (worker) per environment; call from `cmd/worker` after provider creation. Scheduler also starts incident/queue collectors? Choose worker only; document in code and README.

- [ ] **Step 4: Instrument workers and scheduler**

Pin:
- `NewProbeWorker`/`NewAlertWorker` accept `*observability.Metrics`; nil-safe.
- Probe: observe result and duration after each recorded result; skipped reasons (`replay`, `paused`, `suspended`, `missing`) increment the counter. Do not include monitor IDs/URLs.
- Alert: observe channel kind/status and duration around the terminal delivery outcome. `channel_kind` must be one of the four known kinds; unknown maps to `other` (do not add as a metric label? use `other`).
- Scheduler: ticks, enqueued count, duration per tick.
- Tests assert increments via the registry text.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/observability ./internal/worker ./internal/scheduler -v`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/observability internal/worker internal/scheduler cmd && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/observability backend/internal/worker backend/internal/scheduler backend/cmd
git commit -m "feat: add queue, pool, probe, alert, and scheduler metrics"
```

---

### Task 3: OTLP tracing and trace-correlated logs

**Files:**
- Create: `backend/internal/observability/tracing.go`
- Create: `backend/internal/observability/tracing_test.go`
- Modify: `backend/internal/observability/provider.go`
- Modify: `backend/internal/observability/metrics.go` (HTTP span wrapper if separate)
- Modify: `backend/internal/logging/logging.go`
- Modify: `backend/internal/logging/logging_test.go`
- Create: `backend/internal/observability/river.go`
- Create: `backend/internal/observability/river_test.go`
- Modify: `backend/internal/api/router.go`
- Modify: `backend/internal/worker/*.go`
- Modify: `backend/internal/scheduler/scheduler.go`
- Modify: `backend/cmd/api/main.go`, `backend/cmd/worker/main.go`, `backend/cmd/scheduler/main.go`

**Interfaces:**
- Consumes: Task 1 provider.
- Produces:
  - `Tracer` with `Shutdown(ctx) error`, `Enabled() bool`.
  - `Provider.Close` flushes tracing.
  - `observability.RiverMiddleware(metrics, tracer) rivertype.WorkerMiddleware`.
  - Logging handler that adds `trace_id`/`span_id`.

- [ ] **Step 1: Write failing tracing/log tests**

- `NewTracer` with empty endpoint returns disabled, no error.
- With a test OTLP collector or in-memory exporter (use `tracetest.NewInMemoryExporter` + SDK provider injection seam), a span named `river.probe` has attributes `queue`, `attempt`, `kind`; an error sets status.
- Sample ratio clamping.
- Logging test: a handler with a span in context emits `trace_id`/`span_id`; without a span emits neither; existing fields remain.

Use an injectable exporter/provider in `tracing.go` for tests (unexported fields or a `newTracerWithExporter` helper in `package observability` tests).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/observability ./internal/logging -run 'TestTracer|TestLoggingTrace' -v`
Expected: FAIL because tracing/log correlation does not exist.

- [ ] **Step 3: Implement tracing**

Run: `cd backend && go get go.opentelemetry.io/otel/sdk go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`.

Pin:
- OTLP/HTTP exporter with endpoint/headers from config; resource service.name/version/environment; parent-based ratio sampler; batch processor with 5s shutdown timeout.
- No exporter when endpoint empty; `Enabled()` false.
- API: `otelhttp.NewHandler` wraps the router, excluding health/metrics; span name `METHOD route` using the same resolver as metrics. Do not record query strings or request bodies.
- River middleware starts `river.<kind>` spans with queue/attempt attributes; records errors and sets status; measures job duration for a future metric.
- Probe/alert/scheduler add result/channel/enqueued attributes to their spans.
- `cmd/*` call `obs.Close` before pool close; flush failures are logged, not fatal.

- [ ] **Step 4: Implement trace-correlated logging**

Pin:
- Wrap the existing `slog.Handler` with one that checks `trace.SpanFromContext(ctx).SpanContext()` and adds string attrs `trace_id`/`span_id` when valid.
- JSON/text output unchanged otherwise; no IDs when no span.
- `app.Start` continues to create the logger via `logging.New`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/observability ./internal/logging ./internal/api ./internal/worker ./internal/scheduler -run 'TestTracer|TestLoggingTrace|TestRiver' -v`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/observability internal/logging internal/api internal/worker internal/scheduler cmd && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/observability backend/internal/logging backend/internal/api backend/internal/worker backend/internal/scheduler backend/cmd
git commit -m "feat: add OTLP tracing and trace-correlated logs"
```

---

### Task 4: Backend Sentry, panic boundaries, and River error handling

**Files:**
- Create: `backend/internal/observability/sentry.go`
- Create: `backend/internal/observability/sentry_test.go`
- Modify: `backend/internal/observability/provider.go`
- Modify: `backend/internal/observability/river.go`
- Modify: `backend/internal/observability/river_test.go`
- Modify: `backend/internal/api/router.go`
- Modify: `backend/internal/api/router_test.go`
- Modify: `backend/internal/scheduler/scheduler.go`
- Modify: `backend/internal/scheduler/scheduler_test.go`
- Modify: `backend/cmd/worker/main.go`, `backend/cmd/scheduler/main.go`, `backend/cmd/api/main.go`

**Interfaces:**
- Consumes: Task 1 provider, Task 3 River middleware.
- Produces:
  - `SentryEnabled`, `CaptureException(ctx, err)`, `CapturePanic(ctx, rec any)`, `BeforeSend(event, hint) *Event` (exported for tests).
  - `observability.SentryErrorHandler` implementing `river.ErrorHandler` (`HandleError`, `HandlePanic`).
  - `Provider.Close` flushes Sentry.

- [ ] **Step 1: Write failing Sentry tests**

- Empty DSN: `New` leaves Sentry disabled; `CaptureException` is a no-op.
- `BeforeSend` redaction: given an event with request headers (`Authorization`, `Cookie`), body, query string, and user email, the returned event has them cleared; method/route preserved.
- Dropped errors: `context.Canceled` and `http.ErrServerClosed` yield nil.
- API panic: a handler that panics returns 500 and calls capture once (use a test hook counter).
- River error handler: `HandleError` captures once and returns a non-nil `ErrorHandlerResult`; `HandlePanic` captures once with the panic value and returns a result that does not cancel retries.
- Scheduler panic: a tick panics, Sentry captures once, the scheduler logs and continues to the next tick (use a fake tick function).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/observability ./internal/api ./internal/scheduler -run 'TestSentry|TestAPIPanic|TestSchedulerPanic' -v`
Expected: FAIL because Sentry wiring does not exist.

- [ ] **Step 3: Implement Sentry init/redaction/capture**

Run: `cd backend && go get github.com/getsentry/sentry-go`.

Pin:
- Initialize only with a DSN; environment/release; `TracesSampleRate: 0`; `AttachStacktrace: true`.
- `BeforeSend` clears `Request.Data`, `Headers`, `Cookies`, `QueryString`, and `User.Email`; removes URL query; keeps method/route.
- Ignore canceled/server-closed errors.
- `CaptureException`/`CapturePanic` are nil/disabled-safe; `CapturePanic` re-panics only where the caller decides to (the API recover writes 500 and does not re-panic).
- Flush with a 5s timeout on provider close.

- [ ] **Step 4: Wire panic boundaries**

- API `recoverPanics` calls `observability.CapturePanic` before writing 500.
- Worker River config gets `ErrorHandler: observability.SentryErrorHandler{}` (or an instance); `HandleError`/`HandlePanic` capture once with job kind/queue tags, no args.
- Scheduler tick wrapper recovers, captures, logs with `panic`, and continues with the next tick; shutdown remains clean.
- Add integration tests using a fake capture function (package-level test hook, no real DSN).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/observability ./internal/api ./internal/scheduler ./internal/worker -run 'TestSentry|TestAPIPanic|TestSchedulerPanic|TestRiver' -v`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/observability internal/api internal/scheduler internal/worker cmd && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/observability backend/internal/api backend/internal/scheduler backend/internal/worker backend/cmd
git commit -m "feat: capture backend panics and job errors in Sentry"
```

---

### Task 5: Frontend Sentry, error boundary, and build args

**Files:**
- Create: `web/src/lib/sentry.ts`
- Create: `web/src/lib/sentry.test.ts`
- Modify: `web/src/main.tsx`
- Modify: `web/src/components/feedback/AppErrorFallback.tsx` (create/reuse)
- Modify: `web/Dockerfile`
- Modify: `web/package.json`, `web/package-lock.json`
- Modify: `compose.yaml`
- Modify: `.github/workflows/ci.yml` (build args optional)

**Interfaces:**
- Consumes: Vite env.
- Produces:
  - `initSentry()` returning `boolean` (enabled).
  - `scrubSentryEvent(event)` exported for tests.
  - `AppErrorFallback` for `Sentry.ErrorBoundary`.

- [ ] **Step 1: Write failing tests**

- `initSentry` is not called without `VITE_SENTRY_DSN`; with a mocked `Sentry.init` and a DSN, it is called with environment/release, `tracesSampleRate: 0`, and `beforeSend` set.
- `scrubSentryEvent` removes request data/headers/cookies/query and user email while keeping method/route.
- `AppErrorFallback` renders a friendly message and a reload action.
- Main wiring test (or a small helper) shows the boundary is used.

Mock `@sentry/react` with `vi.mock`; avoid real network.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/lib/sentry.test.ts`
Expected: FAIL because Sentry is not installed/wired.

- [ ] **Step 3: Implement the frontend integration**

- `npm install @sentry/react`.
- `src/lib/sentry.ts`: read `import.meta.env.VITE_SENTRY_DSN`/`VITE_RELEASE`; init with `tracesSampleRate: 0`, `environment: import.meta.env.MODE`, `beforeSend: scrubSentryEvent`; return whether enabled.
- `main.tsx`: call `initSentry()` before render and wrap the router with `Sentry.ErrorBoundary` using `AppErrorFallback`.
- `Dockerfile`: `ARG VITE_SENTRY_DSN`, `ARG VITE_RELEASE`, `ENV` before `npm run build`.
- `compose.yaml`: web build args from `${VITE_SENTRY_DSN:-}`/`${VITE_RELEASE:-dev}`.
- CI: pass the args when building the web image (empty by default).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/lib/sentry.test.ts && npm run format && npm run lint && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/sentry.ts web/src/lib/sentry.test.ts web/src/main.tsx web/src/components/feedback web/Dockerfile web/package.json web/package-lock.json compose.yaml .github/workflows/ci.yml
git commit -m "feat: add frontend Sentry capture and build args"
```

---

### Task 6: Caddy, Compose, Alloy, dashboard, alert rules, and runbook

**Files:**
- Modify: `web/Caddyfile`
- Modify: `compose.yaml`
- Create: `deploy/observability/alloy.alloy`
- Create: `deploy/observability/prometheus-rules.yml`
- Create: `deploy/observability/grafana-dashboard.json`
- Create: `deploy/observability/README.md`
- Modify: `README.md`

- [ ] **Step 1: Update Caddy and Compose**

Caddy:
- Add explicit handlers for `/healthz` and `/readyz` proxying to `api:8080` before the SPA fallback.
- Never route `/metrics`.

Compose:
- Bind scheduler/worker health ports to `127.0.0.1:8081:8081` and `127.0.0.1:8082:8082`.
- Do not publish the API `:8083`.
- Add an `alloy` service with `profiles: ["observability"]`, image pinned by tag/digest, mounts `deploy/observability/alloy.alloy`, and env for remote-write URL/user/token. Normal `docker compose up` does not start it.
- Forward the new backend observability env vars through the shared backend environment and the web build args.

- [ ] **Step 2: Add observability artifacts**

Alloy:
- `prometheus.scrape` for `api:8083`, `scheduler:8081`, `worker:8082` every 15s, job labels `o1-api`/`o1-scheduler`/`o1-worker`, env label.
- `prometheus.remote_write` to `${GRAFANA_REMOTE_WRITE_URL}` with basic auth from env. The compose profile requires the URL/user/token; if unset it fails fast with a clear message. Keep credentials out of the file.

Rules:
- Prometheus/Mimir YAML with the alerts from the design, including `O1MetricsMissing` (`absent(up{job=~"o1-.*"})`) and `O1JobsDiscarded`.
- Validate with `promtool check rules deploy/observability/prometheus-rules.yml` if available; document the command.

Dashboard:
- Valid Grafana dashboard JSON with panels for request rate/errors/latency, queue depth/age, probe results/duration, alert deliveries, incidents, DB pool, scheduler ticks, process memory/CPU.

Runbook:
- Grafana Cloud URL/token setup, Alloy profile start command, dashboard import, `mimirtool rules load`, Sentry backend/frontend setup, dead-man's switch (healthchecks.io pinging `https://<host>/healthz`, 2 misses), retention/budget guidance, and a note that `/metrics` is private.

README:
- Add an Observability section linking the runbook and listing the env vars.

- [ ] **Step 3: Validate configuration**

Run:
```bash
cd web && npx caddy validate --config Caddyfile  # if caddy is available; otherwise docker run caddy
cd .. && docker compose config
promtool check rules deploy/observability/prometheus-rules.yml  # if installed
```
Expected: Caddy and Compose configs valid; rules parse. If `caddy`/`promtool` are unavailable, use the documented Docker equivalents and report it.

- [ ] **Step 4: Commit**

```bash
git add web/Caddyfile compose.yaml deploy/observability README.md
git commit -m "feat: add observability deploy bundle and private metrics routing"
```

---

### Task 7: E2E metrics smoke, README, and final verification

**Files:**
- Modify: `web/tests/e2e/smoke.spec.ts`
- Modify: `web/tests/e2e/helpers.ts` (if a compose exec helper is needed)
- Modify: `README.md`

- [ ] **Step 1: Add the metrics smoke check**

Using the existing Compose psql/exec helpers, after the smoke flow:
- `docker compose exec -T web wget -qO- http://api:8083/metrics` (Caddy's alpine image has wget) contains `o1_http_requests_total`.
- Web can also reach `http://scheduler:8081/metrics` and `http://worker:8082/metrics`; assert `o1_queue_jobs` and `o1_build_info`.
- The public URL `/metrics` does not return Prometheus exposition (Caddy SPA/404).
- `/healthz` returns the API health JSON through Caddy, not the SPA HTML.

Keep the existing UI, status-page, API-key, 2FA, theme, and a11y assertions green.

- [ ] **Step 2: Run the E2E suite**

Bring up the Compose stack (`docker compose up -d --build`), then `cd web && npm run e2e`.
Expected: PASS.

- [ ] **Step 3: Update the README**

Ensure the Observability section covers: internal metric endpoints and the Alloy profile, OTLP/Sentry env vars, frontend build args, dashboard/alert import, the dead-man's switch, privacy/cardinality guarantees, and that observability config is optional.

- [ ] **Step 4: Run the complete verification**

```bash
cd backend && gofmt -l . && go vet ./... && golangci-lint run && go test -short ./... && go test ./...
cd web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run && npm run build
cd web && npm run e2e
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```bash
git add README.md web/tests/e2e
git commit -m "test: cover private metrics and health routing end to end"
```

---

## Self-Review Checklist

- [ ] Every spec section maps to a task: config/provider/metrics/listeners (§6/§7) → Task 1; collectors/instrumentation (§7) → Task 2; tracing/logs (§8) → Task 3; Sentry backend (§9) → Task 4; Sentry frontend (§9) → Task 5; deploy artifacts (§10) → Task 6; E2E/docs/acceptance (§11/§12) → Task 7.
- [ ] Review Focus items have tests: privacy/cardinality → Tasks 1/2/6/7; no-op safety → Tasks 1/4/5; trace/log correlation → Task 3; redaction → Tasks 4/5; alert correctness → Task 6.
- [ ] Names/signatures match across tasks: `observability.Provider`, `Metrics`, `NewMetrics`, `HTTPMiddleware`, `NewQueueCollector`, `NewIncidentCollector`, `RiverMiddleware`, `SentryErrorHandler`, `StartHealthServer(..., metrics)`, `o1_*` metric names.
- [ ] No task depends on a later task without saying so; each ends with a passing test cycle and a commit.
