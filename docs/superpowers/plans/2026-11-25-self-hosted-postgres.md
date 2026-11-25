# Self-Hosted PostgreSQL on the Shared VPS — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run production PostgreSQL 18 in the existing production Compose stack on the Oracle VM, with daily encrypted offsite backups and a drilled restore path, replacing Neon.

**Architecture:** A `postgres:18-alpine` service joins `deploy/production/compose.yaml` behind a healthcheck, an explicit `o1-postgres-data` volume, and no published port; the app DSN is derived from one `POSTGRES_PASSWORD`. The Plan 12 restic tooling (branch `plan12-backups-restore`) is merged, pointed at OCI Object Storage, and parameterized so its restore drill uses PostgreSQL 18. Cutover freezes the app, restores a Neon dump into the volume through a temporary container, then deploys the new release.

**Tech Stack:** Docker Compose v2, PostgreSQL 18 (Alpine), Go/pgx/River (unchanged), bash, restic, systemd, OCI Object Storage (S3-compatible).

**Spec:** `docs/superpowers/specs/2026-11-25-self-hosted-postgres-design.md`

## Global Constraints

- Source database measured 2026-11-25: PostgreSQL **18.7**, `C.UTF-8`/`C.UTF-8`, UTF8, **30 MB**, extensions `citext` + `plpgsql` only; Neon database name is `neondb`.
- Target image is exactly `postgres:18-alpine`; `POSTGRES_INITDB_ARGS=--locale=C.UTF-8 --encoding=UTF8`; `citext` ships with the image.
- The PostgreSQL 18 image's `PGDATA` is `/var/lib/postgresql/18/docker`; the named volume mounts at `/var/lib/postgresql`, never `/var/lib/postgresql/data`.
- Production Compose project name is `o1-production`; database volume name is exactly `o1-postgres-data`.
- `5432` is never published; `sslmode=disable` on the private Compose network.
- `POSTGRES_PASSWORD` is 32 random bytes as hex (`openssl rand -hex 32`) and is baked into the volume at first `initdb`.
- `O1_DATABASE_URL` is derived in Compose; a stray `.env` value must not override it.
- `max_connections=100`; three processes × `O1_DB_MAX_CONNS` (default 10).
- Backup RPO ≤ 24 hours, no PITR. Daily dump 02:30 UTC, weekly prune Sunday 03:30 UTC, monthly restore drill. 30 daily / 12 monthly snapshots.
- Backups are encrypted by restic and stored offsite; no plaintext dump survives acceptance.
- Host: 4 OCPU, 29 GiB RAM, 43 GB free on the boot volume; no legacy `postgres-data` volume.

## Review Focus

- **Missing secret:** rendering the stack without `POSTGRES_PASSWORD` must fail before any container starts. Pinned by the `test-compose-config.sh` render-refusal check in Task 2.
- **Password/DSN drift:** the container password and the app DSN must come from the same variable. Pinned by the single-DSN equality assertion in Task 2.
- **Restore major mismatch:** a dump from 18 must not be restored by a 16 client. Pinned by the `test-backup.sh` fixture bump to `postgres:18-alpine` in Task 3.
- **Accidental exposure:** the database must have no published port. Pinned by the empty-`ports` assertion in Task 2.
- **Rollback without a database:** rolling back to a managed release leaves no `postgres` service, so backup timers would fail. Covered by the rollback procedure and timer-pause steps in Tasks 4 and 7.

---

### Task 1: Merge the Plan 12 backup tooling

**Files:**
- No new files; merges branch `plan12-backups-restore` (10 commits).

**Interfaces:**
- Produces: `deploy/scripts/{backup.sh,backup-lib.sh,install-backup.sh,restore-verify.sh,test-backup.sh}`, `deploy/backup/*`, and the CI job that runs `test-backup.sh`. Tasks 3 and 4 modify these files.

- [ ] **Step 1: Confirm the merge is conflict-free**

Run: `git merge-tree $(git merge-base main plan12-backups-restore) main plan12-backups-restore | grep -c '^<<<<<<<'`
Expected: `0`

- [ ] **Step 2: Merge the branch**

```bash
git checkout main
git merge --no-ff plan12-backups-restore -m "merge: encrypted backups and restore drills (plan 12)"
```

- [ ] **Step 3: Verify shell syntax and shellcheck**

Run: `bash -n deploy/scripts/backup.sh deploy/scripts/backup-lib.sh deploy/scripts/install-backup.sh deploy/scripts/restore-verify.sh deploy/scripts/test-backup.sh`
Expected: exit 0, no output.

Run: `shellcheck deploy/scripts/backup.sh deploy/scripts/backup-lib.sh deploy/scripts/install-backup.sh deploy/scripts/restore-verify.sh deploy/scripts/test-backup.sh`
Expected: exit 0. (If shellcheck is unavailable locally, rely on the CI job added by the merge.)

- [ ] **Step 4: Run the backup test suite**

Run: `bash deploy/scripts/test-backup.sh`
Expected: PASS, ending with the suite's success line; Docker must be running.

- [ ] **Step 5: Commit (only if the merge left changes unstaged)**

```bash
git status --short
```
Expected: empty. If not, commit the conflict resolutions with `git commit -m "merge: resolve plan 12 conflicts"`.

---

### Task 2: Add the bundled database to the production stack

**Files:**
- Modify: `deploy/scripts/test-compose-config.sh:52-78`
- Modify: `deploy/production/compose.yaml:1-27` (header) and the `services:` block
- Modify: `deploy/production/env.example:15-23` and `:99-102`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: a rendered `postgres` service named `postgres`, an `o1-postgres-data` volume, and `O1_DATABASE_URL` values identical across `api`, `scheduler`, and `worker`.

- [ ] **Step 1: Replace the database assertions in the test**

In `deploy/scripts/test-compose-config.sh`, replace the block that starts with `# The production stack has no bundled database` through its `check ...` line with:

```bash
# The production stack bundles PostgreSQL and derives the DSN from one secret.
env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$env_file" \
  --env-file "$images_file" \
  -f "$compose_file" config --format json \
  | python3 -c '
import json, sys
c = json.load(sys.stdin)
assert "postgres" in c["services"], sorted(c["services"])
pg = c["services"]["postgres"]
assert pg["image"] == "postgres:18-alpine", pg["image"]
assert not pg.get("ports"), pg.get("ports")
vol = pg["volumes"][0]
assert vol["type"] == "volume", vol
assert vol["source"] == "postgres-data", vol
assert vol["target"] == "/var/lib/postgresql", vol
assert c["volumes"]["postgres-data"]["name"] == "o1-postgres-data"
health = " ".join(pg["healthcheck"]["test"])
assert "pg_isready" in health and "-h 127.0.0.1" in health, health
assert pg["environment"]["POSTGRES_INITDB_ARGS"] == "--locale=C.UTF-8 --encoding=UTF8", pg["environment"]
command = " ".join(pg["command"])
for flag in ("shared_buffers=1GB", "effective_cache_size=4GB", "work_mem=16MB",
             "maintenance_work_mem=256MB", "max_connections=100",
             "wal_compression=on", "checkpoint_completion_target=0.9"):
    assert flag in command, (flag, command)
assert int(pg["mem_limit"]) == 3 * 1024**3, pg["mem_limit"]
assert float(pg["cpus"]) == 2.0, pg["cpus"]
assert int(pg["shm_size"]) == 256 * 1024**2, pg["shm_size"]
assert pg["stop_grace_period"] in ("1m0s", "60s", 60000000000), pg["stop_grace_period"]
assert c["services"]["worker"]["stop_grace_period"] in ("45s", 45000000000), c["services"]["worker"]["stop_grace_period"]
for name in ("api", "scheduler", "worker"):
    dep = c["services"][name]["depends_on"]["postgres"]
    assert dep["condition"] == "service_healthy", dep
dsns = {n: c["services"][n]["environment"]["O1_DATABASE_URL"]
        for n in ("api", "scheduler", "worker")}
assert len(set(dsns.values())) == 1, dsns
dsn = next(iter(dsns.values()))
pw = pg["environment"]["POSTGRES_PASSWORD"]
assert dsn == f"postgres://o1:{pw}@postgres:5432/o1?sslmode=disable", dsn
assert c["services"]["api"]["environment"]["O1_DB_MAX_CONNS"] == "10"
'
check "bundled database with a derived DSN, healthy dependency, and no published port"
```

Then replace the render-refusal block (the `grep -v '^O1_DATABASE_URL='` block and its `check`) with:

```bash
# Rendering without the database password fails before any container starts.
tmp_env=$(mktemp)
trap 'rm -f "$tmp_env"' EXIT
grep -v '^POSTGRES_PASSWORD=' "$env_file" > "$tmp_env"
if env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  -u POSTGRES_PASSWORD \
  docker compose --env-file "$tmp_env" --env-file "$images_file" \
  -f "$compose_file" config --quiet >/dev/null 2>&1; then
  fail "compose rendered without POSTGRES_PASSWORD"
fi
check "compose refuses to render without POSTGRES_PASSWORD"
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash deploy/scripts/test-compose-config.sh`
Expected: FAIL with `AssertionError` naming `postgres` (the current compose has no such service).

- [ ] **Step 3: Add the service and volume to `deploy/production/compose.yaml`**

Update the header comment to say the stack bundles PostgreSQL again on the private Compose network (`5432` unpublished, no TLS on the bridge), then add this service immediately before `api:`:

```yaml
  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: o1
      POSTGRES_DB: o1
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}"
      POSTGRES_INITDB_ARGS: "--locale=C.UTF-8 --encoding=UTF8"
      LANG: C.UTF-8
      TZ: UTC
      PGTZ: UTC
    command:
      - postgres
      - -c
      - shared_buffers=1GB
      - -c
      - effective_cache_size=4GB
      - -c
      - work_mem=16MB
      - -c
      - maintenance_work_mem=256MB
      - -c
      - max_connections=100
      - -c
      - wal_compression=on
      - -c
      - checkpoint_completion_target=0.9
    volumes:
      - postgres-data:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -h 127.0.0.1 -U o1 -d o1"]
      interval: 5s
      timeout: 5s
      retries: 10
      start_period: 30s
    shm_size: 256mb
    stop_grace_period: 60s
    mem_limit: 3g
    cpus: 2.0
    logging:
      driver: json-file
      options:
        max-size: 10m
        max-file: "3"
    restart: unless-stopped
```

Add `depends_on` to `api`, `scheduler`, and `worker` (each service already has an `environment` key):

```yaml
    depends_on:
      postgres:
        condition: service_healthy
```

Give the `worker` service a graceful-stop window long enough for its 30-second
shutdown path:

```yaml
    stop_grace_period: 45s
```

In the shared backend environment anchor, replace the managed DSN line with:

```yaml
      O1_DATABASE_URL: "postgres://o1:${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}@postgres:5432/o1?sslmode=disable"
```

At the bottom `volumes:` block, add:

```yaml
  postgres-data:
    name: o1-postgres-data
```

- [ ] **Step 4: Update `deploy/production/env.example`**

Replace the managed-DSN block (lines 15–23) with:

```
# Password for the bundled PostgreSQL role and database. 32 random bytes as
# hex: `openssl rand -hex 32`. It is baked into the data volume at first start;
# changing this value later does not change the database role. Rotate the role
# with ALTER ROLE and update this value together, then restart the stack.
POSTGRES_PASSWORD=replace-with-output-of-openssl-rand-hex-32
```

Replace the `O1_DB_MAX_CONNS` comment (lines 99–102) with:

```
# Maximum PostgreSQL connections per process (1-100). api, scheduler, and
# worker each open their own pool against the bundled database, whose
# max_connections is 100; 3 x 10 leaves ample migration headroom.
# O1_DB_MAX_CONNS=10
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `bash deploy/scripts/test-compose-config.sh`
Expected: PASS, all checks `ok`.

- [ ] **Step 6: Run the remaining deploy checks**

Run: `bash deploy/scripts/test-workflows.sh && bash deploy/scripts/test-deploy.sh`
Expected: both exit 0.

- [ ] **Step 7: Commit**

```bash
git add deploy/scripts/test-compose-config.sh deploy/production/compose.yaml deploy/production/env.example
git commit -m "feat: bundle PostgreSQL 18 in the production stack"
```

---

### Task 3: Parameterize the restore drill image

**Files:**
- Modify: `deploy/scripts/restore-verify.sh` (scratch container image)
- Modify: `deploy/scripts/test-backup.sh:499-503` (fixture image)
- Modify: `deploy/backup/backup.env.example` (optional section)
- Modify: `deploy/backup/README.md` (image row and prose)

**Interfaces:**
- Consumes: `restore-verify.sh` from Task 1.
- Produces: `O1_BACKUP_PG_IMAGE` (default `postgres:18-alpine`), read by `restore-verify.sh`.

- [ ] **Step 1: Bump the test fixture image to 18**

In `deploy/scripts/test-backup.sh`, change the fixture line from `postgres:16-alpine` to `postgres:18-alpine`:

```bash
    postgres:18-alpine >/dev/null || fail "could not start the fixture Postgres"
```

- [ ] **Step 2: Run the suite to verify it fails**

Run: `bash deploy/scripts/test-backup.sh`
Expected: FAIL at the restore-drill stage, because `restore-verify.sh` still starts a 16 scratch container and cannot restore the 18 dump.

- [ ] **Step 3: Add the variable to `restore-verify.sh`**

After the successful `bl_load_env` block and before the first use, add:

```bash
pg_image=${O1_BACKUP_PG_IMAGE:-postgres:18-alpine}
```

It must come after `bl_load_env`; expanding it earlier latches the default before
the host-side `backup.env` value is loaded, silently ignoring the override.

Replace the hardcoded `postgres:16-alpine` argument in the `docker run` command with `"$pg_image"`.

- [ ] **Step 4: Document the variable**

In `deploy/backup/backup.env.example`, under Optional, add (commented so an
installed host follows the script default):

```
# Postgres image for the monthly scratch restore. Its major must be at least
# the dump major. The script default matches production; uncomment only to
# override it on an installed host.
# O1_BACKUP_PG_IMAGE=postgres:18-alpine
```

In `deploy/backup/README.md`, add `| O1_BACKUP_PG_IMAGE | no | postgres:18-alpine |` to the configuration table and change the prose "Starts a scratch `postgres:16-alpine` container" to name `O1_BACKUP_PG_IMAGE` with its default.

- [ ] **Step 5: Run the suite to verify it passes**

Run: `bash deploy/scripts/test-backup.sh`
Expected: PASS, including the restore drill.

- [ ] **Step 6: Re-run shellcheck on the touched scripts**

Run: `shellcheck deploy/scripts/restore-verify.sh deploy/scripts/test-backup.sh`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add deploy/scripts/restore-verify.sh deploy/scripts/test-backup.sh deploy/backup/backup.env.example deploy/backup/README.md
git commit -m "fix: make the restore drill image configurable for PostgreSQL 18"
```

---

### Task 4: Update the production and backup documentation

**Files:**
- Modify: `deploy/production/README.md` (§3, §4, §5, §8, §10, §13)
- Modify: `deploy/CICD.md` (§5 host setup, §9, §11 security)
- Modify: `README.md` (backups paragraph near line 309 and production section near line 618)
- Modify: `deploy/backup/README.md` (OCI Object Storage specifics)

**Interfaces:**
- Consumes: the compose shape from Task 2, `O1_BACKUP_PG_IMAGE` from Task 3.
- Produces: the operator runbook executed in Tasks 5 and 6.

- [ ] **Step 1: Rewrite `deploy/production/README.md` §3**

Replace the "Managed PostgreSQL" section and its subsections with "Bundled PostgreSQL":

- Requirements: Docker on the host, 43 GB free disk measured, no additional service; the `postgres:18-alpine` container owns volume `o1-postgres-data`.
- Connection: `postgres://o1:<POSTGRES_PASSWORD>@postgres:5432/o1?sslmode=disable`, derived by Compose; `5432` is not published.
- Startup: `api`/`scheduler`/`worker` wait for `pg_isready` and run goose + River migrations under an advisory lock.
- Extensions: `citext` ships with the image; no package install needed.
- Remove the old "One-time cutover from the bundled database" subsection.
- Add "Cutover from Neon" with the Task 6 commands and "Rollback" with the Task 7 rules: Neon is retained for the 7-day soak with its DSN in `/opt/o1/.env`; rolling back runs the retained pre-cutover bundle and pauses backup timers; after acceptance rollback is local-only.
- Backups: link `deploy/backup/README.md`, state RPO ≤ 24h, no PITR, and that `.env`/`POSTGRES_PASSWORD` are restored from the password manager.

- [ ] **Step 2: Update the rest of the production runbook**

- §4 "Generate secrets": replace the DSN bullet with `POSTGRES_PASSWORD` (`openssl rand -hex 32`), and keep the `O1_SECRET_KEY`/`O1_ENCRYPTION_KEY` bullets.
- §5 "Configure the environment": the required list contains `POSTGRES_PASSWORD` instead of `O1_DATABASE_URL`.
- §8 "Logs and day-to-day operations": replace every `psql "$O1_DATABASE_URL"` with `docker compose --env-file /opt/o1/.env -f /opt/o1/current/deploy/production/compose.yaml exec -T postgres psql -U o1 -d o1`.
- Retention section: same container-based `psql` substitution for its queries.
- §10 "Rollback": point at the new rollback subsection; state that image/config rollback never restores data and that `postgres-data` is untouched by deploys.
- §13 "Security notes": `5432` unpublished, `.env` held outside releases, volume only reachable by the Compose network, and backups encrypted offsite. State that `stop`, `down`, and deploys are safe, while `down -v`, `docker volume rm`, and `docker system prune --volumes` destroy the only live copy and must never run against the production compose file.

- [ ] **Step 3: Update `deploy/CICD.md`**

- §5 host setup: required `/opt/o1/.env` values replace `O1_DATABASE_URL` with `POSTGRES_PASSWORD`; mention optional `O1_DB_MAX_CONNS`.
- Add one line: deploys and rollbacks never take a backup and never touch the `postgres-data` volume; backups are the host timer from Plan 12.
- §11 security: keep `5432` in the must-stay-closed list and note the database is now on the host but unpublished.

- [ ] **Step 4: Update the root `README.md`**

- The backups paragraph near line 309: replace "Backups come from the managed database provider" with the restic daily dump, RPO ≤ 24h, no PITR, monthly drill, and the separate `.env`/encryption-key requirement.
- The production paragraph near line 618: replace "The database is a managed PostgreSQL service reached through `O1_DATABASE_URL`" with the bundled `postgres:18-alpine` container, the `o1-postgres-data` volume, and the same backup caveat.

- [ ] **Step 5: Add OCI Object Storage specifics to `deploy/backup/README.md`**

- Repository example: `s3:https://<namespace>.compat.objectstorage.<region>.oraclecloud.com/<bucket>/o1`.
- Credentials are OCI Customer Secret Keys scoped to the bucket/prefix.
- Restic install section covers ARM64 (`arm64` release asset) for Ampere A1 hosts.
- Note that the dump source is the bundled production container by default (`O1_BACKUP_PG_CONTAINER` unset).

- [ ] **Step 6: Verify no stale managed-database guidance remains**

Run:
```bash
! grep -rn "managed PostgreSQL\|managed database provider\|O1_DATABASE_URL='postgres://o1:replace" README.md deploy/production/README.md deploy/CICD.md
grep -q POSTGRES_PASSWORD deploy/production/README.md
grep -q POSTGRES_PASSWORD deploy/CICD.md
grep -q "o1-postgres-data" deploy/production/README.md
grep -q "compat.objectstorage" deploy/backup/README.md
```
Expected: all commands exit 0 (the first prints nothing).

- [ ] **Step 7: Re-run the deploy checks and commit**

Run: `bash deploy/scripts/test-compose-config.sh && bash deploy/scripts/test-workflows.sh`
Expected: PASS.

```bash
git add README.md deploy/production/README.md deploy/CICD.md deploy/backup/README.md
git commit -m "docs: document the bundled production database and Neon cutover"
```

---

### Task 5: Provision offsite backups on the host (operator)

**Files:**
- No repository changes; host setup only. Uses `deploy/backup/` and
  `deploy/scripts/install-backup.sh` from Task 1.

**Interfaces:**
- Consumes: the merged Plan 12 tree.
- Produces: `/opt/o1/bin/{backup.sh,restore-verify.sh,backup-lib.sh}`,
  `/opt/o1/backup/backup.env`, `/opt/o1/backup/restic.pass`, an initialized
  restic repository, and the three systemd units (installed, timers disabled).
  Task 6 enables the timers after the first successful backup and drill.

- [ ] **Step 1: Create the Object Storage bucket and credentials**

In the OCI console, create a bucket in the VM's region and a Customer Secret
Key scoped to read/write/delete under the `o1/` prefix. Record the namespace,
region, bucket name, access key, and secret key. Expected repository URL:
`s3:https://<namespace>.compat.objectstorage.<region>.oraclecloud.com/<bucket>/o1`.

- [ ] **Step 2: Install restic on the ARM host**

Pick the release asset that matches the host (`uname -m`; this VM is
`x86_64`, so `amd64`). `bunzip2` may be absent on a minimal Ubuntu image; install
`bzip2` first, or decompress with Python.

```bash
case $(uname -m) in
  aarch64|arm64) restic_arch=arm64 ;;
  x86_64|amd64)  restic_arch=amd64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
curl -fsSL -o "/tmp/restic-0.19.1-$restic_arch.bz2" \
  "https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_${restic_arch}.bz2"
if command -v bunzip2 >/dev/null; then
  bunzip2 -f "/tmp/restic-0.19.1-$restic_arch.bz2"
else
  python3 -c "import bz2,shutil; shutil.copyfileobj(bz2.open('/tmp/restic-0.19.1-$restic_arch.bz2','rb'), open('/tmp/restic-0.19.1-$restic_arch','wb'))"
fi
sudo install -m 0755 "/tmp/restic-0.19.1-$restic_arch" /usr/local/bin/restic
restic version
```
Expected: `restic 0.19.1`. Run the install as a sudo-capable user; the deploy
user may deliberately lack sudo.

- [ ] **Step 3: Copy the deploy tree and seed the backup environment**

```bash
scp -r deploy <user>@<host>:/tmp/o1-deploy
ssh <user>@<host>
sudo install -d -m 0700 /opt/o1/backup /opt/o1/backup/cache /opt/o1/bin
sudo install -m 0600 /tmp/o1-deploy/backup/backup.env.example /opt/o1/backup/backup.env
sudoedit /opt/o1/backup/backup.env
```
Fill in `RESTIC_REPOSITORY` (the URL from Step 1), `RESTIC_PASSWORD_FILE=/opt/o1/backup/restic.pass`, `RESTIC_CACHE_DIR=/opt/o1/backup/cache`, the two AWS credential variables, `AWS_DEFAULT_REGION` (uncomment it and set the OCI region, for example `us-ashburn-1`; leave it unset for AWS S3), `O1_COMPOSE_FILE=/opt/o1/current/deploy/production/compose.yaml`, `O1_ENV_FILE=/opt/o1/.env`, `O1_BACKUP_PING_URL`, and either `O1_BACKUP_ALERT_EMAIL` or `O1_BACKUP_FAILURE_URL`. Leave `O1_BACKUP_PG_IMAGE` commented so an installed host follows the script default (`postgres:18-alpine`); uncomment it only to override the drill image.

- [ ] **Step 4: Install the tooling and initialize the repository**

```bash
sudo /tmp/o1-deploy/scripts/install-backup.sh --source /tmp/o1-deploy --init-new-repo
```
Expected: scripts installed in `/opt/o1/bin`, `restic.pass` generated, repository initialized, timers installed but disabled.

Store `restic.pass` and the Object Storage keys in the password manager now.

- [ ] **Step 5: Verify the repository is reachable**

```bash
sudo bash -c 'set -a; . /opt/o1/backup/backup.env; set +a; restic cat config >/dev/null && echo repository-ok'
```
Expected: `repository-ok`. The whole command runs as root because `/opt/o1/backup` is `0700` root-owned. Do not run `backup.sh` yet: the bundled `postgres`
service does not exist until Task 6's cutover, so the dump source would fail.
The timers stay disabled until Task 6 Step 9.

---

### Task 6: Execute the Neon cutover (operator, maintenance window)

**Files:**
- No repository changes; host operations only.

**Interfaces:**
- Consumes: the release from Tasks 1–4 and `POSTGRES_PASSWORD` stored in the password manager.
- Produces: a running stack backed by `o1-postgres-data`, plus a restic `pre-cutover` snapshot.

- [ ] **Step 1: Preflight the host**

Run: `free -h; df -h /; docker volume ls | grep -E 'postgres|o1-'`
Expected: ≥ 40 GB free, no `o1-postgres-data` volume yet, and no legacy `postgres-data` volume.

- [ ] **Step 2: Generate and store the password**

Run: `openssl rand -hex 32`
Expected: 64 hex characters. Store it in the password manager and add `POSTGRES_PASSWORD=<value>` to `/opt/o1/.env` (mode 0600). Do not start any container yet.

- [ ] **Step 3: Freeze writes and record Neon row counts**

Export the Neon **direct** (non-pooled) DSN in this shell first, for example
`export NEON_DIRECT_URL='postgres://...@ep-...neon.tech/neondb?sslmode=require'`.

```bash
install -d -m 0700 /opt/o1/state
cd /opt/o1/current
docker compose --env-file /opt/o1/.env -f deploy/production/compose.yaml stop worker
# wait for the worker container to exit (River graceful stop)
docker compose --env-file /opt/o1/.env -f deploy/production/compose.yaml stop scheduler api
docker compose --env-file /opt/o1/.env -f deploy/production/compose.yaml ps
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
Expected: `worker`, `scheduler`, `api` exited; `web` still up. The count query
runs only after the freeze and its output is the comparison baseline.

- [ ] **Step 4: Initialize the target volume with a temporary container**

`POSTGRES_PASSWORD` is hex only, never quoted: the validation rejects a quoted
or non-hex value before the volume is created.

```bash
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
docker exec o1-cutover-pg psql -U o1 -d o1 -Atc 'show lc_collate'
```
Expected: `accepting connections`, then `C.UTF-8`. If `pg_isready` does not
come up, read `docker logs o1-cutover-pg` before retrying.

- [ ] **Step 5: Dump Neon and push it to restic**

```bash
docker run --rm postgres:18-alpine pg_dump -Fc -Z 6 -d "$NEON_DIRECT_URL" -f - \
  > /opt/o1/state/o1-pre-local.dump
chmod 0600 /opt/o1/state/o1-pre-local.dump
sudo bash -c 'set -a; . /opt/o1/backup/backup.env; set +a; restic backup --stdin --stdin-filename o1.dump --host o1-prod --tag o1-db --tag pre-cutover < /opt/o1/state/o1-pre-local.dump'
```
Expected: a dump of roughly the 30 MB database and one snapshot that contains
`o1.dump` (pushed through stdin, not as a host path). `restic` runs as root
because `/opt/o1/backup` is `0700` root-owned.

- [ ] **Step 6: Restore and verify row counts**

```bash
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
Expected: `diff` exits 0 and prints nothing. On any mismatch or any
`pg_restore` error, stop and re-dump rather than proceeding.

- [ ] **Step 7: Replace the temporary container with the real stack**

Leave `O1_DATABASE_URL=<Neon direct DSN>` in `/opt/o1/.env` through the soak.
The bundled Compose ignores it, but a failed cutover deploy automatically
restores the previous pre-cutover release, which needs the Neon DSN to render.
Remove it in Task 7 after acceptance.

```bash
docker rm -f o1-cutover-pg
/opt/o1/bin/deploy.sh /opt/o1/incoming/<release>
docker compose --env-file /opt/o1/.env -f /opt/o1/current/deploy/production/compose.yaml ps
docker volume ls | grep '^local *o1-postgres-data$'
```
Expected: `postgres` healthy on the explicit volume `o1-postgres-data`; `api`,
`scheduler`, `worker`, and `web` running.

- [ ] **Step 8: Verify the application end to end**

```bash
curl -fsS https://<O1_DOMAIN>/healthz
curl -fsS https://<O1_DOMAIN>/readyz
```
Expected: both 200. Then log in, open a monitor with encrypted headers, and confirm one alert email arrives.

- [ ] **Step 9: Run one manual backup and one drill, then delete the plaintext dump**

```bash
sudo /opt/o1/bin/backup.sh
sudo /opt/o1/bin/restore-verify.sh
sudo /tmp/o1-deploy/scripts/install-backup.sh --source /tmp/o1-deploy --enable
sudo systemctl list-timers 'o1-backup*' 'o1-restore-verify*'
rm -f /opt/o1/state/o1-pre-local.dump
```
Expected: `backup.sh` reports one snapshot; `restore-verify.sh` restores it with
`postgres:18-alpine`; all three timers are enabled. Both scripts run as root
because `/opt/o1/backup` is `0700` root-owned.

- [ ] **Step 10: Start the soak**

Record the cutover time. Keep the Neon project, the `O1_DATABASE_URL` entry in
`/opt/o1/.env`, and the restic `pre-cutover` snapshot; keep the plaintext dump
deleted. Do not prune pre-cutover releases for 7 days.

---

### Task 7: Accept, decommission, and prune (operator, after 7 days)

**Files:**
- No repository changes.

**Interfaces:**
- Consumes: Task 6's soak and the Plan 12 timers.
- Produces: Neon removed, release history pruned, backup timers proven.

- [ ] **Step 1: Confirm the soak evidence**

```bash
sudo systemctl list-timers 'o1-backup*' 'o1-restore-verify*'
sudo cat /opt/o1/backup/last-success
sudo cat /opt/o1/backup/last-restore-verify
sudo journalctl -u o1-backup.service --since '7 days ago' --no-pager | tail -50
```
Expected: at least five daily successes, one drill success, no failure alerts.
The commands run as root because `/opt/o1/backup` is `0700` root-owned.

- [ ] **Step 2: Decide on the Neon rollback window**

If any acceptance check failed during the soak, stop here and follow the rollback subsection in `deploy/production/README.md`: run the retained managed release with the Neon DSN in `/opt/o1/.env`, then `sudo systemctl disable --now o1-backup.timer o1-backup-prune.timer o1-restore-verify.timer`. Returning to local later requires a fresh Task 6 dump/restore, and the stale local volume or database must be discarded first: verify the fresh dump, then `docker volume rm o1-postgres-data` (**this destroys the only local copy**) or `DROP DATABASE o1; CREATE DATABASE o1` through the running container, then restore and check the exit status and row counts.

- [ ] **Step 3: Decommission Neon**

Delete the Neon project from the console, remove its DSN from the password manager, and remove `O1_DATABASE_URL` from `/opt/o1/.env` only after Step 1 passes. Keep the restic `pre-cutover` snapshot until the first successful monthly drill after acceptance.

- [ ] **Step 4: Prune pre-cutover releases**

```bash
ls -1 /opt/o1/releases
```
Expected: at most `current`, `previous`, and one rollback target remain; delete any release whose Compose file still expects a managed DSN, then confirm `/opt/o1/current` points at the local-database release.

- [ ] **Step 5: Confirm the timers and final state**

```bash
docker volume ls | grep postgres-data
ss -ltnp | grep -c ':5432' || true
sudo systemctl is-enabled o1-backup.timer o1-backup-prune.timer o1-restore-verify.timer
```
Expected: `o1-postgres-data` present; no `5432` listener on the host; all three timers `enabled`.

---

## Self-Review

- **Spec coverage:** §7.1 compose/env → Task 2; §7.3 backups + OCI specifics → Tasks 1, 3, 4, 5; §7.4 disk → Tasks 2 and 6 preflight; §7.5 cutover → Task 6; §7.6 rollback → Tasks 4 and 7; §7.7 tests → Tasks 2 and 3; §7.8 docs → Task 4; §7.9 shared-VM safety → Tasks 2 and 7.
- **Step scan:** each step names an exact file, command, or value; no "handle edge cases".
- **Type consistency:** `POSTGRES_PASSWORD`, `o1-postgres-data`, `postgres:18-alpine`, and `O1_BACKUP_PG_IMAGE` are used identically across tasks.
- **Review Focus:** each of the five lines is pinned by a named test or an explicit operator check.
