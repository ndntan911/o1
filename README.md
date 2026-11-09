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
email verification, workspace and monitor management, incident
acknowledgement, public status page publishing, API key creation and
authorization, audit attribution, and administrator suspension.
With Compose running:

```sh
cd web
npx playwright install chromium
npx playwright test
```

To promote an administrator, register and verify the account, set
`O1_BOOTSTRAP_ADMIN_EMAIL` to that address, then recreate the API service as
described under [Administrator bootstrap](#administrator-bootstrap).

Incident history, email, webhook, Slack, and Telegram alerts, plus public status
pages, are available in the dashboard. See [Public status pages](#public-status-pages).

The dashboard supports Light, Dark, and System appearance. The choice is stored
in the browser, and System follows the operating system preference.

## Incidents and alerts

The probe worker opens an incident when a monitor crosses its confirmation
threshold and resolves it after the configured recovery threshold. Incidents
move through `open`, `acknowledged`, and `resolved`; open and acknowledged
incidents are active and appear on the Incidents page and on their monitor's
detail page. Resolved incidents remain in the history with their timeline and
duration.

Members can acknowledge an open incident from its detail page. Acknowledgement
records who acknowledged it and when, stops further reminder notifications, and
leaves the incident active until the monitor recovers. Viewers can read
incidents but cannot acknowledge them or change alert settings.

Workspace admins configure alerting under **Settings → Alerts**. Email requires
1–20 unique, valid recipient addresses when enabled; an empty list is allowed
only while email alerts are disabled. The same page manages three non-email
channel kinds:

- **Webhook** posts a JSON incident payload to any public `http` or `https` URL.
  An optional signing secret (16–128 characters) enables HMAC-SHA256
  authentication.
- **Slack** posts an incoming-webhook message to `https://hooks.slack.com` or
  `https://hooks.slack-gov.com`.
- **Telegram** sends a message through the Bot API using a bot token and a chat
  ID (numeric, negative for groups, or `@channelusername`).

Webhook and Slack URLs, the optional webhook signing secret, and the Telegram
bot token are write-only secrets. After saving, the API returns only a masked
preview or a "configured" indicator, and the plaintext value is never shown
again. Channel secrets are encrypted at rest and are never included in delivery
history, logs, or error messages. A channel name is unique among active
channels of its kind and can be reused after a channel is deleted.

Saving a channel attaches it to every workspace monitor. Every workspace
channel appears as a per-monitor switch on the monitor detail page, including
channels that are disabled globally; members can turn each switch on or off,
while viewers only see the section when they can read it. When an admin
disables a channel globally, the monitor switch shows that it is disabled for
the workspace. Each channel also has a **Test** action that sends a test
notification without creating a delivery row; disabled but complete channels
can be tested.

Webhook deliveries send `event` (`opened`, `reminder`, or `resolved`),
`sequence`, `workspace`, `monitor`, `incident`, and `link`. `monitor.status` is
`down` for open/reminder and `up` for resolved; `incident.status` is `open` or
`resolved`. When a signing secret is configured, each request carries
`X-O1-Event`, `X-O1-Timestamp`, and `X-O1-Signature`; the signature is
`sha256=<hex HMAC-SHA256(timestamp + "." + body)>` over the exact request body.
A receiver should verify the signature with the shared secret and reject stale
timestamps. Test sends use `event: "test"` with `monitor` and `incident` set to
`null`.

Each enabled incident notifies every enabled channel when it opens and when it
resolves. While an incident is open and unacknowledged, reminders repeat every
30 minutes, up to 10 reminders per incident. Acknowledging the incident stops
the reminder sequence. A failed delivery is retried up to five times for
transient errors (timeouts, 408, 425, 429, and 5xx); redirects and other 4xx
responses are permanent failures. Admins can see the delivery status, channel,
and attempt count on the incident detail page.

Webhook and Slack destinations are validated against the SSRF policy both when
saved and before every send, and deliveries never follow redirects. This V1
blocks loopback, private, link-local, and metadata addresses, so internal
webhook receivers are not supported yet. A 3xx response from a webhook or Slack
endpoint is treated as a permanent failure.

Development uses Mailpit as the SMTP sink. Compose starts Mailpit at
<http://localhost:8025>, where every alert email and its rendered content can be
inspected. A production email sender is not implemented yet: the worker refuses
to start with `O1_ENV=production`, and production delivery remains a later
milestone. Paging integrations, Slack OAuth, custom webhook headers, maintenance
windows, and per-channel reminder schedules are outside the current scope.

## Public status pages

**Settings → Status page** enables one public page per workspace, sets its
title and description, and selects which monitors appear. The page is served at
`/status/{workspace-slug}`; administrators can copy or open that link directly
from the settings screen.

Visitors see the current status of each published monitor, 90 daily bars
grouped by UTC day, 90-day uptime, and active incidents plus incidents from the
last 7 days. No-data days, paused monitors, and unknown states are not counted
as downtime. The page never shows monitor URLs, headers, errors,
acknowledgements, or delivery data. Responses are cached for 15 seconds and
limited to 60 requests per minute per IP.

V1 limits: one page per workspace, no custom domains, no separate page slug, no
subscriber notifications, and no public incident detail pages. Suspended
workspaces keep the public page read-only.

## API keys

Workspace admins create and revoke script credentials under **Settings → API
keys**. A key has a name, a role, and a public prefix (`o1_<prefix>`); the full
secret is shown once in the creation panel, can be copied there, and is never
returned again. Only a SHA-256 hash and the prefix are stored, so a lost key
must be revoked and replaced.

Roles are capped at the workspace level:

- **Viewer** keys can read the workspace, monitors, results, and incidents.
- **Member** keys can also create, edit, pause, resume, and delete monitors,
  acknowledge incidents, and toggle per-monitor alert switches.

Neither role can manage members, alert channels, the status page, API keys, or
`/api/v1/admin/*`. Keys never carry global administrator rights; key management
itself always requires a browser session. A key is scoped to exactly one
workspace, so using a workspace A key against workspace B returns `404`.

Send the key with the Bearer scheme. The workspace ID is the UUID returned by
`GET /api/v1/me` or `GET /api/v1/workspaces`:

```sh
curl -H "Authorization: Bearer o1_..." \
  http://localhost:8080/api/v1/workspaces/{workspace-id}/monitors
```

An `Authorization` header is authoritative: a malformed, unknown, or revoked
key returns `401` and never falls back to a session cookie. Each key is limited
to 600 requests per minute (10 rps, burst 60), and `last_used_at` is updated at
most once a minute. A workspace can have 20 active keys, with unique names
among active keys. Revocation takes effect immediately; revoked keys disappear
from the list but keep their audit history.

V1 limits: no key expiry, rotation, custom scopes, admin/owner keys, IP
allowlists, or per-key usage analytics. Revoke and recreate is the rotation
path.

## Audit log

Global administrators can read the audit trail from
`GET /api/v1/admin/security-events` and from the dashboard's Security events
page. Each event records the workspace, the acting user (`actor_user_id`), the
acting API key when one was used (`actor_api_key_id`), the event kind, a
redacted detail object, and a timestamp. The page shows "via API key" when the
key attribution is present.

Recorded kinds:

- `login_succeeded` and `login_failed` (IP and reason only, never the email or
  password).
- `member_added` and `member_role_changed`.
- `monitor_created`, `monitor_create_rate_limited`, and `monitor_deleted`.
- `ssrf_blocked` for blocked monitor URLs and alert destinations.
- `alert_channel_created`, `alert_channel_updated`, and
  `alert_channel_deleted`.
- `status_page_updated`.
- `api_key_created` and `api_key_revoked`.
- `workspace_suspended` and `workspace_unsuspended`.

Key-driven monitor creation, rate-limit, SSRF, and deletion events carry
`actor_api_key_id`; worker dial-time SSRF events have no key attribution.
Audit detail contains workspace-owned identifiers and names only. It never
includes passwords, session tokens, API key secrets or hashes, raw URLs with
query strings, headers, or request bodies. Audit writes are best-effort: a
failed insert is logged at warning level and never fails the action that was
being recorded. Incidents keep their own timeline and probe state separately,
so those records are not duplicated here.

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
