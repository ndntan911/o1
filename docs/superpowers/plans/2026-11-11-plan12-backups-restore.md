# Plan 12 — Encrypted Offsite Backups and Restore Drills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a daily encrypted `pg_dump` to S3-compatible offsite storage via restic, independent retention, failure/missed-run alerting, and a monthly automated restore drill into a scratch Postgres.

**Architecture:** A systemd timer runs `/opt/o1/bin/backup.sh`, which streams `pg_dump -Fc` into `restic backup --stdin`. Restic encrypts, deduplicates, uploads, and applies 30-daily/12-monthly retention. A separate weekly prune unit and a monthly `restore-verify.sh` share a repository lock. Backup tooling is stable host tooling delivered by `scp` + `install-backup.sh`, not the application release bundle.

**Tech Stack:** PostgreSQL 16, `pg_dump`/`pg_restore`, restic 0.19.1, Bash, systemd timers, Docker Compose, `curl`/Resend for alerts, GitHub Actions for the shell/backup test.

**Spec:** `docs/superpowers/specs/2026-11-11-plan12-backups-restore-design.md`

## Global Constraints

- Daily backup at 02:30 UTC; weekly prune Sunday 03:30 UTC; monthly restore drill on day 1 at 04:00 UTC.
- Retention: 30 daily and 12 monthly restic snapshots, independent of the app's 90-day/13-month data retention.
- RPO ≤ 24 hours. No WAL archiving, PITR, or streaming replica.
- Restic 0.19.1, Postgres 16; the production Compose stack is unchanged.
- Host dependencies: `docker`, `restic`, `flock`, `curl`, `jq`, `timeout`, `systemd`. No Go/Node.
- Backups are encrypted by restic; the repository password must exist off-host.
- Secrets live in `/opt/o1/backup/backup.env` and `/opt/o1/.env`, mode 0600; never in git, argv, logs, or alert bodies.
- A production install requires `O1_BACKUP_PING_URL` and one failure path (`O1_BACKUP_ALERT_EMAIL` or `O1_BACKUP_FAILURE_URL`).
- The restore drill uses a scratch container/volume and never targets the production database.
- `O1_ENCRYPTION_KEY` and `.env` are restored separately; the automated drill cannot verify app-column decryption.
- Backup scripts are stable `/opt/o1/bin` tooling, installed/upgraded by `install-backup.sh`; no application-code or Compose changes.
- Tests use a local restic repository and a disposable Postgres; CI installs restic and runs the test.
- The restic repository password is generated only for a brand-new repository with `--init-new-repo`.

## Review Focus

1. **Partial dump.** A `pg_dump` failure mid-stream must leave no `o1-db` snapshot, no success state, and no success ping; a captured partial snapshot is removed. Tested in Task 2.
2. **Secret containment.** The restic password, S3 keys, and Resend key never appear in argv or logs; alert headers go through a temp curl config; files are mode 0600. Tested in Tasks 1 and 2.
3. **Missed-run and lock semantics.** A lock-held backup exits nonzero and alerts; success pings only after upload and state; failure pings the failure URL. Tested in Tasks 1 and 2.
4. **Fresh-host password safety.** The installer refuses to generate a new password against an existing repository unless `--init-new-repo` is explicit; the DR runbook restores the password first. Tested in Task 4.
5. **Restore isolation and cleanup.** The drill preflights disk, uses a scratch container/volume, validates schema/core tables, removes everything on failure, and never touches production. Tested in Task 3.

---

## File Structure

```text
deploy/backup/README.md                         # create: operator/DR runbook
deploy/backup/backup.env.example                # create: config template
deploy/backup/systemd/o1-backup.service         # create
deploy/backup/systemd/o1-backup.timer           # create
deploy/backup/systemd/o1-backup-prune.service   # create
deploy/backup/systemd/o1-backup-prune.timer     # create
deploy/backup/systemd/o1-restore-verify.service # create
deploy/backup/systemd/o1-restore-verify.timer   # create
deploy/scripts/backup-lib.sh                    # create: env/lock/alert/ping/state helpers
deploy/scripts/backup.sh                        # create: daily backup and prune-only
deploy/scripts/restore-verify.sh                # create: monthly scratch restore
deploy/scripts/install-backup.sh                # create: host install/upgrade
deploy/scripts/test-backup.sh                   # create: lib/backup/restore/install tests
.github/workflows/ci.yml                        # modify: install restic, run backup tests
README.md                                       # modify: data lifecycle / backup path
deploy/production/README.md                     # modify: backup/restore/retention
deploy/CICD.md                                  # modify: backup note
```

`backup-lib.sh` owns shared mechanics; `backup.sh` owns dump/upload/retention; `restore-verify.sh` owns the drill; `install-backup.sh` owns host delivery.

---

### Task 1: Backup library, config, alerts, and lock

**Files:**
- Create: `deploy/scripts/backup-lib.sh`
- Create: `deploy/scripts/test-backup.sh`
- Create: `deploy/backup/backup.env.example`

**Interfaces:**
- `bl_load_env FILE` — parse `KEY=value` lines, strip one layer of quotes, export; ignore comments/blank lines; do not execute the file.
- `bl_require_var NAME...` — die when a required variable is empty.
- `bl_require_cmd CMD...` — die when a command is missing.
- `bl_log LEVEL MESSAGE...` — UTC-timestamped line to stderr.
- `bl_die MESSAGE...` — log error and exit 1.
- `bl_utc_now` — print `YYYY-MM-DDTHH:MM:SSZ`.
- `bl_take_lock PATH` — open fd 9 and `flock -n`; return 1 when held.
- `bl_release_lock` — close fd 9.
- `bl_write_state PATH CONTENT` — write atomically (temp + `mv`).
- `bl_read_o1_env` — read `O1_RESEND_API_KEY` and `O1_EMAIL_FROM` from `O1_ENV_FILE` without sourcing.
- `bl_send_alert SUBJECT BODY` — Resend email via a temp curl config; no key in argv; warn and return when no email target.
- `bl_ping URL STATUS` — `success` or `failure`; no-op when URL is empty.

- [ ] **Step 1: Write the failing library tests**

Create `deploy/scripts/test-backup.sh` with a temp workspace, a fake `curl` on `PATH`, and a `run_lib_tests` function. Assert:

```bash
# env parsing: quotes, comments, blank lines, no execution of $(...)
# required var: missing -> nonzero and message names the variable
# require cmd: missing -> nonzero
# lock: second lock attempt returns 1; release makes it available again
# state: written atomically; existing file replaced
# alert: fake curl receives --config with an Authorization header and a JSON
#   body; "super-secret" never appears in the fake curl argv log
# ping: success URL vs failure URL selection; empty URL is a no-op
```

The fake `curl` records `$*` to `$FAKE_DIR/curl.args` and copies any `--config` file contents to `$FAKE_DIR/curl.config`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash deploy/scripts/test-backup.sh`
Expected: FAIL because `backup-lib.sh` and the helpers do not exist.

- [ ] **Step 3: Implement `backup-lib.sh`**

- `set -o pipefail` is the caller's job; the library must not call `set -e` globally.
- `bl_load_env` reads line by line with `while IFS= read -r`; match `^[A-Za-z_][A-Za-z0-9_]*=`, strip leading/trailing whitespace and one pair of matching quotes, then `export`.
- `bl_take_lock` uses `exec 9>"$path"` and `flock -n 9`; `bl_release_lock` uses `exec 9>&-`.
- `bl_send_alert`:
  - requires `O1_BACKUP_ALERT_EMAIL`, `O1_RESEND_API_KEY`, `O1_EMAIL_FROM`; if any is missing, log `alert not configured` and return 0.
  - creates a temp header config (`--config`) mode 0600 and a temp JSON body mode 0600 using `jq -n --arg`.
  - calls `curl -fsS --config "$header" -H 'Content-Type: application/json' --data-binary @"$body" https://api.resend.com/emails`.
  - removes both temp files through a trap.
- `bl_ping` selects `O1_BACKUP_PING_URL` for success and `O1_BACKUP_FAILURE_URL` for failure; `curl -fsS -m 10` and log-only on failure.
- `bl_read_o1_env` uses `bl_env_get` (a private parser) and never sources `.env`.

- [ ] **Step 4: Create `backup.env.example`**

```sh
# Required
RESTIC_REPOSITORY=s3:https://s3.example.com/o1-backups
RESTIC_PASSWORD_FILE=/opt/o1/backup/restic.pass
RESTIC_CACHE_DIR=/opt/o1/backup/cache
AWS_ACCESS_KEY_ID=replace-me
AWS_SECRET_ACCESS_KEY=replace-me
O1_COMPOSE_FILE=/opt/o1/current/deploy/production/compose.yaml
O1_ENV_FILE=/opt/o1/.env

# Retention
O1_BACKUP_KEEP_DAILY=30
O1_BACKUP_KEEP_MONTHLY=12

# Alerts (ping required; at least one failure path)
O1_BACKUP_PING_URL=
O1_BACKUP_FAILURE_URL=
O1_BACKUP_ALERT_EMAIL=

# Optional
O1_BACKUP_TIMEOUT=2h
O1_BACKUP_UPLOAD_LIMIT=
O1_BACKUP_PG_CONTAINER=
```

- [ ] **Step 5: Run the tests to verify they pass**

Run:

```bash
bash deploy/scripts/test-backup.sh
bash -n deploy/scripts/backup-lib.sh deploy/scripts/test-backup.sh
shellcheck deploy/scripts/backup-lib.sh deploy/scripts/test-backup.sh
```

Expected: PASS; no shellcheck findings.

- [ ] **Step 6: Commit**

```bash
git add deploy/scripts/backup-lib.sh deploy/scripts/test-backup.sh \
  deploy/backup/backup.env.example
git commit -m "feat: add backup config, alert, and lock helpers"
```

---

### Task 2: Daily backup and prune-only mode

**Files:**
- Create: `deploy/scripts/backup.sh`
- Modify: `deploy/scripts/test-backup.sh` (add `run_backup_tests`)

**Interfaces:**
- `backup.sh [--prune-only]`, `BACKUP_ENV` defaults to `/opt/o1/backup/backup.env`.
- Consumes all `bl_*` helpers.
- Produces `/opt/o1/backup/last-success` with `snapshot|timestamp|duration|bytes`.
- Uses `O1_BACKUP_PG_CONTAINER` when set (test/alternate container), otherwise `docker compose --env-file "$O1_ENV_FILE" -f "$O1_COMPOSE_FILE" exec -T postgres pg_dump ...`.

- [ ] **Step 1: Write the failing backup tests**

Add to `test-backup.sh`:

```bash
# 1. Success: local restic repo + disposable Postgres with a fixture schema.
#    Assert one snapshot tagged o1-db, last-success exists, no *.dump file
#    remains anywhere under the test root.
# 2. pg_dump failure: point O1_BACKUP_PG_CONTAINER at a missing container.
#    Assert nonzero, no o1-db snapshot remains, and the alert path ran.
# 3. Lock held: hold repo.lock, run backup, assert nonzero/skip alert and no
#    new snapshot.
# 4. Prune-only: create snapshots with --time older/newer than 30 days, run
#    backup.sh --prune-only, assert the old snapshot is gone and the new one
#    remains.
# 5. Secret containment: fake curl args never contain AWS_SECRET_ACCESS_KEY,
#    RESTIC_PASSWORD, or the Resend key.
```

Fixture schema for the disposable Postgres:

```sql
CREATE TABLE goose_db_version (id serial primary key, version_id bigint not null);
INSERT INTO goose_db_version (version_id) VALUES (9);
CREATE TABLE users (id uuid primary key);
CREATE TABLE workspaces (id uuid primary key);
CREATE TABLE monitors (id uuid primary key);
CREATE TABLE probe_results (id uuid, checked_at timestamptz);
CREATE TABLE monitor_hourly_stats (monitor_id uuid, bucket_start timestamptz);
CREATE TABLE incidents (id uuid primary key);
INSERT INTO users VALUES (gen_random_uuid());
INSERT INTO workspaces VALUES (gen_random_uuid());
INSERT INTO monitors VALUES (gen_random_uuid());
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bash deploy/scripts/test-backup.sh`
Expected: FAIL because `backup.sh` does not exist.

- [ ] **Step 3: Implement `backup.sh`**

Required flow:

1. `set -Eeuo pipefail`, `umask 077`, load `BACKUP_ENV`.
2. `bl_require_var` for `RESTIC_REPOSITORY`, `RESTIC_PASSWORD_FILE`, `O1_COMPOSE_FILE`, `O1_ENV_FILE`, `O1_BACKUP_PING_URL`; require one of `O1_BACKUP_ALERT_EMAIL`/`O1_BACKUP_FAILURE_URL`.
3. `bl_require_cmd docker restic flock curl jq timeout`.
4. `bl_take_lock "$ROOT/repo.lock"`; on failure: log, alert `backup skipped: lock held`, exit 1. For `--prune-only`, log and exit 0.
5. `--prune-only`: `restic forget --tag o1-db --keep-daily "$DAILY" --keep-monthly "$MONTHLY" --prune`, then exit 0.
6. Verify the repository with `restic cat config >/dev/null`; die with an init instruction if it fails.
7. Dump and upload under `timeout "$O1_BACKUP_TIMEOUT"`:

```bash
if [[ -n ${O1_BACKUP_PG_CONTAINER:-} ]]; then
  dump=(docker exec "$O1_BACKUP_PG_CONTAINER" pg_dump -U o1 -d o1 -Fc -Z 6)
else
  dump=(docker compose --env-file "$O1_ENV_FILE" -f "$O1_COMPOSE_FILE" \
    exec -T postgres pg_dump -U o1 -d o1 -Fc -Z 6)
fi
"${dump[@]}" | restic backup --stdin --stdin-filename o1.dump \
  --tag o1-db --tag daily --json > "$json"
statuses=("${PIPESTATUS[@]}")
```

8. If either status is nonzero: parse a snapshot ID from `$json` if present, `restic forget "$snapshot"`, alert with the stage, and exit 1.
9. Parse the snapshot ID with `jq -r 'select(.message_type=="summary") | .snapshot_id'`; fail if empty.
10. `restic forget --tag o1-db --keep-daily "$DAILY" --keep-monthly "$MONTHLY"` (no prune).
11. Write `last-success` atomically with snapshot, UTC time, duration, and restic bytes processed.
12. `bl_ping "$O1_BACKUP_PING_URL" success`.
13. A `trap` on `ERR` sends the failure alert with host, job, stage, and a bounded tail; disable the trap inside the alert to avoid recursion.

Add optional `--limit-upload "$O1_BACKUP_UPLOAD_LIMIT"` when the variable is set.

- [ ] **Step 4: Run the tests to verify they pass**

Run:

```bash
bash deploy/scripts/test-backup.sh
bash -n deploy/scripts/backup.sh
shellcheck deploy/scripts/backup.sh
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add deploy/scripts/backup.sh deploy/scripts/test-backup.sh
git commit -m "feat: add daily encrypted backup and prune mode"
```

---

### Task 3: Restore verification drill

**Files:**
- Create: `deploy/scripts/restore-verify.sh`
- Modify: `deploy/scripts/test-backup.sh` (add `run_restore_tests`)

**Interfaces:**
- `restore-verify.sh [SNAPSHOT]`; default is the latest snapshot tagged `o1-db`.
- Consumes `bl_*`, restic, Docker, a disposable Postgres.
- Produces `/opt/o1/backup/last-restore-verify` with `snapshot|timestamp|status`.
- `O1_BACKUP_DRILL_MIN_FREE_KB` overrides the disk preflight (test hook).

- [ ] **Step 1: Write the failing restore tests**

Add to `test-backup.sh`:

```bash
# 1. Create a backup snapshot from the fixture database, then run
#    restore-verify.sh. Assert exit 0, last-restore-verify exists, and the
#    scratch container is gone.
# 2. Point at a missing snapshot: assert nonzero and an alert.
# 3. Set O1_BACKUP_DRILL_MIN_FREE_KB to a huge value: assert the drill aborts
#    before creating a container and leaves no scratch container.
# 4. Assert the validation output includes goose_db_version and the core table
#    counts.
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bash deploy/scripts/test-backup.sh`
Expected: FAIL because `restore-verify.sh` does not exist.

- [ ] **Step 3: Implement `restore-verify.sh`**

Required flow:

1. `set -Eeuo pipefail`, `umask 077`, load `BACKUP_ENV`; require `RESTIC_REPOSITORY`, `RESTIC_PASSWORD_FILE`, `O1_COMPOSE_FILE`, `O1_ENV_FILE`; require `docker restic jq flock`.
2. `bl_take_lock "$ROOT/repo.lock"`; log and exit 0 if held.
3. Select `SNAPSHOT` from the argument or `restic snapshots --tag o1-db --latest 1 --json | jq -r '.[0].short_id'`; die if empty.
4. Disk preflight:
   - When `O1_BACKUP_DRILL_MIN_FREE_KB` is set, use it directly and skip the
     database-size query (the test hook).
   - Otherwise `db_kb=$(docker compose ... exec -T postgres psql -tAc "SELECT pg_database_size('o1')/1024")`.
   - `free_kb=$(df -Pk "$RESTIC_CACHE_DIR" | awk 'NR==2 {print $4}')`.
   - `min_kb=${O1_BACKUP_DRILL_MIN_FREE_KB:-$((db_kb * 2))}`; die if
     `free_kb < min_kb` before creating anything.
5. Create a scratch container:

```bash
name="o1-restore-verify-$(date +%s)-$$"
password=$(openssl rand -hex 16)
docker run -d --name "$name" \
  -e POSTGRES_USER=o1 -e POSTGRES_PASSWORD="$password" -e POSTGRES_DB=o1_restore \
  -p 127.0.0.1::5432 postgres:16-alpine
```

6. Wait up to 60s for `pg_isready`.
7. Restore: `restic dump "$SNAPSHOT" o1.dump | docker exec -i "$name" pg_restore -U o1 -d o1_restore --no-owner --no-privileges`.
8. Validate:
   - `goose_db_version` exists and has a row;
   - `users`, `workspaces`, `monitors`, `probe_results`, `monitor_hourly_stats`, `incidents` exist;
   - counts succeed and are logged;
   - a representative join (`monitors` to `workspaces`) executes.
9. `restic check --read-data-subset=5%`.
10. Write `last-restore-verify`; alert on failure with the stage.
11. A `trap` removes the scratch container with `docker rm -f -v "$name"` on every exit.

- [ ] **Step 4: Run the tests to verify they pass**

Run:

```bash
bash deploy/scripts/test-backup.sh
bash -n deploy/scripts/restore-verify.sh
shellcheck deploy/scripts/restore-verify.sh
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add deploy/scripts/restore-verify.sh deploy/scripts/test-backup.sh
git commit -m "feat: add automated restore verification drill"
```

---

### Task 4: Installer and systemd units

**Files:**
- Create: `deploy/scripts/install-backup.sh`
- Create: `deploy/backup/systemd/o1-backup.service`
- Create: `deploy/backup/systemd/o1-backup.timer`
- Create: `deploy/backup/systemd/o1-backup-prune.service`
- Create: `deploy/backup/systemd/o1-backup-prune.timer`
- Create: `deploy/backup/systemd/o1-restore-verify.service`
- Create: `deploy/backup/systemd/o1-restore-verify.timer`
- Modify: `deploy/scripts/test-backup.sh` (add `run_install_tests`)

**Interfaces:**
- `install-backup.sh --source DIR [--root /opt/o1] [--init-new-repo] [--enable]`.
- `--source` contains `backup/` and `scripts/` (the `deploy` tree).
- Installs scripts to `$ROOT/bin` (0755), units to `/etc/systemd/system` (0644), creates `$ROOT/backup` (0700) and `backup.env` (0600) when absent.

- [ ] **Step 1: Write the failing installer tests**

Add to `test-backup.sh` with fake `systemctl`, `restic`, and `docker` on `PATH`, a temp source tree, and a temp root. Assert:

```bash
# 1. Installs scripts/units with expected modes; creates backup dirs.
# 2. Second run preserves an edited backup.env and an existing restic.pass.
# 3. No restic.pass + repository configured + no --init-new-repo -> refuses.
# 4. --init-new-repo with a fake empty repo generates restic.pass and calls
#    restic init; a fake init failure ("repository already exists") removes the
#    generated password and aborts.
# 5. --enable without O1_BACKUP_PING_URL -> does not enable timers.
# 6. --enable with ping + failure URL -> calls systemctl enable for all three
#    timers.
# 7. Missing required commands/source files -> clear failure before installing.
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bash deploy/scripts/test-backup.sh`
Expected: FAIL because `install-backup.sh` and the units do not exist.

- [ ] **Step 3: Create the systemd units**

`o1-backup.service`:

```ini
[Unit]
Description=o1 PostgreSQL encrypted offsite backup
Wants=docker.service
After=docker.service

[Service]
Type=oneshot
User=root
EnvironmentFile=/opt/o1/backup/backup.env
ExecStart=/opt/o1/bin/backup.sh
Nice=10
IOSchedulingClass=best-effort
IOSchedulingPriority=7
TimeoutStartSec=2h
```

`o1-backup.timer`:

```ini
[Unit]
Description=Daily o1 PostgreSQL backup

[Timer]
OnCalendar=*-*-* 02:30:00 UTC
Persistent=true
RandomizedDelaySec=10m
Unit=o1-backup.service

[Install]
WantedBy=timers.target
```

`o1-backup-prune.service` uses `ExecStart=/opt/o1/bin/backup.sh --prune-only`; its timer uses `OnCalendar=Sun *-*-* 03:30:00 UTC`.

`o1-restore-verify.service` uses `ExecStart=/opt/o1/bin/restore-verify.sh` with a 4h timeout; its timer uses `OnCalendar=*-*-01 04:00:00 UTC`.

All timers use `Persistent=true` and target `timers.target`.

- [ ] **Step 4: Implement `install-backup.sh`**

- Parse `--source`, `--root`, `--init-new-repo`, `--enable`.
- Validate source files and `docker restic flock curl jq openssl systemctl timeout`.
- `install -d -m 0700 "$root" "$root/backup" "$root/backup/cache" "$root/bin"`.
- `install -m 0755` the scripts; `install -m 0644` the units to
  `/etc/systemd/system`. When `--root` differs from `/opt/o1`, rewrite the
  `/opt/o1` prefix in the unit files to `$root`. When `O1_BACKUP_TIMEOUT` is
  set, rewrite the backup service `TimeoutStartSec` to that value.
- Create `$root/backup/backup.env` from `backup.env.example` when absent, mode 0600.
- Password guard: if `$root/backup/restic.pass` exists, keep it. Else require
  `--init-new-repo`; generate a candidate only when the repo is confirmed
  uninitialized. Attempt `restic init`; if it fails because the repository
  already exists, delete the generated password file and abort with instructions
  to restore the real password from the password manager. Never leave a new
  password paired with an existing repository.
- Do not print the password; print the path and tell the operator to copy it
  offsite.
- `systemctl daemon-reload`; if `--enable`, require `O1_BACKUP_PING_URL` and one
  failure path in `backup.env`, then `systemctl enable --now` the three timers.
- If `systemd-analyze` exists, run `systemd-analyze verify` on the installed
  units before enabling.
- Never source `backup.env`; read keys with the library parser.

- [ ] **Step 5: Run the tests to verify they pass**

Run:

```bash
bash deploy/scripts/test-backup.sh
bash -n deploy/scripts/install-backup.sh
shellcheck deploy/scripts/install-backup.sh
```

Expected: PASS. If `systemd-analyze` is unavailable locally, the test skips that
check and the report says so.

- [ ] **Step 6: Commit**

```bash
git add deploy/scripts/install-backup.sh deploy/backup/systemd \
  deploy/scripts/test-backup.sh
git commit -m "feat: install backup timers and restore drill"
```

---

### Task 5: CI wiring, docs, and final verification

**Files:**
- Modify: `.github/workflows/ci.yml`
- Create: `deploy/backup/README.md`
- Modify: `README.md`
- Modify: `deploy/production/README.md`
- Modify: `deploy/CICD.md`

**Interfaces:**
- Consumes everything from Tasks 1–4.

- [ ] **Step 1: Wire the backup test into CI**

In the `pipeline` job of `.github/workflows/ci.yml`, add:

```yaml
- name: Install restic
  run: |
    curl -fsSL https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_amd64.bz2 -o /tmp/restic.bz2
    bunzip2 /tmp/restic.bz2
    sudo install -m 0755 /tmp/restic /usr/local/bin/restic
    restic version
- name: Backup and restore tests
  run: bash deploy/scripts/test-backup.sh
```

The runner provides Docker for the disposable Postgres. Add a
`test-workflows.sh` assertion that `ci.yml` installs restic and runs
`test-backup.sh`.

- [ ] **Step 2: Write `deploy/backup/README.md`**

Cover:

- RPO/RTO and the explicit no-PITR trade-off.
- S3-compatible bucket/prefix setup and scoped credentials.
- `scp` delivery of `deploy/backup` and the scripts; `install-backup.sh`.
- Restic password safety and offsite storage.
- `backup.env` variables and retention.
- Manual backup, prune, and restore-drill commands.
- Restore drill behavior and cleanup.
- Full disaster recovery: new host, `.env` + `restic.pass` + `backup.env`,
  Postgres-only start, restore, app start, readiness, encrypted-header smoke
  test, Caddy reissue.
- Alerting/dead-man's switch semantics.
- Sizing, timeout, upload limit, off-peak schedule.
- Backup/retention interaction: restores may reintroduce data deleted by the app.
- `.env`/`O1_ENCRYPTION_KEY` must be stored separately.

- [ ] **Step 3: Update the READMEs**

- Root README: replace "Plan 12 skipped" with the backup/restore path, RPO up
  to 24h, 30 daily/12 monthly, and the separate secrets requirement.
- Production README: replace the skipped note and add a backup/restore section
  and post-restore checklist.
- `deploy/CICD.md`: note that deploys/rollbacks do not back up the database;
  the host timer does, and restoring is a manual procedure.

- [ ] **Step 4: Run the complete verification**

```bash
bash deploy/scripts/test-backup.sh
bash -n deploy/scripts/*.sh
docker run --rm -v "$PWD:/repo" -w /repo koalaman/shellcheck:v0.11.0 deploy/scripts/*.sh
docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.12
cd backend && go test -short ./...
cd ../web && npm test -- --run
```

Expected: all exit 0. The backup test starts a disposable Postgres and a local
restic repository; it does not use S3. `systemd-analyze verify` runs on the host
or is reported as operator acceptance.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml deploy/backup/README.md \
  README.md deploy/production/README.md deploy/CICD.md \
  deploy/scripts/test-workflows.sh
git commit -m "docs: document backups and restore drills"
```

---

## Self-Review Checklist

- [ ] Spec coverage: config/alerts/lock → Task 1; daily backup/prune → Task 2; restore drill → Task 3; installer/systemd → Task 4; CI/docs/final gate → Task 5.
- [ ] Review Focus tests: partial dump → Task 2; secrets → Tasks 1/2; lock/ping → Tasks 1/2; fresh-host password → Task 4; restore isolation/cleanup → Task 3.
- [ ] Names/signatures match: `bl_load_env`, `bl_take_lock`, `bl_send_alert`, `bl_ping`, `backup.sh --prune-only`, `restore-verify.sh`, `install-backup.sh --source`, `last-success`, `last-restore-verify`, `repo.lock`.
- [ ] No task depends on a later task without saying so; each ends with a passing test cycle and a commit.
- [ ] No application-code or Compose changes; no WAL/PITR; no secrets in git.
