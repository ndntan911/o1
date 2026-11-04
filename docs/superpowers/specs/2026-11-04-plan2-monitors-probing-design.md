# Plan 2 — Monitors, Probing & Abuse Controls (Backend) — Design

- **Date:** 2026-11-04
- **Status:** Awaiting approval
- **Parent spec:** `docs/superpowers/specs/2026-11-04-uptime-monitor-design.md`
- **Predecessor:** `docs/superpowers/plans/2026-11-04-uptime-monitor-foundation-auth.md` (merged at `bc343c2`)
- **Scope:** Backend slice of M1: monitor CRUD, SSRF-safe HTTP probing, scheduler/worker loop, dashboard read APIs, quotas, and minimal admin controls. React UI, incidents, alerts, status pages, and production hardening remain later plans.

## 1. Why this slice

Plan 1 shipped accounts, sessions, workspaces, and RBAC. This plan makes the product actually monitor things: a tenant can register an HTTP endpoint, the system probes it on a schedule, stores raw results, derives an up/down state, and exposes that state through dashboard APIs.

It also closes the M1 abuse-control requirements that are unsafe to defer for an open-signup prober: SSRF protection, per-workspace quotas, monitor-creation rate limiting, and admin suspension.

## 2. Scope

### In scope

1. Monitor schema, state schema, raw probe results, security events.
2. Tenant-scoped monitor CRUD with RBAC (`viewer` read-only, `member`+ mutate).
3. Application-level encryption for monitor headers and request bodies.
4. SSRF guard used both at write time and at dial time, including redirects.
5. Quota (5 monitors/workspace) and creation rate limit (10/hour/workspace), both data-backed.
6. `scheduler` binary that enqueues due probes through River.
7. `worker` binary that executes probes and advances a per-monitor state machine (`unknown`/`up`/`down`, plus `paused` presentation).
8. Dashboard read APIs: list monitors with 24h summary, monitor detail, recent results, windowed uptime/latency summary.
9. Minimal admin APIs: list workspaces, suspend/unsuspend workspace, list monitors with redacted URLs, list security events.
10. Config, health endpoints, graceful shutdown, Docker image, and tests for all of the above.

### Explicitly out of scope (later plans)

- Incidents, incident events, acknowledgement, alert channels, alert deliveries. The state machine emits transitions in a form Plan 4 can consume, but does not open incidents yet.
- React dashboard (Plan 3).
- Public status pages (Plan 4).
- Hourly/daily rollups and data retention. Plan 2 reads raw results only, capped to a 7-day window. Rollups and partition drops land before public launch.
- API keys, audit logs, Prometheus/OTel/Sentry, load tests, 2FA (Plan 5).
- Email or webhook notifications for suspicious activity. Plan 2 records security events and logs them; alerting is Plan 5.

## 3. Key decisions

| # | Decision | Rationale |
|---|----------|-----------|
| D1 | Probe jobs go through River, as the parent spec requires. | Durable retries, idempotency, same transaction as `next_check_at`, no new infrastructure. |
| D2 | Scheduler holds a row lock on each due `monitor_state` row and inserts the River job + advances `next_check_at` in one transaction. | A crash cannot both enqueue and lose the schedule advance; `SKIP LOCKED` permits multiple scheduler instances. |
| D3 | Probe jobs are unique on `(monitor_id, scheduled_unix)` via River `UniqueOpts{ByArgs: true}`. | Duplicate delivery after a crash is cheap and harmless; the worker checks for an existing result before writing, so a replay cannot double-count. |
| D4 | Probe failures (timeout, TLS error, wrong status) are *results*, not job failures. Only DB/decrypt/internal errors fail the job. | A dead target must not burn retries or back off monitoring; it must record `down`. |
| D5 | Monitor headers and request body are encrypted with AES-256-GCM before storage; URLs are stored plaintext. | Spec §7.8 requires tenant secrets encrypted. URL is needed for display and dialing; query strings are redacted in logs and admin views. |
| D6 | Encryption key comes from `O1_ENCRYPTION_KEY`; in `development`/`test` it may be derived from `O1_SECRET_KEY`. Production refuses to start without an explicit key. | Rotation safety: rotating sessions must not make stored secrets undecryptable. |
| D7 | `probe_results` is range-partitioned by month on `checked_at`; partitions are created by Go at startup and by the scheduler daily. | Spec §7.8 requires monthly partitions; retention later becomes `DROP PARTITION`. |
| D8 | Pause is a boolean on `monitors`; the API computes `status = "paused"` when paused. Counters and the underlying state are preserved. | Matches spec §7.4 without a fifth persisted state that could drift. |
| D9 | Creation quota/rate limit are enforced under a `SELECT ... FOR UPDATE` on the workspace row. | Serializes concurrent creates per workspace with no new infrastructure and no race above the limit. |
| D10 | Admin identity is `users.is_admin`; optional `O1_BOOTSTRAP_ADMIN_EMAIL` promotes an existing verified user at startup. | Operable on day one without a second auth system. |
| D11 | Suspension blocks mutations and probing, but not reads or login. | Members can still see why their workspace is suspended; user accounts may belong to other workspaces. |
| D12 | All new binaries share `internal/app` startup (config, logger, pool, migrations, health, shutdown). | Keeps api/scheduler/worker lifecycle identical and testable. |

## 4. Data model

New migration `00003_monitors.sql`. All primary keys are UUIDv7 generated in Go. All timestamps are `timestamptz`. Every business table is reachable from `workspace_id`.

### 4.1 Workspace and user additions

```sql
ALTER TABLE users ADD COLUMN is_admin boolean NOT NULL DEFAULT false;

ALTER TABLE workspaces
  ADD COLUMN monitor_limit integer NOT NULL DEFAULT 5 CHECK (monitor_limit > 0),
  ADD COLUMN suspended_at timestamptz,
  ADD COLUMN suspended_by uuid REFERENCES users(id),
  ADD COLUMN suspended_reason text CHECK (length(suspended_reason) <= 500);
```

`monitor_limit` is the per-workspace manual override hook for "raise by hand"; the global default remains 5.

### 4.2 Monitors

```sql
CREATE TABLE monitors (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 100),
  url text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
  method text NOT NULL CHECK (method IN ('GET','HEAD','POST','PUT','PATCH','DELETE')),
  headers_enc text,             -- AES-GCM, JSON object of string->string
  body_enc text,                -- AES-GCM, raw request body
  content_type text CHECK (length(content_type) <= 200),
  expected_status text NOT NULL DEFAULT '2xx',
  interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 60 AND 86400),
  timeout_seconds integer NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 30),
  confirmation_threshold smallint NOT NULL DEFAULT 2 CHECK (confirmation_threshold BETWEEN 1 AND 5),
  recovery_threshold smallint NOT NULL DEFAULT 2 CHECK (recovery_threshold BETWEEN 1 AND 5),
  paused boolean NOT NULL DEFAULT false,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX monitors_workspace_created_idx ON monitors (workspace_id, created_at DESC, id DESC);
CREATE INDEX monitors_created_at_idx ON monitors (created_at);
```

Hard delete. `probe_results` and `monitor_state` cascade. Losing history on delete is acceptable for V1 and avoids soft-delete filters on every query.

### 4.3 Monitor state

```sql
CREATE TABLE monitor_state (
  monitor_id uuid PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown','up','down')),
  consecutive_failures integer NOT NULL DEFAULT 0,
  consecutive_successes integer NOT NULL DEFAULT 0,
  last_checked_at timestamptz,
  last_status_code integer,
  last_latency_ms integer,
  last_error text,
  next_check_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX monitor_state_due_idx ON monitor_state (next_check_at);
```

The scheduler joins `monitors` and `workspaces`, so paused/suspended rows are filtered there rather than duplicated here.

### 4.4 Probe results

```sql
CREATE TABLE probe_results (
  id uuid NOT NULL,
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  scheduled_for timestamptz NOT NULL,
  checked_at timestamptz NOT NULL,
  status text NOT NULL CHECK (status IN ('up','down')),
  status_code integer,
  latency_ms integer NOT NULL CHECK (latency_ms >= 0),
  error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, checked_at)
) PARTITION BY RANGE (checked_at);

CREATE UNIQUE INDEX probe_results_monitor_scheduled_key
  ON probe_results (monitor_id, scheduled_for, checked_at);
CREATE INDEX probe_results_monitor_checked_idx
  ON probe_results (monitor_id, checked_at DESC, id DESC);
CREATE INDEX probe_results_workspace_checked_idx
  ON probe_results (workspace_id, checked_at DESC);
```

Partitions are named `probe_results_YYYY_MM`. Go creates `[current month − 1, current month + 2]` idempotently at startup and once per scheduler day. A missing partition is a loud insert error, not silent data loss.

The unique index includes `checked_at` because PostgreSQL requires every unique index on a partitioned table to contain the partition key. Job idempotency is therefore enforced by the worker's `(monitor_id, scheduled_for)` existence check (River never runs the same job concurrently); the index remains a guard against exact-row duplicates.

`latency_ms` measures time to response headers (the probe does not need the body). The body is drained up to 1 MiB and closed.

### 4.5 Security events

```sql
CREATE TABLE security_events (
  id uuid PRIMARY KEY,
  workspace_id uuid REFERENCES workspaces(id) ON DELETE CASCADE,
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  kind text NOT NULL,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_events_kind_created_idx ON security_events (kind, created_at DESC);
CREATE INDEX security_events_workspace_created_idx ON security_events (workspace_id, created_at DESC);
```

Kinds in this plan: `monitor_created`, `monitor_create_rate_limited`, `ssrf_blocked`, `workspace_suspended`, `workspace_unsuspended`. `detail` stores redacted URL (scheme/host/path, query stripped), rejection reason, and limits — never header values, request bodies, or raw query strings.

## 5. Secret encryption

New package `internal/secretbox`:

- `New(key []byte) (*Box, error)` requires exactly 32 bytes.
- `Seal(plaintext []byte) (string, error)` returns `v1:<base64url(nonce || ciphertext || tag)>`.
- `Open(value string) ([]byte, error)` parses the version prefix, rejects unknown versions, and authenticates before returning plaintext.
- A fresh 12-byte nonce from `crypto/rand` per seal; never reuse.
- Empty input stores SQL `NULL`/empty string, not an encrypted empty blob.

Key selection:

- `O1_ENCRYPTION_KEY`: base64, 32 bytes. Required when `O1_ENV=production`.
- Development/test fallback: HKDF-SHA256(`O1_SECRET_KEY`, info `o1:tenant-secrets:v1`), 32 bytes. Logged once at startup as a warning in non-production.
- The `v1:` prefix is the rotation seam. A future plan adds a key map and re-encryption job.

What is encrypted: the JSON header map and request body. What is not: name, URL, method, expected status, content type, intervals, thresholds. The URL is visible to workspace members and to the probe, and query strings are redacted everywhere else.

API behavior: responses never return header values. They return header names with `"***"` values, e.g. `{"Authorization":"***"}`. `PATCH` replaces the whole header map; omitting `headers` leaves it unchanged. This prevents a client from round-tripping masked values as real secrets.

## 6. SSRF guard

New package `internal/probe` with a `Guard` type built from a `Policy`.

### 6.1 Policy

```go
type Policy struct {
    AllowedPorts   map[int]struct{} // default {80,443}, from O1_PROBE_ALLOWED_PORTS
    MaxRedirects   int              // default 5
    MaxResponseBody int64           // default 1 MiB
    ConnectTimeout time.Duration
    TLSHandshakeTimeout time.Duration
    // test-only escape hatch; never set from environment
    AllowPrivateNetworks bool
}
```

Production constructs `Policy` only from config. `AllowPrivateNetworks` exists so `httptest` servers (127.0.0.1) can be probed in tests; it is never set by `config.Load`.

### 6.2 URL validation (write time and every redirect)

Reject when any of the following is true:

- Scheme is not `http`/`https`.
- URL has userinfo (`https://user:pass@host/`).
- URL has a fragment.
- Host is empty or longer than 253 characters.
- Port is present and not in `AllowedPorts` (default ports map to 80/443).
- Resolved addresses (when DNS succeeds) include any non-public IP.

A DNS lookup failure at write time does **not** reject the monitor; the probe will record a DNS failure. The dial-time check still prevents a later resolution from reaching an internal address. This avoids making monitor creation depend on transient DNS.

### 6.3 Public-address classification

Reject `ip.IsLoopback()`, `ip.IsPrivate()`, `ip.IsUnspecified()`, `ip.IsLinkLocalUnicast()`, `ip.IsLinkLocalMulticast()`, `ip.IsMulticast()`, plus explicit CIDRs: `0.0.0.0/8`, `100.64.0.0/10`, `192.0.0.0/24`, `198.18.0.0/15`, `240.0.0.0/4`, `255.255.255.255/32`, `fc00::/7`, `fe80::/10`, `ff00::/8`, `::/128`, `2001:db8::/32`. IPv4-mapped IPv6 is unwrapped with `ip.Unmap()` before classification.

### 6.4 Dial-time enforcement

- `net.Resolver.LookupIPAddr` is called per connection attempt.
- Every candidate is classified; blocked candidates are skipped.
- The dialer connects to the numeric IP with `net.Dialer.DialContext`, not the hostname, so a second DNS lookup cannot swap in a private address between check and connect (DNS rebinding).
- TLS SNI and certificate verification still use the original hostname because Go's `http.Transport` derives `ServerName` from the request URL, not the dial address.
- Proxy is explicitly disabled (`Transport.Proxy = nil`) so `HTTP_PROXY` cannot tunnel probes elsewhere.
- Keep-alives are disabled; each probe uses a fresh connection and closes it. At the target 17 probes/second this is cheap and avoids pooling tenant-specific connections.

### 6.5 Redirects

`CheckRedirect`:

- Rejects the 6th redirect.
- Re-runs the full URL validation (scheme, port, host resolution, public IPs).
- On any host change, drops all user-supplied headers and `Authorization`; only `User-Agent`, `Accept`, and `Accept-Encoding` are kept.
- On same-host redirects, user headers are preserved except `Host`.

### 6.6 Request construction

- Method from the monitor.
- No user-supplied `Host`. Header names `Host`, `Content-Length`, `Connection`, `Transfer-Encoding`, `Proxy-Connection`, `Upgrade`, and `TE` are rejected at validation time.
- Header limits: max 20 headers, name ≤ 64 chars matching an RFC 7230 token, value ≤ 1024 chars, total ≤ 8 KiB.
- Body limit: 16 KiB; only for methods that can carry a body; `GET`/`HEAD` bodies rejected.
- `Content-Type` only when a body is present.
- `User-Agent: o1-probe/1.0`.
- Total timeout = monitor `timeout_seconds`; per-phase timeouts are bounded by it.

### 6.7 Result classification

- Success requires a response (headers received) whose status matches `expected_status`.
- TLS verification failure, timeout, connection error, DNS error, redirect-policy violation, and body-read error all count as `down`.
- `expected_status` grammar: `2xx`-style class wildcards, explicit codes, and inclusive ranges, comma-separated, max 20 entries, codes 100–599. Stored normalized, e.g. `200-299,301`. Default `2xx`.
- Error text stored on the result is sanitized: no request headers, no body, no full URL with query. It contains the failure class and a short message.

## 7. State machine

The worker computes the transition inside the same transaction that writes `probe_results`.

Success/failure:

- Success: `consecutive_successes++`, `consecutive_failures = 0`.
- Failure: `consecutive_failures++`, `consecutive_successes = 0`.
- Counters cap at 1000 to avoid unbounded integers.

Transitions:

- `unknown` + success → `up`.
- `unknown` + `consecutive_failures >= confirmation_threshold` → `down`.
- `up` + `consecutive_failures >= confirmation_threshold` → `down`.
- `down` + `consecutive_successes >= recovery_threshold` → `up`.
- Otherwise unchanged.

Notes:

- The first successful probe moves `unknown → up` immediately, regardless of `recovery_threshold`.
- A monitor that fails from birth still goes `unknown → down` after the confirmation threshold (spec §7.4).
- Paused monitors keep status and counters. Resume sets `next_check_at = now()` and probing continues from the preserved counters.
- Transition events are recorded in `security_events`? **No** — that table is for abuse signals. Plan 2 exposes transitions by writing a structured log line (`monitor_transition`, previous/new status, monitor_id, workspace_id). Plan 4 replaces this with `incident_events` in the same transaction.
- Latency never changes state in V1.

## 8. Scheduler and River

### 8.1 River setup

Add `github.com/riverqueue/river v0.51.0`, `riverdriver/riverpgxv5`, and `rivermigrate`.

- `db.Migrate` runs, in order: a process-wide Postgres advisory lock → goose migrations → River migrations (`rivermigrate.DirectionUp`, target latest) → probe partition maintenance → unlock.
- All three binaries call `db.Migrate` at startup; the advisory lock makes this safe when compose starts them together.
- River uses the default schema (`river_*` tables) and is not managed by goose.

### 8.2 Job type

```go
type ProbeArgs struct {
    MonitorID   uuid.UUID `json:"monitor_id" river:"unique"`
    ScheduledFor time.Time `json:"scheduled_for" river:"unique"`
}
func (ProbeArgs) Kind() string { return "probe" }
```

Unique options: `ByArgs: true`, states `available, scheduled, running, pending`. Max attempts 3. Queue `probe`. No secrets in args.

### 8.3 Tick algorithm

Every `O1_SCHEDULER_TICK` (default 10s), in one transaction:

1. Select up to `O1_SCHEDULER_BATCH` (default 500) rows:
   ```sql
   SELECT m.id, m.interval_seconds
   FROM monitor_state ms
   JOIN monitors m ON m.id = ms.monitor_id
   JOIN workspaces w ON w.id = m.workspace_id
   WHERE ms.next_check_at <= now()
     AND m.paused = false
     AND w.suspended_at IS NULL
   ORDER BY ms.next_check_at
   LIMIT $1
   FOR UPDATE OF ms SKIP LOCKED;
   ```
2. For each row, compute `next := now() + interval_seconds + jitter(0..10%)`.
3. Insert the River job with `ScheduledAt = now()` and unique args for `next.Unix()`.
4. Update `monitor_state.next_check_at = next`.
5. Commit. Duplicate unique inserts are ignored.

No overlapping ticks: a tick that runs long delays the next one rather than stacking. Errors are logged and retried on the next tick.

### 8.4 Global probe rate

The worker holds a shared `golang.org/x/time/rate.Limiter` at `O1_PROBE_GLOBAL_RPS` (default 50). Before each probe it waits for a token; waiting respects job context cancellation. This is the "total probes/second" M1 control from spec §7.11.

## 9. Worker

`cmd/worker` starts a River client with the probe worker registered.

Job flow:

1. Load monitor + state + workspace in one query.
2. If the monitor is missing, paused, or the workspace is suspended, return success without writing a result.
3. Decrypt headers/body. Decryption failure is an internal error: the job retries, then is discarded and logged. It never produces a `down` result.
4. If a result already exists for `(monitor_id, scheduled_for)`, return success (at-least-once delivery).
5. Execute the probe.
6. In one transaction: insert `probe_results`, apply the state machine, update `monitor_state`.
7. Log the transition when status changes.
8. Return nil for any probe outcome; return an error only for DB/internal failures.

Worker concurrency: `O1_WORKER_CONCURRENCY` (default 50), queue `probe`. Job timeout = 30s (the maximum monitor timeout) to guarantee a wedged job cannot outlive its probe budget.

## 10. API contracts

Base path `/api/v1`. All responses are JSON. Errors are RFC 9457 `application/problem+json` with existing `code` values plus new ones: `not_found`, `workspace_suspended`, `quota_exceeded`, `monitor_rate_limited` (the generic `rate_limited` stays for middleware).

Role rules: `viewer` reads; `member`, `admin`, `owner` mutate. Non-members get `404` for every route, including admin-scoped lookups of other tenants.

### 10.1 Monitor object

```json
{
  "id": "uuid",
  "workspace_id": "uuid",
  "name": "API health",
  "url": "https://example.com/health?token=...",
  "method": "GET",
  "headers": {"Authorization": "***"},
  "has_body": false,
  "content_type": null,
  "expected_status": "2xx",
  "interval_seconds": 60,
  "timeout_seconds": 10,
  "confirmation_threshold": 2,
  "recovery_threshold": 2,
  "paused": false,
  "status": "up",
  "last_check": {
    "checked_at": "2026-11-04T10:00:00Z",
    "status_code": 200,
    "latency_ms": 123,
    "error": null
  },
  "summary_24h": {
    "uptime": 0.9995,
    "checks": 1440,
    "up": 1439,
    "down": 1,
    "avg_latency_ms": 120,
    "p95_latency_ms": 250
  },
  "created_at": "2026-11-04T09:00:00Z",
  "updated_at": "2026-11-04T09:00:00Z"
}
```

`status` is `paused` when `paused` is true, else `unknown`/`up`/`down` from `monitor_state`. `summary_24h` is computed from raw results and is `null` when there are no results (no-data is excluded from uptime, per spec §7.8).

### 10.2 Endpoints

| Method | Path | Role | Behavior |
|---|---|---|---|
| POST | `/workspaces/{workspaceID}/monitors` | member+ | Create monitor, state row, security event, `next_check_at = now()`. 201. |
| GET | `/workspaces/{workspaceID}/monitors` | viewer+ | Cursor-paginated list (`limit` default 50, max 100), newest first, with 24h summary. |
| GET | `/workspaces/{workspaceID}/monitors/{monitorID}` | viewer+ | Full monitor object. |
| PATCH | `/workspaces/{workspaceID}/monitors/{monitorID}` | member+ | Partial update; `headers` replaces the map; validates URL/limits; 200. |
| DELETE | `/workspaces/{workspaceID}/monitors/{monitorID}` | member+ | Hard delete, cascade; 204. |
| POST | `/workspaces/{workspaceID}/monitors/{monitorID}/pause` | member+ | Idempotent pause; 200. |
| POST | `/workspaces/{workspaceID}/monitors/{monitorID}/resume` | member+ | Idempotent resume, `next_check_at = now()`; 200. |
| GET | `/workspaces/{workspaceID}/monitors/{monitorID}/results` | viewer+ | Raw results, newest first; `window=1h\|6h\|24h\|7d` (default 24h), `limit` default 100/max 500, cursor. |
| GET | `/workspaces/{workspaceID}/monitors/{monitorID}/summary` | viewer+ | `{window, checks, up, down, uptime, avg_latency_ms, p95_latency_ms, min_latency_ms, max_latency_ms}`; `uptime` null when `checks == 0`. |
| GET | `/admin/workspaces` | admin | List/search workspaces (`query`, `suspended`, cursor, limit 50/max 100). |
| POST | `/admin/workspaces/{workspaceID}/suspend` | admin | Body `{reason}` (1–500 chars); sets suspension fields, records event; 200. |
| POST | `/admin/workspaces/{workspaceID}/unsuspend` | admin | Clears suspension, records event; 200. |
| GET | `/admin/monitors` | admin | List monitors across tenants; `workspace_id`, `query`, cursor, limit; URLs redacted (query stripped). |
| GET | `/admin/security-events` | admin | `kind`, `workspace_id`, cursor, limit; newest first. |

Admin routes live under `/api/v1/admin` and require `requireSession` plus `requireAdmin`. Non-admin authenticated users get `403 forbidden`.

### 10.3 Create/update request

```json
{
  "name": "API health",
  "url": "https://example.com/health",
  "method": "GET",
  "headers": {"Authorization": "Bearer secret"},
  "body": null,
  "content_type": null,
  "expected_status": "2xx",
  "interval_seconds": 60,
  "timeout_seconds": 10,
  "confirmation_threshold": 2,
  "recovery_threshold": 2
}
```

Validation errors return `400` with `code=validation_error` and `fields` keyed by JSON field (`url`, `headers`, `interval_seconds`, ...). Quota returns `409 quota_exceeded`; creation rate limit returns `429 monitor_rate_limited` with `Retry-After` computed from the oldest creation in the window.

### 10.4 Error semantics

- Cross-tenant or missing monitor/workspace → `404 not_found`.
- Suspended workspace mutation → `403 workspace_suspended` with `detail` explaining reads still work.
- Validation → `400 validation_error`.
- Quota → `409 quota_exceeded`.
- Creation rate → `429 monitor_rate_limited` + `Retry-After`.
- Unknown internals → `500 internal_error`, no detail leakage.

Cursor format is base64url(JSON `{ "created_at": ..., "id": ... }`) for monitors and `{ "checked_at": ..., "id": ... }` for results; invalid cursors return `400 validation_error` with `fields.cursor`.

## 11. Abuse controls

### 11.1 Quota

Inside the create transaction, after `SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`:

- Load `monitor_limit` (default 5) and count monitors for the workspace.
- If `count >= monitor_limit`, return `ErrQuotaExceeded`.

The workspace row lock serializes concurrent creates in that workspace. Since monitors are hard-deleted, delete immediately frees a slot.

### 11.2 Creation rate limit

In the same transaction, count monitors created in the last hour. If `>= 10` (configurable `O1_MONITOR_CREATE_LIMIT`), return `ErrRateLimited` with the oldest `created_at + 1h`.

Because deleted monitors disappear, a create/delete loop could evade the hourly limit. Mitigation: the `monitor_created` security event and the `security_events` count are the durable signal; Plan 5 adds alerting on repeated rate hits. (Quota still caps concurrent monitors at 5, so the loop cannot multiply probes.)

### 11.3 SSRF signals

- Creation/update rejection records `ssrf_blocked` with redacted detail.
- Dial-time or redirect-time rejection during probing is recorded as a `down` result with error class `ssrf_blocked`, and a `security_events` row.
- The admin monitor list shows redacted URLs and current status; the security-events endpoint surfaces repeated blocks per workspace.

### 11.4 Suspension

- `suspended_at` non-null excludes the workspace from the scheduler and makes the worker no-op for its monitors.
- Mutations under a suspended workspace return `403 workspace_suspended`.
- Reads remain available.
- Suspend/unsuspend requires admin and records a security event with actor and reason.

## 12. Configuration

New variables (all `O1_` prefixed):

| Variable | Default | Notes |
|---|---|---|
| `O1_ENCRYPTION_KEY` | — | base64 32 bytes; required in production |
| `O1_MONITOR_MIN_INTERVAL` | `60s` | enforced |
| `O1_MONITOR_MAX_INTERVAL` | `24h` | enforced |
| `O1_MONITOR_CREATE_LIMIT` | `10` | per workspace per hour |
| `O1_PROBE_ALLOWED_PORTS` | `80,443` | comma list |
| `O1_PROBE_MAX_REDIRECTS` | `5` | |
| `O1_PROBE_MAX_BODY_BYTES` | `1048576` | drain cap |
| `O1_PROBE_GLOBAL_RPS` | `50` | worker-wide |
| `O1_SCHEDULER_TICK` | `10s` | |
| `O1_SCHEDULER_BATCH` | `500` | |
| `O1_WORKER_CONCURRENCY` | `50` | |
| `O1_SCHEDULER_HEALTH_ADDR` | `:8081` | scheduler health server |
| `O1_WORKER_HEALTH_ADDR` | `:8082` | worker health server |
| `O1_BOOTSTRAP_ADMIN_EMAIL` | — | promote existing verified user at startup |

`config.Load` validates durations, integer ranges, ports (1–65535, non-empty), and the encryption key rules (production requires explicit key; dev/test may derive from `O1_SECRET_KEY`).

## 13. Lifecycle, health, and deployment

- `internal/app` provides: load config → logger → pool → migrations → return a `Runtime` with `Close`.
- `api`: unchanged behavior plus migrations now include River and partitions.
- `scheduler`: health server with `/healthz` (process alive) and `/readyz` (DB ping); tick loop; graceful stop waits for the in-flight tick.
- `worker`: health server with `/healthz` and `/readyz` (DB ping); River client `Start`; graceful stop via `Stop(ctx)` with a 30s timeout, then pool close.
- Docker image builds `/api`, `/scheduler`, `/worker` from one builder stage. `ENTRYPOINT ["/api"]`; compose overrides `command` for the other two.
- `compose.yaml` adds `scheduler` and `worker` services (same image, `depends_on: postgres: condition: service_healthy`).
- Shutdown order per binary: stop accepting work → drain → close River → close pool.
- Logs are structured JSON with `request_id` for HTTP and `monitor_id`/`workspace_id` for probe work; never log secrets, header values, bodies, or unredacted query strings.

## 14. Testing strategy

### Unit (no Docker, `go test -short`)

- SSRF classification table: all blocked CIDRs, IPv4-mapped IPv6, ports, schemes, userinfo, fragments, header `Host` rejection, header/body limits.
- Redirect policy: max hops, private redirect target, cross-host header stripping.
- Expected-status parser/evaluator: classes, ranges, lists, invalid input.
- State machine: every transition, thresholds 1 and 5, counter caps, paused/resume preservation.
- Uptime/latency summary math including zero-data.
- Secretbox: round-trip, tamper detection, version rejection, nonce uniqueness.
- Monitor validation and config parsing.

### Integration (testcontainers Postgres)

- Migrations apply cleanly; partitions for the expected months exist.
- Monitor CRUD with RBAC: viewer cannot mutate; member/admin/owner can; cross-tenant routes return 404.
- Quota: the 6th monitor returns `409`; after delete the slot frees.
- Creation rate limit: 11th creation in an hour returns `429` with `Retry-After`.
- Encryption at rest: raw `headers_enc`/`body_enc` columns contain no plaintext; API masks values.
- Scheduler tick: due/unpaused/unsuspended monitors get exactly one River job, `next_check_at` advances, paused/suspended/future rows are skipped; a second tick does not duplicate.
- Worker against `httptest`: success, wrong status, timeout, TLS failure (using a self-signed test server), redirect to loopback is blocked, result row + state transition are atomic.
- Idempotency: replaying the same `(monitor_id, scheduled_for)` does not duplicate results or counters.
- Dashboard aggregates: uptime excludes no-data, p95 matches seeded data, cursor pagination is stable.
- Admin: suspend blocks mutation and probing; unsuspend restores; non-admin gets 403; actions create security events.

River client itself is smoke-tested once (insert + start + wait for completion). Scheduler/worker logic is tested by calling the tick function and `ProbeWorker.Work` directly, avoiding timing-dependent queue drains in most tests.

## 15. Risks and mitigations

| Risk | Mitigation |
|---|---|
| DNS rebinding bypasses creation-time checks | Dial-time IP classification; connect to the checked numeric IP. |
| Redirect-based SSRF | Validate every hop; reject non-public targets; strip user headers cross-host. |
| Secret leakage via API/logs | Encrypt at rest; mask header values; redact URL query strings; structured log tests. |
| Scheduler double-enqueue after crash | Unique River args + `FOR UPDATE SKIP LOCKED` + same-transaction schedule advance. |
| River migrations racing across binaries | Process-wide Postgres advisory lock around all migrations. |
| Partition missing at month rollover | Create `[prev, +2 months]` at startup and daily in the scheduler; insert fails loudly if maintenance ever stops. |
| Probe body memory blowup | 1 MiB drain cap; never buffer the full body. |
| Quota race | Workspace row lock around count + insert. |

## 16. Definition of done for Plan 2

- `go test -count=1 ./...`, `go test -short ./...`, `go vet ./...`, `golangci-lint run`, `gofmt` clean.
- `docker build -f backend/Dockerfile backend` succeeds and contains all three binaries.
- With `docker compose up`, `api`, `scheduler`, and `worker` start, run migrations once, and shut down cleanly on SIGTERM.
- A workspace can create a monitor, see it probed within one interval, and read status, results, and 24h summary through the API.
- SSRF attempts (loopback, private, link-local, redirect-to-private, non-allowed port) are rejected and recorded.
- Quota and hourly creation limits hold under concurrent requests.
- An admin can suspend a workspace; its probes stop and mutations return `workspace_suspended`; unsuspend restores both.
- No plaintext monitor headers or bodies exist in the database, logs, or API responses.
