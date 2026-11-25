# Self-Hosted PostgreSQL on the Shared VPS — Design

**Status:** Draft for approval
**Date:** 2026-11-25
**Depends on:** Plan 10 (production stack), Plan 11 (host scripts / CI-CD), Plan 12 (backups and restore drills)
**Supersedes:** `2026-11-11-managed-postgres-design.md` §6.1–6.2 (Compose/env shape) and the "backups come from the provider" statements in `deploy/production/README.md`

## 1. Intent

Production runs on Neon's Free plan and is about to hit its limits. Instead of
upgrading to a paid managed tier (or paying ~$150+/month for OCI Database with
PostgreSQL), run PostgreSQL inside the existing production Compose stack on the
shared Oracle Cloud VM.

The application does not change: pgx, River, sqlc, goose migrations, `citext`,
advisory locks, and the `probe_results` partition maintenance all keep working
against a local database. What changes is who owns the database — and therefore
backups, upgrades, disk, and failure modes.

Backups use the Plan 12 tooling already written on the `plan12-backups-restore`
branch: a daily `pg_dump -Fc` streamed into `restic` and uploaded to
S3-compatible offsite storage, plus weekly prune and a monthly automated restore
drill. RPO is up to 24 hours; there is no PITR. This is the "minimum level" the
user chose.

## 2. Current State

- `deploy/production/compose.yaml` deliberately has no database service and no
  database volume. `O1_DATABASE_URL` is required input and points at a managed
  service (Neon today).
- `api`, `scheduler`, and `worker` all run startup migrations under a PostgreSQL
  advisory lock; River owns its own schema; `00001_auth.sql` installs `citext`;
  the scheduler creates and drops monthly `probe_results` partitions.
- Each process uses `O1_DB_MAX_CONNS` (default 10), so the stack holds up to 30
  connections plus migration headroom. The production host has
  `O1_DB_MAX_CONNS` unset.
- A direct/session connection is required: advisory locks and River's semantics
  rule out a transaction pooler.
- `deploy/scripts/test-compose-config.sh` currently asserts the *absence* of a
  `postgres` service and `postgres-data` volume, and that every backend service
  gets `O1_DATABASE_URL` from the env file.
- Branch `plan12-backups-restore` (local and `origin`) contains a complete,
  reviewed backup system: `deploy/backup/*`, `deploy/scripts/backup.sh`,
  `backup-lib.sh`, `install-backup.sh`, `restore-verify.sh`, `test-backup.sh`,
  systemd units, and `deploy/backup/README.md`. It targets a bundled Postgres
  container (`docker compose ... exec -T postgres pg_dump ...`), which is
  exactly the shape this design restores. It is not merged into `main`.
- The branch's `deploy/production/README.md` and root `README.md` still carry
  managed-database text from `main`, so they need reconciling.
- Measured on 2026-11-25:
  - Neon is **PostgreSQL 18.7**, `C.UTF-8` / `C.UTF-8`, UTF8, 30 MB, extensions
    `citext` and `plpgsql` only. Nothing requires a glibc image.
  - The VM has 4 OCPU, 29 GiB RAM (28 GiB available), and 43 GB free on a 49 GB
    boot volume. There is **no** legacy `postgres-data` volume; the only volumes
    are Alloy and Caddy state.
  - Running containers: `o1-production-{web,api,worker,scheduler,alloy}`. The
    Compose project name is `o1-production`.

## 3. Goals

1. The production stack starts a pinned `postgres` container with a named
   volume, a healthcheck, and no published host port.
2. `api`, `scheduler`, and `worker` wait for the database healthcheck and read
   `O1_DATABASE_URL` from Compose, derived from a single secret.
3. Daily encrypted offsite backups with 30-day daily / 12-month monthly
   retention, failure alerting, and a monthly restore drill (Plan 12).
4. A documented, tested cutover from Neon to the local database with a short
   maintenance window and an explicit rollback path.
5. Host footprint is bounded so PostgreSQL cannot starve the other workloads on
   the shared VM.
6. The existing test and deploy pipelines keep passing, with their database
   assertions updated to the new shape.

## 4. Non-Goals

- Point-in-time recovery, WAL archiving, streaming replicas, failover, or a
  standby host.
- OCI Database with PostgreSQL, Oracle Database, or any managed database.
- Terraform, Ansible, or other host automation beyond the existing scripts.
- PgBouncer or any connection pooler.
- Backing up `/opt/o1/.env`, `O1_ENCRYPTION_KEY`, `O1_SECRET_KEY`, the restic
  password, or S3 credentials; those stay in the operator's password manager.
- Changes to application code, schema, queries, or the development stack.
- Multi-tenant or per-app database separation on the VM.

## 5. Anti-Goals

- A database reachable from the Internet or published on `5432`.
- A plaintext dump left on the host's disk after the cutover is verified.
- A backup repository stored on the same block volume as the data.
- A backup job that fails silently while the operator believes it works.
- A restore that has never been drilled.
- A `postgres` container that consumes unbounded RAM/CPU on a shared host, or
  unbounded Docker logs filling the boot volume.
- Losing the Neon database before the local database is verified and accepted.
- A rollback that points a pre-cutover release at an empty local database.

## 6. Approaches Considered

### 6.1 Hosting

**A. `postgres` service in the production Compose stack (chosen).** Matches the
development stack and the pre-managed production shape; the Plan 12 backup
scripts already exec into `docker compose ... postgres`; one deploy path, one
network, no published port. Cost: the database's lifecycle is coupled to the app
stack, so `down -v` is destructive and upgrades are Compose edits.

**B. Separate Compose project (`deploy/db/`) on an external network.** Better
lifecycle isolation and a clean path to a second consumer. Rejected for now: it
adds a second set of commands, a shared network, and installer work, for one VM
and one app. Revisit when the VM hosts a second workload that needs the DB.

**C. Native PostgreSQL via apt + systemd.** Rejected: mixes configuration
management models (host packages plus containerized app), duplicates the
development/runtime difference, and gains nothing at this size.

### 6.2 Backup

**A. Plan 12: host systemd timer, `pg_dump -Fc -Z 6` piped into `restic`, S3
offsite (chosen).** One tool for encryption, dedupe, retention, and integrity;
no plaintext dump on disk; restore drill included. RPO ≤ 24h.

**B. pgBackRest / WAL-G with WAL archiving.** Rejected by the user's choice:
PITR is more setup and more failure modes than needed for a <1 GB database.

**C. Provider-managed backups (stay on Neon paid / OCI managed).** Rejected:
that is the cost this change exists to remove.

## 7. Design

### 7.1 `deploy/production/compose.yaml`

- Add a `postgres` service:
  - `image: postgres:18-alpine`, tag-pinned (the deploy script's digest
    enforcement covers only the application images). The source is PostgreSQL
    18.7 with `C.UTF-8`/UTF8 (measured 2026-11-25), so Alpine's musl locale
    support is sufficient; `POSTGRES_INITDB_ARGS=--locale=C.UTF-8
    --encoding=UTF8` pins the locale to match the source.
  - `environment`: `POSTGRES_USER=o1`, `POSTGRES_DB=o1`,
    `POSTGRES_PASSWORD=${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}`,
    `POSTGRES_INITDB_ARGS=--locale=C.UTF-8 --encoding=UTF8`, `LANG=C.UTF-8`,
    `TZ=UTC`, `PGTZ=UTC`.
  - `volumes: postgres-data:/var/lib/postgresql` — the PostgreSQL 18 image's
    `PGDATA` is `/var/lib/postgresql/18/docker` and its declared volume is
    `/var/lib/postgresql`. Mounting `/var/lib/postgresql/data` (the pre-18
    path) would leave the named volume empty.
  - The volume is declared with an explicit name, `o1-postgres-data`, so an
    old `o1-production_postgres-data` volume from before the managed cutover
    can never be picked up accidentally (see §7.5.0). The current host has no
    such legacy volume.
  - `POSTGRES_PASSWORD`, encoding, locale, and image major are baked into the
    volume at first `initdb`; all of them must be final before the container
    starts for the first time.
  - `healthcheck`: `pg_isready -h 127.0.0.1 -U o1 -d o1`, 5s interval, 5s
    timeout, 10 retries, `start_period: 30s`. The TCP host avoids the
    entrypoint's temporary unix-socket server during `initdb`.
  - No `ports:` entry. The container is reachable only on the Compose network.
  - `shm_size: 256mb`; `stop_grace_period: 60s`;
    `restart: unless-stopped`.
  - `logging`: `json-file` with `max-size: 10m`, `max-file: 3`, so a noisy
    database cannot fill the boot volume.
  - `command`: tuned for the measured 4 OCPU / 29 GiB host, which also runs
    `api`, `scheduler`, `worker`, `web`, and Alloy: `shared_buffers=1GB`,
    `effective_cache_size=4GB`, `work_mem=16MB`,
    `maintenance_work_mem=256MB`, `max_connections=100`,
    `wal_compression=on`, `checkpoint_completion_target=0.9`. The database is
    30 MB, so these are headroom caps, not a sizing requirement.
  - `mem_limit: 3g` and `cpus: 2.0` bound the container on the shared VM.
- Add `volumes: postgres-data:`.
- In the shared backend environment anchor, replace the managed DSN with
  `O1_DATABASE_URL: "postgres://o1:${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}@postgres:5432/o1?sslmode=disable"`.
  One secret, no drift between container password and DSN. Passwords must be
  hex (as the runbook already generates) so URL-encoding is never an issue.
- Add `depends_on: postgres: {condition: service_healthy}` to `api`,
  `scheduler`, and `worker`; `web` keeps `depends_on: [api]`.
- File header comment updated: the stack bundles PostgreSQL again; TLS is not
  used on the private bridge network; `5432` stays unpublished.

### 7.2 `deploy/production/env.example` and `.env`

- Required: `POSTGRES_PASSWORD` (32-byte hex), `O1_SECRET_KEY`,
  `O1_ENCRYPTION_KEY`, `O1_DOMAIN`, mail settings.
- Remove the managed `O1_DATABASE_URL` requirement and its TLS notes. The DSN
  is derived inside Compose, so a stray `O1_DATABASE_URL` in `.env` cannot
  override it. The only way to point at an external database again is to run
  the retained pre-cutover release bundle, whose Compose file still requires
  `O1_DATABASE_URL` (§7.6).
- Keep `O1_DB_MAX_CONNS=10` documented, with the connection budget: three
  processes × 10, plus migration headroom, against `max_connections=100`.
- `.env` remains untracked; the runbook keeps instructing the operator to store
  it (and `POSTGRES_PASSWORD`) in the password manager, because backups do not
  include it.

### 7.3 Backups (merge Plan 12, adapt docs)

- Merge `plan12-backups-restore` into `main` first; do not reimplement it.
- Adapt `deploy/backup/README.md` and `backup.env.example`:
  - `RESTIC_REPOSITORY` example uses OCI Object Storage's S3-compatible
    endpoint: `s3:https://<namespace>.compat.objectstorage.<region>.oraclecloud.com/<bucket>/o1`.
  - `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` are OCI Customer Secret Keys,
    scoped to the bucket/prefix.
  - Restic must be the ARM64 build on Ampere A1 hosts; installation section
    keeps the pinned version and covers both architectures.
  - `O1_BACKUP_PG_CONTAINER` stays optional but unset: the script uses
    `docker compose -f "$O1_COMPOSE_FILE" exec -T postgres`, which is now the
    production database.
  - `O1_COMPOSE_FILE=/opt/o1/current/deploy/production/compose.yaml` and
    `O1_ENV_FILE=/opt/o1/.env` remain correct.
  - Plan 12 hardcodes `postgres:16-alpine` in `restore-verify.sh` (scratch
    container) and `test-backup.sh`. The scratch container must match the dump's
    major or the restore cannot be read, so both become configurable through
    `O1_BACKUP_PG_IMAGE` with the production default `postgres:18-alpine`;
    `test-backup.sh` keeps pinning its own disposable image.
- Keep the Plan 12 schedule: daily dump 02:30 UTC, weekly prune Sunday 03:30
  UTC, monthly restore drill, 30 daily / 12 monthly snapshots, external success
  ping plus one failure path, shared repository lock, scratch-container drill.
- Object Storage sits in the Always Free allowance (20 GB total), which is
  ample for a <1 GB database with restic dedupe. Retention is independent of
  the app's 90-day raw / 13-month rollup retention.

### 7.4 Data and disk

- The named volume lives on the 49 GB boot block volume (43 GB free measured).
  The database is 30 MB, and one restic cache plus a 30 MB dump is negligible,
  so no extra block volume is needed at cutover. `O1_BACKUP_MIN_FREE_KB` is set
  to 2 GiB, and the runbook records the free-space baseline.
- Growth is driven by `probe_results` retention (90 days raw, dry-run today).
  If the database or restic cache approaches 50% of the disk, attach a separate
  block volume and move the Docker data root or the Postgres volume to it; that
  is a later operation, not part of this change.
- The scheduler's retention job is currently dry-run in production; the
  cutover does not change that. Retention stays a separate decision.
- Monitoring: the existing pool/queue collectors keep working; add an operator
  check (manual or node_exporter if present) for volume free space. No new
  application metrics.

### 7.5 Cutover from Neon (short maintenance window)

0. **Volume and parameter preflight (measured 2026-11-25).** `docker volume ls`
   shows no legacy `postgres-data` volume, so there is nothing to preserve; the
   new volume is still explicitly named `o1-postgres-data`. Generate
   `POSTGRES_PASSWORD` before the first `initdb`, with the image major
   (`18`), encoding (UTF8), and locale (`C.UTF-8`) already final, because they
   cannot be changed afterwards without recreating the volume.
1. **Source preflight (recorded).** Neon is PostgreSQL 18.7, database
   `neondb`, `C.UTF-8`/`C.UTF-8`, UTF8, 30 MB, extensions `citext` and
   `plpgsql` only. Therefore: use `postgres:18-alpine`, no
   `POSTGRES_INITDB_ARGS`, and `citext` is available in the image. Before the
   cutover, start a throwaway `postgres:18-alpine` container and confirm
   `initdb` + `CREATE EXTENSION citext` succeed. Re-run the checks in this step
   if the Neon project changes before cutover.
2. **Provision.** Add the `postgres` service and volume, start only `postgres`,
   and confirm healthy.
3. **Freeze writes.** `docker compose stop worker`, wait for drain (River's
   graceful stop, bounded by `SoftStopTimeout`), then `stop scheduler`, then
   `stop api`. Leave Postgres and web up.
4. **Dump.** Run a one-off `postgres:18-alpine` client container against the
   Neon direct, non-pooled endpoint (`neondb` database):
   `docker run --rm postgres:18-alpine pg_dump -Fc -Z 6 -d
   "$NEON_DIRECT_URL" -f - > /opt/o1/state/o1-pre-local.dump`, mode `0600`, on
   the data disk rather than `/tmp`. The 30 MB database dumps in seconds.
   Immediately back the file up with restic (`restic backup
   /opt/o1/state/o1-pre-local.dump --tag o1-db --tag pre-cutover`) so the
   durable copy is encrypted and offsite. The plaintext file is deleted in
   step 7.
5. **Restore.** Stream the dump into the target container so the host needs no
   PostgreSQL client: `docker compose ... exec -T postgres pg_restore -U o1 -d
   o1 --no-owner --no-privileges < /opt/o1/state/o1-pre-local.dump`. Then
   compare row counts on `users`, `workspaces`, `monitors`, `probe_results`,
   `incidents`, `goose_db_version`, and `river_job`.
6. **Switch.** Confirm `POSTGRES_PASSWORD` is set and no `O1_DATABASE_URL`
   override is active in `.env`, start `api`, `scheduler`, `worker`, and confirm
   `/healthz`, `/readyz`, login, a monitor page with encrypted headers, and one
   alert delivery.
7. **Soak.** Keep the Neon project and the restic snapshot for at least 7 days.
   Delete the plaintext dump as soon as the local database is verified. Decide
   on the legacy volume separately (see §7.6).

Downtime is the dump+restore window — minutes for a <1 GB database — plus
verification. Jobs running at freeze time are either drained or rescued by
River's stuck-job maintenance after restart.

### 7.6 Rollback

The Neon project stays untouched and its DSN stays in the operator's password
manager for the whole soak. The .env does **not** keep an active
`${O1_DATABASE_URL:-...}` fallback: deriving the DSN from `POSTGRES_PASSWORD` is
the only default, so a stale Neon DSN cannot silently win.

- **During the 7-day soak, rollback to Neon is allowed.** Keep the last
  managed-aware release bundle (it has no `postgres` service and accepts the
  Neon DSN). To roll back: stop the stack, set `O1_DATABASE_URL` explicitly in
  `/opt/o1/.env`, start the retained release, verify `/readyz`, and **disable
  the backup, prune, and restore-verify timers** — with no `postgres` service
  in the active release they would fail. Data written locally during the soak
  is lost; note the window in the incident log.
- **Returning to local after a rollback requires a fresh dump/restore**, because
  Neon will have accumulated writes in the meantime. Re-run §7.5 rather than
  replaying an old dump.
- **After acceptance, rollback is local-only.** Prune pre-cutover releases,
  delete the Neon project, and rely on the restic snapshots plus the Plan 12
  restore runbook. Never roll back to a release that predates the local
  database's Compose shape; it would start an empty database.
- **Legacy volume.** The 2026-11-25 inventory shows no
  `o1-production_postgres-data` volume on the host, so there is nothing to
  preserve or delete. The step is kept in the runbook for a rebuilt host.
- Restoring a backup is the Plan 12 runbook, not a Compose rollback.

### 7.7 Tests

- `deploy/scripts/test-compose-config.sh`: invert the database assertions — the
  rendered config must contain `postgres` and `postgres-data`, the service must
  use `postgres:18-alpine`, publish no ports, have a `pg_isready` healthcheck,
  declare the explicit `o1-postgres-data` volume name, `api`/`scheduler`/
  `worker` must `depends_on` it with `service_healthy`, `O1_DATABASE_URL` must
  be derived and point at `postgres:5432`, `sslmode=disable`, and
  `O1_DB_MAX_CONNS` must still default to `10`. Keep the "refuses to render
  without `POSTGRES_PASSWORD`" check.
- `deploy/scripts/test-workflows.sh` and `validate-release.sh` stay green; no
  release-bundle changes beyond the compose file.
- `deploy/scripts/test-backup.sh` from Plan 12 runs against its disposable
  Postgres container, and `restore-verify.sh` honours `O1_BACKUP_PG_IMAGE`
  (default `postgres:18-alpine`) so a production dump can be restored in the
  drill.
- `backend`: `go test -short ./...` unchanged; no app code edits.
- Manual acceptance on the host: `docker compose config`, backup + restore
  drill, and a full stack restart.

### 7.8 Documentation

- `deploy/production/README.md`: replace "Managed PostgreSQL" with "Bundled
  PostgreSQL" (provisioning, volume, healthcheck, DSN, password rotation via
  `ALTER ROLE`, `psql` through the container), replace the provider-backup
  paragraph with Plan 12 references and the 24h RPO caveat, and add the Neon
  cutover + rollback sections from §7.5–7.6.
- `deploy/CICD.md`: required host values include `POSTGRES_PASSWORD`; note that
  `5432` stays closed; note that deploys and rollbacks never take a backup and
  never touch `postgres-data`.
- Root `README.md`: production section describes the bundled database, the
  backup timer, RPO, and the fact that `.env`/keys are restored separately.
- `deploy/backup/README.md`: OCI Object Storage specifics (endpoint, Customer
  Secret Keys, ARM64 restic) and the note that the dump source is now the
  bundled container by default.

### 7.9 Shared-VM safety

- No published database port; the OCI security list keeps `5432` closed.
- Container memory/CPU bounds and `shared_buffers` caps keep the database from
  starving other workloads; Docker log rotation is mandatory.
- Postgres restarts with the host (`restart: unless-stopped`, `Persistent=true`
  timers).
- Minor upgrades are a `postgres:18.x` image bump in a normal release; major
  upgrades are out of scope and need their own plan (`pg_upgrade` or
  dump/restore).

## 8. Decisions Left to the Builder

- Exact tuning values are set in §7.1 from the measured host; revisit only if
  the VM gains another heavy workload or the database size grows by orders of
  magnitude.
- Whether to pin the `postgres` image by digest in the base compose file or via
  `images.env` like the app images.
- How the runbook words the Neon rollback (retained release bundle plus its
  required `O1_DATABASE_URL`), given the current release derives its DSN.
- Whether `POSTGRES_PASSWORD` rotation is scripted or documented as a manual
  `ALTER ROLE` + `.env` edit with a restart.
- Exact Object Storage bucket/prefix layout and whether restic `--limit-upload`
  is needed.
- Wording/ordering inside the runbook and the cutover checklist.

## 9. Rollout

1. Merge `plan12-backups-restore`; run its test suite (`test-backup.sh`,
   shellcheck) on `main`.
2. Inventory the VM (`free -h`, `df -h`, `docker volume ls`, Docker version,
   restic architecture) and record the Neon source version, locale, encoding,
   extensions, and database size. Generate `POSTGRES_PASSWORD` and store it in
   the password manager before the volume's first start.
3. Create the Object Storage bucket and scoped Customer Secret Keys; install
   restic; run `install-backup.sh` with a new repository password stored off
   host.
4. Implement the compose/env/test/doc changes in this design.
5. Deploy the new stack to staging (or the VM) and confirm `postgres` healthy,
   `/readyz` green, and one manual `backup.sh` + `restore-verify.sh` pass.
6. Execute the Neon cutover (§7.5) inside the maintenance window, then delete
   the plaintext dump once the local database is verified.
7. Soak for 7 days with nightly backups and one drill; then decommission Neon,
   decide on the legacy volume, and prune pre-cutover releases.

## 10. Acceptance

- `docker compose --env-file deploy/production/env.example -f deploy/production/compose.yaml config` renders a `postgres` service with a healthcheck, a named volume, and no published ports; `O1_DATABASE_URL` is derived and points at `postgres:5432`.
- `bash deploy/scripts/test-compose-config.sh` and `bash deploy/scripts/test-workflows.sh` pass.
- `bash deploy/scripts/test-backup.sh` passes; a manual backup creates one
  snapshot and a manual drill restores and validates it.
- `cd backend && go test -short ./...` passes with no code changes.
- On the host: `/healthz`, `/readyz`, login, monitor detail with encrypted
  headers, an alert email, and a restart of the stack all succeed offline from
  Neon.
- `5432` is unreachable from outside the VM.
- The host has no `o1-postgres-data` volume before the cutover (or the legacy
  volume is explicitly accounted for), the plaintext dump is deleted after
  verification, and the Neon project plus the restic pre-cutover snapshot are
  deleted or retained only by the documented decision after the soak.

## 11. Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Block volume or VM failure loses the database | Daily encrypted offsite restic backup + monthly drill; RPO ≤24h documented |
| Shared VM memory pressure kills Postgres | `shared_buffers`/`mem_limit` caps, log rotation, host inventory before tuning |
| Cutover restores an incompatible dump (major/locale mismatch) | Preflight `server_version`, encoding, collation, and extensions; match image major and variant, set `POSTGRES_INITDB_ARGS`, verify `citext` in a throwaway container |
| An old pre-managed volume is picked up and `initdb` is skipped | New volume named `o1-postgres-data`; `docker volume ls` preflight; legacy volume kept, never reused |
| Plaintext cutover dump lingers on disk | `0600` on the data disk, backed up to restic immediately, deleted after verification |
| Rollback to Neon during soak silently keeps writing to neither DB | Explicit rollback procedure, timers paused, data-loss window recorded |
| Jobs stuck after the freeze | Graceful worker stop with drain; River rescue on restart; row-count verification before start |
| Restore drill fills the disk | Plan 12 preflight requires free space ≥2× database size |
| Password drift between container and DSN | Single `POSTGRES_PASSWORD` source; hex-only password |
| Rollback to a pre-cutover image starts an empty DB | Prune old releases; keep Neon and the dump until accepted; documented forward-only rule |
| Backup silently stops | External success ping, failure email/URL, `last-success` file, monthly drill |
| Restoring without `O1_ENCRYPTION_KEY` | Runbook restores `.env` from the password manager and smoke-tests encrypted headers |
| `docker compose down -v` destroys data | Runbook warning; offsite backup; volume name is explicit |
