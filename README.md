# o1

Uptime and incident monitoring for small teams. Go backend, PostgreSQL, and a
React dashboard. This repository ships authentication, workspaces and RBAC,
monitor CRUD, SSRF-guarded probing, scheduling, and administrator controls
behind the dashboard.

## Architecture

Three binaries share one image and one database:

- `api` serves the HTTP API and health endpoints on `:8080`.
- `scheduler` finds due monitors and enqueues River probe jobs. Health on
  `:8081`.
- `worker` executes probe jobs and advances monitor state. Health on `:8082`.

All three run database migrations at startup. A PostgreSQL advisory lock
serializes migrations, so they can be started concurrently.

Probe results are stored in monthly range partitions. The scheduler creates
partitions for the previous month through two months ahead and refreshes them
daily.

## Quick start

```sh
docker compose up --build
```

This starts PostgreSQL, Mailpit, the API, the scheduler, the worker, and the
dashboard. The dashboard listens on <http://localhost:5173>, the API on
<http://localhost:8080>, and Mailpit's web UI on <http://localhost:8025>.

Health endpoints:

```sh
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

The compose file uses a development-only `O1_SECRET_KEY`. Never use it outside
local development.

## Dashboard

`docker compose up --build` serves the dashboard at
<http://localhost:5173> and the API at <http://localhost:8080>. The web
container (Caddy) serves the single-page app and proxies `/api` to `api:8080`
on the same origin, so no CORS setup or cookie changes are needed.

Local frontend development uses Vite and proxies `/api` to the Compose API:

```sh
cd web
npm install
npm run dev
```

Frontend checks live under `web/`:

```sh
cd web
npm run format:check
npm run lint
npm run typecheck
npm test -- --run
npm run build
```

The Playwright smoke test drives the deployed stack through registration,
email verification, workspace and monitor management, and administrator
suspension. With Compose running:

```sh
cd web
npx playwright install chromium
npx playwright test
```

To promote an administrator, register and verify the account, set
`O1_BOOTSTRAP_ADMIN_EMAIL` to that address, then recreate the API service as
described under [Administrator bootstrap](#administrator-bootstrap).

Incident history and email alerts are available in the dashboard. Public status
pages and additional alert channels (such as webhooks) are later milestones.

## Incidents and email alerts

The probe worker opens an incident when a monitor crosses its confirmation
threshold and resolves it after the configured recovery threshold. Incidents
move through `open`, `acknowledged`, and `resolved`; open and acknowledged
incidents are active and appear on the Incidents page and on their monitor's
detail page. Resolved incidents remain in the history with their timeline and
duration.

Members can acknowledge an open incident from its detail page. Acknowledgement
records who acknowledged it and when, stops further reminder emails, and leaves
the incident active until the monitor recovers. Viewers can read incidents but
cannot acknowledge them or change alert settings.

Workspace admins configure alerting under **Settings → Alerts**. The email
channel requires 1–20 unique, valid recipient addresses when enabled; an empty
list is allowed only while email alerts are disabled. Saving the channel
attaches it to every workspace monitor that does not have a per-monitor setting
yet. Members can turn **Email alerts** off or on for an individual monitor from
that monitor's detail page; the toggle is unavailable to viewers. When an admin
disables the workspace email channel, the monitor page shows that email alerts
are disabled for the workspace and disables the per-monitor switch. When no
email channel exists yet, the monitor page explains that alerts are not
configured and links admins to the settings screen.

Each enabled incident sends an email to every recipient when it opens and when
it resolves. While an incident is open and unacknowledged, reminders repeat
every 30 minutes, up to 10 reminders per incident. Acknowledging the incident
stops the reminder sequence. Admins can see the delivery status for each
recipient on the incident detail page.

Development uses Mailpit as the SMTP sink. Compose starts Mailpit at
<http://localhost:8025>, where every alert email and its rendered content can be
inspected. A production email sender is not implemented yet: only email channels
are supported in Plan 4, the worker refuses to start with `O1_ENV=production`,
and production delivery remains a later milestone. Other channels, paging
integrations, and public status pages are outside the Plan 4 scope.

## Configuration

Configuration is read from environment variables prefixed with `O1_`.

Required:

- `O1_DATABASE_URL` — PostgreSQL connection string.
- `O1_SECRET_KEY` — base64-encoded, exactly 32 bytes. Used for session and
  key-derivation material. Generate with `openssl rand -base64 32`.
- `O1_ENCRYPTION_KEY` — base64-encoded, exactly 32 bytes. Required in
  production and used to encrypt monitor headers and request bodies with
  AES-256-GCM. In development and tests it is derived from `O1_SECRET_KEY` when
  unset. Generate with `openssl rand -base64 32`.

Common optional settings:

- `O1_ENV` — `development` (default), `test`, or `production`.
- `O1_HTTP_ADDR` — API listen address, default `:8080`.
- `O1_SCHEDULER_HEALTH_ADDR` — default `:8081`.
- `O1_WORKER_HEALTH_ADDR` — default `:8082`.
- `O1_APP_BASE_URL` — dashboard origin used in verification and reset links,
  default `http://localhost:5173`.
- `O1_ALLOWED_ORIGINS` — comma-separated Origin allowlist for unsafe requests.
  Defaults to `O1_APP_BASE_URL`. Compose forwards this variable; leave it empty
  to use the dashboard origin.
- `O1_SMTP_ADDR` and `O1_EMAIL_FROM` — transactional email. Development uses
  Mailpit. A production email sender is not implemented yet, and the API
  refuses to start with `O1_ENV=production`.
- `O1_MONITOR_MIN_INTERVAL` / `O1_MONITOR_MAX_INTERVAL` — default `60s` /
  `24h`.
- `O1_MONITOR_CREATE_LIMIT` — monitor creations per workspace per hour,
  default `10`.
- `O1_PROBE_ALLOWED_PORTS` — comma-separated ports, default `80,443`.
- `O1_PROBE_GLOBAL_RPS` — global probe rate limit, default `50`.
- `O1_SCHEDULER_TICK` / `O1_SCHEDULER_BATCH` — default `10s` / `500`.
- `O1_WORKER_CONCURRENCY` — probe workers per process, default `50`.
- `O1_BOOTSTRAP_ADMIN_EMAIL` — promotes an existing verified account to
  administrator at startup.

## Running without Docker

The backend needs Go 1.26 and a PostgreSQL 16 database. Docker is required only
for integration tests (testcontainers).

```sh
cd backend
go build ./...
go run ./cmd/api
go run ./cmd/scheduler
go run ./cmd/worker
```

Each process needs the same `O1_DATABASE_URL` and `O1_SECRET_KEY`. Run the
binaries in separate terminals or under a process supervisor.

## Administrator bootstrap

1. Register and verify the account through the normal signup flow.
2. Set `O1_BOOTSTRAP_ADMIN_EMAIL` to that address.
3. Restart the API (or any binary). The account is promoted only if it exists
   and its email is verified.

With Docker Compose, put `O1_BOOTSTRAP_ADMIN_EMAIL` in `.env` and recreate the
services so the new environment is applied:

```sh
docker compose up -d --force-recreate api scheduler worker
```

Compose only forwards `.env` values into containers when the variable is
referenced in `compose.yaml`; `O1_BOOTSTRAP_ADMIN_EMAIL` is forwarded by the
shared backend environment.

Administrators can list workspaces, suspend and unsuspend workspaces, inspect
monitors across tenants, and read the security-event audit trail under
`/api/v1/admin/*`. Suspension blocks monitor mutations and probes while leaving
reads, login, and billing-style access available.

Administrator endpoints:

```text
GET  /api/v1/admin/workspaces
POST /api/v1/admin/workspaces/{workspaceID}/suspend
POST /api/v1/admin/workspaces/{workspaceID}/unsuspend
GET  /api/v1/admin/monitors
GET  /api/v1/admin/security-events
```

Suspension requires a JSON body such as `{"reason": "abuse"}`.

## Development checks

```sh
cd backend
gofmt -l .
go vet ./...
go test -short ./...
go test -count=1 ./...
golangci-lint run
docker build -f Dockerfile .
```

From the repository root:

```sh
docker compose config
```

Integration tests start disposable PostgreSQL 16 containers and are skipped in
`-short` mode.

## Security notes

- Monitor headers, bodies, and other tenant secrets are encrypted at rest.
  They are never returned in API responses; header values are masked as `***`.
- The SSRF guard validates schemes, ports, userinfo, fragments, and every
  resolved address at validation and dial time, including redirects.
- Cross-tenant requests return `404` so resource existence is not leaked.
- Failed probes (timeouts, TLS errors, status mismatches) are recorded as
  results, not as failed queue jobs.
- The API emits RFC 9457 `application/problem+json` errors with stable codes.
