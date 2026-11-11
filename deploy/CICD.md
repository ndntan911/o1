# CI/CD: registry publishing and automated deploys

This document is the operator guide for the release pipeline and for deploying
o1 to a host. It covers the one-time GitHub and host setup, the release and
rollback procedure, migrations, and troubleshooting. The production runbook in
[production/README.md](production/README.md) covers DNS, TLS, Resend, and
day-to-day operation of the stack itself.

## 1. Pipeline overview

```text
pull request ──► CI checks ──► merge to main
                                  │
                                  ├─ tag vX.Y.Z-rc.1 ──┐
                                  └─ tag vX.Y.Z ───────┤
                                                       ▼
                                   Release workflow (release.yml)
                                     verify: reusable ci.yml (backend,
                                       integration, frontend, e2e, pipeline)
                                     publish: build both images, scan with
                                       Trivy, push only if scans pass
                                                       │
                                                       ▼
                                   GHCR: ghcr.io/<owner>/o1-backend
                                         ghcr.io/<owner>/o1-web
                                                       │
                                                       ▼
                                   Deploy workflow (deploy.yml), dispatched
                                   by hand with environment + version
                                     environment gate: production requires
                                       reviewers and a dispatch from main
                                     validate-release.sh
                                     production: tag commit must be an
                                       ancestor of origin/main
                                     resolve digests, assemble bundle
                                     scp bundle to /opt/o1/incoming/<id>
                                     ssh /opt/o1/bin/deploy.sh <bundle>
                                                       │
                                                       ▼
                                   live release, health-checked on /readyz
```

- `.github/workflows/ci.yml` runs on every branch push and pull request. Its
  five jobs are the required status checks: `backend`, `integration`,
  `frontend`, `e2e`, `pipeline`. The `pipeline` job runs actionlint,
  shellcheck, and the Compose and deploy script suites locally in CI.
  `govulncheck` runs in `backend` as an advisory step (`continue-on-error`)
  until the Plan 14 security pass; Trivy image scans and every test suite
  remain blocking. `release.yml` reuses the workflow through `workflow_call:`,
  so a tag cannot publish code that would fail the PR gate.
- `.github/workflows/release.yml` triggers on `v*` tags. It rejects a tag that
  is not `vMAJOR.MINOR.PATCH[-PRE]`, runs verification, then builds each image
  locally, scans it with Trivy, and pushes only after both scans pass. A failed
  scan leaves the registry untouched.
- `.github/workflows/deploy.yml` is `workflow_dispatch` only. It checks out the
  requested tag, resolves the published image digests, uploads a release
  bundle, and runs the host's deploy script over SSH. It never builds images.
  For production it also fetches `main` and requires the tag commit to be an
  ancestor of `origin/main`. The release id is
  `<version>-<12-char-sha>-<run_id>-<run_attempt>`, so a retry never collides
  with an earlier run.
- Published tags per image: `vX.Y.Z` (release), `sha-<12-char-commit>`
  (immutable reference for pinning and debugging), and `X.Y` (derived from the
  base version, so `v1.2.3-rc.1` publishes `1.2`; convenience only, never
  deployed). There is no `latest`. Images are `linux/amd64` with an SBOM and
  provenance. Deploys pin images by `@sha256:` digest.
- The only supported deploy path is the Actions job. Running `deploy.sh` by
  hand on the host is recovery, not a release process.

## 2. One-time GitHub setup

1. Create the repository on GitHub and attach it as the remote:

   ```sh
   git remote add origin git@github.com:<owner>/<repo>.git
   git push -u origin main
   ```

2. In **Settings → Environments**, create `staging` and `production`.
   - `production`: enable **Required reviewers** and add at least one reviewer.
     Under **Deployment branches and tags**, allow only the `main` branch.
     The workflow also rejects pre-release versions and requires the tag commit
     to be an ancestor of `main` for production.
   - `staging`: no reviewers are required; allow `main` and the release tags
     you want to deploy.

3. Add these secrets to **each** environment. Put the `secret` rows under
   **Environment → Secrets** and the `variable` row under **Variables**. Use
   per-environment values; staging and production are different hosts.

   | Name | Kind | Purpose |
   |------|------|---------|
   | `SSH_HOST` | secret | Deploy host name or address |
   | `SSH_USER` | secret | Deploy user on the host |
   | `SSH_PRIVATE_KEY` | secret | Private key whose public half is in the deploy user's `authorized_keys` |
   | `SSH_KNOWN_HOSTS` | secret | Pinned host key line(s), including the non-default port if used |
   | `SSH_PORT` | variable | SSH port, default `22` |

   Generate a dedicated key rather than reusing a personal one:

   ```sh
   ssh-keygen -t ed25519 -C o1-deploy -f ~/.ssh/o1-deploy
   # public key -> deploy user's ~/.ssh/authorized_keys on the host
   # private key -> SSH_PRIVATE_KEY
   ```

   Populate `SSH_KNOWN_HOSTS` from the host and verify the fingerprint through
   a second channel before saving it:

   ```sh
   ssh-keyscan -p 22 o1.example.com
   ssh-keygen -lf <(ssh-keyscan -p 22 o1.example.com 2>/dev/null)
   ```

4. Optionally add the repository variable `VITE_SENTRY_DSN`
   (**Settings → Variables → Actions**) if the browser bundle should report to
   Sentry. It is empty by default, which disables Sentry in the frontend.
   `VITE_RELEASE` is set from the release tag by the workflow.

No registry credential is stored in the repository. Publishing and digest
resolution use the automatic `GITHUB_TOKEN` (`packages: write` for
`release.yml`, `packages: read` for `deploy.yml`).

## 3. Branch and tag protection

Configure these as repository rules (Settings → Rules or Branch protection
rules). They are repository settings, not code in the workflows.

For `main`:

- Require a pull request before merging; direct pushes are disabled.
- Require the status checks `backend`, `integration`, `frontend`, `e2e`, and
  `pipeline`.
- Require linear history (merge commits are rejected; use squash or rebase).
- Block force pushes and branch deletion.
- Require review conversation resolution if the team uses review threads.

For tags:

- Restrict `v*` tag creation (and movement) to maintainers. Release tags are
  immutable by policy; do not delete or re-point a published tag.
- Do not grant the Actions token permission to create tags. Release tags are
  pushed by a person.

The production GitHub Environment is the second gate: reviewers must approve
the run, only the `main` workflow ref may dispatch it, and
`deploy/scripts/validate-release.sh` rejects any version containing a
pre-release suffix and any non-`main` ref at job start. The workflow then
fetches `origin/main` and requires the checked-out tag commit to be an ancestor
of it, so an unreviewed or unmerged commit cannot be deployed even behind a
valid-looking tag.

## 4. GHCR packages

The release workflow pushes to `ghcr.io/<owner>/o1-backend` and
`ghcr.io/<owner>/o1-web`. GitHub Container Registry packages are **private by
default**.

- **Private packages (recommended):** every host needs a read credential.
  In `/opt/o1/.env` set:

  ```dotenv
  GHCR_USER=<github-user>
  GHCR_READ_TOKEN=<token with read:packages>
  ```

  Use a classic personal access token with the `read:packages` scope, or a
  fine-grained token with package read access. Do not use an account password,
  and do not commit the token. `deploy.sh` and `rollback.sh` log in on each run
  when both values are present; `bootstrap-host.sh` logs in once if they are
  set. A token that expires turns into a pull failure on the next deploy.
- **Public packages:** set the package visibility to public (repository →
  **Packages** → package → **Package settings** → **Change visibility**). The
  host may omit `GHCR_USER`/`GHCR_READ_TOKEN`. The trade-off is that anyone can
  pull and inspect the image layers; the source is already public in this repo,
  so no extra secret is exposed, but the images become trivially available.

Changing visibility does not change the image references. Deploys stay
digest-pinned either way.

## 5. Host setup

Each environment needs its own host: staging and production both bind `80` and
`443`. Prepare the host once.

1. Install Docker Engine 24+ with the Compose v2 plugin, plus `git`, `curl`,
   `flock` (util-linux), and `openssl`. Confirm:

   ```sh
   docker version
   docker compose version
   ```

2. Create the deploy user and give it Docker access and ownership of
   `/opt/o1`. Only this user is used by the workflow.

   ```sh
   sudo useradd -m -s /bin/bash o1deploy
   sudo usermod -aG docker o1deploy
   sudo install -d -m 0700 -o o1deploy -g o1deploy /opt/o1
   ```

   Put the deploy public key in `~o1deploy/.ssh/authorized_keys`.

3. Open inbound TCP `80` and `443` (for Caddy and ACME) and SSH. Do not open
   `5432`, `8080`, `8081`, `8082`, or `8083`; they are internal to Compose or,
   for `5432`, absent because the database is managed off-host.

4. Run the bootstrap script **from a checkout of the repository**, as the
   deploy user:

   ```sh
   git clone https://github.com/<owner>/<repo>.git ~/o1-src
   cd ~/o1-src
   ./deploy/scripts/bootstrap-host.sh
   ```

   The checkout is needed only for bootstrap. Deploys never read it, so you may
   remove `~/o1-src` afterwards. Rerun bootstrap from a fresh checkout (or copy
   the updated scripts into `/opt/o1/bin`) after changing anything under
   `deploy/scripts/`; the host runs the installed copies.

   The script infers the repository root from its own path. If you run a copy
   of the script from elsewhere, it infers the wrong source directory and
   fails; pass the checkout explicitly instead:

   ```sh
   ./deploy/scripts/bootstrap-host.sh --source ~/o1-src
   ```

   Run it as the deploy user (it needs Docker access). If you run it as root,
   fix ownership afterwards with
   `chown -R o1deploy:o1deploy /opt/o1`, otherwise the workflow cannot upload
   into `/opt/o1/incoming` or take the deploy lock.

   Bootstrap creates this layout and installs the stable scripts:

   ```text
   /opt/o1/
     .env                         # secrets and O1_DOMAIN; operator-managed
     bin/
       deploy.sh                  # installed by bootstrap
       rollback.sh
       healthcheck.sh
       validate-release.sh
     releases/
       <id>/                      # one immutable release bundle
         deploy/production/compose.yaml
         deploy/observability/alloy.alloy
         images.env
     current -> releases/<id>
     previous -> releases/<id>
     incoming/<id>/               # upload target consumed by deploy.sh
     state/deploy.lock
   ```

   Here `<id>` is `<version>-<12-char-sha>-<run_id>-<run_attempt>`, for example
   `v1.2.3-abc123def456-1234567890-1`. Bootstrap creates the `bin`, `releases`,
   `incoming`, and `state` directories. `state/deploy.lock` appears when the
   first deploy or rollback takes the lock, and `incoming/<id>/` appears when
   the deploy workflow uploads a bundle; bootstrap creates neither. Bootstrap
   does not start the stack. It validates the Compose file with placeholder
   digest refs and never persists them.

5. Edit `/opt/o1/.env`. It is operator-managed, lives outside the release
   directories, and is never written by CI. Bootstrap creates it from
   `deploy/production/env.example` and warns that the values are placeholders.

   ```sh
   $EDITOR /opt/o1/.env
   chmod 600 /opt/o1/.env
   ```

   Required: `O1_DOMAIN`, `O1_DATABASE_URL`, `O1_SECRET_KEY`,
   `O1_ENCRYPTION_KEY`, `O1_EMAIL_FROM`, `O1_RESEND_API_KEY`. Optional:
   `O1_DB_MAX_CONNS`, `O1_EMAIL_REPLY_TO`, `O1_RESEND_RPS`, `O1_RESEND_BURST`,
   `O1_BOOTSTRAP_ADMIN_EMAIL`, the observability values, `GHCR_USER`/
   `GHCR_READ_TOKEN` for private packages, and `DEPLOY_HEALTH_TIMEOUT`.

   `O1_DATABASE_URL` points at a managed PostgreSQL service outside the host;
   no database runs in a release bundle. See
   [production/README.md](production/README.md#3-managed-postgresql) for
   provisioning, TLS requirements, and the direct-versus-pooler distinction.

   `deploy.sh` and `rollback.sh` read `O1_DOMAIN`, `GHCR_USER`,
   `GHCR_READ_TOKEN`, and optional `DEPLOY_HEALTH_TIMEOUT` from this file. They
   read keys individually rather than sourcing it and never echo them.

6. Optional but recommended: confirm the Compose file and environment resolve
   before the first deploy:

   ```sh
   cd ~/o1-src
   docker compose --env-file /opt/o1/.env \
     -f deploy/production/compose.yaml config --quiet
   ```

   This uses the local image defaults, so it validates variables, not image
   availability.

## 6. Release process

Migrations are not reversible in this process (see §8), so review schema
changes before tagging.

1. **Merge to `main`.** The PR must pass `backend`, `integration`, `frontend`,
   `e2e`, and `pipeline` with linear history.
2. **Tag a release candidate** on `main` and push the tag:

   ```sh
   git switch main
   git pull --ff-only
   git tag -a v1.2.3-rc.1 -m "o1 v1.2.3-rc.1"
   git push origin v1.2.3-rc.1
   ```

   `release.yml` re-runs the full suite, builds `o1-backend` and `o1-web`, scans
   both, and pushes `v1.2.3-rc.1`, `sha-<sha>`, and `1.2` tags. The job summary
   lists the published digests and states that the scan gates passed; the raw
   Trivy output is in the job log, not the summary.
3. **Deploy staging.** Actions → **Deploy** → *Run workflow*, choose the
   `staging` environment and version `v1.2.3-rc.1`, leave `dry_run` off, and
   dispatch **from the `main` branch**. The workflow validates the version,
   resolves digests, and runs the host deploy script.
4. **Verify staging and run the rollback drill.** Confirm
   `readlink -f /opt/o1/current` names
   `v1.2.3-rc.1-<sha>-<run_id>-<run_attempt>` and that
   `cat /opt/o1/current/images.env` matches the digests in the job summary.
   Once staging has deployed at least two releases, run the drill:

   ```sh
   /opt/o1/bin/rollback.sh
   ```

   Confirm `current` now points at the older release, the old digests are
   running, and `https://<staging-domain>/readyz` is healthy. Roll forward with
   `/opt/o1/bin/rollback.sh v1.2.3-rc.1-<sha>-<run_id>-<run_attempt>` if the
   new release should be restored. This drill proves the previous release and
   its images are intact before production needs them.

   A dry run is also useful before the first real staging deploy: dispatch
   with `dry_run` enabled, or run
   `/opt/o1/bin/deploy.sh /opt/o1/incoming/<id> --dry-run` on the host. It
   validates the bundle and prints the compose and health steps without moving
   the release or touching containers.
5. **Tag the final release** after staging acceptance:

   ```sh
   git tag -a v1.2.3 -m "o1 v1.2.3"
   git push origin v1.2.3
   ```

   Wait for `release.yml` to publish the final digests.
6. **Deploy production.** Dispatch **Deploy** with environment `production`,
   version `v1.2.3`, from `main`. The production Environment pauses for
   reviewer approval. Approving starts the same deploy path: digest resolution,
   bundle upload, `deploy.sh`, readiness check.

Production accepts only final `vX.Y.Z` versions; a pre-release tag is rejected
before any SSH connection. Staging accepts pre-release `vX.Y.Z-rc.N` tags as
well as final versions. The deploy input is always a tag; the published
`sha-<12>` tags are for pinning and debugging, not a separate deploy path.

### Retries and pruning

The release id includes the Actions run id and attempt, so every dispatch —
including a retry of the same version — produces a new
`releases/<version>-<sha>-<run_id>-<run_attempt>` directory. Retrying a failed
run is simply another dispatch; it never reuses or overwrites an existing
release directory.

After confirming the active release is healthy, keep the `current` and
`previous` bundles and prune older `releases/<id>` directories to bound disk
use. Never remove a directory that a symlink still points at:

```sh
readlink -f /opt/o1/current /opt/o1/previous
find /opt/o1/releases -mindepth 1 -maxdepth 1 -type d -print
rm -rf /opt/o1/releases/<old-id>
```

## 7. Manual recovery

Manual access is for recovery, not for routine releases. `deploy.sh` and
`rollback.sh` share `/opt/o1/state/deploy.lock`: a second concurrent deploy or
rollback exits with code `3` and changes nothing.

### Roll back to the previous release

```sh
/opt/o1/bin/rollback.sh
```

With no argument the target is the `previous` symlink. To choose a release, or
to roll forward after a fix, pass the release directory name:

```sh
/opt/o1/bin/rollback.sh v1.2.2-abc123def456-1234567890-1
```

`rollback.sh` switches `current`, sets `previous` to the release that was
active, pulls the pinned images, runs `up -d --remove-orphans`, and polls
`https://$O1_DOMAIN/readyz` with the same timeout as `deploy.sh`. If the target
is unhealthy it restores the release that was current and exits non-zero.

The managed database is outside every release bundle. A release built before
the managed cutover still contains the bundled `postgres` service, so rolling
back to it starts an **empty local database** and points the application at it:
`/readyz` passes while production data is unreachable. After the first verified
managed deploy, prune the pre-cutover release directories, and keep the cutover
`pg_dump` until the managed database is accepted. Only roll back to
managed-aware releases.

Inspect what is deployed at any time:

```sh
readlink -f /opt/o1/current
readlink -f /opt/o1/previous
cat /opt/o1/current/images.env
docker compose --env-file /opt/o1/.env \
  --env-file /opt/o1/current/images.env \
  -f /opt/o1/current/deploy/production/compose.yaml ps
```

### Re-run a deploy

```sh
/opt/o1/bin/deploy.sh /opt/o1/incoming/<release-id>
```

`deploy.sh` validates the bundle in `incoming/` and only then moves it to
`releases/<release-id>`. A bundle that fails validation (missing file or key, a
non-digest image ref, invalid Compose configuration) stays in `incoming/` and
can be corrected and re-run in place. A duplicate release id is also rejected
before the move; use a new incoming directory name, or activate the existing
release with `rollback.sh`. Failures after the move (GHCR login, pull, compose
up, or health) leave the bundle under `releases/`, and the script refuses an
existing release id on a re-run; copy it into a fresh incoming directory first:

```sh
cp -a /opt/o1/releases/<release-id> /opt/o1/incoming/<release-id>-retry
/opt/o1/bin/deploy.sh /opt/o1/incoming/<release-id>-retry
```

To activate a bundle that is already in `releases/` after fixing the cause,
use `rollback.sh <release-id>` instead.

A failed **first** deploy that reached the release switch has no previous
release. `deploy.sh` reports that there is nothing to roll back to, leaves the
new release active with its containers in place for inspection, and exits
non-zero. Fix the host or the bundle, then retry from a fresh incoming copy.

`deploy.sh` reads `O1_DOMAIN` from `/opt/o1/.env`, so the health check always
targets the host's domain; CI never passes it. A missing `.env`, missing
`O1_DOMAIN`, a non-digest image reference, or a missing compose/Alloy file
fails before any container is touched.

### Offline or manual build on a checkout

If the registry is unavailable, or you deliberately want a local build, use the
build override from a checkout. It restores the `build:` contexts and fixes the
image names to `o1-backend:local` and `o1-web:local`, so an `images.env` on the
host cannot leak into the build.

```sh
docker compose --env-file /opt/o1/.env \
  -f deploy/production/compose.yaml \
  -f deploy/production/compose.build.yaml up -d --build
```

Run this from the repository root on the host. It publishes `80`/`443` with the
production domain from `/opt/o1/.env`, so it is not a workstation path. This
bypasses the release symlinks: `current`/`previous` still name the last registry
release, and a later `deploy.sh` or `rollback.sh` replaces the locally built
containers.

## 8. Migrations

- Migrations run at process startup from `api`, `scheduler`, and `worker`. A
  PostgreSQL advisory lock serializes them, so concurrent starts are safe.
  There is no separate migration job or container.
- Migrations run against the managed database over the network. A failed
  migration still fails `/readyz`; provider point-in-time recovery, not release
  rollback, is the data recovery path. Confirm PITR is enabled before setting
  `O1_RETENTION_DRY_RUN=false`.
- Deploy pipelines do not invoke retention directly, but a deploy or rollback
  recreates the scheduler, and every scheduler restart runs one startup
  maintenance pass. With `O1_RETENTION_DRY_RUN=false`, that pass can drop up to
  `O1_RETENTION_MAX_PARTITION_DROPS` raw partitions immediately; the scheduler
  also runs retention daily. `O1_RETENTION_DRY_RUN` controls deletion.
- This release builds a new index on `probe_results`. Schedule the first deploy
  off-peak and expect probe writes to pause while it builds.
- Migrations are **forward-only**. There are no down migrations.
- Image and config rollback does **not** undo schema. Restoring an earlier
  release restores its compose file and digest-pinned images only. If the new
  release migrated the schema, the old code runs against the new schema.
- Therefore schema changes must be backward compatible for at least one
  release: add columns and tables, backfill, and only remove the old shape
  after every deployable previous release no longer reads it. A migration that
  rewrites or drops what the previous code needs makes rollback unsafe.
- Review the migrations in `backend/internal/db/migrations/` before tagging,
  and note any schema change in the release notes. A failed migration makes
  `/readyz` fail, which triggers the automatic restore, but the schema may
  already be partway ahead. Read the API logs and fix forward; do not assume a
  rollback undoes it.

## 9. Observability

Alloy is behind the `observability` Compose profile and is not part of the
default service set. Deploys run `up -d --remove-orphans` for the base
services and leave a running Alloy container untouched, with its old config.

If a release changes `deploy/observability/alloy.alloy`, restart Alloy
explicitly after the deploy so it picks up the new file from the active
release:

```sh
docker compose --env-file /opt/o1/.env \
  --env-file /opt/o1/current/images.env \
  -f /opt/o1/current/deploy/production/compose.yaml \
  --profile observability up -d alloy
```

For staging, add `O1_COMPOSE_PROJECT=<staging-project>` to that environment's
`.env`; the default project name is `o1-production`.

Alloy refuses to start without `GRAFANA_REMOTE_WRITE_URL`,
`GRAFANA_REMOTE_WRITE_USER`, and `GRAFANA_REMOTE_WRITE_TOKEN`, and it does not
restart-loop. See [observability/README.md](observability/README.md) for
Grafana Cloud setup. Point the external dead-man's switch at
`https://$O1_DOMAIN/healthz`, never at `/metrics`.

## 10. Troubleshooting

| Symptom | Cause and fix |
|---------|----------------|
| `Host key verification failed` | The `SSH_KNOWN_HOSTS` secret does not match the host, or the host was rebuilt. Re-run `ssh-keyscan -p <port> <host>`, verify the fingerprint out of band, and update the secret. Never disable host-key checking. |
| `Permission denied (publickey)` | The public key is not in the deploy user's `authorized_keys`, the wrong user is configured, or the key is passphrase-protected. `ssh -i <key> <user>@<host>` from your machine isolates it. |
| `denied` / `unauthorized` while pulling from GHCR | Private package without credentials, a token missing `read:packages`, or an expired token. Set `GHCR_USER` and `GHCR_READ_TOKEN` in `/opt/o1/.env`, or make the packages public. On the host, `docker login ghcr.io` reproduces the error. `manifest unknown` means the version tag was never published or the owner name is wrong. |
| Deploy fails at readiness | `/readyz` also checks PostgreSQL. Inspect `docker compose ... logs api` and `logs web`; a first deploy includes ACME issuance and migrations, so `DEPLOY_HEALTH_TIMEOUT` (default 300s) may be too short. `deploy.sh` restores the previous release automatically and exits non-zero. |
| Migration failure | The API exits or `/readyz` fails while the schema is mid-change. Read the migration error in `logs api`. Fix forward with a new release; rolling back code does not reverse the schema (see §8). |
| Trivy blocks a release | A `HIGH` or `CRITICAL` fixable vulnerability in an image (unfixed findings are ignored), or a repository secret/misconfiguration. Upgrade the base image or dependency, remove the secret, or add a narrowly scoped `.trivyignore` entry with a comment naming the check, the reason it is accepted, and a review date. The only current exception is `DS-0002` (the web Caddy image runs as root to bind 80/443; re-review 2027-05-10). |
| `deploy: missing /opt/o1/.env` | Bootstrap has not run, or the file is at the wrong path. `bootstrap-host.sh` seeds it from `env.example`; edit it before deploying. A missing `O1_DOMAIN`, `O1_DATABASE_URL`, or app key fails production Compose. |
| Deploy fails because the database is unreachable | Check the managed DSN, `sslmode` and CA material, that the endpoint is direct (not a transaction pooler), the provider firewall/allowlist for the host address, and that `3 x O1_DB_MAX_CONNS` fits the tier's connection limit. `/readyz` checks the database, so a bad DSN fails the deploy and triggers the automatic restore. |
| `deploy: another deploy or rollback holds ...` | A previous run is still active. Exit code is `3`. The kernel releases the lock when that process exits; do not delete the lock file while a deploy is running. If no deploy exists, check with `fuser /opt/o1/state/deploy.lock` and remove the file only when nothing holds it. |
| Alloy still scrapes the old config | The deploy does not restart profiled services. Re-run the `--profile observability up -d alloy` command in §9. |
| API or web version unchanged after a successful deploy | Check `readlink -f /opt/o1/current` and `cat /opt/o1/current/images.env`. A manual build override leaves local `:local` images running; the next deploy replaces them. |
| Caddy/TLS or Resend failures | See [production/README.md](production/README.md); those are stack issues, not pipeline issues. |

## 11. Security

- **No laptop deploys.** The only supported release path is `deploy.yml` with
  an approved Environment. Manual `deploy.sh`/`rollback.sh` access is for
  recovery and requires host access.
- **Secrets never enter git or images.** Compose receives secrets from
  `/opt/o1/.env` (mounted nowhere) and environment variables. CI secrets live
  in GitHub Environments. The repository has no registry credential; the
  automatic `GITHUB_TOKEN` is scoped per job. `env.example` holds placeholders
  only. Never commit `.env`, `images.env` with real refs, or a token.
- **Least-privilege tokens.** The host PAT needs only `read:packages` and
  should expire. The deploy SSH key is dedicated, per-environment, and stored
  only as an Environment secret. Revoke both when a host is retired.
- **Host-key pinning.** The workflow uses
  `StrictHostKeyChecking=yes` with `SSH_KNOWN_HOSTS`; never add
  `StrictHostKeyChecking=no` or `UserKnownHostsFile=/dev/null`.
- **Digest pinning.** `deploy.sh` and `rollback.sh` reject anything that is not
  `<image>@sha256:<64 hex>`, so a moved tag cannot silently change a deploy.
  Tags are convenience; digests are the deployed artifact.
- **Scans before push.** Trivy runs on the filesystem (`secret,misconfig`,
  `HIGH,CRITICAL`) and on both images (`HIGH,CRITICAL`, unfixed ignored)
  before any push. `govulncheck` runs in `backend` as an advisory step until
  the Plan 14 security pass: reachable findings include the Go standard library
  and no-fix testcontainers/container modules, so it reports without blocking.
  The test suites and Trivy image scans remain blocking. The single
  `.trivyignore` exception is `DS-0002`: `web/Dockerfile` builds on the
  official Caddy image, which runs as root to bind 80/443; TLS terminates
  there and no application code runs as root in that container. Re-review
  2027-05-10. Any new exception must carry a comment naming the check/CVE, the
  reason, and a review date; review exceptions whenever the file changes.
- **Repository controls.** Required checks, linear history, no force pushes,
  restricted `v*` tags, and a reviewed production Environment. Together they
  prevent untested or unreviewed code from reaching a host.
- **Private packages by default.** Public packages are allowed but expose the
  image layers; keep them private unless there is a reason not to.

## 12. Verification and live acceptance

### Local gate

These run without a host and must pass before a release is cut:

```sh
docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.12
docker run --rm -v "$PWD:/repo" -w /repo koalaman/shellcheck:v0.11.0 deploy/scripts/*.sh
bash -n deploy/scripts/*.sh
bash deploy/scripts/test-compose-config.sh
bash deploy/scripts/test-deploy.sh
bash deploy/scripts/test-bootstrap.sh
bash deploy/scripts/test-workflows.sh
docker run --rm -v "$PWD:/repo" -w /repo aquasec/trivy:0.76.2 fs --scanners secret,misconfig --severity HIGH,CRITICAL --exit-code 1 .
cd backend && go test -short ./...
cd ../web && npm test -- --run
```

`govulncheck` is advisory, not part of the blocking gate:

```sh
cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

It is expected to report reachable findings until the Plan 14 security pass;
the CI step uses `continue-on-error: true`. The script tests use fake `docker`,
`curl`, and `flock` binaries on `PATH` to
prove activation, readiness polling, rollback on failed login/pull/up/health,
first-deploy behavior, lock exclusion, and dry-run. The Compose tests prove the
fixture digests propagate into the rendered config and the build override still
validates. Digest enforcement is not a Compose check: `deploy.sh` and
`rollback.sh` reject non-digest refs, and `test-deploy.sh` covers that.

### Live acceptance checklist

The publish and deploy jobs cannot be exercised without a real GitHub remote
and a provisioned host; treat them as unverified until this checklist passes on
the real infrastructure.

- [ ] Push a `vX.Y.Z-rc.1` tag; confirm `release.yml` runs the full CI suite,
      builds and scans both images, and publishes them to GHCR. The job summary
      lists both digests.
- [ ] Confirm the pushed tags are `vX.Y.Z-rc.1`, `sha-<12>`, and `X.Y`, and
      that the images run on `linux/amd64`.
- [ ] Deploy staging from `main`; confirm the workflow completes and
      `readlink -f /opt/o1/current` names the expected release.
- [ ] Confirm the digests in the deploy job summary match
      `/opt/o1/current/images.env` and the running containers
      (`docker inspect --format '{{.Config.Image}}' <container>`).
- [ ] Confirm staging serves `/healthz`, `/readyz`, and `/` over HTTPS, and
      that `/metrics` returns `404`.
- [ ] Force an unhealthy release (for example an image that fails readiness);
      confirm `deploy.sh` restores the previous release, its compose file, and
      its digests, and exits non-zero.
- [ ] Deploy production with environment `production`; confirm the run pauses
      for reviewer approval, and that a pre-release tag or a non-`main` ref is
      rejected before any SSH connection.
- [ ] Confirm production rejects a tag whose commit is not an ancestor of
      `origin/main` before any SSH connection.
- [ ] Confirm production is healthy after the deploy and that
      `/opt/o1/previous` names the prior release.
- [ ] Roll back production once with `/opt/o1/bin/rollback.sh`, confirm health,
      then roll forward to the intended release.
- [ ] Confirm a second concurrent deploy or rollback exits with code `3` and
      changes nothing.
- [ ] Confirm Alloy's container was not restarted by the deploy, and restarts
      cleanly with `--profile observability up -d alloy` after a config change.
- [ ] Confirm no secret appears in `git log`, image history, workflow logs, or
      the job summary.
