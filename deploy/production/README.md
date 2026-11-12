# Production deployment runbook

This directory holds the standalone production stack for o1:

| File | Purpose |
|------|---------|
| `compose.yaml` | PostgreSQL, API, scheduler, worker, web (Caddy), and an optional Alloy profile. |
| `compose.build.yaml` | Local-build override: builds the app images from this checkout instead of pulling registry images. |
| `env.example` | Every required and optional production variable, with placeholders. |
| `images.env.example` | Fixture showing the digest-pinned image refs a deploy writes to the host. |
| `README.md` | This runbook. |

The stack is intentionally separate from the development `compose.yaml` at the
repository root. It has no Mailpit, no published PostgreSQL/API/metrics ports,
no development keys, and no raised auth rate limits. Only the web container
publishes ports `80` and `443`; Caddy serves the SPA, proxies the API on the same
origin, and obtains the TLS certificate.

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
- **Plan 12** adds encrypted offsite backups and restore drills. The host runs a
  daily `pg_dump` through restic to S3-compatible storage, keeps 30 daily and 12
  monthly snapshots, prunes weekly, and runs a restore drill monthly. The RPO is
  up to 24 hours and there is no point-in-time recovery. Backups are host
  tooling installed separately from the app release bundle; see
  [Backups and restore](#8-backups-and-restore) and
  [../backup/README.md](../backup/README.md). Retention remains a dry run until
  `O1_RETENTION_DRY_RUN=false`.
- `/metrics` is private. Caddy answers `404` and the metrics listeners are never
  published.
- The Resend free tier has daily and monthly send caps. Verification mail,
  password resets, and incident alerts all share that quota. Check the current
  limits in the Resend dashboard and size the plan for incident volume;
  production alerting should not run on a quota that a single incident can
  exhaust.

## Prerequisites

- A Linux host with Docker Engine 24+ and the Compose v2 plugin, plus `git`,
  `curl`, and `openssl`.
- A domain or subdomain you control, for example `o1.example.com`.
- Inbound TCP `80` and `443` reachable from the internet, and SSH for
  administration. Do not expose `5432`, `8080`, `8081`, `8082`, or `8083`.
- A Resend account with a verified sending domain and an API key.

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

## 2. Resend domain verification, SPF, and DKIM

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

## 3. Generate secrets

Generate each value separately and store it in a password manager:

```sh
openssl rand -hex 32     # POSTGRES_PASSWORD
openssl rand -base64 32  # O1_SECRET_KEY
openssl rand -base64 32  # O1_ENCRYPTION_KEY
```

- Use the hex output for `POSTGRES_PASSWORD`. It is embedded in
  `O1_DATABASE_URL`, and base64 characters such as `@`, `:`, `/`, `?`, and `#`
  would need percent-encoding.
- `O1_SECRET_KEY` and `O1_ENCRYPTION_KEY` each decode to exactly 32 bytes.
- Never reuse a development key. The API refuses to start in production without
  `O1_ENCRYPTION_KEY`.
- Treat `O1_ENCRYPTION_KEY` as long-lived: it encrypts monitor headers and
  request bodies at rest. Changing it later makes existing encrypted data
  unreadable; this plan has no key-rotation or re-encryption procedure.

Do not start the stack with the placeholder `POSTGRES_PASSWORD` and change it
afterwards. PostgreSQL sets the role password when it first initializes the
`postgres-data` volume; later edits to the variable do not update the role, and
the API cannot authenticate. Generate the real password before the first start.

If the stack has already started with the placeholder, you can recover without
deleting data: update the role password inside PostgreSQL, then update `.env` to
match and recreate the application containers.

```sh
# Replace <new-hex-password> with the same value you put in .env.
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml exec postgres \
  psql -U o1 -d o1 -c "ALTER ROLE o1 WITH PASSWORD '<new-hex-password>';"
$EDITOR deploy/production/.env   # set POSTGRES_PASSWORD to the same value
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml up -d --force-recreate api scheduler worker
```

Only recreate the volume (`docker compose ... down -v`, which deletes all data)
when the existing contents are expendable.

## 4. Configure the environment

```sh
cp deploy/production/env.example deploy/production/.env
chmod 600 deploy/production/.env
$EDITOR deploy/production/.env
```

Fill in at minimum:

- `O1_DOMAIN` — the exact public hostname Caddy should serve.
- `POSTGRES_PASSWORD` — the hex value from step 3.
- `O1_SECRET_KEY` and `O1_ENCRYPTION_KEY` — the base64 values from step 3.
- `O1_EMAIL_FROM` — a mailbox on the verified Resend domain.
- `O1_RESEND_API_KEY` — the Resend sending key.

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

## 5. First deploy

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

- PostgreSQL starts and becomes healthy.
- The API, scheduler, and worker run migrations at startup. With the build
  override they are built from `backend/`; a registry deploy pulls them instead.
  A PostgreSQL advisory lock serializes migrations, so concurrent starts are
  safe.
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

## 6. Verify the deployment

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
2. Confirm the verification email arrives from the verified Resend domain with
   subject `Verify your email`.
3. Confirm the link uses `https://o1.example.com`, not localhost, and completes
   verification.

If the mail does not arrive, check the Resend dashboard's email log for the
send, the spam folder, that the domain is verified, that the API key is active,
and that the from address is on the verified domain. A `403` or validation error
in the API logs points at the key or from address.

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

## 7. Logs and day-to-day operations

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
not be proven. Inspect the database directly when needed:

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml exec postgres \
  psql -U o1 -d o1 -c "SELECT watermark FROM rollup_state;"
```

```sql
SELECT max(bucket_start) FROM monitor_hourly_stats;
SELECT watermark FROM rollup_state;
SELECT count(*) FROM monitor_hourly_stats;
SELECT relname FROM pg_class WHERE relname LIKE 'probe_results_%' AND relkind = 'r';
```

Deletion by the application is irreversible from the app's own history, but
Plan 12 backs the database up: a restore can bring back data that retention has
since deleted. Restic retention (30 daily plus 12 monthly snapshots) is
independent of the retention windows above, and a snapshot may be up to 24
hours old. Once a raw partition drops, the live database loses those individual
probe rows, status codes, error text, and exact check timestamps; they exist
again only if an older backup is restored. Hourly rollups preserve up/down
counts and avg/min/max/p95 latency for the status page and long-window
aggregates. See [Backups and restore](#8-backups-and-restore).

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

## 8. Backups and restore

Production backups are host tooling, not part of the app release bundle, and
deploys and rollbacks never take or restore a database backup. The daily timer
runs `pg_dump` streamed through restic to an encrypted S3-compatible repository;
the RPO is up to 24 hours and there is no PITR. The host keeps 30 daily and 12
monthly snapshots, prunes weekly, and runs an automated restore drill monthly.

The backup tree is delivered with `scp` of `deploy/` and installed with
`install-backup.sh --source <dir>`, where `<dir>` contains `backup/` and
`scripts/`. Do not expect it in a release bundle. The full setup, configuration,
password handling, manual commands, restore drill behavior, and disaster
recovery runbook are in [../backup/README.md](../backup/README.md).

Two independent copies of secrets are required, stored off the host:

- `/opt/o1/.env`, including `O1_ENCRYPTION_KEY`, `O1_SECRET_KEY`,
  `POSTGRES_PASSWORD`, and the Resend API key. Without `O1_ENCRYPTION_KEY`, a
  restored database cannot decrypt monitor headers or request bodies.
- `/opt/o1/backup/restic.pass` plus `/opt/o1/backup/backup.env` (S3
  credentials). Without the restic password, the backups are unreadable.

Backups run at 02:30 UTC daily, prune Sunday 03:30 UTC, and drill on the first
of the month at 04:00 UTC. Check the timers and the last successful runs:

```sh
systemctl list-timers 'o1-*'
sudo cat /opt/o1/backup/last-success
sudo cat /opt/o1/backup/last-restore-verify
sudo journalctl -u o1-backup.service -n 100 --no-pager
```

### Post-restore checklist

Use the full runbook in [../backup/README.md](../backup/README.md) for a new
host. After any restore, before declaring recovery:

- [ ] `/healthz` and `/readyz` succeed, and the API/scheduler/worker logs show no
      migration or decryption errors.
- [ ] `goose_db_version` is at or ahead of the deployed release's latest
      migration.
- [ ] An administrator can log in.
- [ ] A monitor with encrypted headers or a request body opens and a probe
      succeeds; this proves `O1_ENCRYPTION_KEY` matches the data.
- [ ] Public status pages and the expected monitors load.
- [ ] Alert email delivery works.
- [ ] Caddy has a valid certificate and redirects HTTP to HTTPS.
- [ ] A manual backup succeeds, updates `last-success`, and pings the external
      success URL.
- [ ] The snapshot ID, restore times, and data-loss window (up to 24 hours, more
      if an older snapshot was chosen) are recorded.

A restore can reintroduce data that retention deleted. Restic retention is
independent of `O1_RAW_RETENTION_DAYS`, `O1_ROLLUP_RETENTION_MONTHS`, and
`O1_AUDIT_RETENTION_MONTHS`; review restored data and expected retention
behavior before re-enabling deletion.

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

## 11. Troubleshooting

| Symptom | Check |
|---------|-------|
| Caddy cannot obtain a certificate | DNS `A`/`AAAA` must point at this host; `80` and `443` must be reachable from the internet; no other process may own those ports. Inspect `logs -f web` for the ACME error. Fix DNS/firewall and let Caddy retry; repeated forced retries can hit ACME rate limits. |
| Browser shows `502 Bad Gateway` | The API is not ready. Check `logs api`; verify PostgreSQL is healthy and `O1_DATABASE_URL` is correct. |
| API exits at startup with a config error | `O1_SECRET_KEY`/`O1_ENCRYPTION_KEY` must be base64 of exactly 32 bytes; `O1_EMAIL_FROM` must be a valid mailbox; the Resend provider requires `O1_RESEND_API_KEY`; production rejects the `mailpit` provider. |
| Database authentication fails | `POSTGRES_PASSWORD` must match across the first volume initialization and `O1_DATABASE_URL`. If it contains URL-special characters, percent-encode it or regenerate as hex. Changing it after the volume exists does not change the PostgreSQL role password; see [step 3](#3-generate-secrets) to recover without deleting data. |
| Email not delivered | Check the Resend email log and quota, domain verification, API key, and that the from/reply-to addresses are on the verified domain. Check the incident delivery status and `logs worker`. |
| `X-Forwarded-For` warning in Caddy logs | Caddy prints an "Unnecessary header_up X-Forwarded-For" warning at startup. It is informational: the explicit setting overwrites client-supplied values so the API sees the proxy peer. |
| Ports already in use | Stop the development stack (`docker compose down` at the repository root) or any other service on `80`/`443`. |

## 12. Optional metrics with Grafana Alloy

Alloy is behind the `observability` profile and is not started by a normal
`up`. Set `GRAFANA_REMOTE_WRITE_URL`, `GRAFANA_REMOTE_WRITE_USER`, and
`GRAFANA_REMOTE_WRITE_TOKEN` in `deploy/production/.env`, then run:

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml --profile observability up -d alloy
```

Alloy scrapes the internal listeners (`api:8083`, `scheduler:8081`,
`worker:8082`) and remote-writes to Grafana Cloud. It exits with a clear error
when credentials are missing instead of silently dropping metrics. See
[../observability/README.md](../observability/README.md) for Grafana Cloud
setup, dashboards, alert rules, and Sentry configuration. Point an external
dead-man's switch at `https://o1.example.com/healthz`, never at `/metrics`.

Alloy's own UI on port `12345` is not published; use its logs for scrape and
remote-write errors.

## 13. Security notes

- Only `80` and `443` are published. PostgreSQL, the API, the scheduler, the
  worker, and every health/metrics listener stay on the private Compose
  network.
- `/metrics` returns `404` through Caddy.
- All secrets come from `deploy/production/.env` or the host environment.
  `env.example` contains placeholders only. Never commit `.env`.
- The stack sets `O1_ENV=production`, which requires a real encryption key and
  rejects the development email provider.
- Keep Caddy's `caddy_data` volume; it holds the certificates and ACME account
  material. Application data lives in `postgres-data`.
- Plan 12 backups are encrypted and offsite: restic encrypts the repository, and
  the S3 credentials and restic password live only in `/opt/o1/backup/backup.env`
  and `/opt/o1/backup/restic.pass` (both `0600`, root-owned). `.env` and
  `O1_ENCRYPTION_KEY` are not uploaded and must be stored separately. Retention
  can delete expired raw probes; it is a dry run until
  `O1_RETENTION_DRY_RUN=false`, and a restore can reintroduce data deleted by
  retention. See [Retention](#retention) and
  [Backups and restore](#8-backups-and-restore). CI/CD deploys and the
  release/rollback process are documented in [../CICD.md](../CICD.md).
