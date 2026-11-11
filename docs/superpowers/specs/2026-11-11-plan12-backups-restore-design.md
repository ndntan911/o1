# Plan 12 — Encrypted Offsite Backups and Restore Drills Design

**Status:** Draft for approval
**Date:** 2026-11-11
**Depends on:** Plan 10 (production stack), Plan 11 (host scripts), Plan 13 (rollups and retention)
**Scope owner:** o1 uptime monitor

## 1. Intent

PostgreSQL runs on the same VPS as the application, and Plan 13 now deletes raw
probe data after roughly 90 days. There is no backup, WAL archive, or restore
path. Plan 12 adds a daily encrypted logical backup to S3-compatible offsite
storage, independent retention, failure alerting, and a monthly automated
restore drill.

The user chose a daily logical dump over full point-in-time recovery. The
foundation spec's WAL-archiving requirement is therefore deliberately relaxed:
the RPO is up to 24 hours and there is no PITR. This design states that trade
off everywhere it matters.

Plan 12 was previously marked skipped in the Plan 13 docs. This design
supersedes those statements.

## 2. Current State

- Production Postgres is one `postgres:16-alpine` container with a named volume,
  `wal_level=replica`, and `archive_mode=off`.
- `pg_dump`, `pg_restore`, and `psql` are available inside that image.
- The host has `/opt/o1/{.env,bin,releases,incoming,state}` from Plan 11 and the
  release directory is reachable through `/opt/o1/current`.
- The scheduler performs rollups and retention; retention is dry-run by default.
- No offsite storage, backup job, restore procedure, or backup alert exists.
- The production runbook and root README say backups are skipped and recommend
  a manual `pg_dump`.

## 3. Goals

1. Take a consistent daily logical dump of the `o1` database without stopping the
   application.
2. Encrypt it before it leaves the host and upload it to S3-compatible offsite
   storage.
3. Keep 30 daily and 12 monthly snapshots, independent of the application's own
   data-retention windows.
4. Alert on backup failure, missed runs, and restore-drill failure.
5. Provide an automated monthly restore drill into a scratch Postgres that
   validates schema and core tables without touching production.
6. Provide a clear disaster-recovery runbook, including secrets and encrypted
   columns.
7. Keep the production Compose stack unchanged: the dump runs from the host
   against the existing Postgres container.

## 4. Non-Goals

- WAL archiving, point-in-time recovery, or streaming replication.
- Managed PostgreSQL, a standby region, or cross-region replication.
- Backing up Caddy certificate volumes (Caddy reissues certificates).
- Backing up `.env` or secret keys automatically (the operator stores them in a
  password manager).
- Automatic production cutover after a restore; the cutover is manual.
- Backup-key rotation, legal hold, or per-tenant restore.
- Backup of River job history separately; it is part of the database dump.
- CI/CD integration; backups are a host responsibility.

## 5. Anti-Goals

- A backup that is unencrypted, only on the same host, or never restored.
- A backup job that silently fails and leaves the operator believing it works.
- A restore drill that writes to the production database.
- A dump stream that creates a partial snapshot when `pg_dump` fails.
- Storing the restic password, S3 keys, or Resend key in argv, logs, or git.
- A retention policy that deletes the only copy of a backup.
- A restore that forgets `O1_ENCRYPTION_KEY` and leaves monitor headers/body
  unreadable.

## 6. Approaches Considered

### A1. Host timer + `pg_dump` piped into restic (chosen)

A systemd timer runs `/opt/o1/bin/backup.sh`. The script pipes
`pg_dump -Fc` into `restic backup --stdin`, which encrypts, deduplicates, and
uploads to S3. Restic handles retention and integrity checks. A second timer
runs the restore drill.

- Pros: one tool for encryption, upload, retention, and integrity; no plaintext
  dump on disk; no new container; fits the existing host-script layout; local
  restic repository makes the drill testable without cloud.
- Cons: restic repository password and S3 credentials must be managed; daily
  full dumps are larger than WAL; restore is only to the last daily snapshot.

### A2. Host timer + `pg_dump | age` + `rclone`

The original sketch: stream the dump through `age`, upload ciphertext with
`rclone`, and prune daily/monthly prefixes with custom script logic.

- Pros: small, transparent dependencies; age keys are simple.
- Cons: custom retention and integrity code, two credentials (age identity and
  rclone config), and no built-in deduplication or repository check. Restic
  replaces all of that with one tool.

### B. Compose backup service

A pinned custom image with `pg_dump`/restic, run by a host timer or an internal
cron loop.

- Pros: ships with the stack and is reproducible.
- Cons: an extra image to build and publish, another scheduling surface, and DB
  network access from a container. The host script already has what it needs.

### C. Local dump plus rsync

Rejected: not encrypted, not genuinely offsite, and no restore verification.

### D. Full PITR (pgBackRest or WAL-G)

Rejected by the user's choice. It would require a custom Postgres image,
`archive_mode`, WAL storage, and a more complex restore path. The design keeps
the door open for a later plan but does not implement it.

## 7. Decisions

### 7.1 Backup mechanism

- Restic repository in S3-compatible storage; `pg_dump -Fc -Z 6` streamed to
  `restic backup --stdin --stdin-filename o1.dump --tag o1-db --tag daily`.
- One snapshot per successful daily run. Restic deduplicates blocks, so repeated
  full dumps share unchanged data.
- `set -o pipefail` and explicit `PIPESTATUS` checks. If `pg_dump` fails after
  restic has created a snapshot, the script removes that snapshot before exiting.
- The dump runs inside the Postgres container through the active release:
  `docker compose --env-file /opt/o1/.env -f /opt/o1/current/deploy/production/compose.yaml exec -T postgres pg_dump -U o1 -d o1 -Fc -Z 6`.
- No plaintext dump is written to disk on the normal path.

### 7.2 Encryption and credentials

- Restic encrypts the repository. The repository password is generated by the
  install script only for a brand-new repository (`--init-new-repo`); otherwise
  the operator must restore `/opt/o1/backup/restic.pass` from the password
  manager before the installer will connect. A fresh host must never generate a
  new password against an existing repository.
- The operator must store the repository password off the host in a password
  manager. Losing it makes every backup unrecoverable.
- S3 credentials live in `/opt/o1/backup/backup.env` (mode 0600), scoped to the
  backup bucket/prefix where the provider supports it.
- `O1_ENCRYPTION_KEY` and the rest of `.env` are not uploaded by the backup
  script. The operator stores them separately; without the encryption key, the
  restored database's encrypted monitor headers and bodies are unreadable.
- Alerts use the existing Resend account. The API key is read from
  `/opt/o1/.env` and passed to `curl` through a temporary config file, never
  argv or logs. If no alert target is configured, the script logs a warning and
  still exits nonzero for systemd.

### 7.3 Storage layout and retention

- Repository: `RESTIC_REPOSITORY` points at an S3-compatible bucket/prefix, for
  example `s3:s3.amazonaws.com/<bucket>/o1` or
  `s3:https://<endpoint>/<bucket>/o1`.
- Snapshots are tagged `o1-db` and `daily`; the stdin filename is `o1.dump`.
- Retention: `restic forget --tag o1-db --keep-daily 30 --keep-monthly 12`.
  Restic keeps the last snapshot of each month for the monthly policy, so the
  first successful run of a month becomes that month's archive.
- `--prune` runs in a separate weekly `o1-backup-prune.service`/timer, not inside
  the daily backup service. Prune can exceed the backup timeout and takes an
  exclusive repository lock; separating it keeps a successful backup from being
  reported as failed.
- Backup, prune, and the restore drill share a repository lock
  (`/opt/o1/backup/repo.lock`). A run that cannot take the lock skips and logs;
  backup skips alert, prune/drill skips are informational.
- Application retention (90-day raw / 13-month rollups) is independent. A
  restore may reintroduce data that the application has since deleted; that is
  expected and documented.
- Optional S3 lifecycle/versioning is an operator choice; the script does not
  depend on it.

### 7.4 Scheduling, sizing, and failure handling

- `o1-backup.timer`: daily at 02:30 UTC, `Persistent=true`, randomized delay up
  to 10 minutes, so a missed run starts after boot.
- `o1-backup.service`: oneshot, root, `Nice=10`, best-effort I/O priority,
  `TimeoutStartSec=2h` by default. The installer rewrites the unit's timeout
  from `O1_BACKUP_TIMEOUT` when set, and the script enforces the same timeout
  internally.
- `o1-backup-prune.service`/`o1-backup-prune.timer`: weekly, Sunday 03:30 UTC,
  `ExecStart=/opt/o1/bin/backup.sh --prune-only`.
- `o1-restore-verify.service`/timer: monthly, first day at 04:00 UTC.
- Backup, prune, and drill share `repo.lock`; a skipped run logs and exits 0
  (prune/drill) or exits nonzero with an alert (backup).
- Before dumping, the script checks Postgres health, required variables, restic
  repository access, database size, free disk for the restic cache, and an
  optional upload limit (`O1_BACKUP_UPLOAD_LIMIT`, restic `--limit-upload`).
  The timeout is configurable; the install script warns when the database is
  large enough that the default 2h may be too short.
- The restore drill preflights free disk for a scratch restore (at least twice
  `pg_database_size`) and aborts before creating the scratch volume if it does
  not fit. It runs off-peak with `Nice`/`ionice`.
- On success the backup writes `/opt/o1/backup/last-success` (UTC timestamp,
  snapshot short ID, duration) and pings the success URL.
- On failure it sends an email with host, job, timestamp, exit stage, and a
  bounded log tail, and pings the failure URL. Secrets and row contents are
  never included.
- A production install requires `O1_BACKUP_PING_URL` (the external dead-man's
  switch) so a dead host or timer is detected. Email is optional. At least one
  failure-delivery path is required: `O1_BACKUP_ALERT_EMAIL` or
  `O1_BACKUP_FAILURE_URL`. A local watchdog is not a substitute for the external
  ping because it cannot report a dead host.

### 7.5 Restore drill

`/opt/o1/bin/restore-verify.sh`, run monthly by `o1-restore-verify.timer`:

1. Preflight free disk for a scratch restore (at least twice
   `pg_database_size`); abort before creating anything if it does not fit.
2. Select the latest snapshot tagged `o1-db`.
3. Start a scratch `postgres:16-alpine` container with a random name, a random
   password, a temporary volume, and no published host port except loopback.
4. `restic dump <snapshot> o1.dump | docker exec -i <scratch> pg_restore -U o1
   -d o1_restore --no-owner --no-privileges`.
5. Validate:
   - `goose_db_version` has a row and the core tables exist
     (`users`, `workspaces`, `monitors`, `probe_results`,
     `monitor_hourly_stats`, `incidents`).
   - `SELECT count(*)` succeeds on the core tables; counts are logged.
   - `SELECT 1` succeeds through a representative join used by the app.
6. Run `restic check --read-data-subset=5%` to detect repository corruption.
7. Write a drill report to `/opt/o1/backup/last-restore-verify` and send an
   alert on failure.
8. Always remove the scratch container and volume, even on failure.

The drill never targets the production database. It proves the backup decrypts,
restores, and contains a valid schema. It does **not** prove that
`O1_ENCRYPTION_KEY` decrypts application columns, because the host has no
application decryptor and Plan 12 adds no app code. The runbook's post-restore
smoke test covers that by opening a monitor with encrypted headers after the
app starts.

### 7.6 Restore runbook

1. Provision the replacement host, install Docker and `restic`, restore `.env`,
   the release bundle, `backup.env`, and `restic.pass` from the password manager.
   Do not run `install-backup.sh` with a new repository password against the
   existing repository.
2. Install the backup scripts and units, but do not initialize a repository.
3. Start only Postgres: `docker compose ... up -d postgres`.
4. `restic snapshots --tag o1-db --latest 1` and choose the snapshot.
5. `restic dump <snapshot> o1.dump | docker compose ... exec -T postgres
   pg_restore -U o1 -d o1 --no-owner --no-privileges`.
6. Start the rest of the stack; startup migrations see the restored
   `goose_db_version` and apply any newer migrations.
7. Verify `/healthz`, `/readyz`, login, monitor data, and an alert email. Open a
   monitor with encrypted headers/body to confirm `O1_ENCRYPTION_KEY` is the
   right one.
8. Caddy reissues TLS; certificate volumes are not restored.
9. Record the restore time and the data-loss window (up to 24 hours).

The runbook warns that restoring a dump older than the current application
schema is fine (migrations move forward) but a dump newer than the running
application is not supported.

### 7.7 Host delivery, installation, and upgrades

Backup scripts are stable host tooling, not part of the application release
bundle. They live in `/opt/o1/bin` and change only when the operator re-runs the
installer. This keeps backup behavior from rolling back with an app deploy.

- The operator copies the backup tree from a checkout, for example:
  `scp -r deploy/backup deploy/scripts/backup.sh deploy/scripts/restore-verify.sh deploy/scripts/install-backup.sh deploy/scripts/backup-lib.sh <user>@<host>:/tmp/o1-backup/`
- `install-backup.sh --source /tmp/o1-backup` validates the source, installs the
  scripts and units, and can be re-run for upgrades.
- It checks for `docker`, `restic`, `flock`, `curl`, and `systemd`.
- It creates `/opt/o1/backup` (0700), cache and tmp directories.
- It installs the backup, restore, and alert helper scripts into `/opt/o1/bin`;
  the prune unit invokes `backup.sh --prune-only`. It installs the systemd units
  into `/etc/systemd/system`.
- It creates `backup.env` from the example if absent, mode 0600.
- Password safety: it generates `restic.pass` only with `--init-new-repo` and
  only after confirming the repository is empty. If a repository is configured
  and the password file is absent, it refuses and tells the operator to restore
  the password from the password manager.
- It optionally initializes the restic repository after explicit confirmation.
- It enables the timers only when the repository, `O1_BACKUP_PING_URL`, and at
  least one failure-delivery path are configured.
- It runs `systemctl daemon-reload` and enables the backup, prune, and
  restore-verify timers.

`deploy/backup/backup.env.example` documents the repository, credentials,
retention, alert email, ping URLs, timeout, upload limit, and compose/env paths.

### 7.8 Configuration

`/opt/o1/backup/backup.env`:

| Variable | Required | Purpose |
|---|---|---|
| `RESTIC_REPOSITORY` | yes | S3-compatible repository URL |
| `RESTIC_PASSWORD_FILE` | yes | `/opt/o1/backup/restic.pass` |
| `RESTIC_CACHE_DIR` | yes | `/opt/o1/backup/cache` |
| `AWS_ACCESS_KEY_ID` | yes | S3 access key |
| `AWS_SECRET_ACCESS_KEY` | yes | S3 secret |
| `O1_COMPOSE_FILE` | yes | `/opt/o1/current/deploy/production/compose.yaml` |
| `O1_ENV_FILE` | yes | `/opt/o1/.env` |
| `O1_BACKUP_KEEP_DAILY` | no | default 30 |
| `O1_BACKUP_KEEP_MONTHLY` | no | default 12 |
| `O1_BACKUP_TIMEOUT` | no | default `2h`; systemd start timeout |
| `O1_BACKUP_UPLOAD_LIMIT` | no | optional restic upload limit in KiB/s, e.g. `2048` |
| `O1_BACKUP_PING_URL` | yes (production) | external dead-man's-switch success URL |
| `O1_BACKUP_FAILURE_URL` | one failure path | dead-man's-switch failure URL |
| `O1_BACKUP_ALERT_EMAIL` | one failure path | failure recipient; may be omitted if `O1_BACKUP_FAILURE_URL` is set |

Resend alert credentials come from `O1_RESEND_API_KEY` and `O1_EMAIL_FROM` in
`/opt/o1/.env`.

### 7.9 Observability

- Systemd journal is the primary log. Each run logs start, snapshot ID, bytes
  added, duration, retention result, and success/failure.
- `/opt/o1/backup/last-success` and `/opt/o1/backup/last-restore-verify` hold
  UTC timestamps and snapshot IDs for external monitoring.
- The optional ping URLs give a dead-man's switch: success pings
  `O1_BACKUP_PING_URL` after upload; failure pings `O1_BACKUP_FAILURE_URL` (or
  sends email) on error. They are separate URLs because pinging the success URL
  on failure would make the switch look healthy. If the host dies, the missing
  success ping alerts.
- No new Prometheus exporter is added; the app metrics are unrelated to host
  backup state.

### 7.10 Documentation

- New `deploy/backup/README.md`: bucket setup, restic install, credentials,
  source delivery/`scp`, install script, password safety, timers, manual run,
  retention/prune, restore drill, full disaster recovery, key management,
  RPO/RTO, and the no-PITR caveat.
- Root README: replace the "Plan 12 skipped" statements with the current backup
  and restore path, RPO up to 24h, and the separate `.env`/encryption-key
  requirement.
- Production README: same, plus the retention/backup interaction and a
  post-restore checklist.
- `deploy/CICD.md`: note that deploys and rollbacks do not back up the database;
  backups are the host timer, and a restore is a separate manual procedure.

## 8. Constraints

- Single VPS, Postgres in Docker, no managed database.
- RPO ≤ 24h; no PITR. A failed or delayed run increases the window.
- Restic must be installed on the host; the production Compose stack is
  unchanged.
- The dump runs through the active release compose file and `/opt/o1/.env`.
- The host must have outbound HTTPS to the S3 endpoint and Resend.
- Backups are encrypted at rest by restic; the repository password is required
  to restore.
- Backup scripts are delivered out-of-band from the application release and
  installed as stable host tooling; they are not part of the image bundle.
- The production install requires an external success ping for missed-run
  detection, plus one failure-delivery path.
- The automated backup test runs in CI with a local restic repository and a
  disposable Postgres; it does not require live S3.
- The monthly drill runs on the same host by default; a separate host is an
  optional hardening step.
- The database contains application-encrypted monitor headers/bodies; restoring
  without `O1_ENCRYPTION_KEY` leaves those columns unreadable.
- The monthly drill uses Docker and a scratch Postgres; it must not touch the
  production volume.
- Tests must not require live S3; a local restic repository is enough for the
  automated test.
- No secrets in git, logs, argv, or the restic repository metadata.

## 9. Deliverables

```text
deploy/backup/README.md                         # create
deploy/backup/backup.env.example                # create
deploy/backup/systemd/o1-backup.service         # create
deploy/backup/systemd/o1-backup.timer           # create
deploy/backup/systemd/o1-backup-prune.service   # create
deploy/backup/systemd/o1-backup-prune.timer     # create
deploy/backup/systemd/o1-restore-verify.service # create
deploy/backup/systemd/o1-restore-verify.timer   # create
deploy/scripts/backup.sh                        # create
deploy/scripts/restore-verify.sh                # create
deploy/scripts/install-backup.sh                # create
deploy/scripts/backup-lib.sh                    # create: shared env/alert/lock helpers
deploy/scripts/test-backup.sh                   # create: local restic + Postgres test
.github/workflows/ci.yml                        # modify: install restic, run test-backup.sh
README.md                                       # modify
deploy/production/README.md                     # modify
deploy/CICD.md                                  # modify
```

No Compose or application-code changes.

## 10. Verification

- `shellcheck` and `bash -n` pass on all scripts.
- `deploy/scripts/test-backup.sh` uses a local restic repository and a disposable
  Postgres container:
  - a successful backup creates one snapshot tagged `o1-db`;
  - a forced `pg_dump` failure leaves no snapshot and triggers the failure path;
  - a lock-held run exits without dumping;
  - `restore-verify.sh` restores the snapshot into a scratch container and
    validates the schema/core tables;
  - the scratch container and volume are removed afterwards;
  - the drill aborts before creating a scratch volume when free disk is below
    the preflight threshold;
  - no plaintext dump remains on disk.
- CI installs `restic` in the pipeline job and runs `test-backup.sh`; the test is
  not dead code.
- Unit parsing/systemd checks:
  - `systemd-analyze verify` on a Linux host (operator acceptance if not
    available locally);
  - timer expressions resolve to daily 02:30 UTC and monthly.
- `restic check --read-data-subset=5%` passes on the test repository.
- Documentation commands are executable as written, with placeholders only.
- Existing backend/web suites stay green; no application code changes.

## 11. Decisions Left to the Builder

- Exact restic snapshot JSON parsing and whether `jq` is installed.
- Restore-drill validation query set and report format.
- How CI installs `restic` (apt package versus pinned binary).
- The exact `scp` source layout and whether the installer expects a tarball or a
  directory tree.
- How the install script detects a systemd host versus a manual-run host.
- Exact systemd sandboxing flags compatible with Docker socket access.
- Whether the success timestamp is also written to a Prometheus textfile for
  future host monitoring.

## 12. Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Restore password lost | Install script requires an existing password for an existing repo; operator stores it offsite; drill fails loudly if decryption fails |
| Fresh host generates a new password against the old repo | Installer refuses unless `--init-new-repo`; DR runbook restores `restic.pass`/`backup.env` first |
| S3 outage or credential expiry | Alert on failure; dead-man's switch; retry on next timer run |
| Dead host or timer never runs | External success ping is mandatory in production; missing ping alerts |
| `pg_dump` fails mid-stream and leaves a partial snapshot | `pipefail` + explicit `PIPESTATUS`; remove the partial snapshot |
| Backup too large/slow | `-Fc -Z 6`, restic dedup, configurable timeout, optional upload limit, off-peak run |
| Restore drill fills the disk | Preflight requires at least twice `pg_database_size`; abort before creating the scratch volume |
| Prune collides with backup/drill | Separate weekly prune unit plus a shared repository lock |
| Restore drill damages production | Scratch container, separate volume, no production DB target |
| Restore without `O1_ENCRYPTION_KEY` | Runbook requires `.env` restoration and an encrypted-header smoke test; the automated drill documents this limit |
| Restoring a dump newer than the app | Runbook documents forward-only migrations; do not downgrade |
| Backup on the same host only | Restore drill downloads from the repository; S3 is offsite |
| Secret leakage | 0600 files, no argv, temp curl config, no row contents in alerts |
| Retention deletes backups | Restic retention is separate; `--keep-daily`/`--keep-monthly` only |
| RPO is up to 24h | Stated in docs; a missed run alerts and must be fixed the same day |

## 13. Rollout

1. Create the S3-compatible bucket/prefix and scoped credentials.
2. Install `restic` on the host, copy the backup tree with `scp`, and run
   `install-backup.sh --source`.
3. Edit `backup.env`; for a new repository generate/store the restic password
   and initialize; for an existing repository restore the password first.
4. Run `backup.sh` manually; confirm the snapshot and the success timestamp.
5. Run `restore-verify.sh`; confirm schema/core-table counts and cleanup.
6. Enable the backup, prune, and restore-verify timers.
7. Simulate a clean-host restore from the latest snapshot and record the RTO.
8. Confirm CI runs `test-backup.sh` with a local repository.
9. Update the READMEs and remove the "Plan 12 skipped" statements.
