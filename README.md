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
local development. Development is plain HTTP on <http://localhost:5173>;
production TLS and the standalone production stack are covered in
[Production deployment](#production-deployment).

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
authorization, audit attribution, administrator suspension, and TOTP
enrollment, two-step login, recovery-code use, and disabling.
With Compose running:

```sh
cd web
npx playwright install chromium
npx playwright test
```

`E2E_BASE_URL` (default `http://localhost:5173`) points the browser at the web
app. `E2E_API_URL` (default `http://localhost:8080`) is the public API listener
used for direct-port assertions; set both when Compose publishes different
host ports.

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
inspected. Production sends through Resend: the production stack sets
`O1_EMAIL_PROVIDER=resend`; when running the API outside that stack, set it
yourself along with a Resend API key and a from address on the verified sending
domain. Production refuses to start with the `mailpit` provider or without a
Resend key.

Transient send failures (timeouts, `408`, `425`, `429`, and `5xx`) are retried
with short backoff and an idempotency key, so a lost response does not duplicate
a message. Permanent failures such as authentication errors and other `4xx`
responses are recorded as failed deliveries and are not retried. The Resend free
tier has daily and monthly quotas; size the plan for incident volume. See
[Configuration](#configuration) and the
[production runbook](deploy/production/README.md).

Paging integrations, Slack OAuth, custom webhook headers, maintenance
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

Send the key with the Bearer scheme. The workspace UUID comes from the dashboard
URL or workspace settings, or from a browser-session call to `GET /api/v1/me`
or `GET /api/v1/workspaces`:

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

## Two-factor authentication

Two-factor authentication (2FA) is opt-in per user under **Security** in the
account menu (`/account/security`). Each user enrolls their own account; there
is no workspace-wide enforcement in V1.

Enrollment uses a standard TOTP authenticator app (SHA-1, six digits, 30-second
period). Starting setup requires the account password. The page then shows a QR
code and the base32 secret for manual entry. Confirming enrollment requires the
password again plus a current code. The ten single-use recovery codes are
shown once and are never retrievable again; only hashes are stored. Enabling
2FA revokes every other session while keeping the current one.

Sign-in becomes two steps. After the password, the API returns a short-lived
challenge instead of a session cookie, and the dashboard asks for either the
current authenticator code or a recovery code. A challenge expires after five
minutes and is consumed after five failed attempts. Both codes are single use:
used TOTP time steps are recorded so a code cannot be replayed, and each
recovery code is marked used atomically. Regenerating recovery codes with the
password and one factor replaces all remaining codes and invalidates the old
ones.

Disabling 2FA and regenerating recovery codes both require the password plus
exactly one factor: a current authenticator code or a recovery code. Disabling
clears the active secret, pending setup, recovery codes, and login challenges,
then revokes other sessions. A password reset preserves an enabled 2FA
configuration and clears any pending setup or challenge.

API keys bypass TOTP entirely, so automation keeps working while browser
sessions require the second factor. V1 has no administrator reset, trusted
devices, passkeys, or recovery by support request; a user who loses both the
authenticator and all recovery codes loses browser access.

The audit trail records `totp_enabled`, `totp_disabled`, and
`totp_recovery_codes_regenerated`. These events carry the acting user and never
contain the secret, a TOTP code, or a recovery code.

## Audit log

Global administrators can read the audit trail from
`GET /api/v1/admin/security-events` and from the dashboard's Security events
page. Each event records the workspace, the acting user (`actor_user_id`), and
the acting API key when one was used (`actor_api_key_id`), where applicable,
plus the event kind, a redacted detail object, and a timestamp. Login events
are global (no workspace), and failed logins may have no actor; their detail
carries only the request IP and a redacted failure reason. The page shows "via
API key" when the key attribution is present.

Recorded kinds:

- `login_succeeded` and `login_failed` (IP and reason only, never the email or
  password).
- `member_added` and `member_role_changed`.
- `monitor_created`, `monitor_create_rate_limited`, and `monitor_deleted`.
- `ssrf_blocked` for blocked monitor URLs and alert destinations.
- `alert_channel_created`, `alert_channel_updated`, and
  `alert_channel_deleted` (including the workspace email channel).
- `status_page_updated`.
- `api_key_created` and `api_key_revoked`.
- `totp_enabled`, `totp_disabled`, and `totp_recovery_codes_regenerated`.
- `workspace_suspended` and `workspace_unsuspended`.

Key-driven monitor creation, rate-limit, SSRF, and deletion events carry
`actor_api_key_id`; worker dial-time SSRF events have no key attribution.
Audit detail contains workspace-owned identifiers and names, redacted request
metadata such as the login IP and failure reason, and simple booleans or counts.
It never includes passwords, session tokens, API key secrets or hashes, raw URLs
with query strings, headers, or request bodies. Audit writes are best-effort: a
failed insert is logged at warning level and never fails the action that was
being recorded. Incidents keep their own timeline and probe state separately,
so those records are not duplicated here.

## Observability

Metrics, traces, and error reporting are optional. With every observability
variable empty the stack starts normally: no exporter or Sentry client is
created, no data leaves the host, and no endpoint is required.

### Metrics endpoints

- The API serves Prometheus metrics on an internal listener
  (`O1_API_HEALTH_ADDR`, default `:8083`) that Compose does not publish.
- The scheduler and worker serve metrics on their health listeners (`:8081`
  and `:8082`), which Compose publishes on `127.0.0.1` only.
- All three expose `/healthz`, `/readyz`, `/metrics`, the default Go/process
  collectors, and `o1_build_info{service,version}`. The HTTP, probe, alert, and
  scheduler metrics live in the process that records them. The worker also
  refreshes the `o1_queue_jobs`, `o1_queue_oldest_job_age_seconds`, and
  `o1_incidents_open` gauges in the background; the API and scheduler do not
  run those collectors.
- Caddy proxies `/healthz` and `/readyz` to `api:8080` and responds `404` for
  `/metrics`. Metrics are never public, including on the public API listener.

Inspect an internal listener from the Compose network. The API image is
distroless, so use the `web` container (Caddy on Alpine):

```sh
docker compose exec -T web wget -qO- http://api:8083/metrics
docker compose exec -T web wget -qO- http://scheduler:8081/metrics
docker compose exec -T web wget -qO- http://worker:8082/metrics
```

Metric labels are bounded enums and templates: method, route pattern (for
example `GET /api/v1/workspaces/{workspaceID}/monitors/{monitorID}`), status
code, queue/state, channel kind, and result. Raw paths, UUIDs, workspace or
monitor IDs, URLs, query strings, and email addresses never become labels, so
cardinality stays predictable and tenant data stays out of Prometheus.

### Tracing and error reporting

- Tracing uses OTLP/HTTP when `O1_OTEL_EXPORTER_OTLP_ENDPOINT` is set. An empty
  endpoint disables tracing without making export attempts. Logs add
  `trace_id` and `span_id` when a span is active.
- Sentry error capture is enabled by `O1_SENTRY_DSN` on the backend and by the
  `VITE_SENTRY_DSN` web build arg. Request bodies, headers, cookies, query
  strings, breadcrumb URLs, and user emails are stripped before sending. Sentry
  performance is disabled because OTel owns traces.
- Frontend DSN and release are baked into the static bundle at build time, not
  read at runtime.

Backend environment variables:

- `O1_API_HEALTH_ADDR` — internal API health/metrics listener, default `:8083`.
- `O1_SCHEDULER_HEALTH_ADDR` — scheduler health/metrics listener, default
  `:8081`.
- `O1_WORKER_HEALTH_ADDR` — worker health/metrics listener, default `:8082`.
- `O1_OTEL_EXPORTER_OTLP_ENDPOINT` — OTLP/HTTP traces endpoint; empty disables
  tracing.
- `O1_OTEL_EXPORTER_OTLP_HEADERS` — comma-separated `key=value` auth headers,
  never logged.
- `O1_OTEL_SAMPLE_RATIO` — trace sampler ratio, default `0.05`.
- `O1_SENTRY_DSN` — backend Sentry DSN; empty disables Sentry.
- `O1_SENTRY_ENVIRONMENT` — defaults to `O1_ENV`.
- `O1_RELEASE` — service version for traces, Sentry, and build info; default
  `dev`.

Frontend build args:

- `VITE_SENTRY_DSN` — frontend Sentry DSN; empty disables Sentry.
- `VITE_RELEASE` — frontend release; default `dev`.

### Deploy bundle

`deploy/observability/` contains the Grafana Alloy config, Prometheus alert
rules, Grafana dashboard JSON, and the full runbook. Start Alloy with the
optional Compose profile:

```sh
docker compose --profile observability up -d alloy
```

For the production stack the equivalent command is in
[deploy/production/README.md](deploy/production/README.md); it mounts the same
`deploy/observability/alloy.alloy` configuration.

Alloy joins the Compose network, scrapes `api:8083`, `scheduler:8081`, and
`worker:8082` every 15 seconds, and remote-writes to Grafana Cloud. A normal
`docker compose up` does not start it, and Alloy exits with a clear error if
`GRAFANA_REMOTE_WRITE_URL`, `GRAFANA_REMOTE_WRITE_USER`, or
`GRAFANA_REMOTE_WRITE_TOKEN` is empty.

Grafana Alloy (`observability` profile):

- `GRAFANA_REMOTE_WRITE_URL` — Grafana Cloud Prometheus remote-write endpoint.
- `GRAFANA_REMOTE_WRITE_USER` — numeric Grafana Cloud user/instance ID.
- `GRAFANA_REMOTE_WRITE_TOKEN` — access-policy token with `metrics:write`.

Import the dashboard JSON in Grafana (**Dashboards → New → Import**, UID
`o1-observability`) and load the alert rules with `mimirtool rules load`.
Panels cover request rate/errors/latency, queue depth and age, probes, alert
deliveries, incidents, database pool, scheduler ticks, and process resources.
The rules include instance-down, missing-metrics, error-rate, latency, queue
backlog/age, discarded jobs, probe failure, alert failure, pool saturation,
and scheduler-stalled alerts.

Alerting inside Grafana cannot report that the whole host or scrape path is
gone. Point an external dead-man's switch (a free healthchecks.io check) at
`https://<host>/healthz` every minute with a two-minute grace period. Caddy
routes `/healthz` to the API, so the switch fails when the API is unavailable
instead of receiving the SPA shell; `/readyz` adds database readiness for load
balancers. Never point it at `/metrics`, which is private and returns `404`.

See [deploy/observability/README.md](deploy/observability/README.md) for
Grafana Cloud setup, exact dashboard/rule import commands, Sentry project
setup, retention/budget guidance, and troubleshooting.

The Playwright smoke suite also covers this contract from inside Compose: the
internal metrics listeners contain the catalog metrics, public `/metrics`
returns `404`, and `/healthz` and `/readyz` return API JSON rather than the
SPA.

## Production deployment

`deploy/production/` contains a standalone production Compose stack. It does
not inherit the development `compose.yaml`: there is no Mailpit, no published
PostgreSQL/API/metrics port, no development key, and no raised auth rate limit.
Only the `web` container publishes ports `80` and `443`; Caddy serves the SPA,
proxies the API on the same origin, and obtains and renews the TLS certificate.

```sh
cp deploy/production/env.example deploy/production/.env
$EDITOR deploy/production/.env
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml \
  -f deploy/production/compose.build.yaml up -d --build
```

The base `compose.yaml` pulls digest-pinned registry images when an
`images.env` supplies them; without it the local image defaults apply. Any
manual or offline deploy from a checkout also passes `compose.build.yaml`,
which adds the `build:` contexts and local image names; `up -d --build` without
it fails on a fresh checkout.

`O1_DOMAIN` is the public hostname. The stack derives `O1_APP_BASE_URL` and
`O1_ALLOWED_ORIGINS` from it and sets the web container's `O1_SITE_ADDRESS` to
it, so Caddy serves HTTPS for that name. `O1_SITE_ADDRESS` defaults to `:80`;
the development Compose explicitly sets that value because it serves plain HTTP
on <http://localhost:5173>.

The full runbook covers DNS and firewall prerequisites, Resend domain
verification and SPF/DKIM, secret generation, the first deploy, health and TLS
checks, administrator bootstrap, email smoke tests, logs, updates, and
troubleshooting: [deploy/production/README.md](deploy/production/README.md).

Production email is Resend only. `O1_ENV=production` requires
`O1_ENCRYPTION_KEY` and rejects `O1_EMAIL_PROVIDER=mailpit`. Backups and restore
are Plan 12.

### CI/CD

Releases are built, scanned, and published by GitHub Actions; deploys are
dispatched deliberately, not run from a laptop.

- Pushing a `v*` tag runs the full CI suite, builds `ghcr.io/<owner>/o1-backend`
  and `ghcr.io/<owner>/o1-web` for `linux/amd64`, scans both with Trivy, and
  pushes only if the scans pass. Each image gets the release tag,
  `sha-<12-char-commit>`, and `X.Y`; deploys pin the `@sha256` digest.
- Deploys run from **Actions → Deploy** with an `environment` (`staging` or
  `production`) and a `version`. The job resolves the digests, uploads a release
  bundle to `/opt/o1/incoming/<version>-<sha>`, and runs
  `/opt/o1/bin/deploy.sh` over SSH. Production requires reviewer approval and
  accepts only final `vX.Y.Z` releases from `main`.
- `/opt/o1/bin/rollback.sh` restores the previous release bundle. Image and
  config rollback does not undo migrations.

The full operator guide, including GitHub Environments and secrets, branch and
tag protection, GHCR visibility, host bootstrap, the release/rollback drill,
migrations, troubleshooting, and the live acceptance checklist, is
[deploy/CICD.md](deploy/CICD.md).

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
- `O1_API_HEALTH_ADDR` — internal API health/metrics listener, default `:8083`;
  not published by Compose.
- `O1_APP_BASE_URL` — dashboard origin used in verification and reset links,
  default `http://localhost:5173`.
- `O1_ALLOWED_ORIGINS` — comma-separated Origin allowlist for unsafe requests.
  Defaults to `O1_APP_BASE_URL`. Compose forwards this variable; leave it empty
  to use the dashboard origin.
- `O1_SMTP_ADDR` — Mailpit SMTP address for the `mailpit` provider, default
  `localhost:1025`.
- `O1_EMAIL_PROVIDER` — `mailpit` or `resend`. Empty resolves to `mailpit` in
  development/test and `resend` in production. Production rejects `mailpit`.
- `O1_EMAIL_FROM` — sender mailbox for transactional and alert email. Must be a
  valid address and, in production, on the Resend verified domain.
- `O1_RESEND_API_KEY` — Resend API key; required for the `resend` provider.
- `O1_EMAIL_REPLY_TO` — optional Reply-To mailbox.
- `O1_RESEND_RPS` / `O1_RESEND_BURST` — per-process Resend send rate limit,
  default `2` requests/second with a burst of `2`.
- `O1_RESEND_API_URL` — Resend API base URL, default `https://api.resend.com`;
  overridable for tests.
- `O1_SITE_ADDRESS` — Caddy site address used by the `web` container, default
  `:80`. Production sets the public domain so Caddy serves HTTPS.
- `O1_MONITOR_MIN_INTERVAL` / `O1_MONITOR_MAX_INTERVAL` — default `60s` /
  `24h`.
- `O1_MONITOR_CREATE_LIMIT` — monitor creations per workspace per hour,
  default `10`.
- `O1_AUTH_RATE_PER_MINUTE` / `O1_AUTH_RATE_BURST` — per-IP rate limit for
  credential-handling auth endpoints (login, TOTP, registration, password
  reset), default `10` per minute with a burst of `10`. `compose.yaml` sets
  `120`/`60` for development and E2E so the signup and 2FA test flows are not
  throttled; production should leave both unset to keep the defaults.
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
- The production stack publishes only `80`/`443`. PostgreSQL, the API, the
  scheduler, the worker, and all health/metrics listeners stay on the private
  Compose network.
- Caddy overwrites `X-Forwarded-For` with the immediate peer address before
  proxying to the API, so a client cannot spoof the source IP used for auth rate
  limits and audit events. The API trusts forwarded headers only from loopback
  or private peers.
