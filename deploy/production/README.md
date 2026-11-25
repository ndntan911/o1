# Production deployment runbook

This directory holds the standalone production stack for o1:

| File | Purpose |
|------|---------|
| `compose.yaml` | API, scheduler, worker, web (Caddy), the bundled `postgres` service, and an optional Alloy profile. |
| `compose.build.yaml` | Local-build override: builds the app images from this checkout instead of pulling registry images. |
| `env.example` | Every required and optional production variable, with placeholders. |
| `images.env.example` | Fixture showing the digest-pinned image refs a deploy writes to the host. |
| `README.md` | This runbook. |

The stack is intentionally separate from the development `compose.yaml` at the
repository root. It has no Mailpit, no published database/API/metrics ports, no
development keys, and no raised auth rate limits. Only the web container
publishes ports `80` and `443`; Caddy serves the SPA, proxies the API on the
same origin, and obtains the TLS certificate. PostgreSQL 18 runs as the
`postgres` service inside this stack, stores its data on the `o1-postgres-data`
volume, and is reachable only on the private Compose network.

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
  [Backups and restore](#backups-and-restore) and
  [../backup/README.md](../backup/README.md). Retention remains a dry run until
  `O1_RETENTION_DRY_RUN=false`. The Caddy volumes hold TLS certificates, not
  application data.
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
- 43 GB free on the boot volume, measured: the database, its images, the
  restic cache, and release bundles share that disk. See
  [Bundled PostgreSQL](#3-bundled-postgresql).
- A domain or subdomain you control, for example `o1.example.com`.
- Inbound TCP `80` and `443` reachable from the internet, and SSH for
  administration. Do not expose `5432` (the bundled database publishes no
  port), `8080`, `8081`, `8082`, or `8083`.
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

## 3. Bundled PostgreSQL

The production stack runs its own PostgreSQL 18 as the `postgres` service on
the private Compose network. It uses the `postgres:18-alpine` image, owns the
named volume `o1-postgres-data`, and publishes no host port. The application
never receives an operator-supplied DSN: Compose derives `O1_DATABASE_URL` from
`POSTGRES_PASSWORD`.

### Requirements

- The existing host requirements: Docker Engine 24+ and the Compose v2 plugin.
  The database adds no extra host daemon, package, or external account.
- 43 GB free on the boot volume, measured before the cutover. The Neon source
  measured about 30 MB, but images, the volume, restic's cache, and retained
  release bundles share the disk.
- Enough memory and CPU for a 3 GB / 2 CPU container: the stack caps
  `shared_buffers` at 1 GB and `max_connections` at 100.
- `POSTGRES_PASSWORD` is baked into the cluster by the first `initdb`. Changing
  it in `.env` later does not change the role password; run
  `ALTER ROLE o1 PASSWORD '...'` inside the container and update `.env` in the
  same change, then recreate `api`, `scheduler`, and `worker`.

### Connection

```dotenv
POSTGRES_PASSWORD=<output of openssl rand -hex 32>
```

Compose derives the one supported production DSN:

```text
postgres://o1:<POSTGRES_PASSWORD>@postgres:5432/o1?sslmode=disable
```

`5432` is not published; the DSN resolves only on the Compose bridge, where
TLS is unnecessary and `sslmode=disable` is deliberate. The bundled Compose
ignores `O1_DATABASE_URL`: it sets its own for every backend service, so a
stray value never reaches the containers. During the 7-day soak keep the Neon
**direct** DSN in `/opt/o1/.env` anyway: the retained pre-cutover bundle in
[Rollback](#rollback) reads it, and an automatic rollback after a failed
cutover deploy needs it to render. Remove it in Task 7 after acceptance.
Outside the soak, do not set `O1_DATABASE_URL` at all.

### Startup

`api`, `scheduler`, and `worker` declare `depends_on: postgres` with
`condition: service_healthy`, so they do not start until the image's
`pg_isready` healthcheck passes. Each process then runs goose and River
migrations at startup; a PostgreSQL advisory lock serializes them, so
concurrent starts are safe.

### Extensions

`citext` and `plpgsql` ship with `postgres:18-alpine`. The migrations create
`citext` themselves; there is no package install and no provider extension
list to maintain.

### Cutover from Neon

The cutover from Neon is a one-time, maintenance-window operation: freeze
writes, dump Neon, restore into a temporary container on `o1-postgres-data`,
then deploy the bundled stack. The full command sequence with expected output is
Task 6 of
[docs/superpowers/plans/2026-11-25-self-hosted-postgres.md](../../docs/superpowers/plans/2026-11-25-self-hosted-postgres.md).
In short, on the host:

1. Confirm the preflight: at least 40 GB free (43 GB measured), no
   `o1-postgres-data` volume yet, and no legacy `postgres-data` volume.
2. Generate `openssl rand -hex 32`, store it in the password manager, and add
   `POSTGRES_PASSWORD=<value>` to `/opt/o1/.env` (mode `0600`). Do not start
   any container with the new stack yet.
3. Freeze writes on the running release and record the Neon row counts. Stop
   `worker` first and let River drain, then `scheduler`, then `api`; leave `web`
   up. Export the Neon **direct** (non-pooled) DSN first:

   ```sh
   export NEON_DIRECT_URL='postgres://...@ep-...neon.tech/neondb?sslmode=require'
   install -d -m 0700 /opt/o1/state
   docker compose --env-file /opt/o1/.env \
     -f /opt/o1/current/deploy/production/compose.yaml stop worker
   docker compose --env-file /opt/o1/.env \
     -f /opt/o1/current/deploy/production/compose.yaml stop scheduler api
   docker run --rm postgres:18-alpine psql "$NEON_DIRECT_URL" -Atc \
     "select 'users', count(*) from users union all
      select 'workspaces', count(*) from workspaces union all
      select 'monitors', count(*) from monitors union all
      select 'probe_results', count(*) from probe_results union all
      select 'incidents', count(*) from incidents union all
      select 'goose_db_version', count(*) from goose_db_version union all
      select 'river_job', count(*) from river_job;" \
     | tee /opt/o1/state/neon-counts.txt
   ```

   Run the count query only after the freeze; its output is the comparison
   baseline.

4. Create the volume and seed it with a temporary container. Mount the volume
   at `/var/lib/postgresql`, where the PostgreSQL 18 image keeps `PGDATA`
   (`/var/lib/postgresql/18/docker`); the old `/var/lib/postgresql/data` path
   would leave the restored dump outside the volume. `POSTGRES_PASSWORD` is
   hex only, never quoted: the validation rejects a quoted or non-hex value
   before the volume is created.

   ```sh
   POSTGRES_PASSWORD=$(sed -n 's/^POSTGRES_PASSWORD=//p' /opt/o1/.env | tr -d "\"'" | tr -d '[:space:]')
   [[ $POSTGRES_PASSWORD =~ ^[0-9a-fA-F]{64}$ ]] || { echo "POSTGRES_PASSWORD must be 64 hex chars, unquoted"; exit 1; }
   docker volume create o1-postgres-data
   docker run -d --name o1-cutover-pg \
     -e POSTGRES_USER=o1 -e POSTGRES_DB=o1 \
     -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
     -e POSTGRES_INITDB_ARGS='--locale=C.UTF-8 --encoding=UTF8' \
     -e LANG=C.UTF-8 \
     -v o1-postgres-data:/var/lib/postgresql \
     postgres:18-alpine
   for i in $(seq 1 60); do
     docker exec o1-cutover-pg pg_isready -U o1 -d o1 && break
     sleep 1
   done
   docker exec o1-cutover-pg psql -U o1 -d o1 -Atc "select datcollate, datctype, pg_encoding_to_char(encoding) from pg_database where datname = current_database()"
   ```

   Wait until the loop reports `accepting connections` (roughly 10–30 seconds),
   then confirm the locale is `C.UTF-8`. If `pg_isready` does not come up, read
   `docker logs o1-cutover-pg` before retrying.

5. Dump Neon's **direct** (non-pooled) endpoint, push it to restic, restore it
   into the temporary container, and compare the counts:

   ```sh
   docker run --rm postgres:18-alpine pg_dump -Fc -Z 6 -d "$NEON_DIRECT_URL" -f - \
     > /opt/o1/state/o1-pre-local.dump
   chmod 0600 /opt/o1/state/o1-pre-local.dump
   sudo bash -c 'set -a; . /opt/o1/backup/backup.env; set +a; restic backup --stdin --stdin-filename o1.dump --host o1-prod --tag o1-db --tag pre-cutover < /opt/o1/state/o1-pre-local.dump'

   if ! docker exec -i o1-cutover-pg pg_restore -U o1 -d o1 --no-owner --no-privileges \
     < /opt/o1/state/o1-pre-local.dump; then
     echo "pg_restore failed: stop, fix the cause, re-dump Neon, and restore again" >&2
     exit 1
   fi
   docker exec o1-cutover-pg psql -U o1 -d o1 -Atc \
     "select 'users', count(*) from users union all
      select 'workspaces', count(*) from workspaces union all
      select 'monitors', count(*) from monitors union all
      select 'probe_results', count(*) from probe_results union all
      select 'incidents', count(*) from incidents union all
      select 'goose_db_version', count(*) from goose_db_version union all
      select 'river_job', count(*) from river_job;" \
     | tee /opt/o1/state/local-counts.txt
   diff -u /opt/o1/state/neon-counts.txt /opt/o1/state/local-counts.txt || {
     echo "row counts differ: stop, fix the cause, re-dump Neon, and restore again" >&2
     exit 1
   }
   ```

   The snapshot contains `o1.dump` because it was pushed through stdin, not as
   a host path. `restic` runs as root because `/opt/o1/backup` is `0700`
   root-owned. `diff` must exit 0 and print nothing; on any mismatch or any
   `pg_restore` error, stop and re-dump rather than proceeding.
6. Remove the temporary container with `docker rm -f o1-cutover-pg`, deploy
   the new release, and confirm `/healthz`, `/readyz`, login, a monitor with
   encrypted headers, and one alert email. Leave `O1_DATABASE_URL=<Neon direct
   DSN>` in `/opt/o1/.env` through the soak: the bundled Compose ignores it,
   but a failed cutover deploy automatically restores the previous pre-cutover
   release, which needs it to render.
7. Soak for 7 days: keep the Neon project, the `O1_DATABASE_URL` entry, the
   restic `pre-cutover` snapshot, and the pre-cutover release bundles. Delete
   the plaintext dump as soon as the local database is verified.

### Rollback

During the 7-day soak, rolling back to Neon is allowed. Keep the last
managed-aware release bundle, which has no `postgres` service and reads its DSN
from `O1_DATABASE_URL`. To roll back:

1. The Neon **direct** DSN is already in `/opt/o1/.env` as
   `O1_DATABASE_URL` from the cutover. If it was removed, add it back. The
   bundled stack ignores it, but the retained pre-cutover compose file
   interpolates it.
2. Activate the retained bundle with `/opt/o1/bin/rollback.sh <release-id>`.
   `up -d --remove-orphans` removes the now-unused `postgres` container,
   leaves the `o1-postgres-data` volume in place, and verifies `/readyz`.
   The same DSN is required for an automatic rollback after a failed cutover
   deploy, because that path re-renders the previous managed release.
3. Pause the backup timers; with no `postgres` service in the active release,
   the dump and drill would fail:

   ```sh
   sudo systemctl disable --now o1-backup.timer o1-backup-prune.timer \
     o1-restore-verify.timer
   ```

4. Record the data-loss window: everything written to `o1-postgres-data`
   during the soak is not on Neon. Returning to the local database later needs
   a fresh dump and restore, not the old one.
5. To return to the local database, verify a fresh Neon dump first, then
   discard the stale local state. Either remove the volume with
   `docker volume rm o1-postgres-data` (**this destroys the only local copy**;
   acceptable only because Neon is authoritative for this window) or, through
   the running container, run `DROP DATABASE o1; CREATE DATABASE o1`. Then
   restore the fresh dump and check the exit status and row counts. Never
   reuse the stale volume/database as-is: it can hold a partially diverged
   copy.

After acceptance, rollback is local-only: `rollback.sh` switches image and
config bundles, and data recovery is the Plan 12 restore procedure. Prune the
pre-cutover bundles, delete the Neon project, and remove `O1_DATABASE_URL` from
`/opt/o1/.env`. A pre-cutover release has no `postgres` service and reads
`O1_DATABASE_URL`, so running one is a deliberate move back to Neon, not an
ordinary rollback; it re-points the application at Neon and strands the writes
made to `o1-postgres-data` since the cutover.

### Backups

Backups depend on the bundled stack: the daily restic dump reads the bundled
`postgres` container, so the timers only work after the cutover. The RPO is up to
24 hours and there is no point-in-time recovery: restic keeps 30 daily and 12
monthly snapshots, prunes weekly, and runs a monthly restore drill. `.env` and
its `POSTGRES_PASSWORD`, `O1_SECRET_KEY`, and `O1_ENCRYPTION_KEY` are not in the
dump; restore them from the password manager. See
[Backups and restore](#backups-and-restore) and
[../backup/README.md](../backup/README.md).

## 4. Generate secrets

The database password is generated on the host and stored in the password
manager; no provider console is involved. Generate each secret separately:

```sh
openssl rand -hex 32     # POSTGRES_PASSWORD
openssl rand -base64 32  # O1_SECRET_KEY
openssl rand -base64 32  # O1_ENCRYPTION_KEY
```

- `POSTGRES_PASSWORD` is 32 random bytes as hex (64 characters). The first
  `initdb` bakes it into the `o1-postgres-data` volume, so changing `.env`
  afterwards does not change the role password; see
  [Bundled PostgreSQL](#3-bundled-postgresql).
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
- `POSTGRES_PASSWORD` — the hex value from step 4. Compose derives the DSN from
  it. Do not set `O1_DATABASE_URL` on a fresh install; on a cutover host keep
  the Neon DSN through the soak as described in
  [Cutover from Neon](#cutover-from-neon).
- `O1_SECRET_KEY` and `O1_ENCRYPTION_KEY` — the base64 values from step 4.
- `O1_EMAIL_FROM` — a mailbox the selected email provider accepts as a sender.
- `O1_EMAIL_PROVIDER` — `resend` (default) or `smtp`.
- `O1_RESEND_API_KEY` — the Resend sending key, required only for `resend`.
- SMTP alternative to Resend: `O1_SMTP_ADDR`, `O1_SMTP_USERNAME`,
  `O1_SMTP_PASSWORD`, and `O1_SMTP_TLS` (see [Email provider](#2-email-provider)).

`O1_CSP_EXTRA_CONNECT` is optional. It appends origins to the CSP's
`connect-src`; set it to the Sentry ingest origin when a frontend DSN is baked
into the build, for example `https://o1.ingest.sentry.io`. Leave the rate
variables (`O1_PUBLIC_RATE_*`, `O1_APIKEY_RATE_*`) unset in production.

`O1_DB_MAX_CONNS` is optional. Leave it unset for the default of `10`: three
processes use at most 30 connections against the server's
`max_connections=100`, which leaves room for migrations and `psql`.

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

- The stack starts the `postgres` service on `postgres:18-alpine` and waits for
  its `pg_isready` healthcheck. `api`, `scheduler`, and `worker` connect to
  `postgres:5432` on the private Compose network; the data lives on the
  `o1-postgres-data` volume.
- The API, scheduler, and worker run goose and River migrations against that
  database at startup. With the build override they are built from `backend/`;
  a registry deploy pulls them instead. A PostgreSQL advisory lock serializes
  migrations, so concurrent starts are safe.
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
- `postgres` logs show startup, checkpoint, and connection errors.
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
not be proven. Inspect the database directly when needed through the bundled
container. On a registry-deployed host, `/opt/o1/.env` and the active release's
compose file apply:

```sh
docker compose --env-file /opt/o1/.env \
  -f /opt/o1/current/deploy/production/compose.yaml \
  exec -T postgres psql -U o1 -d o1 -c "SELECT watermark FROM rollup_state;"
```

For a checkout stack, substitute `deploy/production/.env` and
`deploy/production/compose.yaml`. Run the remaining queries the same way, or in
one `psql` session:

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
aggregates. See [Backups and restore](#backups-and-restore).

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

## Backups and restore

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

Backups run at 02:30 UTC daily, prune Sunday 03:30 UTC (with up to 10 minutes of
randomized delay), and drill on the first of the month at 06:00 UTC (with up to
15 minutes of randomized delay). Check the timers and the last successful runs:

```sh
systemctl list-timers 'o1-*'
sudo cat /opt/o1/backup/last-success
sudo cat /opt/o1/backup/last-restore-verify
sudo cat /opt/o1/backup/last-restore-attempt
sudo journalctl -u o1-backup.service -n 100 --no-pager
```

`last-restore-attempt` records `skipped|timestamp|lock` when the drill could not
take the repository lock; `last-restore-verify` records only the last success or
failure.

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

### Unprivileged Caddy volumes

This release runs `web` as uid 1000 instead of root. Volumes created by an
earlier release are root-owned, so chown them once before the first deploy of
this release. The old stack can keep running while you do it:

```sh
docker run --rm \
  -v o1-production_caddy_data:/data \
  -v o1-production_caddy_config:/config \
  alpine chown -R 1000:1000 /data /config
```

Adjust the volume prefix if `O1_COMPOSE_PROJECT` changes the Compose project
name. Without this step Caddy cannot read the existing certificates and tries
to re-issue them, which risks ACME rate limits.

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

Rollback does **not** move data either. The `postgres` service is part of the
release's compose file, but the `o1-postgres-data` volume is not: `deploy.sh` and
`rollback.sh` run `up -d --remove-orphans` and never create, format, or remove
that volume. An image and config rollback therefore leaves the database and its
data untouched; use the Plan 12 restore procedure, not a Compose rollback, to
recover data.

The only cross-database rollback is the 7-day Neon window described in
[Rollback](#rollback) under Bundled PostgreSQL. A pre-cutover managed release
has no `postgres` service and reads `O1_DATABASE_URL`; running it is a
deliberate data move, not an ordinary rollback. After acceptance, prune those
bundles so `rollback.sh` can only select bundled-database releases.

## 11. Troubleshooting

| Symptom | Check |
|---------|-------|
| Caddy cannot obtain a certificate | DNS `A`/`AAAA` must point at this host; `80` and `443` must be reachable from the internet; no other process may own those ports. Inspect `logs -f web` for the ACME error. Fix DNS/firewall and let Caddy retry; repeated forced retries can hit ACME rate limits. |
| Browser shows `502 Bad Gateway` | The API is not ready. Check `logs api`; confirm the `postgres` container is healthy (`docker compose ... ps`) and inspect `logs postgres`. |
| API exits at startup with a config error | `O1_SECRET_KEY`/`O1_ENCRYPTION_KEY` must be base64 of exactly 32 bytes; `O1_EMAIL_FROM` must be a valid mailbox; the Resend provider requires `O1_RESEND_API_KEY`; production rejects the `mailpit` provider; `O1_DB_MAX_CONNS` must be between 1 and 100. |
| Database connection fails | Confirm the `postgres` container is healthy, that nothing on the host listens on `5432`, and that `3 x O1_DB_MAX_CONNS` fits the server's `max_connections=100`; `/readyz` also checks the database. The role password cannot be recovered from `.env`: the volume keeps the password from first `initdb`, so a value changed only in `.env` does not update the role. Reset it with `ALTER ROLE` through `docker compose --env-file /opt/o1/.env -f /opt/o1/current/deploy/production/compose.yaml exec -T postgres psql -U o1 -d o1`, update `.env` to match, and recreate `api`, `scheduler`, and `worker`. Never remove `o1-postgres-data` to fix an auth error: the volume is the only live copy. |
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

- `stop`, `down`, and deploys are safe: they leave the `o1-postgres-data`
  volume and its data in place. `down -v`, `docker volume rm`, and
  `docker system prune --volumes` destroy the only live copy of the database:
  never run them against the production compose file. Offsite restic backups
  are the only recovery path after such a removal.
- Only `80` and `443` are published. The API, the scheduler, the worker, and
  every health/metrics listener stay on the private Compose network; the
  bundled `postgres` service runs on that network, publishes no host port, and
  `5432` is never published.
- The `o1-postgres-data` volume is reachable only by the `postgres` container on
  the Compose network. Nothing else mounts it, and it is not exported over the
  network.
- `/metrics` returns `404` through Caddy.
- All secrets come from `deploy/production/.env` or `/opt/o1/.env` on a
  registry-deployed host. That file lives outside every release directory, is
  never written by CI, and survives a rollback. `env.example` contains
  placeholders only. Never commit `.env`, and keep a copy in the password
  manager: it holds `POSTGRES_PASSWORD`, `O1_SECRET_KEY`, and
  `O1_ENCRYPTION_KEY`, which a database restore needs.
- The stack sets `O1_ENV=production`, which requires a real encryption key and
  rejects the development email provider.
- Keep Caddy's `caddy_data` volume; it holds the certificates and ACME account
  material. Application data lives in `o1-postgres-data`.
- Plan 12 backups are encrypted and offsite: restic encrypts the repository, and
  the S3 credentials and restic password live only in `/opt/o1/backup/backup.env`
  and `/opt/o1/backup/restic.pass` (both `0600`, root-owned). `.env` and
  `O1_ENCRYPTION_KEY` are not uploaded and must be stored separately. Retention
  can delete expired raw probes; it is a dry run until
  `O1_RETENTION_DRY_RUN=false`, and a restore can reintroduce data deleted by
  retention. See [Retention](#retention) and
  [Backups and restore](#backups-and-restore). CI/CD deploys and the
  release/rollback process are documented in [../CICD.md](../CICD.md).
