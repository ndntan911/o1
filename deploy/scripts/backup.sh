#!/usr/bin/env bash
# Daily encrypted logical backup, plus weekly repository prune mode.
#
# The dump never lands on disk: pg_dump writes a custom-format archive to
# stdout and restic reads it from stdin. A failed dump is detected through
# PIPESTATUS, and any snapshot restic captured from the partial stream is
# forgotten before the failure alert.
set -Eeuo pipefail
umask 077

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=backup-lib.sh disable=SC1091
source "$script_dir/backup-lib.sh"

BACKUP_ENV=${BACKUP_ENV:-/opt/o1/backup/backup.env}
ROOT=$(dirname -- "$BACKUP_ENV")

usage="usage: backup.sh [--prune-only]"
prune_only=0
case ${1:-} in
  "") ;;
  --prune-only) prune_only=1 ;;
  *) bl_die "$usage" ;;
esac
[[ $# -le 1 ]] || bl_die "$usage"

if ! bl_load_env "$BACKUP_ENV"; then
  bl_die "cannot load the backup environment: $BACKUP_ENV"
fi

bl_require_var RESTIC_REPOSITORY RESTIC_PASSWORD_FILE O1_COMPOSE_FILE \
  O1_ENV_FILE O1_BACKUP_PING_URL
if [[ -z ${O1_BACKUP_ALERT_EMAIL:-} && -z ${O1_BACKUP_FAILURE_URL:-} ]]; then
  bl_die "set O1_BACKUP_ALERT_EMAIL or O1_BACKUP_FAILURE_URL"
fi
bl_require_cmd docker restic flock curl jq timeout sed

redacted_repo=$(redact_repository "$RESTIC_REPOSITORY")
keep_daily=${O1_BACKUP_KEEP_DAILY:-30}
keep_monthly=${O1_BACKUP_KEEP_MONTHLY:-12}
backup_timeout=${O1_BACKUP_TIMEOUT:-2h}
min_free_kb=${O1_BACKUP_MIN_FREE_KB:-1048576}

current_stage=startup
json_file=
errlog=
lock_held=0

cleanup() {
  if [[ -n $json_file ]]; then
    rm -f -- "$json_file"
  fi
  if [[ -n $errlog ]]; then
    rm -f -- "$errlog"
  fi
  if ((lock_held)); then
    bl_release_lock
  fi
}
trap cleanup EXIT

# send_failure_alert STAGE MESSAGE: email the failure and ping the failure
# URL. It disables the ERR trap first so an alert failure cannot recurse.
send_failure_alert() {
  local stage=$1 message=$2 body
  trap - ERR
  set +e

  body=$(printf 'host=%s\njob=backup\ntime=%s\nstage=%s\n%s' \
    "$(hostname)" "$(bl_utc_now)" "$stage" "$message")
  if [[ -n $errlog && -s $errlog ]]; then
    body+=$'\n--- log tail ---\n'
    body+=$(tail -n 20 -- "$errlog" 2>/dev/null)
  fi

  # Ping first so a stalled email cannot delay the dead-man's switch.
  bl_ping "${O1_BACKUP_FAILURE_URL:-}" failure
  # bl_read_o1_env returns nonzero when the deployment env is incomplete; the
  # alert helpers then log "alert not configured".
  bl_read_o1_env || true
  bl_send_alert "o1 backup failed: $stage on $(hostname)" "$body" ||
    bl_log warn "the failure alert could not be sent"
}

on_error() {
  local status=$?
  local stage=$current_stage
  trap - ERR
  set +e
  bl_log error "backup failed during $stage"
  send_failure_alert "$stage" "unexpected failure (exit status $status)"
  exit "$status"
}
trap on_error ERR

current_stage=lock
if ! bl_take_lock "$ROOT/repo.lock"; then
  if ((prune_only)); then
    bl_log warn "prune skipped: another job holds $ROOT/repo.lock"
    exit 0
  fi
  bl_log warn "backup skipped: another job holds $ROOT/repo.lock"
  send_failure_alert lock "another backup, prune, or restore drill holds $ROOT/repo.lock"
  exit 1
fi
lock_held=1

if ((prune_only)); then
  current_stage=prune
  bl_log info "pruning snapshots (keep-daily=$keep_daily keep-monthly=$keep_monthly)"
  forget_output=$(restic forget --host o1-prod --tag o1-db \
    --keep-daily "$keep_daily" --keep-monthly "$keep_monthly" --prune 2>&1)
  while IFS= read -r line; do
    if [[ -n $line ]]; then
      bl_log info "retention: $line"
    fi
  done <<<"$forget_output"
  bl_log info "prune finished"
  exit 0
fi

current_stage=verify
if ! restic cat config >/dev/null 2>&1; then
  send_failure_alert verify \
    "cannot read the restic repository ($redacted_repo); check credentials or the repository URL"
  exit 1
fi

if [[ -n ${O1_BACKUP_PG_CONTAINER:-} ]]; then
  pg_exec=(docker exec "$O1_BACKUP_PG_CONTAINER")
else
  pg_exec=(docker compose --env-file "$O1_ENV_FILE" -f "$O1_COMPOSE_FILE" \
    exec -T postgres)
fi

current_stage=preflight
if ! "${pg_exec[@]}" pg_isready -U o1 -d o1 >/dev/null 2>&1; then
  send_failure_alert preflight \
    "Postgres is not ready; check the database container and connection settings"
  exit 1
fi

if ! db_kb=$("${pg_exec[@]}" psql -U o1 -d o1 -tAc \
  "SELECT pg_database_size('o1')/1024" 2>/dev/null); then
  send_failure_alert preflight "could not read the database size"
  exit 1
fi
db_kb=${db_kb//[[:space:]]/}
if [[ ! $db_kb =~ ^[0-9]+$ ]]; then
  send_failure_alert preflight "unexpected database size: ${db_kb:-<empty>}"
  exit 1
fi
bl_log info "database size: ${db_kb}KB"

[[ $min_free_kb =~ ^[0-9]+$ ]] ||
  bl_die "O1_BACKUP_MIN_FREE_KB is not an integer: $min_free_kb"
cache_dir=${RESTIC_CACHE_DIR:-$ROOT}
if ! free_kb=$(df -Pk "$cache_dir" | awk 'NR==2 {print $4}'); then
  send_failure_alert preflight "could not determine free disk for $cache_dir"
  exit 1
fi
free_kb=${free_kb//[[:space:]]/}
if [[ ! $free_kb =~ ^[0-9]+$ ]]; then
  send_failure_alert preflight "unexpected free disk value: ${free_kb:-<empty>}"
  exit 1
fi
if ((free_kb < min_free_kb)); then
  send_failure_alert preflight \
    "not enough free disk for the restic cache: free=${free_kb}KB required=${min_free_kb}KB"
  exit 1
fi
bl_log info "disk preflight passed: free=${free_kb}KB required=${min_free_kb}KB"

json_file=$(mktemp "$ROOT/.backup-json.XXXXXX")
errlog=$(mktemp "$ROOT/.backup-err.XXXXXX")

dump=("${pg_exec[@]}" pg_dump -U o1 -d o1 -Fc -Z 6)

restic_args=(backup --host o1-prod --stdin --stdin-filename o1.dump --tag o1-db
  --tag daily --json)
if [[ -n ${O1_BACKUP_UPLOAD_LIMIT:-} ]]; then
  restic_args+=(--limit-upload "$O1_BACKUP_UPLOAD_LIMIT")
fi

current_stage=dump
start=$SECONDS
bl_log info "dumping and uploading to $redacted_repo"

# The pipeline is expected to fail when pg_dump or restic does. `set +e` is
# not enough: an ERR trap still fires on a failing pipeline, so disarm it and
# inspect PIPESTATUS by hand.
trap - ERR
set +e
timeout "$backup_timeout" "${dump[@]}" 2>"$errlog" |
  timeout "$backup_timeout" restic "${restic_args[@]}" >>"$json_file"
statuses=("${PIPESTATUS[@]}")
set -e
trap on_error ERR
duration=$((SECONDS - start))

snapshot=""
if [[ -s $json_file ]]; then
  snapshot=$(jq -r 'select(.message_type == "summary") | .snapshot_id' \
    "$json_file" 2>/dev/null) || snapshot=""
  snapshot=${snapshot%%$'\n'*}
fi

if ((statuses[0] != 0 || statuses[1] != 0)); then
  # A failed dump usually makes restic fail too (it reads no data); report the
  # upstream stage so the alert points at the real cause.
  stage=upload
  if ((statuses[0] != 0)); then
    stage=dump
  fi
  if [[ -n $snapshot ]]; then
    bl_log warn "removing the partial snapshot $snapshot"
    restic forget "$snapshot" >/dev/null 2>&1 ||
      bl_log warn "could not remove the partial snapshot $snapshot"
  fi
  send_failure_alert "$stage" \
    "pg_dump exited ${statuses[0]}, restic exited ${statuses[1]}"
  exit 1
fi

if [[ -z $snapshot ]]; then
  send_failure_alert upload "restic did not report a snapshot ID"
  exit 1
fi
bl_log info "snapshot $snapshot uploaded in ${duration}s"

current_stage=retention
bl_log info "applying retention (keep-daily=$keep_daily keep-monthly=$keep_monthly)"
forget_output=$(restic forget --host o1-prod --tag o1-db \
  --keep-daily "$keep_daily" --keep-monthly "$keep_monthly" 2>&1)
while IFS= read -r line; do
  if [[ -n $line ]]; then
    bl_log info "retention: $line"
  fi
done <<<"$forget_output"

bytes=$(jq -r \
  'select(.message_type == "summary") | .total_bytes_processed // 0' \
  "$json_file") || bytes=0
bytes=${bytes:-0}

current_stage=state
bl_write_state "$ROOT/last-success" \
  "$snapshot|$(bl_utc_now)|$duration|$bytes"

current_stage=ping
bl_ping "$O1_BACKUP_PING_URL" success

bl_log info "backup succeeded: snapshot=$snapshot bytes=$bytes duration=${duration}s"
