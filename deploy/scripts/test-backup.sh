#!/usr/bin/env bash
# Exercise the backup helpers without network, Docker, or root.
#
# A fake curl records its argv, any --config file, and any --data-binary body
# so the tests can assert that secrets stay out of argv. macOS has no flock;
# a small test-only shim gives the lock tests the same semantics.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
lib="$repo_root/deploy/scripts/backup-lib.sh"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/o1-backup-test.XXXXXX")"
backup_test_container=
hold_pid=
cleanup_test_root() {
  if [[ -n $hold_pid ]]; then
    kill "$hold_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n $backup_test_container ]]; then
    docker rm -f "$backup_test_container" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup_test_root EXIT

FAKE_DIR="$tmp/fake"
mkdir -p "$FAKE_DIR"
export FAKE_DIR
export PATH="$FAKE_DIR:$PATH"

# --- fake commands ---------------------------------------------------------

cat >"$FAKE_DIR/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >>"$FAKE_DIR/curl.args"
printf '%s\n' "$*" >>"$FAKE_DIR/curl.all.args"

previous=
for arg in "$@"; do
  case $previous in
    --config)
      cp "$arg" "$FAKE_DIR/curl.config"
      if stat -c '%a' "$arg" >/dev/null 2>&1; then
        stat -c '%a' "$arg" >"$FAKE_DIR/curl.config.mode"
      else
        stat -f '%Lp' "$arg" >"$FAKE_DIR/curl.config.mode"
      fi
      ;;
    --data-binary)
      case $arg in
        @*)
          cp "${arg#@}" "$FAKE_DIR/curl.body"
          cat "${arg#@}" >>"$FAKE_DIR/curl.all.body"
          ;;
      esac
      ;;
  esac
  previous=$arg
done

exit "${FAKE_CURL_EXIT:-0}"
FAKE_CURL

# Linux has flock; the lock tests need real advisory locking on macOS too.
if ! command -v flock >/dev/null 2>&1; then
  cat >"$FAKE_DIR/flock" <<'FAKE_FLOCK'
#!/usr/bin/env bash
# Minimal flock(1) for hosts without util-linux: lock the inherited fd.
set -u
[[ ${1:-} == -n ]] || exit 2
exec perl -MFcntl=:flock -e '
  open(my $fh, ">&=$ARGV[0]") or exit 2;
  exit(flock($fh, LOCK_EX | LOCK_NB) ? 0 : 1);
' "${2:-9}"
FAKE_FLOCK
fi

# macOS also lacks GNU timeout; the tests do not need to exercise expiry.
if ! command -v timeout >/dev/null 2>&1; then
  cat >"$FAKE_DIR/timeout" <<'FAKE_TIMEOUT'
#!/usr/bin/env bash
# Minimal timeout(1) for hosts without coreutils: run without a deadline.
set -u
[[ $# -ge 2 ]] || exit 2
shift
exec "$@"
FAKE_TIMEOUT
fi

chmod +x "$FAKE_DIR/curl"
[[ -e $FAKE_DIR/flock ]] && chmod +x "$FAKE_DIR/flock"
[[ -e $FAKE_DIR/timeout ]] && chmod +x "$FAKE_DIR/timeout"

# Per-run files are reset between scenarios; the .all files accumulate every
# curl call so the final secret-containment check sees all of them.
: >"$FAKE_DIR/curl.all.args"
: >"$FAKE_DIR/curl.all.body"

# --- helpers ---------------------------------------------------------------

fail() {
  echo "FAIL: $*" >&2
  if [[ -f $tmp/last.out ]]; then
    sed 's/^/  /' "$tmp/last.out" >&2
  fi
  exit 1
}

checks=0
pass() {
  checks=$((checks + 1))
  echo "ok $checks - $*"
}

reset_curl() {
  : >"$FAKE_DIR/curl.args"
  rm -f "$FAKE_DIR/curl.config" "$FAKE_DIR/curl.config.mode" \
    "$FAKE_DIR/curl.body"
}

file_mode() {
  if stat -c '%a' "$1" >/dev/null 2>&1; then
    stat -c '%a' "$1"
  else
    stat -f '%Lp' "$1"
  fi
}

[[ -f $lib ]] || fail "$lib is missing"

# shellcheck disable=SC1090,SC1091  # the library is verified separately
source "$lib"

# --- library tests ---------------------------------------------------------

run_lib_tests() {
  # env parsing: quotes, comments, blank lines, no execution of $(...)
  local marker="$tmp/env-executed"
  cat >"$tmp/load.env" <<'ENV'
# a comment

BL_TEST_PLAIN=plain
BL_TEST_SINGLE='single value'
BL_TEST_DOUBLE="double value"
BL_TEST_HASH=value#kept
ENV
  printf 'BL_TEST_SPACED=   padded value   \n' >>"$tmp/load.env"
  # shellcheck disable=SC2016  # the literal $(...) must survive the write
  printf 'BL_TEST_EXEC=$(touch %s)\n' "$marker" >>"$tmp/load.env"

  bl_load_env "$tmp/load.env" || fail "bl_load_env rejected a valid file"
  [[ ${BL_TEST_PLAIN:-} == plain ]] || fail "plain value not parsed"
  [[ ${BL_TEST_SINGLE:-} == 'single value' ]] ||
    fail "single-quoted value not unwrapped"
  [[ ${BL_TEST_DOUBLE:-} == 'double value' ]] ||
    fail "double-quoted value not unwrapped"
  [[ ${BL_TEST_SPACED:-} == 'padded value' ]] ||
    fail "value whitespace not trimmed"
  [[ ${BL_TEST_HASH:-} == 'value#kept' ]] ||
    fail "value containing # was altered"
  [[ ${BL_TEST_EXEC:-} == "\$(touch $marker)" ]] ||
    fail "command substitution value was altered"
  [[ ! -e $marker ]] || fail "bl_load_env executed \$(...) in the env file"
  [[ $(bash -c 'printf %s "${BL_TEST_PLAIN:-}"') == plain ]] ||
    fail "bl_load_env did not export the variable"
  pass "bl_load_env parses quotes and comments without executing"

  if ( bl_load_env "$tmp/does-not-exist.env" ) >"$tmp/last.out" 2>&1; then
    fail "bl_load_env accepted a missing file"
  fi
  pass "bl_load_env rejects a missing file"

  # required var: missing -> nonzero and message names the variable
  if ( unset BL_TEST_MISSING; bl_require_var BL_TEST_MISSING ) \
    >"$tmp/last.out" 2>&1; then
    fail "bl_require_var accepted an empty variable"
  fi
  grep -q BL_TEST_MISSING "$tmp/last.out" ||
    fail "bl_require_var did not name the missing variable"
  BL_TEST_PRESENT=here bl_require_var BL_TEST_PRESENT ||
    fail "bl_require_var rejected a set variable"
  pass "bl_require_var checks required variables"

  # require cmd: missing -> nonzero
  if ( bl_require_cmd o1-command-that-does-not-exist ) \
    >"$tmp/last.out" 2>&1; then
    fail "bl_require_cmd accepted a missing command"
  fi
  bl_require_cmd bash || fail "bl_require_cmd rejected bash"
  pass "bl_require_cmd checks required commands"

  # logging: UTC timestamp to stderr
  bl_utc_now >"$tmp/utc"
  grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' \
    "$tmp/utc" || fail "bl_utc_now did not print a UTC timestamp"

  bl_log info "hello world" >"$tmp/log.out" 2>"$tmp/log.err"
  [[ ! -s $tmp/log.out ]] || fail "bl_log wrote to stdout"
  grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z' \
    "$tmp/log.err" || fail "bl_log did not prefix a UTC timestamp"
  grep -q 'hello world' "$tmp/log.err" || fail "bl_log lost the message"

  local die_status=0
  ( bl_die "fatal thing" ) 2>"$tmp/die.err" || die_status=$?
  [[ $die_status -eq 1 ]] || fail "bl_die did not exit 1"
  grep -q 'fatal thing' "$tmp/die.err" || fail "bl_die lost the message"
  pass "bl_log and bl_die write UTC-stamped stderr lines"

  # lock: second lock attempt returns 1; release makes it available again
  mkdir -p "$tmp/lock"
  local lock="$tmp/lock/repo.lock"
  bl_take_lock "$lock" || fail "first bl_take_lock failed"
  if ( bl_take_lock "$lock" ) >"$tmp/last.out" 2>&1; then
    bl_release_lock
    fail "bl_take_lock allowed a second holder"
  fi
  bl_release_lock
  bl_take_lock "$lock" || fail "bl_take_lock failed after release"
  bl_release_lock
  pass "bl_take_lock is exclusive and released by bl_release_lock"

  # state: written atomically; existing file replaced
  mkdir -p "$tmp/state"
  local state="$tmp/state/last-success"
  bl_write_state "$state" "snap-one|2026-11-11T00:00:00Z" ||
    fail "bl_write_state could not create the state file"
  [[ $(cat "$state") == "snap-one|2026-11-11T00:00:00Z" ]] ||
    fail "bl_write_state wrote unexpected content"
  [[ $(file_mode "$state") == 600 ]] ||
    fail "bl_write_state did not use mode 0600"
  bl_write_state "$state" "snap-two|2026-11-11T01:00:00Z" ||
    fail "bl_write_state could not replace the state file"
  [[ $(cat "$state") == "snap-two|2026-11-11T01:00:00Z" ]] ||
    fail "bl_write_state did not replace the old content"
  local leftover
  leftover="$(find "$tmp/state" -maxdepth 1 -type f ! -name last-success -print -quit)"
  [[ -z $leftover ]] || fail "bl_write_state left a temp file: $leftover"
  pass "bl_write_state replaces state files atomically"

  # bl_read_o1_env reads the deployment env without sourcing it
  local o1marker="$tmp/o1-executed"
  cat >"$tmp/o1.env" <<'O1ENV'
# deployment env
O1_RESEND_API_KEY=resend-from-file
O1_EMAIL_FROM='o1 <noreply@example.com>'
O1ENV
  # shellcheck disable=SC2016  # the literal $(...) must survive the write
  printf 'O1_EXEC_PROBE=$(touch %s)\n' "$o1marker" >>"$tmp/o1.env"
  export O1_ENV_FILE="$tmp/o1.env"
  bl_read_o1_env || fail "bl_read_o1_env rejected a valid env file"
  [[ $O1_RESEND_API_KEY == resend-from-file ]] ||
    fail "bl_read_o1_env did not read the Resend key"
  [[ $O1_EMAIL_FROM == 'o1 <noreply@example.com>' ]] ||
    fail "bl_read_o1_env did not read O1_EMAIL_FROM"
  [[ ! -e $o1marker ]] || fail "bl_read_o1_env executed \$(...) in the env file"
  pass "bl_read_o1_env reads keys without sourcing"

  # alert: temp curl config carries the Authorization header, body is JSON,
  # and the key never appears in argv
  export O1_BACKUP_ALERT_EMAIL="ops@example.com"
  O1_RESEND_API_KEY="super-secret"
  O1_EMAIL_FROM="o1 <noreply@example.com>"
  reset_curl
  bl_send_alert "backup failed" "database dump failed" ||
    fail "bl_send_alert failed with a complete configuration"
  [[ -f $FAKE_DIR/curl.config ]] || fail "bl_send_alert did not use --config"
  grep -q 'Authorization: Bearer super-secret' "$FAKE_DIR/curl.config" ||
    fail "curl config is missing the Authorization header"
  [[ $(cat "$FAKE_DIR/curl.config.mode") == 600 ]] ||
    fail "curl config was not mode 0600"
  grep -q 'https://api.resend.com/emails' "$FAKE_DIR/curl.args" ||
    fail "bl_send_alert did not call the Resend endpoint"
  if grep -q 'super-secret' "$FAKE_DIR/curl.args"; then
    fail "the Resend key leaked into curl argv"
  fi
  jq -e '
    .from == "o1 <noreply@example.com>" and
    .to == ["ops@example.com"] and
    .subject == "backup failed" and
    .text == "database dump failed"
  ' "$FAKE_DIR/curl.body" >/dev/null ||
    fail "the Resend JSON body is missing alert fields"
  local header_path
  header_path="$(awk '{for (i = 1; i < NF; i++) if ($i == "--config") print $(i + 1)}' \
    "$FAKE_DIR/curl.args")"
  [[ -n $header_path ]] || fail "no --config path was recorded"
  [[ ! -e $header_path ]] || fail "bl_send_alert left the temp header behind"
  pass "bl_send_alert sends Resend JSON without exposing the key"

  reset_curl
  export FAKE_CURL_EXIT=1
  if bl_send_alert "subject" "body" >"$tmp/last.out" 2>&1; then
    unset FAKE_CURL_EXIT
    fail "bl_send_alert ignored a curl failure"
  fi
  unset FAKE_CURL_EXIT
  header_path="$(awk '{for (i = 1; i < NF; i++) if ($i == "--config") print $(i + 1)}' \
    "$FAKE_DIR/curl.args")"
  [[ -n $header_path ]] || fail "failed alert did not use --config"
  [[ ! -e $header_path ]] || fail "failed alert left the temp header behind"
  pass "bl_send_alert cleans up when the email fails"

  reset_curl
  if ! ( unset O1_BACKUP_ALERT_EMAIL O1_RESEND_API_KEY O1_EMAIL_FROM
    bl_send_alert "subject" "body" ) >"$tmp/last.out" 2>&1; then
    fail "bl_send_alert failed when no alert target is configured"
  fi
  grep -q 'alert not configured' "$tmp/last.out" ||
    fail "bl_send_alert did not warn about missing alert configuration"
  [[ ! -s $FAKE_DIR/curl.args ]] ||
    fail "bl_send_alert called curl without an alert target"
  pass "bl_send_alert warns and returns when unconfigured"

  # ping: success URL vs failure URL selection; empty URL is a no-op
  export O1_BACKUP_PING_URL="https://ping.example/success"
  export O1_BACKUP_FAILURE_URL="https://ping.example/failure"

  reset_curl
  bl_ping success || fail "bl_ping success failed"
  grep -q 'https://ping.example/success' "$FAKE_DIR/curl.args" ||
    fail "bl_ping success did not use the success URL"
  if grep -q 'https://ping.example/failure' "$FAKE_DIR/curl.args"; then
    fail "bl_ping success used the failure URL"
  fi

  reset_curl
  bl_ping failure || fail "bl_ping failure failed"
  grep -q 'https://ping.example/failure' "$FAKE_DIR/curl.args" ||
    fail "bl_ping failure did not use the failure URL"

  reset_curl
  bl_ping "https://explicit.example/ping" success ||
    fail "bl_ping with an explicit URL failed"
  grep -q 'https://explicit.example/ping' "$FAKE_DIR/curl.args" ||
    fail "bl_ping ignored an explicit URL"
  pass "bl_ping selects the success and failure URLs"

  reset_curl
  O1_BACKUP_PING_URL=""
  bl_ping success || fail "bl_ping failed on an empty URL"
  [[ ! -s $FAKE_DIR/curl.args ]] || fail "bl_ping called curl for an empty URL"
  export O1_BACKUP_PING_URL="https://ping.example/success"
  pass "bl_ping is a no-op without a URL"

  reset_curl
  export FAKE_CURL_EXIT=1
  if ! bl_ping failure >"$tmp/last.out" 2>&1; then
    unset FAKE_CURL_EXIT
    fail "bl_ping did not treat a transport failure as log-only"
  fi
  unset FAKE_CURL_EXIT
  grep -q 'ping failed' "$tmp/last.out" ||
    fail "bl_ping did not log the failed ping"
  pass "bl_ping logs and returns 0 when the ping fails"
}

# --- backup.sh tests -------------------------------------------------------

backup_script=
TEST_RESTIC_PASSWORD=restic-secret-for-tests

run_backup_script() {
  local envfile=$1
  shift
  BACKUP_ENV="$envfile" bash "$backup_script" "$@"
}

repo_restic() {
  local root=$1
  shift
  RESTIC_REPOSITORY="$root/repo" \
    RESTIC_PASSWORD_FILE="$root/restic.pass" \
    RESTIC_CACHE_DIR="$root/cache" \
    restic "$@"
}

init_test_repo() {
  local root=$1
  mkdir -p "$root/cache"
  printf '%s\n' "$TEST_RESTIC_PASSWORD" >"$root/restic.pass"
  chmod 600 "$root/restic.pass"
  repo_restic "$root" init >/dev/null 2>&1 || fail "restic init failed for $root"
}

write_backup_env() {
  local file=$1 root=$2 pg_container=$3 daily=${4:-30} monthly=${5:-12}
  cat >"$file" <<ENV
RESTIC_REPOSITORY=$root/repo
RESTIC_PASSWORD_FILE=$root/restic.pass
RESTIC_CACHE_DIR=$root/cache
AWS_ACCESS_KEY_ID=aws-key-for-tests
AWS_SECRET_ACCESS_KEY=aws-secret-for-tests
O1_COMPOSE_FILE=$tmp/compose.yaml
O1_ENV_FILE=$tmp/backup-o1.env
O1_BACKUP_PING_URL=https://ping.example/success
O1_BACKUP_FAILURE_URL=https://ping.example/failure
O1_BACKUP_ALERT_EMAIL=ops@example.com
O1_BACKUP_TIMEOUT=2m
O1_BACKUP_KEEP_DAILY=$daily
O1_BACKUP_KEEP_MONTHLY=$monthly
O1_BACKUP_PG_CONTAINER=$pg_container
ENV
}

# --- restore drill helpers -------------------------------------------------

restore_script=

run_restore_script() {
  local envfile=$1
  shift
  BACKUP_ENV="$envfile" bash "$restore_script" "$@"
}

# Containers created by restore-verify.sh share this name prefix.
scratch_containers() {
  docker ps -a --filter 'name=o1-restore-verify-' --format '{{.Names}}' \
    2>/dev/null || true
}

# assert_no_new_scratch LABEL BEFORE: fail when a drill leaked a container.
assert_no_new_scratch() {
  local label=$1 before=$2 after
  after=$(scratch_containers)
  [[ $after == "$before" ]] ||
    fail "$label left a scratch container behind: ${after:-<none>}"
}

create_restore_snapshot() {
  local root=$1 container=$2
  docker exec "$container" pg_dump -U o1 -d o1 -Fc |
    repo_restic "$root" backup --stdin --stdin-filename o1.dump \
      --tag o1-db >/dev/null ||
    fail "could not create the restore fixture snapshot"
}

start_fixture_postgres() {
  local name="o1-backup-test-pg-$$-$RANDOM"
  docker run -d --rm --name "$name" \
    -e POSTGRES_USER=o1 -e POSTGRES_PASSWORD=o1 -e POSTGRES_DB=o1 \
    postgres:16-alpine >/dev/null || fail "could not start the fixture Postgres"
  backup_test_container=$name
  for _ in {1..60}; do
    if docker exec "$name" psql -U o1 -d o1 -c 'select 1' >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  fail "the fixture Postgres did not become ready"
}

create_fixture_schema() {
  local container=$1
  docker exec -i "$container" psql -v ON_ERROR_STOP=1 -U o1 -d o1 \
    >/dev/null 2>&1 <<'SQL' || fail "could not create the fixture schema"
CREATE TABLE goose_db_version (id serial primary key, version_id bigint not null);
INSERT INTO goose_db_version (version_id) VALUES (9);
CREATE TABLE users (id uuid primary key);
CREATE TABLE workspaces (id uuid primary key);
CREATE TABLE monitors (id uuid primary key, workspace_id uuid not null REFERENCES workspaces(id));
CREATE TABLE probe_results (id uuid, workspace_id uuid, checked_at timestamptz);
CREATE TABLE monitor_hourly_stats (monitor_id uuid, workspace_id uuid, bucket_start timestamptz);
CREATE TABLE incidents (id uuid primary key, workspace_id uuid);
INSERT INTO users VALUES (gen_random_uuid());
INSERT INTO workspaces VALUES ('11111111-1111-1111-1111-111111111111');
INSERT INTO monitors VALUES ('22222222-2222-2222-2222-222222222222', '11111111-1111-1111-1111-111111111111');
INSERT INTO probe_results VALUES (gen_random_uuid(), '11111111-1111-1111-1111-111111111111', now());
INSERT INTO incidents VALUES (gen_random_uuid(), '11111111-1111-1111-1111-111111111111');
SQL
}

days_ago() {
  if date -u -v-1d '+%Y' >/dev/null 2>&1; then
    date -u -v-"$1"d '+%Y-%m-%d %H:%M:%S'
  else
    date -u -d "$1 days ago" '+%Y-%m-%d %H:%M:%S'
  fi
}

# hold_lock PATH READY: hold an exclusive lock from a background perl process.
hold_lock() {
  perl -MFcntl=:flock -e '
    open(my $fh, ">", $ARGV[0]) or die "open: $!";
    flock($fh, LOCK_EX) or die "flock: $!";
    open(my $ready, ">", $ARGV[1]) or die "ready: $!";
    close($ready);
    sleep 60;
  ' "$1" "$2" &
  hold_pid=$!
  for _ in {1..100}; do
    [[ -e $2 ]] && return 0
    sleep 0.05
  done
  return 1
}

run_backup_tests() {
  local backup="$repo_root/deploy/scripts/backup.sh"
  local root status count snapid ts dur bytes id
  local old_time new_time old_id new_id ids secret

  [[ -f $backup ]] || fail "$backup is missing"
  backup_script=$backup

  # The library tests export alert settings; keep backup runs isolated.
  unset O1_BACKUP_ALERT_EMAIL O1_BACKUP_FAILURE_URL O1_BACKUP_PING_URL \
    O1_RESEND_API_KEY O1_EMAIL_FROM O1_ENV_FILE O1_BACKUP_PG_CONTAINER \
    O1_BACKUP_TIMEOUT O1_BACKUP_KEEP_DAILY O1_BACKUP_KEEP_MONTHLY \
    AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY

  cat >"$tmp/backup-o1.env" <<'O1ENV'
O1_RESEND_API_KEY=resend-secret-for-tests
O1_EMAIL_FROM=o1 <noreply@example.com>
O1ENV

  start_fixture_postgres
  create_fixture_schema "$backup_test_container"

  # 1. Success: one tagged snapshot, state file, no stray dump, success ping.
  root="$tmp/backup-success"
  mkdir -p "$root"
  init_test_repo "$root"
  write_backup_env "$root/backup.env" "$root" "$backup_test_container"
  reset_curl
  status=0
  run_backup_script "$root/backup.env" >"$tmp/last.out" 2>&1 || status=$?
  [[ $status -eq 0 ]] || fail "backup.sh failed for a healthy database"

  count=$(repo_restic "$root" snapshots --tag o1-db --json | jq 'length') ||
    fail "could not list snapshots"
  [[ $count -eq 1 ]] || fail "expected one o1-db snapshot, found $count"
  repo_restic "$root" snapshots --json |
    jq -e '.[0].tags | index("daily")' >/dev/null ||
    fail "the snapshot is not tagged daily"
  repo_restic "$root" ls latest >"$tmp/ls.out" 2>"$tmp/ls.err" ||
    fail "could not list the snapshot: $(cat "$tmp/ls.err")"
  grep -q 'o1.dump' "$tmp/ls.out" ||
    fail "the snapshot does not contain o1.dump"

  [[ -f $root/last-success ]] || fail "backup.sh did not write last-success"
  IFS='|' read -r snapid ts dur bytes <"$root/last-success"
  [[ -n $snapid && -n $ts && -n $dur && -n $bytes ]] ||
    fail "last-success does not have four fields: $(cat "$root/last-success")"
  [[ $ts =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
    fail "last-success timestamp is not UTC: $ts"
  [[ $dur =~ ^[0-9]+$ ]] || fail "last-success duration is not seconds: $dur"
  if [[ ! $bytes =~ ^[0-9]+$ ]] || ((bytes <= 0)); then
    fail "last-success byte count is not positive: $bytes"
  fi
  id=$(repo_restic "$root" snapshots --tag o1-db --json | jq -r '.[0].short_id')
  [[ ${snapid:0:8} == "$id" ]] ||
    fail "last-success snapshot $snapid does not match $id"
  [[ $(file_mode "$root/last-success") == 600 ]] ||
    fail "last-success is not mode 0600"
  [[ -z $(find "$root" -name '*.dump' -print -quit) ]] ||
    fail "a *.dump file was left under the test root"

  grep -q 'https://ping.example/success' "$FAKE_DIR/curl.args" ||
    fail "the successful backup did not ping the success URL"
  if grep -q 'https://ping.example/failure' "$FAKE_DIR/curl.args"; then
    fail "the successful backup pinged the failure URL"
  fi
  if grep -q 'api.resend.com' "$FAKE_DIR/curl.args"; then
    fail "the successful backup sent a failure alert"
  fi
  pass "backup.sh uploads a tagged snapshot and records last-success"

  # 2. pg_dump failure: partial snapshot removed, alert and failure ping sent.
  root="$tmp/backup-dump-failure"
  mkdir -p "$root"
  init_test_repo "$root"
  write_backup_env "$root/backup.env" "$root" "o1-backup-test-missing-$$"
  reset_curl
  status=0
  run_backup_script "$root/backup.env" >"$tmp/last.out" 2>&1 || status=$?
  [[ $status -ne 0 ]] || fail "backup.sh succeeded with a missing container"
  count=$(repo_restic "$root" snapshots --tag o1-db --json | jq 'length') ||
    fail "could not list snapshots after the dump failure"
  [[ $count -eq 0 ]] || fail "the partial snapshot was not removed"
  [[ ! -e $root/last-success ]] ||
    fail "backup.sh wrote last-success after a dump failure"
  grep -q 'api.resend.com' "$FAKE_DIR/curl.args" ||
    fail "the dump failure did not send an alert"
  [[ $(grep -c 'api.resend.com' "$FAKE_DIR/curl.args") -eq 1 ]] ||
    fail "the dump failure sent more than one alert"
  grep -q 'stage=dump' "$FAKE_DIR/curl.body" ||
    fail "the alert does not name the dump stage: $(cat "$FAKE_DIR/curl.body" 2>/dev/null)"
  grep -q 'https://ping.example/failure' "$FAKE_DIR/curl.args" ||
    fail "the dump failure did not ping the failure URL"
  pass "backup.sh removes a partial snapshot and alerts on dump failure"

  # 3. Lock held: backup fails and alerts, prune-only skips quietly.
  root="$tmp/backup-lock"
  mkdir -p "$root"
  init_test_repo "$root"
  write_backup_env "$root/backup.env" "$root" "$backup_test_container"
  hold_lock "$root/repo.lock" "$root/lock.ready" ||
    fail "could not hold the repository lock"
  reset_curl
  status=0
  run_backup_script "$root/backup.env" >"$tmp/last.out" 2>&1 || status=$?
  [[ $status -ne 0 ]] || fail "backup.sh ignored a held lock"
  grep -q 'api.resend.com' "$FAKE_DIR/curl.args" ||
    fail "the lock-held backup did not alert"
  grep -q 'lock' "$FAKE_DIR/curl.body" ||
    fail "the lock-held alert does not mention the lock"
  count=$(repo_restic "$root" snapshots --tag o1-db --json | jq 'length') ||
    fail "could not list snapshots after the lock-held run"
  [[ $count -eq 0 ]] || fail "the lock-held backup created a snapshot"

  reset_curl
  status=0
  run_backup_script "$root/backup.env" --prune-only >"$tmp/last.out" 2>&1 ||
    status=$?
  [[ $status -eq 0 ]] || fail "prune-only did not exit 0 when the lock was held"
  [[ ! -s $FAKE_DIR/curl.args ]] ||
    fail "a skipped prune-only run sent an alert or ping"

  kill "$hold_pid" 2>/dev/null || true
  wait "$hold_pid" 2>/dev/null || true
  hold_pid=
  pass "backup.sh alerts on a held lock and prune-only skips quietly"

  # 4. Prune-only: a snapshot older than 30 days is removed, the new one stays.
  #    Restic counts periods that contain snapshots, not calendar days, so pin
  #    both policies to one: a two-snapshot fixture then exercises pruning.
  root="$tmp/backup-prune"
  mkdir -p "$root"
  init_test_repo "$root"
  old_time=$(days_ago 40)
  new_time=$(date -u '+%Y-%m-%d %H:%M:%S')
  printf 'old' | repo_restic "$root" backup --stdin --stdin-filename o1.dump \
    --tag o1-db --time "$old_time" >/dev/null || fail "could not write the old snapshot"
  printf 'new' | repo_restic "$root" backup --stdin --stdin-filename o1.dump \
    --tag o1-db --time "$new_time" >/dev/null || fail "could not write the new snapshot"
  old_id=$(repo_restic "$root" snapshots --json |
    jq -r 'sort_by(.time) | .[0].short_id')
  new_id=$(repo_restic "$root" snapshots --json |
    jq -r 'sort_by(.time) | .[-1].short_id')
  write_backup_env "$root/backup.env" "$root" "$backup_test_container" 1 1
  reset_curl
  status=0
  run_backup_script "$root/backup.env" --prune-only >"$tmp/last.out" 2>&1 ||
    status=$?
  [[ $status -eq 0 ]] || fail "prune-only failed"
  ids=$(repo_restic "$root" snapshots --json | jq -r '.[].short_id')
  grep -qx "$new_id" <<<"$ids" || fail "prune-only removed the new snapshot"
  if grep -qx "$old_id" <<<"$ids"; then
    fail "prune-only kept the snapshot older than 30 days"
  fi
  [[ ! -e $root/last-success ]] ||
    fail "prune-only wrote last-success"
  [[ ! -s $FAKE_DIR/curl.args ]] || fail "prune-only sent a ping"
  pass "prune-only forgets old snapshots and keeps the latest"

  # 5. Secret containment: no secret reaches curl argv or the alert body in
  #    any call made by any scenario above.
  [[ -s $FAKE_DIR/curl.all.args ]] ||
    fail "no curl calls were recorded for the secret check"
  [[ -s $FAKE_DIR/curl.all.body ]] ||
    fail "no alert bodies were recorded for the secret check"
  for secret in "$TEST_RESTIC_PASSWORD" aws-secret-for-tests \
    resend-secret-for-tests; do
    if grep -qF -- "$secret" "$FAKE_DIR/curl.all.args"; then
      fail "a secret leaked into curl argv"
    fi
    if grep -qF -- "$secret" "$FAKE_DIR/curl.all.body"; then
      fail "a secret leaked into the alert body"
    fi
  done
  if grep -qF 'AWS_SECRET_ACCESS_KEY' "$FAKE_DIR/curl.all.args"; then
    fail "the AWS secret variable name reached curl argv"
  fi
  pass "backup runs keep repository and notification secrets out of argv"
}

# --- restore-verify.sh tests ----------------------------------------------

run_restore_tests() {
  local restore="$repo_root/deploy/scripts/restore-verify.sh"
  local root status before snapid ts rstatus sid table log

  [[ -f $restore ]] || fail "$restore is missing"
  restore_script=$restore
  [[ -n $backup_test_container ]] ||
    fail "restore tests need the fixture Postgres started by the backup tests"

  # 1. Success: restore the snapshot, validate it, record the report, and
  #    remove the scratch container.
  root="$tmp/restore-success"
  mkdir -p "$root"
  init_test_repo "$root"
  create_restore_snapshot "$root" "$backup_test_container"
  write_backup_env "$root/restore.env" "$root" "$backup_test_container"
  printf 'O1_BACKUP_DRILL_MIN_FREE_KB=1\n' >>"$root/restore.env"
  log="$tmp/restore-success.log"
  reset_curl
  before=$(scratch_containers)
  status=0
  run_restore_script "$root/restore.env" >"$log" 2>&1 || status=$?
  cp "$log" "$tmp/last.out"
  [[ $status -eq 0 ]] || fail "restore-verify.sh failed for a good snapshot"
  assert_no_new_scratch "the successful restore drill" "$before"

  [[ -f $root/last-restore-verify ]] ||
    fail "restore-verify.sh did not write last-restore-verify"
  IFS='|' read -r snapid ts rstatus <"$root/last-restore-verify"
  [[ -n $snapid && -n $ts && $rstatus == success ]] ||
    fail "last-restore-verify is malformed: $(cat "$root/last-restore-verify")"
  [[ $ts =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
    fail "last-restore-verify timestamp is not UTC: $ts"
  [[ $(file_mode "$root/last-restore-verify") == 600 ]] ||
    fail "last-restore-verify is not mode 0600"
  sid=$(repo_restic "$root" snapshots --tag o1-db --json | jq -r '.[0].short_id')
  [[ ${snapid:0:8} == "$sid" ]] ||
    fail "last-restore-verify snapshot $snapid does not match $sid"
  if grep -q 'api.resend.com' "$FAKE_DIR/curl.args"; then
    fail "the successful restore drill sent an alert"
  fi
  pass "restore-verify.sh restores a snapshot and records last-restore-verify"

  # 2. Missing snapshot: nonzero, an alert naming the stage, no report, and no
  #    scratch container.
  root="$tmp/restore-missing"
  mkdir -p "$root"
  init_test_repo "$root"
  write_backup_env "$root/restore.env" "$root" "$backup_test_container"
  printf 'O1_BACKUP_DRILL_MIN_FREE_KB=1\n' >>"$root/restore.env"
  reset_curl
  before=$(scratch_containers)
  status=0
  run_restore_script "$root/restore.env" deadbeef >"$tmp/last.out" 2>&1 ||
    status=$?
  [[ $status -ne 0 ]] || fail "restore-verify.sh accepted a missing snapshot"
  grep -q 'api.resend.com' "$FAKE_DIR/curl.args" ||
    fail "the missing snapshot did not send an alert"
  grep -q 'snapshot' "$FAKE_DIR/curl.body" ||
    fail "the alert does not name the snapshot stage"
  [[ ! -e $root/last-restore-verify ]] ||
    fail "a failed drill wrote last-restore-verify"
  assert_no_new_scratch "the missing snapshot drill" "$before"
  pass "restore-verify.sh alerts and stops for a missing snapshot"

  # 3. Disk preflight: a huge minimum aborts before creating a container.
  root="$tmp/restore-disk"
  mkdir -p "$root"
  init_test_repo "$root"
  create_restore_snapshot "$root" "$backup_test_container"
  write_backup_env "$root/restore.env" "$root" "$backup_test_container"
  printf 'O1_BACKUP_DRILL_MIN_FREE_KB=999999999999999\n' >>"$root/restore.env"
  reset_curl
  before=$(scratch_containers)
  status=0
  run_restore_script "$root/restore.env" >"$tmp/last.out" 2>&1 || status=$?
  [[ $status -ne 0 ]] || fail "restore-verify.sh ignored the disk preflight"
  grep -q 'free disk' "$tmp/last.out" ||
    fail "the disk failure does not mention free disk"
  assert_no_new_scratch "the disk preflight" "$before"
  grep -q 'api.resend.com' "$FAKE_DIR/curl.args" ||
    fail "the disk preflight did not send an alert"
  [[ ! -e $root/last-restore-verify ]] ||
    fail "the disk preflight wrote last-restore-verify"
  pass "restore-verify.sh aborts before Docker when free disk is too low"

  # 4. The validation output names goose_db_version and every core table count.
  grep -q 'goose_db_version' "$log" ||
    fail "the drill did not log goose_db_version"
  for table in users workspaces monitors probe_results monitor_hourly_stats \
    incidents; do
    grep -q "table $table count=" "$log" ||
      fail "the drill did not log the $table count"
  done
  grep -q 'monitors/workspaces join' "$log" ||
    fail "the drill did not log the monitors/workspaces join"
  pass "the drill logs goose_db_version, core table counts, and the join"

  # 5. Lock held: the drill skips with exit 0 and stays quiet.
  root="$tmp/restore-lock"
  mkdir -p "$root"
  init_test_repo "$root"
  create_restore_snapshot "$root" "$backup_test_container"
  write_backup_env "$root/restore.env" "$root" "$backup_test_container"
  printf 'O1_BACKUP_DRILL_MIN_FREE_KB=1\n' >>"$root/restore.env"
  hold_lock "$root/repo.lock" "$root/lock.ready" ||
    fail "could not hold the restore repository lock"
  reset_curl
  before=$(scratch_containers)
  status=0
  run_restore_script "$root/restore.env" >"$tmp/last.out" 2>&1 || status=$?
  [[ $status -eq 0 ]] || fail "a lock-held restore drill did not exit 0"
  grep -q 'skipped' "$tmp/last.out" ||
    fail "the lock-held drill did not log a skip"
  [[ ! -s $FAKE_DIR/curl.args ]] ||
    fail "the lock-held drill sent an alert"
  assert_no_new_scratch "the lock-held drill" "$before"
  [[ ! -e $root/last-restore-verify ]] ||
    fail "the lock-held drill wrote last-restore-verify"
  kill "$hold_pid" 2>/dev/null || true
  wait "$hold_pid" 2>/dev/null || true
  hold_pid=
  pass "restore-verify.sh skips quietly while another job holds the lock"
}

run_lib_tests
run_backup_tests
run_restore_tests

echo "PASS: all backup tests ($checks checks)"
