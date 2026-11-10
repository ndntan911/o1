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
bundle to `/opt/o1/incoming/<version>-<sha>` and runs
`/opt/o1/bin/deploy.sh`, which activates it and polls `/readyz`. Rollback is
`/opt/o1/bin/rollback.sh`.

See [../CICD.md](../CICD.md) for the pipeline overview, GitHub Environments and
secrets, branch and tag protection, GHCR visibility, host bootstrap, the
release and rollback drill, migrations, and troubleshooting. The manual
commands in this runbook remain the first-run, offline, and recovery path, and
they exercise the same compose file the deploy script uses.

## Plan boundaries

- **This plan (Plan 10)** ships production email through Resend and this manual
  go-live stack.
- **CI/CD is Plan 11 and is implemented.** Deploys normally run from the
  Deploy workflow with digest-pinned registry images; the manual commands here
  remain for first-run, offline, and recovery use. See [../CICD.md](../CICD.md).
- **Backups and restore are Plan 12.** There is no backup job, retention policy,
  or restore procedure in this stack yet. Until Plan 12 lands, take and store a
  `pg_dump` yourself if the data matters, and note that the Caddy volumes hold
  the TLS certificates, not application data.
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
[Administrator bootstrap](#6-administrator-bootstrap).

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

The base `compose.yaml` pulls digest-pinned images and has no build contexts;
`compose.build.yaml` adds them and pins the local image names. A plain
`up -d --build` without the override therefore fails on a fresh checkout.

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
- `scheduler` logs show due-monitor enqueueing and partition maintenance.

## 8. Update and restart

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

Migrations run automatically at startup. To apply only environment changes,
recreate the affected services:

```sh
docker compose --env-file deploy/production/.env \
  -f deploy/production/compose.yaml \
  -f deploy/production/compose.build.yaml up -d --force-recreate api scheduler worker
```

(Adding the override is harmless for environment-only changes and keeps the
local image names consistent with the manual build.)

Frontend `VITE_*` values are baked into the static bundle at build time. Changing
`VITE_SENTRY_DSN` or `VITE_RELEASE` requires rebuilding the `web` image.

## 9. Rollback

Each successful registry deploy stores an immutable bundle under
`/opt/o1/releases/<version>-<sha>/` containing the release's
`deploy/production/compose.yaml`, `deploy/observability/alloy.alloy`, and
digest-pinned `images.env`. The `current` and `previous` symlinks select the
active and prior bundles; `/opt/o1/.env` sits outside the releases so secrets
are unchanged by a rollback.

Roll back to the previous release:

```sh
/opt/o1/bin/rollback.sh
```

Roll back to a named release, or roll forward after a fix:

```sh
/opt/o1/bin/rollback.sh v1.2.2-abc123def456
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

## 10. Troubleshooting

| Symptom | Check |
|---------|-------|
| Caddy cannot obtain a certificate | DNS `A`/`AAAA` must point at this host; `80` and `443` must be reachable from the internet; no other process may own those ports. Inspect `logs -f web` for the ACME error. Fix DNS/firewall and let Caddy retry; repeated forced retries can hit ACME rate limits. |
| Browser shows `502 Bad Gateway` | The API is not ready. Check `logs api`; verify PostgreSQL is healthy and `O1_DATABASE_URL` is correct. |
| API exits at startup with a config error | `O1_SECRET_KEY`/`O1_ENCRYPTION_KEY` must be base64 of exactly 32 bytes; `O1_EMAIL_FROM` must be a valid mailbox; the Resend provider requires `O1_RESEND_API_KEY`; production rejects the `mailpit` provider. |
| Database authentication fails | `POSTGRES_PASSWORD` must match across the first volume initialization and `O1_DATABASE_URL`. If it contains URL-special characters, percent-encode it or regenerate as hex. Changing it after the volume exists does not change the PostgreSQL role password; see [step 3](#3-generate-secrets) to recover without deleting data. |
| Email not delivered | Check the Resend email log and quota, domain verification, API key, and that the from/reply-to addresses are on the verified domain. Check the incident delivery status and `logs worker`. |
| `X-Forwarded-For` warning in Caddy logs | Caddy prints an "Unnecessary header_up X-Forwarded-For" warning at startup. It is informational: the explicit setting overwrites client-supplied values so the API sees the proxy peer. |
| Ports already in use | Stop the development stack (`docker compose down` at the repository root) or any other service on `80`/`443`. |

## 11. Optional metrics with Grafana Alloy

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

## 12. Security notes

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
- Backups are Plan 12. CI/CD deploys and the release/rollback process are
  documented in [../CICD.md](../CICD.md).
