# Production deployment runbook

This directory holds the standalone production stack for o1:

| File | Purpose |
|------|---------|
| `compose.yaml` | API, scheduler, worker, web (Caddy), and an optional Alloy profile. PostgreSQL is a managed service reached through `O1_DATABASE_URL`, not a container. |
| `compose.build.yaml` | Local-build override: builds the app images from this checkout instead of pulling registry images. |
| `env.example` | Every required and optional production variable, with placeholders. |
| `images.env.example` | Fixture showing the digest-pinned image refs a deploy writes to the host. |
| `README.md` | This runbook. |

The stack is intentionally separate from the development `compose.yaml` at the
repository root. It has no Mailpit, no database container, no published
database/API/metrics ports, no development keys, and no raised auth rate
limits. Only the web container publishes ports `80` and `443`; Caddy serves the
SPA, proxies the API on the same origin, and obtains the TLS certificate. The
database is a managed PostgreSQL service supplied as `O1_DATABASE_URL`.

All commands below run from the repository root unless they use an absolute
path. Each Compose command names the production compose file and environment
file explicitly, so a development `.env` or development stack on the same host
is not picked up accidentally.

## CI/CD deploys

Production and staging are normally deployed from GitHub Actions, not from a
checkout on the host. A pushed `v*` tag is verified, built, scanned, and
published to GHCR. A manually dispatched Deploy run then uploads a release
bundle to `/opt/o1/incoming/<version>-<sha>-<run_id>-<run_attempt>` and runs
`/opt/o1/bin/deploy.sh`, which activates it and polls `/readyz`. Rollback is
`/opt/o1/bin/rollback.sh`.

See [../CICD.md](../CICD.md) for the pipeline overview, GitHub Environments and
secrets, branch and tag protection, GHCR visibility, host bootstrap, the
release and rollback drill, migrations, and troubleshooting. The manual
commands in this runbook remain the first-run, offline, and recovery path, and
they exercise the same compose file the deploy script uses.

## Plan boundaries

- **Plan 10** shipped production email through Resend and this manual go-live
  stack.
- **Plan 11** added CI/CD and is implemented. Deploys normally run from the
  Deploy workflow with digest-pinned registry images; the manual commands here
  remain for first-run, offline, and recovery use. See [../CICD.md](../CICD.md).
- **Backups come from the managed database provider.** The stack runs no
  database of its own and writes no backups; configure the provider's automated
  backups and enable point-in-time recovery before relying on retention.
  Retention can still delete raw probe partitions older than 90+ days and prune
  hourly rollups and audit events past their windows, and rollups are
  aggregates, not a backup of those raw rows. The Caddy volumes hold TLS
  certificates, not application data. See [Retention](#retention).
- `/metrics` is private. Caddy answers `404` and the metrics listeners are never
  published.
- The Resend free tier has daily and monthly send caps. Verification mail,
  password resets, and incident alerts all share that quota. Check the current
  limits in the Resend dashboard and size the plan for incident volume;
  production alerting should not run on a quota that a single incident can
  exhaust.

## Prerequisites

- A Linux host with Docker Engine 24+ and the Compose v2 plugin, plus `git`,
  `curl`, `psql`, and `openssl`.
- A managed PostgreSQL 16-compatible database with a role that owns the `o1`
  database, reachable over TLS. See [Managed PostgreSQL](#3-managed-postgresql).
- A domain or subdomain you control, for example `o1.example.com`.
- Inbound TCP `80` and `443` reachable from the internet, and SSH for
  administration. Do not expose `5432` (the database is not on this host),
  `8080`, `8081`, `8082`, or `8083`.
- A Resend account with a verified sending domain and an API key, **or** an
  SMTP relay with sender credentials. See [Email provider](#2-email-provider).

## 1. DNS and network

Create the DNS records before the first start:

- An `A` record for `O1_DOMAIN` pointing at the host's public IPv4 address.
- Optionally an `AAAA` record pointing at its public IPv6 address.

A wrong or stale `AAAA` record breaks certificate issuance even when IPv4 is
correct. If the host has no public IPv6 address, leave `AAAA` out rather than
pointing it at a private or loopback address.

```sh
dig +short A o1.example.com
dig +short AAAA o1.example.com
```

Open `80` and `443` in the host firewall and any cloud security group. Caddy
uses both ports: `80` for the HTTP-to-HTTPS redirect and ACME HTTP validation,
and `443` for TLS. Let's Encrypt and ZeroSSL must be able to reach the host.
Confirm from outside the network:

```sh
curl -I http://o1.example.com
```

Before the stack is running this may fail, but it must not time out because of a
firewall after the stack starts.

## 2. Email provider

Production sends transactional and alert email through exactly one provider,
selected by `O1_EMAIL_PROVIDER`: `resend` (the default) or `smtp`. `mailpit` is
development-only and is rejected in production. Configure the credentials for
the provider you choose and leave the other block unset.

### Resend

1. In the Resend dashboard open **Domains → Add Domain** and enter the domain
   you want to send from, for example `example.com` or a sending subdomain such
   as `mail.example.com`.
2. Add the DNS records Resend shows. They normally include MX and SPF `TXT`
   records for the sending subdomain plus DKIM `TXT` records. Copy them exactly
   from the dashboard; Resend rotates or varies them by region and account.
3. Wait until the domain shows **Verified**. DNS propagation can take minutes to
   hours.

`O1_EMAIL_FROM` must be a mailbox on the verified domain (or one of its
subdomains). For example, if `example.com` is verified:

```dotenv
O1_EMAIL_FROM="o1 <noreply@example.com>"
```

If only `mail.example.com` is verified, use an address such as
`noreply@mail.example.com`. A display name is allowed; keep the address itself
on the verified domain or Resend rejects the send.

Create an API key under **API Keys** with permission to send, and put it in
`O1_RESEND_API_KEY`. Set `O1_EMAIL_REPLY_TO` if replies should go somewhere
other than the from address. Both addresses are validated at startup.

### SMTP relay

Use any relay that supports SMTP submission: Amazon SES, Mailgun, Postmark, an
internal relay, or Gmail with an app password. The relay still requires its own
verified sending domain or identity; that verification is outside this runbook.

```dotenv
O1_EMAIL_PROVIDER=smtp
O1_SMTP_ADDR=smtp.example.net:587
O1_SMTP_USERNAME=replace-with-username
O1_SMTP_PASSWORD=replace-with-password
O1_SMTP_TLS=starttls
```

- `O1_SMTP_ADDR` — `host:port`. Port `587` normally uses `starttls`; port `465`
  normally uses `implicit`. In production the Mailpit address
  `localhost:1025` is rejected.
- `O1_SMTP_USERNAME` / `O1_SMTP_PASSWORD` — optional, but either both are set or
  neither. When set, `O1_SMTP_TLS` must not be `none`.
- `O1_SMTP_TLS` — `starttls`, `implicit`, or `none`. `none` is rejected in
  production and rejected whenever credentials are configured.
- `O1_EMAIL_FROM` must be an address the relay accepts as a sender.

Authentication uses PLAIN over TLS 1.2+. Certificate verification is always on;
there is no skip-verify switch. A 5xx relay response is a permanent failure and
is not retried; 4xx, TLS, and timeout errors are retried with backoff.

## 3. Managed PostgreSQL

The production stack runs no database of its own. Provision a managed
PostgreSQL service before the first deploy.

### Requirements

- PostgreSQL 16-compatible, with a database named `o1` and a role that **owns**
  that database. Migrations install the `citext` extension, create the River
  queue schema, and create and drop `probe_results` partitions; a bare
  non-owner role fails at startup.
- TLS enforced. Use `sslmode=verify-full` when the provider has a publicly
  trusted CA. For a provider with a private CA, use `sslmode=require` and supply
  the provider's CA material (`sslrootcert` or the documented mount).
- A **direct (session) endpoint**, not a transaction pooler. Startup migrations
  take PostgreSQL advisory locks and River needs session semantics; Neon's
  `-pooler` host and Supabase's `6543` port are transaction pools and will not
  work.
- A region close to the deploy host. Every probe writes a row over this link.
- Enough connections: `api`, `scheduler`, and `worker` each hold up to
  `O1_DB_MAX_CONNS` (default `10`), so the tier must allow at least 30
  connections plus migration headroom.

OCI Database with PostgreSQL in the VPS region, Neon's direct endpoint,
Supabase's direct connection, RDS, and Aiven all satisfy this.

### Connection string

```dotenv
O1_DATABASE_URL='postgres://o1:PASSWORD@db.example.com:5432/o1?sslmode=verify-full'
```

Wrap the value in single quotes. In an unquoted `.env` value, Compose expands
`$` as a variable reference, so a password containing `$` silently loses part
of itself; quoting avoids that, and percent-encoding `$` as `%24` is the
alternative. Percent-encode `@`, `:`, `/`, `?`, and `#` in the password too.
The API refuses to start when `O1_DATABASE_URL` is empty, and Compose refuses
to render the stack without it.

Backups and point-in-time recovery are the provider's responsibility. Confirm
the backup retention window, and that PITR is enabled on the plan you buy,
before setting `O1_RETENTION_DRY_RUN=false`; rollups are aggregates, not a
backup of raw probe rows.

### One-time cutover from the bundled database

This applies only to a host that already ran a release with the local
PostgreSQL container. Steps 1–4 run against the compose file that is **still
running**, before you update the checkout or deploy the new release, because the
new file no longer defines the `postgres` service. On a registry-deployed host
that is the active release's copy:

```sh
COMPOSE_FILE=/opt/o1/current/deploy/production/compose.yaml
ENV_FILE=/opt/o1/.env

# Export the managed DSN in this shell. Use the same value you will write to
# .env in step 5; the restore and the spot check read this variable.
export O1_DATABASE_URL='postgres://o1:PASSWORD@db.example.com:5432/o1?sslmode=verify-full'
```

On a checkout-based host, run these steps before `git pull` and keep using
`deploy/production/compose.yaml`.

1. Stop the writers and leave PostgreSQL running:

   ```sh
   docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" \
     stop api scheduler worker
   ```

2. Dump the old database:

   ```sh
   docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" \
     exec -T postgres pg_dump -U o1 -d o1 -Fc > o1-pre-managed.dump
   ```

3. Restore into the managed database:

   ```sh
   pg_restore --no-owner --no-privileges -d "$O1_DATABASE_URL" o1-pre-managed.dump
   ```

4. Spot-check row counts on both sides (`monitors`, `users`, `probe_results`)
   and confirm the newest `probe_results.checked_at` is current.

5. Set `O1_DATABASE_URL` in `deploy/production/.env`, or `/opt/o1/.env` on a
   registry-deployed host, then deploy a release built after this change.

6. Confirm `/readyz` is healthy and that no `postgres` container is running.

7. Keep the dump and the old `postgres-data` volume until the managed database
   has served production through a full retention cycle. Then remove the volume
   manually and prune pre-cutover releases; see
   [../CICD.md](../CICD.md#7-manual-recovery).

## 4. Generate secrets

The database password belongs to the managed service: issue and rotate it from
the provider console, never from the host. The application has two secrets of
its own; generate each separately and store both in a password manager:

```sh
openssl rand -base64 32  # O1_SECRET_KEY
openssl rand -base64 32  # O1_ENCRYPTION_KEY
```

- `O1_SECRET_KEY` and `O1_ENCRYPTION_KEY` each decode to exactly 32 bytes.
- Never reuse a development key. The API refuses to start in production without
  `O1_ENCRYPTION_KEY`.
- Treat `O1_ENCRYPTION_KEY` as long-lived: it encrypts monitor headers and
  request bodies at rest. Changing it later makes existing encrypted data
  unreadable; this plan has no key-rotation or re-encryption procedure.

## 5. Configure the environment

```sh
cp deploy/production/env.example deploy/production/.env
chmod 600 deploy/production/.env
$EDITOR deploy/production/.env
```

Fill in at minimum:

- `O1_DOMAIN` — the exact public hostname Caddy should serve.
- `O1_DATABASE_URL` — the managed database DSN from step 3.
- `O1_SECRET_KEY` and `O1_ENCRYPTION_KEY` — the base64 values from step 4.
- `O1_EMAIL_FROM` — a mailbox the selected email provider accepts as a sender.
- `O1_EMAIL_PROVIDER` — `resend` (default) or `smtp`.
- `O1_RESEND_API_KEY` — the Resend sending key, required only for `resend`.
- SMTP alternative to Resend: `O1_SMTP_ADDR`, `O1_SMTP_USERNAME`,
  `O1_SMTP_PASSWORD`, and `O1_SMTP_TLS` (see [Email provider](#2-email-provider)).

`O1_DB_MAX_CONNS` is optional. Leave it unset for the default of `10` unless
the provider tier cannot handle 30 connections from the three processes.

Leave `O1_BOOTSTRAP_ADMIN_EMAIL` unset until the first account exists and is
verified; the promotion runs only at process startup. See
[Administrator bootstrap](#administrator-bootstrap).

Check that Compose can resolve the file before building. Do not paste the output
anywhere: it contains secrets.

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml config >/dev/null
```

`deploy/production/.env` is ignored by git. If you prefer to work from this
directory, the equivalent commands are `cp env.example .env` and
`docker compose --env-file .env -f compose.yaml ...`.

On a registry-deployed host the same variables live in `/opt/o1/.env`, created
by `bootstrap-host.sh`. That file is operator-managed, lives outside every
release directory, and is never written by CI, so secrets survive a rollback.
See [../CICD.md](../CICD.md).

## 6. First deploy

On a provisioned host the first release is activated by the Deploy workflow,
which writes `images.env` and runs `/opt/o1/bin/deploy.sh`; see
[CI/CD deploys](#cicd-deploys). To start the stack manually from a checkout
instead, build locally with the override:

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml \
  -f deploy/production/compose.build.yaml up -d --build
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml \
  -f deploy/production/compose.build.yaml ps
```

The base `compose.yaml` pulls digest-pinned images when an `images.env` is
supplied, and it has no build contexts; `compose.build.yaml` adds them and pins
the local image names. A plain `up -d --build` without the override therefore
fails on a fresh checkout.

### Registry images

A registry deploy supplies digest-pinned references in an `images.env` file and
uses the base compose file only; the build override is not involved:

```sh
docker compose --env-file deploy/production/.env \
  --env-file deploy/production/images.env \
  -f deploy/production/compose.yaml up -d --remove-orphans
```

On a host, `deploy.sh` does exactly this against `/opt/o1/current/images.env`
and the active release's compose file, and it refuses any reference that is not
pinned as `<image>@sha256:<64 hex>`. `images.env.example` is a fixture for
config validation; never put it on a host as-is.

What happens on first start:

- The stack starts no database container; the API connects to the managed
  database named by `O1_DATABASE_URL`.
- The API, scheduler, and worker run migrations against that database at
  startup. With the build override they are built from `backend/`; a registry
  deploy pulls them instead. A PostgreSQL advisory lock serializes migrations,
  so concurrent starts are safe.
- The web image serves the SPA and Caddy; Caddy obtains a certificate for
  `O1_DOMAIN` and redirects HTTP to HTTPS. The build override builds it from
  `web/`; a registry deploy pulls it.

Watch the certificate request and the first startup:

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml logs -f web
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml logs --tail=100 api scheduler worker
```

## 7. Verify the deployment

### Health and routing

```sh
curl -fsS https://o1.example.com/healthz
curl -fsS https://o1.example.com/readyz
curl -fsSI https://o1.example.com
curl -s -o /dev/null -w '%{http_code}\n' https://o1.example.com/metrics
curl -sI http://o1.example.com | head -n 1
```

Expected:

- `/healthz` and `/readyz` return API JSON, not the SPA shell. `/readyz` also
  checks the database.
- `/` returns `200` and the dashboard HTML.
- `/metrics` returns `404`; metrics are never public.
- Plain HTTP returns a redirect (`308`) to `https://`.

### TLS certificate

```sh
echo | openssl s_client -connect o1.example.com:443 \
  -servername o1.example.com 2>/dev/null |
  openssl x509 -noout -subject -issuer -dates
```

The certificate subject should match `O1_DOMAIN`, the issuer should be Let's
Encrypt or ZeroSSL, and the dates should be current. Caddy stores certificates
in the `caddy_data` volume and renews them automatically. Do not delete that
volume: losing it forces re-issuance and can run into ACME rate limits.

### Registration verification email

1. Open `https://o1.example.com` and register with an address you can read.
2. Confirm the verification email arrives from the configured sender address
   with subject `Verify your email`.
3. Confirm the link uses `https://o1.example.com`, not localhost, and completes
   verification.

If the mail does not arrive, check the provider's email log and quota, the
sender address, and the credentials. For Resend, also check domain verification
and the API key; for SMTP, check the relay's auth and that the sender identity is
accepted. A permanent `4xx`/`5xx` failure in the API or worker logs points at the
credentials, sender address, or provider policy.

### Administrator bootstrap

1. Register and verify the account that should be the global administrator.
2. Set `O1_BOOTSTRAP_ADMIN_EMAIL` to that exact address in
   `deploy/production/.env`.
3. Recreate the services so the new environment is applied:

   ```sh
   docker compose --env-file deploy/production/.env \
     -f deploy/production/compose.yaml up -d --force-recreate api scheduler worker
   ```

The account is promoted only if it exists and its email is verified at
startup. Log in and confirm the admin views are available.

### Incident alert email

1. Create a monitor with a short interval that will fail (for example a URL
   that returns `500`), and add an email alert channel with your address.
2. Wait for the scheduler and worker to record the failure and open an
   incident.
3. Confirm the alert email arrives from the verified domain. The incident detail
   page shows the delivery status, channel, and attempt count.
4. Fix or resolve the incident and confirm the recovery email arrives.

Transient provider errors (timeouts, `408`, `425`, `429`, `5xx`) are retried.
Authentication failures and other permanent `4xx` responses are recorded as
failed deliveries without endless retries.

## 8. Logs and day-to-day operations

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml ps
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml logs -f web api scheduler worker
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml logs --tail=200 api
```

Useful checks:

- `web` logs contain the ACME certificate result and request errors.
- `api` logs show startup configuration errors, migration progress, and request
  failures.
- `worker` logs show probe jobs, incidents, and email delivery failures. Errors
  are sanitized: they never include the Resend key, recipient address, or
  provider response body.
- `scheduler` logs show due-monitor enqueueing, partition maintenance, hourly
  rollups, and retention counts.

## Retention

The scheduler owns maintenance. It runs one maintenance pass at startup, then
rolls up completed UTC hours every `O1_ROLLUP_INTERVAL` (default `15m`) and runs
retention once per day. Each pass takes a PostgreSQL advisory lock, so only one
scheduler process does the work.

Deploy pipelines do not invoke retention directly. Any scheduler restart —
including a deploy, a rollback, or an explicit `--force-recreate scheduler` —
runs one startup maintenance pass. With `O1_RETENTION_DRY_RUN=false`, that
startup pass can drop up to `O1_RETENTION_MAX_PARTITION_DROPS` raw partitions
immediately, before the next daily run.

Retention is a dry run by default. With `O1_RETENTION_DRY_RUN=true`, the
scheduler computes candidates and logs the counts it would delete but changes no
data. Deletion requires an explicit `O1_RETENTION_DRY_RUN=false`.

The windows are:

- Raw probe partitions: at least 90 days (`O1_RAW_RETENTION_DAYS`, range 7–365).
  Partitions are UTC months, so once deletion is enabled the effective window is
  90–120 days. With the dry-run default, raw data grows without bound.
- Hourly rollups: 13 months (`O1_ROLLUP_RETENTION_MONTHS`, range 4–60).
- Audit events: 12 months (`O1_AUDIT_RETENTION_MONTHS`, range 1–60).

At most `O1_RETENTION_MAX_PARTITION_DROPS` raw partitions (default `1`, range
1–12) are dropped per run. A partition drops only when it is wholly expired and
either beyond the rollup window or fully covered by rollups whose check counts
match the raw rows. Coverage requires `rollup_state.watermark` to be at or past
the partition end before the counts are compared; otherwise the partition is
skipped for this run. A mismatch is re-rolled once per run; a partition that
still mismatches stays in place for a later run and for operator review.

Preview a pass from the scheduler logs:

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml logs --tail=200 scheduler | grep retention
```

Dry-run lines carry `partition`, `raw_checks`, `rollup_checks`, and
`gate_required`. The dry run logs every candidate partition, including ones the
per-run cap would defer. `gate_required` is true when the partition is still
inside the rollup window and must pass the watermark and count checks; it is
false when the partition is beyond the rollup window and drops without a
coverage check.

The pass summary reports `candidates`, `partitions_dropped`, `skipped_dry_run`,
`skipped_watermark`, `skipped_mismatch`, `rollup_rows`, and `audit_rows`.
`skipped_watermark` counts partitions retained because
`rollup_state.watermark` had not reached the partition end, so coverage could
not be proven. Inspect the database directly when needed, either with `psql`
and the managed DSN — export it first, for example with
`set -a; . /opt/o1/.env; set +a` on a registry host — or from the provider
console:

```sh
psql "$O1_DATABASE_URL" -c "SELECT watermark FROM rollup_state;"
```

```sql
SELECT max(bucket_start) FROM monitor_hourly_stats;
SELECT watermark FROM rollup_state;
SELECT count(*) FROM monitor_hourly_stats;
SELECT relname FROM pg_class WHERE relname LIKE 'probe_results_%' AND relkind = 'r';
```

Deletion is irreversible. Backups and point-in-time recovery are the managed
provider's responsibility: confirm both are enabled (see
[Managed PostgreSQL](#3-managed-postgresql)) before turning deletion on. Once a
raw partition drops, the individual probe
rows, status codes, error text, and exact check timestamps are gone. The hourly
rollups preserve up/down counts and avg/min/max/p95 latency for the status page
and long-window aggregates, but they are not a backup of the raw data.

### Retention rollout

1. Deploy with the defaults. `O1_RETENTION_DRY_RUN` is `true`, so the release
   rolls up existing raw data and deletes nothing.
2. Watch `rollup_state.watermark` advance from the oldest raw hour until it
   reaches the most recent completed hour. The watermark settles roughly 1–2
   hours behind while the 15-minute late-arrival lag and the hour truncation are
   applied. A pass processes at most
   `O1_ROLLUP_MAX_HOURS_PER_RUN` hours (default `24`), so a long backfill takes
   several intervals.
3. Confirm the public status page still shows the same 90 daily bars and uptime
   as before the release.
4. Review the dry-run retention logs, especially `candidates`, `raw_checks`, and
   `rollup_checks` for the oldest partitions.
5. Set `O1_RETENTION_DRY_RUN=false` in the environment file — `/opt/o1/.env` on
   a registry-deployed host, or `deploy/production/.env` for a checkout stack —
   and recreate the scheduler so the new environment applies:

   ```sh
   docker compose --env-file deploy/production/.env \
     -f deploy/production/compose.yaml up -d --force-recreate scheduler
   ```

   On a registry-deployed host, use `/opt/o1/.env`, `/opt/o1/current/images.env`,
   and the active release's compose file instead. Recreating the scheduler
   triggers a startup maintenance pass, so the first drop can happen
   immediately, not on the next daily run. Expect at most one partition drop per
   startup pass and per daily run, so a backlog clears one month at a time.

## 9. Update and restart

Manual update from a checkout, with the local build override:

```sh
git pull
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml \
  -f deploy/production/compose.build.yaml up -d --build
```

On a registry-deployed host, dispatch the Deploy workflow instead. It resolves
the published digests, writes `images.env`, and runs
`/opt/o1/bin/deploy.sh`; there is no local build. See
[../CICD.md](../CICD.md).

Migrations run automatically at startup. This release builds a new index on
`probe_results`; schedule the first deploy off-peak and expect probe writes to
pause while it builds. To apply only environment changes, recreate the affected
services:

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml \
  -f deploy/production/compose.build.yaml up -d --force-recreate api scheduler worker
```

(Adding the override is harmless for environment-only changes and keeps the
local image names consistent with the manual build.)

Frontend `VITE_*` values are baked into the static bundle at build time. Changing
`VITE_SENTRY_DSN` or `VITE_RELEASE` requires rebuilding the `web` image.

## 10. Rollback

Each successful registry deploy stores an immutable bundle under
`/opt/o1/releases/<version>-<sha>-<run_id>-<run_attempt>/`. The bundle holds the
release's `deploy/production/compose.yaml`,
`deploy/observability/alloy.alloy`, and digest-pinned `images.env`. The
`current` and `previous` symlinks select the
active and prior bundles; `/opt/o1/.env` sits outside the releases so secrets
are unchanged by a rollback.

Roll back to the previous release:

```sh
/opt/o1/bin/rollback.sh
```

Roll back to a named release, or roll forward after a fix:

```sh
/opt/o1/bin/rollback.sh v1.2.2-abc123def456-1234567890-1
```

`rollback.sh` takes the same `flock` as `deploy.sh`, switches `current`, pulls
the pinned images, runs `up -d --remove-orphans`, and polls
`https://$O1_DOMAIN/readyz`. If the target is unhealthy it restores the release
that was active and exits non-zero. Inspect what is live with:

```sh
readlink -f /opt/o1/current
readlink -f /opt/o1/previous
cat /opt/o1/current/images.env
```

Image and config rollback does **not** undo migrations. Restoring an earlier
release restores its compose file and image digests, not the database schema.
If the failed release migrated the schema, the old code runs against the new
schema; see [../CICD.md](../CICD.md#8-migrations). A manual rebuild from a
checkout bypasses the release symlinks entirely and is replaced by the next
deploy or rollback.

Rollback does **not** move data either, and the managed database is outside the
release bundle. An older release built before the managed cutover carries a
compose file that still defines the bundled `postgres` service, so rolling back
to it starts an **empty local database** and points the app at it: `/readyz`
turns green while the real data is unreachable. After the first verified
managed deploy, prune the pre-cutover release directories so `rollback.sh` can
only select managed-aware bundles, and keep `o1-pre-managed.dump` until the
managed database is accepted. See
[Managed PostgreSQL](#3-managed-postgresql).

## 11. Troubleshooting

| Symptom | Check |
|---------|-------|
| Caddy cannot obtain a certificate | DNS `A`/`AAAA` must point at this host; `80` and `443` must be reachable from the internet; no other process may own those ports. Inspect `logs -f web` for the ACME error. Fix DNS/firewall and let Caddy retry; repeated forced retries can hit ACME rate limits. |
| Browser shows `502 Bad Gateway` | The API is not ready. Check `logs api`; verify the managed database is reachable and `O1_DATABASE_URL` is correct. |
| API exits at startup with a config error | `O1_SECRET_KEY`/`O1_ENCRYPTION_KEY` must be base64 of exactly 32 bytes; `O1_EMAIL_FROM` must be a valid mailbox; the Resend provider requires `O1_RESEND_API_KEY`; production rejects the `mailpit` provider; `O1_DB_MAX_CONNS` must be between 1 and 100. |
| Database connection fails | Check `sslmode` (`verify-full`, or `require` plus the provider CA), that the host is a direct/session endpoint rather than a transaction pooler, that the password is percent-encoded, that the VPS address passes the provider's IP allowlist or firewall, and that `3 x O1_DB_MAX_CONNS` fits the tier's connection limit. `/readyz` also checks the database. |
| Email not delivered | Check the provider's email log and quota. For Resend: domain verification, API key, and from/reply-to on the verified domain. For SMTP: relay reachability, auth pair, TLS mode, and an accepted sender identity. Check the incident delivery status and `logs worker`. |
| `X-Forwarded-For` warning in Caddy logs | Caddy prints an "Unnecessary header_up X-Forwarded-For" warning at startup. It is informational: the explicit setting overwrites client-supplied values so the API sees the proxy peer. |
| Ports already in use | Stop the development stack (`docker compose down` at the repository root) or any other service on `80`/`443`. |

## 12. Optional observability with Grafana Alloy

Alloy is behind the `observability` profile and is not started by a normal
`up`. Set all six Grafana values in `deploy/production/.env`:
`GRAFANA_REMOTE_WRITE_URL`, `GRAFANA_REMOTE_WRITE_USER`,
`GRAFANA_REMOTE_WRITE_TOKEN`, `GRAFANA_LOKI_URL`, `GRAFANA_LOKI_USER`, and
`GRAFANA_LOKI_TOKEN`, then run:

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml --profile observability up -d alloy
```

Alloy scrapes the internal listeners (`api:8083`, `scheduler:8081`,
`worker:8082`), remote-writes metrics to Grafana Cloud, and ships the logs of
`api`, `scheduler`, `worker`, and `web` to Grafana Cloud Loki. It reads the
Docker socket read-only and only collects containers in this Compose project.
It exits with a clear error when any Grafana variable is missing instead of
silently dropping data. See
[../observability/README.md](../observability/README.md) for Grafana Cloud
setup, dashboards, alert rules, and Sentry configuration. Point an external
dead-man's switch at `https://o1.example.com/healthz`, never at `/metrics`.

Alloy's own UI on port `12345` is not published; use its logs for scrape and
remote-write errors.

## 13. Security notes

- Only `80` and `443` are published. The API, the scheduler, the worker, and
  every health/metrics listener stay on the private Compose network; the
  managed database is reached outbound over TLS and has no local listener.
- `/metrics` returns `404` through Caddy.
- All secrets come from `deploy/production/.env` or the host environment.
  `env.example` contains placeholders only. Never commit `.env`.
- The stack sets `O1_ENV=production`, which requires a real encryption key and
  rejects the development email provider.
- Keep Caddy's `caddy_data` volume; it holds the certificates and ACME account
  material. Application data lives in the managed database, not on the host, so
  no database volume exists to protect or delete.
- Backups and point-in-time recovery come from the managed database provider;
  confirm both before enabling retention deletion. Once enabled, retention can
  delete expired raw probes; it is a dry run until `O1_RETENTION_DRY_RUN=false`.
  See [Retention](#retention). CI/CD deploys and the release/rollback process
  are documented in [../CICD.md](../CICD.md).
