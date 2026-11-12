# Encrypted offsite backups and restore drills

This directory holds the host-side backup tooling for the production stack. It is
operator tooling, not part of the application release bundle: a deploy or
rollback neither installs nor updates it, and neither one takes a backup.

## 1. What is backed up, and what is not

`backup.sh` streams `pg_dump -Fc -Z 6` from the Postgres container into
`restic backup --stdin`, so the dump never lands on the host's disk. Restic
encrypts and deduplicates the stream in an S3-compatible repository.

- **RPO: up to 24 hours.** There is one snapshot per successful daily run. A
  failed, skipped, or delayed run widens the window; the alerting in §9 is how
  you find out.
- **RTO: tens of minutes to a few hours**, depending on database size, download
  speed, and whether a host already exists. This is a full logical restore, not
  a standby.
- **No point-in-time recovery.** There is no WAL archive and no `archive_mode`.
  A restore lands on the chosen daily (or monthly) snapshot. Intra-day data
  changes since that snapshot are lost. This is the deliberate trade-off that
  keeps the deployment a single VPS: full dumps are simpler to reason about and
  to verify than a WAL chain.
- **Not backed up:** `/opt/o1/.env` (including `O1_ENCRYPTION_KEY`,
  `O1_SECRET_KEY`, `POSTGRES_PASSWORD`, and the Resend API key), the restic
  password, the S3 credentials, `images.env`, and Caddy's certificate volumes.
  Those are restored from the password manager during disaster recovery.

Without `O1_ENCRYPTION_KEY`, monitor headers and request bodies in a restored
database cannot be decrypted. Keep `.env` and the encryption key stored
separately from the database backup, in a password manager or sealed offline
copy, so that losing the host loses neither the data nor the key.

## 2. Files and schedules

| File | Purpose |
|------|---------|
| `backup.env.example` | Template for `/opt/o1/backup/backup.env`; placeholders only. |
| `systemd/o1-backup.service`, `.timer` | Daily dump at 02:30 UTC, up to 10 minutes of randomized delay, `Persistent=true`. |
| `systemd/o1-backup-prune.service`, `.timer` | Weekly `backup.sh --prune-only`, Sunday 03:30 UTC with up to 10 minutes of randomized delay. |
| `systemd/o1-restore-verify.service`, `.timer` | Monthly restore drill, first day at 06:00 UTC with up to 15 minutes of randomized delay. |

Scripts installed into `/opt/o1/bin`:

| Script | Purpose |
|--------|---------|
| `backup.sh` | Daily dump + upload; `--prune-only` for the weekly prune. |
| `restore-verify.sh` | Restore the latest `o1-db` snapshot into a scratch Postgres and validate it. |
| `backup-lib.sh` | Shared env parsing, locking, state, alerting, and ping helpers. |

The timers are `Persistent=true`: a run missed while the host was down starts
after boot. Backup, prune, and the restore drill share
`/opt/o1/backup/repo.lock`; only one runs at a time.

## 3. Host prerequisites

The host needs Docker with the Compose v2 plugin, `restic` 0.19.1, `flock`,
`curl`, `jq`, `openssl`, `timeout`, and systemd. `bunzip2`, `sed`, `mktemp`, and
`install` are used during installation; they are part of a standard base
install.

Install restic from the pinned release (use `arm64` on ARM hosts):

```sh
curl -fsSL -o /tmp/restic.bz2 \
  https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_amd64.bz2
bunzip2 /tmp/restic.bz2
sudo install -m 0755 /tmp/restic /usr/local/bin/restic
restic version
```

The installer checks for these commands before it changes anything. It does not
install Docker or restic.

## 4. Repository and credentials

Use a private S3-compatible bucket (AWS S3, MinIO, Cloudflare R2, or a provider
exposing the S3 API) with a dedicated prefix, for example `o1`. Versioning and
lifecycle rules are optional; the scripts do not depend on them.

`RESTIC_REPOSITORY` takes either form:

```text
s3:s3.amazonaws.com/<bucket>/o1
s3:https://<endpoint>/<bucket>/o1
```

Create credentials scoped to that bucket and prefix: list the bucket, and read,
write, and delete objects under `o1/` only. Nothing else on the account is
needed. Put the endpoint-specific settings in `backup.env` when the provider
needs them (for example `AWS_DEFAULT_REGION`); the scheduled units load that
file.

`backup.env` is parsed key by key by the scripts; it is never sourced or
executed. Keep it `0600` root-owned because it contains the S3 secret and the
restic password path. Do not add shell syntax such as `$(...)`, spaces around
`=`, or multi-line values.

After `backup.env` is configured (§6), initialize the repository once, for a
brand-new bucket and prefix:

```sh
sudo /tmp/o1-deploy/scripts/install-backup.sh \
  --source /tmp/o1-deploy --init-new-repo
```

`--init-new-repo` is the only path that generates a password. If the repository
already exists, the installer removes the candidate password and aborts; it
never overwrites an existing `restic.pass`. If the provider needs extra
environment for `restic init`, pass it through sudo explicitly, because sudo
resets the environment and an exported variable in your shell will not reach
the installer:

```sh
sudo AWS_DEFAULT_REGION=us-east-1 /tmp/o1-deploy/scripts/install-backup.sh \
  --source /tmp/o1-deploy --init-new-repo
```

`sudo -E` also preserves the caller's environment when the sudo policy allows
it. For `restic init`, the installer exports `RESTIC_REPOSITORY`,
`RESTIC_PASSWORD_FILE`, `RESTIC_CACHE_DIR`, and the two AWS credential variables
inside a subshell instead of passing `env VAR=value` argv, so credentials never
appear in a process listing.

## 5. Restic password safety and offsite storage

The repository password is the single point of failure for every backup. Restic
cannot decrypt a snapshot without it, and the scripts never print it.

- `restic.pass` lives at `/opt/o1/backup/restic.pass`, mode `0600`, root-owned.
- The installer keeps an existing `restic.pass` unchanged. It generates a new
  one only for an explicit `--init-new-repo`, and only after confirming a local
  repository has no `config` file. If `restic init` reports that the repository
  already exists, the candidate is deleted and the install aborts.
- On a fresh or replacement host, do **not** use `--init-new-repo`. Copy the
  original `restic.pass` from offsite storage first (see §8). A newly generated
  password cannot read the existing repository, so the installer refuses a
  missing password unless `--init-new-repo` is explicit.
- Store `restic.pass` off the host in a password manager, separately from
  `backup.env` and the S3 credentials. Losing it makes every snapshot
  unrecoverable. If you also keep an offline copy, refresh it whenever the
  password changes (this plan has no rotation procedure).

## 6. Delivery and installation

The backup tree changes only when an operator installs a new copy. It is not in
the application release bundle and does not roll back with a deploy.

From a checkout, copy the whole `deploy` tree, not a flat set of files. The
installer's `--source` must point at a directory that contains `backup/` and
`scripts/`:

```sh
scp -r deploy <user>@<host>:/tmp/o1-deploy
```

If the destination directory already exists, `scp -r` creates
`/tmp/o1-deploy/deploy`; either remove the old path first or pass the nested
`deploy` directory to `--source`. The directory you pass must contain `backup/`
and `scripts/` directly.

On the host, seed the environment from the example, edit it, then install. A
brand-new repository needs `--init-new-repo` on the install run; a replacement
host that already has the original `restic.pass` must omit it (§5, §8.1):

```sh
sudo install -d -m 0700 /opt/o1/backup /opt/o1/backup/cache /opt/o1/bin
sudo install -m 0600 /tmp/o1-deploy/backup/backup.env.example \
  /opt/o1/backup/backup.env
sudoedit /opt/o1/backup/backup.env
sudo /tmp/o1-deploy/scripts/install-backup.sh \
  --source /tmp/o1-deploy --init-new-repo
```

That run installs the scripts and all six units and creates the restic
password, and leaves the timers disabled. Once `backup.env` has a real
`O1_BACKUP_PING_URL` and one failure path, enable them:

```sh
sudo /tmp/o1-deploy/scripts/install-backup.sh --source /tmp/o1-deploy --enable
```

Re-running the installer is the upgrade path. It preserves an existing
`backup.env` and `restic.pass`, rewrites `/opt/o1` if `--root` differs, applies
`O1_BACKUP_TIMEOUT` to the backup service, runs `systemd-analyze verify` when
available, and runs `systemctl daemon-reload`. The installer itself is not
copied into `/opt/o1/bin`.

Production requires `O1_BACKUP_PING_URL` and at least one failure path
(`O1_BACKUP_ALERT_EMAIL` or `O1_BACKUP_FAILURE_URL`). `--enable` enforces that,
and also requires `RESTIC_REPOSITORY` and `RESTIC_PASSWORD_FILE`. Without
`--enable`, a fresh install enables no timers; on an upgrade re-run, previously
enabled timers keep firing because the installer does not disable them. Turn
all three off explicitly when you need a quiet host:

```sh
sudo systemctl disable --now o1-backup.timer o1-backup-prune.timer \
  o1-restore-verify.timer
```

## 7. Configuration and retention

`/opt/o1/backup/backup.env`:

| Variable | Required | Purpose |
|----------|----------|---------|
| `RESTIC_REPOSITORY` | yes | S3-compatible repository URL. |
| `RESTIC_PASSWORD_FILE` | yes | `/opt/o1/backup/restic.pass`. |
| `RESTIC_CACHE_DIR` | yes | `/opt/o1/backup/cache`; needs room for restic's cache. |
| `AWS_ACCESS_KEY_ID` | yes | Scoped S3 access key. |
| `AWS_SECRET_ACCESS_KEY` | yes | Scoped S3 secret. |
| `O1_COMPOSE_FILE` | yes | `/opt/o1/current/deploy/production/compose.yaml`. |
| `O1_ENV_FILE` | yes | `/opt/o1/.env`; used for Compose and Resend alert credentials. |
| `O1_BACKUP_PG_CONTAINER` | no | Backup only: dump with `docker exec <container> pg_dump` instead of resolving Compose. |
| `O1_BACKUP_KEEP_DAILY` | no | Daily snapshots to keep; default `30`. |
| `O1_BACKUP_KEEP_MONTHLY` | no | Monthly snapshots to keep; default `12`. |
| `O1_BACKUP_TIMEOUT` | no | Backup timeout; default `2h`. Rewrites the unit on install. |
| `O1_BACKUP_UPLOAD_LIMIT` | no | Optional restic upload limit in KiB/s, for example `2048`. |
| `O1_BACKUP_MIN_FREE_KB` | no | Free-disk preflight for `RESTIC_CACHE_DIR`, in KiB; default `1048576` (1 GiB). |
| `O1_BACKUP_LARGE_DB_WARN_KB` | no | Install-time warning threshold for `pg_database_size('o1')/1024`; default `10485760` (10 GiB). |
| `O1_BACKUP_PING_URL` | yes (production) | External dead-man's-switch success URL. |
| `O1_BACKUP_FAILURE_URL` | one failure path | Failure URL; separate from the success URL. |
| `O1_BACKUP_ALERT_EMAIL` | one failure path | Failure recipient; needs Resend credentials in `.env`. |

Retention is 30 daily snapshots plus 12 monthly snapshots. The daily run
applies `restic forget --tag o1-db --keep-daily 30 --keep-monthly 12` without
pruning. The weekly prune (Sunday 03:30 UTC) adds `--prune` and physically
removes unreferenced packs. Restic keeps the last snapshot of each month for the
monthly policy, so the steady state is roughly 42 snapshots. Keep daily and
monthly together: a short daily policy alone would leave only a month of
history.

Backup retention is independent of application retention (Plan 13: 90 days of
raw probes, 13 months of hourly rollups, 12 months of audit events). Restic does
not know what the application deleted, and the application does not know what
restic keeps. Restoring a snapshot older than the application retention cutoff
reintroduces data that retention has since deleted: raw probe partitions,
rollups, and audit rows can reappear. That is expected. Retention will delete
them again on its normal schedule, and the reintroduced rows may not match the
current `rollup_state` watermark. Choose the newest snapshot that meets the
recovery goal rather than an arbitrarily old one.

## 8. Manual operation and disaster recovery

### Manual backup, prune, and drill

```sh
sudo systemctl start o1-backup.service          # daily backup, blocks until done
sudo systemctl start o1-backup-prune.service    # retention + restic prune
sudo systemctl start o1-restore-verify.service  # restore drill
sudo journalctl -u o1-backup.service -n 100 --no-pager
sudo systemctl list-timers 'o1-*'
```

A manual drill can target one snapshot:

```sh
sudo /opt/o1/bin/restore-verify.sh <snapshot-id>
```

Inspect state and list snapshots. `backup.env` is shell-compatible for
interactive use; do not modify it to add shell constructs:

```sh
sudo cat /opt/o1/backup/last-success
sudo cat /opt/o1/backup/last-restore-verify
sudo bash -c '
  set -a; . /opt/o1/backup/backup.env; set +a
  restic snapshots --tag o1-db --latest 5
'
```

### Restore drill behavior

`restore-verify.sh` selects the latest `o1-db` snapshot (or the snapshot given
as an argument) and:

1. Preflights free disk under `RESTIC_CACHE_DIR` for at least twice
   `pg_database_size` and aborts before creating anything if it does not fit.
2. Starts a scratch `postgres:16-alpine` container with a unique name, a random
   password, and no published host port.
3. Restores with `restic dump <snapshot> o1.dump | docker exec -i <scratch>
   pg_restore -U o1 -d o1_restore --no-owner --no-privileges`.
4. Checks `goose_db_version` and the core tables (`users`, `workspaces`,
   `monitors`, `probe_results`, `monitor_hourly_stats`, `incidents`), their row
   counts, and a representative monitors/workspaces join.
5. Runs `restic check --read-data-subset=5%`.
6. Writes `/opt/o1/backup/last-restore-verify` as
   `snapshot|timestamp|success` or `failure|timestamp|stage`, and sends a
   failure alert if any step fails.
7. If another job holds `repo.lock`, writes
   `/opt/o1/backup/last-restore-attempt` as `skipped|timestamp|lock` and exits
   0. That marker distinguishes a lock skip from a failed drill; it does not
   replace the success/failure report.
8. Always removes the scratch container and its anonymous volume, on success or
   failure. It never targets the production database.

The drill proves that the repository password works, the snapshot decrypts,
`pg_restore` succeeds, and the schema is present. It does **not** prove that
`O1_ENCRYPTION_KEY` decrypts application columns; the host has no application
decryptor. The encrypted-header smoke test in §8.1 (step 9) covers that after
the app starts.

### 8.1 Full disaster recovery: new host

Preconditions: the new host has the same `O1_DOMAIN` and can receive DNS, and
you have these from the password manager, stored separately from the backup
repository:

- `/opt/o1/.env` (or the ability to recreate it with the same
  `POSTGRES_PASSWORD`, `O1_SECRET_KEY`, and `O1_ENCRYPTION_KEY`);
- `/opt/o1/backup/backup.env`;
- `/opt/o1/backup/restic.pass`;
- the release bundle (`compose.yaml`, `images.env`, and the deploy tree) or a
  checkout at the matching version.

1. **Provision the host.** Install Docker Engine with the Compose v2 plugin,
   restic 0.19.1, and the utilities from §3. Open inbound `80` and `443`, and
   SSH for administration. Do not publish `5432`, `8080`, or the metrics
   ports.
2. **Install the application.** Bootstrap the host or copy the release bundle
   into `/opt/o1`, restore `.env` with mode `0600`, and make
   `/opt/o1/current` point at the release. Do not start the app yet.
3. **Restore the backup configuration.** Copy the `deploy` tree to the host and
   install it, then restore the two secret files. Copy `restic.pass` first so
   the installer never considers generating a new one:

   ```sh
   scp -r deploy <user>@<host>:/tmp/o1-deploy
   sudo install -d -m 0700 /opt/o1/backup /opt/o1/backup/cache /opt/o1/bin
   sudo install -m 0600 backup.env /opt/o1/backup/backup.env        # from the password manager
   sudo install -m 0600 restic.pass /opt/o1/backup/restic.pass      # from the password manager
   sudo /tmp/o1-deploy/scripts/install-backup.sh --source /tmp/o1-deploy
   ```

   Do **not** pass `--init-new-repo` against the existing repository, and do
   not run `restic init`. If the installer reports a missing password, the copy
   failed; restore it before continuing.
4. **Start Postgres only.** On a registry-deployed host the digest-pinned image
   refs live in `/opt/o1/current/images.env`; every Compose command below passes
   it, or Compose would fall back to the local `o1-backend:local` and
   `o1-web:local` names. The application has not run, so the database is empty
   and ready for `pg_restore`:

   ```sh
   sudo docker compose --env-file /opt/o1/.env \
     --env-file /opt/o1/current/images.env \
     -f /opt/o1/current/deploy/production/compose.yaml up -d postgres
   sudo docker compose --env-file /opt/o1/.env \
     --env-file /opt/o1/current/images.env \
     -f /opt/o1/current/deploy/production/compose.yaml \
     exec -T postgres pg_isready -U o1 -d o1
   ```

5. **Choose a snapshot.** Prefer the newest one that meets the recovery goal.
   Remember that a restore can reintroduce data deleted by application
   retention, and that anything after the snapshot is lost:

   ```sh
   sudo bash -c '
     set -a; . /opt/o1/backup/backup.env; set +a
     restic snapshots --tag o1-db --latest 5
   '
   ```

6. **Restore the dump.** Never restore over a live database; if the target
   database already has data, recreate an empty `o1` database first or restore
   into a scratch database and swap it deliberately.

   ```sh
   sudo bash -c '
     set -a; . /opt/o1/backup/backup.env; set +a
     restic dump <snapshot-id> o1.dump | \
       docker compose --env-file /opt/o1/.env \
         --env-file /opt/o1/current/images.env \
         -f /opt/o1/current/deploy/production/compose.yaml \
         exec -T postgres pg_restore -U o1 -d o1 --no-owner --no-privileges
   '
   ```

   If the snapshot is older than the deployed release, startup migrations move
   the schema forward. A snapshot from a newer release than the running app is
   not supported; deploy the matching or a newer release instead.

7. **Start the rest of the stack.** Startup migrations see the restored
   `goose_db_version` and apply anything newer:

   ```sh
   sudo docker compose --env-file /opt/o1/.env \
     --env-file /opt/o1/current/images.env \
     -f /opt/o1/current/deploy/production/compose.yaml up -d
   ```

8. **Verify readiness.** `https://$O1_DOMAIN/healthz` and
   `https://$O1_DOMAIN/readyz` must return success. `/readyz` also checks
   PostgreSQL. Check `logs api`, `logs scheduler`, and `logs worker` for
   migration or decryption errors.
9. **Encrypted-header smoke test.** Log in as an administrator and open a
   monitor that has encrypted request headers or a request body. The values
   must display (masked in responses), and a probe must succeed. This is the
   only step that proves `O1_ENCRYPTION_KEY` matches the data; if headers are
   unreadable or probes fail to decrypt, stop and restore the correct key from
   the password manager — do not generate a new one.
10. **Confirm TLS.** Caddy's data volumes are not restored, so Caddy requests
    new certificates on first start. DNS must already point at the new host and
    port `80` must be reachable. Watch `logs web` for ACME success. Issuance is
    rate-limited; do not restart Caddy repeatedly while debugging DNS.
11. **Re-enable and prove backups.** Run the installer with `--enable`, start a
    manual backup, confirm a new snapshot and `last-success`, and confirm the
    success ping reaches the external dead-man's switch:

    ```sh
    sudo /tmp/o1-deploy/scripts/install-backup.sh --source /tmp/o1-deploy --enable
    sudo systemctl start o1-backup.service
    sudo cat /opt/o1/backup/last-success
    ```

12. **Record the recovery.** Note the restore start and finish times, the
    snapshot used, and the data-loss window (up to 24 hours, or longer if the
    chosen snapshot was older). Update incident records and any external
    monitors that still point at the old host.

### 8.2 Post-restore checklist

- [ ] `/healthz` and `/readyz` return success.
- [ ] `goose_db_version` is at or ahead of the deployed release's latest
      migration.
- [ ] Login works for a known administrator.
- [ ] A monitor with encrypted headers/body opens and a probe succeeds.
- [ ] Public status pages load with the expected monitors.
- [ ] An alert email or test notification is delivered.
- [ ] Caddy has a valid certificate for `O1_DOMAIN`, and `http` redirects to
      `https`.
- [ ] `systemctl list-timers 'o1-*'` shows the backup, prune, and drill timers.
- [ ] A manual backup succeeds, writes `last-success`, and pings the success
      URL.
- [ ] The restore time, snapshot ID, and data-loss window are recorded.

### 8.3 Restoring to a scratch database first

For a high-value recovery, restore into a scratch Postgres before touching the
production database. The drill already does this for validation. For a manual
inspection, give the scratch container a random password, no published port,
and its own temporary volume, then remove the container with `docker rm -f -v`.
Never point the application at the scratch database.

### 8.4 Restore caveats

- `pg_dump` does not include roles; the restored data is owned by the role that
  connects, and `--no-owner --no-privileges` avoids ownership errors. The
  `POSTGRES_PASSWORD` in `.env` is the new cluster's role password; it does not
  come from the dump.
- A restore is all-or-nothing per snapshot. There is no per-table or per-tenant
  restore and no PITR.
- Restoring an old snapshot can resurrect deleted monitors, users, incidents,
  and audit events. Review access before exposing the restored stack.
- Restic deduplication means a restore downloads the whole snapshot; leave
  enough free space in `RESTIC_CACHE_DIR` and on the Postgres volume.

## 9. Alerting and the dead-man's switch

On success, `backup.sh` pings `O1_BACKUP_PING_URL`. On failure it sends email
through Resend (when `O1_BACKUP_ALERT_EMAIL`, `O1_RESEND_API_KEY`, and
`O1_EMAIL_FROM` are configured) and pings `O1_BACKUP_FAILURE_URL`. The failure
message names the host, job, UTC time, failing stage, and a bounded log tail. It
never includes secrets or row contents. A failure of the alert itself is logged
and does not mask the original error.

- Backup stages: `startup`, `lock`, `verify`, `preflight`, `dump`, `upload`,
  `retention`, `prune`, `state`, `ping`. `upload` is reported when a `pg_dump`/restic
  pipeline fails after the dump itself succeeded, and `prune` applies to the
  weekly `--prune-only` run.
- Restore drill stages: `startup`, `lock`, `snapshot`, `preflight`, `scratch`,
  `ready`, `restore`, `validate`, `join`, `check`, `state`.

Use two different URLs. Point the success URL at an external dead-man's switch
that alerts when the daily ping is missing; that is the only way to detect a
dead host, a stopped timer, or a hung unit. Point the failure URL at a regular
"alert now" check. Pinging the success URL on failure would make the switch look
healthy. Pings use `curl -m 10`, so a hung ping endpoint cannot hold the job
open.

Production installs require `O1_BACKUP_PING_URL` and one failure path. Without
an external ping there is no protection against a dead host, which is why the
installer refuses to enable timers without one. A local watchdog is not a
substitute.

Repository lock contention is not silent: a backup that cannot take
`repo.lock` logs a warning, sends a failure alert, and exits nonzero. Prune and
the restore drill log the skip and exit 0, because another job is already using
the repository; the success ping from the backup still proves the host is alive.
A skipped drill also writes `last-restore-attempt` as
`skipped|timestamp|lock`, so an operator can tell it apart from a drill that
never ran.

## 10. Sizing, timeout, upload limit, and schedule

- **Repository size.** Start with 2–3× the uncompressed database size and watch
  growth. The dump is compressed and restic deduplicates repeated blocks across
  days, so growth is closer to the daily change volume than to a full dump per
  day. The cache in `RESTIC_CACHE_DIR` needs room for the packs it reads.
- **Timeout.** `O1_BACKUP_TIMEOUT` defaults to `2h` and is enforced twice: as
  `TimeoutStartSec` in `o1-backup.service` and with `timeout` around both the
  dump and the restic upload. If a full dump plus upload cannot finish in two
  hours, raise it in `backup.env` and re-run the installer so the unit is
  rewritten. The weekly prune has its own longer unit timeout.
- **Upload limit.** Set `O1_BACKUP_UPLOAD_LIMIT` (restic KiB/s, for example
  `2048` for about 2 MiB/s) when backups compete with production traffic or the
  provider throttles. It only limits restic's upload, not the local dump.
- **Off-peak schedule.** The daily backup is 02:30 UTC with up to 10 minutes of
  randomized delay; prune is Sunday 03:30 UTC with up to 10 minutes; the drill
  is the first of the month at 06:00 UTC with up to 15 minutes. The drill was
  moved off the prune window so the first Sunday of a month does not routinely
  skip the drill on the shared repository lock. Adjust the `OnCalendar` lines
  in `deploy/backup/systemd/*.timer`, re-install, and keep the jobs apart.
- **Monitoring history.** The 30 daily and 12 monthly snapshots together give
  roughly 42 recovery points. Choose retention for the recovery objectives, not
  just for storage cost.

## 11. Verification and CI

`deploy/scripts/test-backup.sh` exercises the library, `backup.sh`,
`restore-verify.sh`, and `install-backup.sh` with fakes and a disposable local
Postgres; it never contacts S3. CI installs restic 0.19.1 in the `pipeline` job
and runs this suite, alongside shellcheck and actionlint. On a host,
`systemd-analyze verify` runs during installation, and a real manual backup plus
`restore-verify.sh` are the operator acceptance checks.

Run the suites locally from the repository root:

```sh
bash deploy/scripts/test-backup.sh
bash -n deploy/scripts/*.sh
docker run --rm -v "$PWD:/repo" -w /repo \
  koalaman/shellcheck:v0.11.0 deploy/scripts/*.sh
```

## 12. Troubleshooting

| Symptom | Check | Fix |
|---------|-------|-----|
| `unable to create lock in backend: repository is already locked` | `sudo journalctl -u o1-backup.service -n 50 --no-pager`; confirm no backup, prune, or drill is running | A restic lock is stale after a crash or killed unit. Remove it with `restic unlock`, using the configured repository and password file: `sudo bash -c 'set -a; . /opt/o1/backup/backup.env; set +a; restic unlock'`. Do not delete lock files in the bucket by hand. |
| `backup skipped: another job holds /opt/o1/backup/repo.lock` | `systemctl list-jobs 'o1-*'`; `ps -ef \| grep '[b]ackup.sh'` | Wait for the other job. If no job is running, the advisory lock belonged to a dead process; it is released automatically, so re-run the unit. |
| Drill log says `restore drill skipped` | `sudo cat /opt/o1/backup/last-restore-attempt` | `skipped\|...\|lock` means another job held `repo.lock`; the next timer run retries. `last-restore-verify` still records the last success or failure. |
| Backup alerts `stage=preflight` | `sudo docker compose ... exec postgres pg_isready -U o1 -d o1`; `df -Pk /opt/o1/backup/cache` | Fix database health or free space, or raise `O1_BACKUP_MIN_FREE_KB` only when the host really has the space. |
| Backup alerts `stage=verify` | Confirm `RESTIC_REPOSITORY`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and `RESTIC_PASSWORD_FILE` in `backup.env` | Correct the credential or URL and run a manual backup. The alert prints the repository with userinfo redacted. |
