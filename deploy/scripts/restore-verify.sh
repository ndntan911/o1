#!/usr/bin/env bash
# Monthly restore drill: restore the latest o1-db snapshot into a scratch
# Postgres, validate the schema and a representative join, then verify 5% of
# the repository data.
#
# The scratch container has a random name and password, publishes only a
# loopback port, and is always removed with its anonymous volume. Production
# Compose is never targeted: the only production access is an optional
# database-size query for the disk preflight.
set -Eeuo pipefail
umask 077

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=backup-lib.sh disable=SC1091
source "$script_dir/backup-lib.sh"

BACKUP_ENV=${BACKUP_ENV:-/opt/o1/backup/backup.env}
ROOT=$(dirname -- "$BACKUP_ENV")

usage="usage: restore-verify.sh [SNAPSHOT]"
[[ $# -le 1 ]] || bl_die "$usage"
SNAPSHOT=${1:-}

if ! bl_load_env "$BACKUP_ENV"; then
  bl_die "cannot load the backup environment: $BACKUP_ENV"
fi

bl_require_var RESTIC_REPOSITORY RESTIC_PASSWORD_FILE O1_COMPOSE_FILE O1_ENV_FILE
if [[ -z ${O1_BACKUP_ALERT_EMAIL:-} && -z ${O1_BACKUP_FAILURE_URL:-} ]]; then
  bl_die "set O1_BACKUP_ALERT_EMAIL or O1_BACKUP_FAILURE_URL"
fi
bl_require_cmd docker restic jq flock openssl

current_stage=startup
scratch_name=
errlog=
lock_held=0

# shellcheck disable=SC2329  # invoked by the EXIT trap
cleanup() {
  local status=$?
  trap - ERR
  if [[ -n $scratch_name ]]; then
    # -v removes the anonymous Postgres data volume with the container.
    docker rm -f -v "$scratch_name" >/dev/null 2>&1 || true
  fi
  if [[ -n $errlog ]]; then
    rm -f -- "$errlog" || true
  fi
  if ((lock_held)); then
    bl_release_lock
  fi
  return "$status"
}
trap cleanup EXIT

# send_failure_alert STAGE MESSAGE: email the failure and ping the failure
# URL. It disables the ERR trap first so an alert failure cannot recurse.
send_failure_alert() {
  local stage=$1 message=$2 body
  trap - ERR
  set +e

  body=$(printf 'host=%s\njob=restore-verify\nsnapshot=%s\ntime=%s\nstage=%s\n%s' \
    "$(hostname)" "${SNAPSHOT:-none}" "$(bl_utc_now)" "$stage" "$message")
  if [[ -n $errlog && -s $errlog ]]; then
    body+=$'\n--- log tail ---\n'
    body+=$(tail -n 20 -- "$errlog" 2>/dev/null)
  fi

  # The deployment env may be unreadable or incomplete; the alert helpers then
  # warn and the failure ping still runs.
  bl_read_o1_env || true
  bl_send_alert "o1 restore drill failed: $stage on $(hostname)" "$body" ||
    bl_log warn "the failure alert could not be sent"
  bl_ping "${O1_BACKUP_FAILURE_URL:-}" failure
}

fail() {
  local stage=$current_stage message=${1:-restore drill failure}
  bl_log error "restore drill failed during $stage: $message"
  send_failure_alert "$stage" "$message"
  exit 1
}

# shellcheck disable=SC2329  # invoked by the ERR trap
on_error() {
  local status=$?
  local stage=$current_stage
  trap - ERR
  set +e
  bl_log error "restore drill failed during $stage"
  send_failure_alert "$stage" "unexpected failure (exit status $status)"
  exit "$status"
}

current_stage=lock
if ! bl_take_lock "$ROOT/repo.lock"; then
  bl_log warn "restore drill skipped: another job holds $ROOT/repo.lock"
  exit 0
fi
lock_held=1
trap on_error ERR

current_stage=snapshot
if [[ -z $SNAPSHOT ]]; then
  SNAPSHOT=$(restic snapshots --tag o1-db --latest 1 --json |
    jq -r 'if length > 0 then .[0].short_id else "" end') ||
    fail "could not list snapshots in $RESTIC_REPOSITORY"
  [[ -n $SNAPSHOT ]] ||
    fail "no snapshot tagged o1-db in $RESTIC_REPOSITORY"
else
  restic snapshots "$SNAPSHOT" --json >/dev/null 2>&1 ||
    fail "snapshot not found: $SNAPSHOT"
fi
bl_log info "restore drill selected snapshot $SNAPSHOT"

current_stage=preflight
min_kb=${O1_BACKUP_DRILL_MIN_FREE_KB:-}
if [[ -z $min_kb ]]; then
  # The test hook skips this query so the drill can run without production.
  db_kb=$(docker compose --env-file "$O1_ENV_FILE" -f "$O1_COMPOSE_FILE" \
    exec -T postgres psql -U o1 -d o1 -tAc \
    "SELECT pg_database_size('o1')/1024") ||
    fail "could not read the database size"
  db_kb=${db_kb//[[:space:]]/}
  [[ $db_kb =~ ^[0-9]+$ ]] ||
    fail "unexpected database size: ${db_kb:-<empty>}"
  min_kb=$((db_kb * 2))
fi
[[ $min_kb =~ ^[0-9]+$ ]] ||
  fail "O1_BACKUP_DRILL_MIN_FREE_KB is not an integer: $min_kb"

cache_dir=${RESTIC_CACHE_DIR:-$ROOT}
free_kb=$(df -Pk "$cache_dir" | awk 'NR==2 {print $4}') ||
  fail "could not determine free disk for $cache_dir"
free_kb=${free_kb//[[:space:]]/}
[[ $free_kb =~ ^[0-9]+$ ]] ||
  fail "unexpected free disk value: ${free_kb:-<empty>}"
if ((free_kb < min_kb)); then
  fail "not enough free disk for the restore: free=${free_kb}KB required=${min_kb}KB"
fi
bl_log info "disk preflight passed: free=${free_kb}KB required=${min_kb}KB"

current_stage=scratch
name="o1-restore-verify-$(date +%s)-$$"
scratch_name=$name
password=$(openssl rand -hex 16) ||
  fail "could not generate a scratch database password"
docker run -d --name "$name" \
  -e POSTGRES_USER=o1 -e POSTGRES_PASSWORD="$password" \
  -e POSTGRES_DB=o1_restore \
  -p 127.0.0.1::5432 postgres:16-alpine >/dev/null ||
  fail "could not start the scratch Postgres"
bl_log info "scratch Postgres started: container=$name"

current_stage=ready
ready=0
for _ in {1..120}; do
  if docker exec "$name" pg_isready -h 127.0.0.1 -p 5432 -U o1 \
    -d o1_restore >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done
((ready)) || fail "the scratch Postgres did not become ready within 60s"

current_stage=restore
errlog=$(mktemp "$ROOT/.restore-err.XXXXXX") ||
  fail "could not create the restore log"
bl_log info "restoring snapshot $SNAPSHOT into the scratch database"
trap - ERR
set +e
restic dump "$SNAPSHOT" o1.dump 2>>"$errlog" |
  docker exec -i "$name" pg_restore -U o1 -d o1_restore \
    --no-owner --no-privileges 2>>"$errlog"
statuses=("${PIPESTATUS[@]}")
set -e
trap on_error ERR
if ((statuses[0] != 0 || statuses[1] != 0)); then
  fail "restore failed: restic exited ${statuses[0]}, pg_restore exited ${statuses[1]}"
fi

# scratch_psql SQL: run one query inside the scratch database.
scratch_psql() {
  docker exec -i "$name" psql -v ON_ERROR_STOP=1 -U o1 -d o1_restore \
    -tAc "$1"
}

current_stage=validate
goose_rows=$(scratch_psql 'SELECT count(*) FROM goose_db_version') ||
  fail "goose_db_version is missing or unreadable"
goose_rows=${goose_rows//[[:space:]]/}
[[ $goose_rows =~ ^[0-9]+$ ]] ||
  fail "unexpected goose_db_version row count: ${goose_rows:-<empty>}"
((goose_rows > 0)) || fail "goose_db_version has no rows"
bl_log info "restore validation: goose_db_version rows=$goose_rows"

for table in users workspaces monitors probe_results monitor_hourly_stats \
  incidents; do
  count=$(scratch_psql "SELECT count(*) FROM $table") ||
    fail "core table is missing or unreadable: $table"
  count=${count//[[:space:]]/}
  [[ $count =~ ^[0-9]+$ ]] ||
    fail "unexpected row count for $table: ${count:-<empty>}"
  bl_log info "restore validation: table $table count=$count"
done

current_stage="join"
scratch_psql 'SELECT 1 FROM monitors m JOIN workspaces w ON w.id = m.workspace_id LIMIT 1' \
  >/dev/null ||
  fail "the representative monitors/workspaces join failed"
bl_log info "restore validation: monitors/workspaces join ok"

current_stage=check
bl_log info "checking 5% of repository data"
restic check --read-data-subset=5% || fail "restic check failed"

current_stage=state
bl_write_state "$ROOT/last-restore-verify" \
  "$SNAPSHOT|$(bl_utc_now)|success" ||
  fail "could not write the restore drill report"

bl_log info "restore drill succeeded: snapshot=$SNAPSHOT"
exit 0
