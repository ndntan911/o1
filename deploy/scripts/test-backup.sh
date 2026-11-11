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
trap 'rm -rf "$tmp"' EXIT

FAKE_DIR="$tmp/fake"
mkdir -p "$FAKE_DIR"
export FAKE_DIR
export PATH="$FAKE_DIR:$PATH"

# --- fake commands ---------------------------------------------------------

cat >"$FAKE_DIR/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >>"$FAKE_DIR/curl.args"

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
        @*) cp "${arg#@}" "$FAKE_DIR/curl.body" ;;
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

chmod +x "$FAKE_DIR/curl"
[[ -e $FAKE_DIR/flock ]] && chmod +x "$FAKE_DIR/flock"

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

run_lib_tests

echo "PASS: all backup library tests ($checks checks)"
