#!/usr/bin/env bash
# Exercise deploy.sh, rollback.sh, validate-release.sh, and healthcheck.sh
# without Docker, a registry, or root. The scripts under test run from this
# checkout with a temporary O1_ROOT and fake docker/curl/flock on PATH.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/o1-deploy-test.XXXXXX")"
ROOT="$tmp/root"
FAKE_DIR="$tmp/fake"
mkdir -p "$ROOT/state" "$ROOT/releases" "$ROOT/incoming" "$FAKE_DIR"
trap 'rm -rf "$tmp"' EXIT

export O1_ROOT="$ROOT"
export FAKE_DIR
export PATH="$FAKE_DIR:$PATH"

# --- fake commands ---------------------------------------------------------

cat >"$FAKE_DIR/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -u
printf 'docker %s\n' "$*" >>"$FAKE_DIR/docker.log"

if [[ ${1:-} == login ]]; then
  cat >"$FAKE_DIR/login.stdin"
  exit "${FAKE_LOGIN_EXIT:-0}"
fi

if [[ ${1:-} == compose ]]; then
  for arg in "$@"; do
    case $arg in
      pull) exit "${FAKE_PULL_EXIT:-0}" ;;
      up)
        if [[ -n ${FAKE_UP_EXIT:-} && ! -e "$FAKE_DIR/up.failed" ]]; then
          : >"$FAKE_DIR/up.failed"
          exit "$FAKE_UP_EXIT"
        fi
        exit 0
        ;;
      config|ps|logs|down) exit 0 ;;
    esac
  done
fi

exit 0
FAKE_DOCKER

cat >"$FAKE_DIR/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -u
url=
for arg in "$@"; do
  case $arg in
    http://*|https://*) url=$arg ;;
  esac
done
printf '%s\n' "$url" >>"$FAKE_DIR/curl.log"
exit "${FAKE_CURL_EXIT:-0}"
FAKE_CURL

cat >"$FAKE_DIR/flock" <<'FAKE_FLOCK'
#!/usr/bin/env bash
exit "${FAKE_FLOCK_EXIT:-0}"
FAKE_FLOCK

chmod +x "$FAKE_DIR/docker" "$FAKE_DIR/curl" "$FAKE_DIR/flock"

# --- helpers ---------------------------------------------------------------

fail() {
  echo "FAIL: $*" >&2
  if [[ -f "$tmp/last.out" ]]; then
    sed 's/^/  /' "$tmp/last.out" >&2
  fi
  exit 1
}

pass() {
  echo "PASS: $*"
}

expect_status() { # expect_status EXPECTED LABEL COMMAND...
  local expected=$1 label=$2 got
  shift 2
  set +e
  "$@" >"$tmp/last.out" 2>&1
  got=$?
  set -e
  [[ $got -eq $expected ]] || fail "$label: exit $got, expected $expected"
  pass "$label"
}

run_script() { # run_script COMMAND...
  set +e
  "$@" >"$tmp/last.out" 2>&1
  STATUS=$?
  set -e
}

assert_link() { # assert_link LINKPATH EXPECTED_TARGET
  local actual
  actual="$(readlink "$1" 2>/dev/null || true)"
  [[ $actual == "$2" ]] || fail "$1 -> '${actual}', expected '$2'"
}

assert_no_link() { # assert_no_link LINKPATH
  [[ ! -L "$1" ]] || fail "$1 exists but should be absent"
}

reset_root() {
  rm -rf "$ROOT/releases" "$ROOT/incoming"
  rm -f "$ROOT/current" "$ROOT/previous" "$ROOT/current.new" "$ROOT/previous.new"
  rm -f "$FAKE_DIR/docker.log" "$FAKE_DIR/curl.log" "$FAKE_DIR/login.stdin" "$FAKE_DIR/up.failed"
  mkdir -p "$ROOT/releases" "$ROOT/incoming"
  unset FAKE_CURL_EXIT FAKE_PULL_EXIT FAKE_UP_EXIT FAKE_LOGIN_EXIT FAKE_FLOCK_EXIT DEPLOY_HEALTH_TIMEOUT
}

HEX64="$(printf 'a%.0s' {1..64})"
seed_release() { # seed_release NAME
  local dir="$ROOT/releases/$1"
  mkdir -p "$dir/deploy/production" "$dir/deploy/observability"
  cp deploy/production/compose.yaml "$dir/deploy/production/"
  cp deploy/observability/alloy.alloy "$dir/deploy/observability/"
  printf 'O1_BACKEND_IMAGE=ghcr.io/example/o1-backend@sha256:%s\nO1_WEB_IMAGE=ghcr.io/example/o1-web@sha256:%s\n' \
    "$HEX64" "$HEX64" >"$dir/images.env"
}

seed_incoming() { # seed_incoming NAME
  local dir="$ROOT/incoming/$1"
  mkdir -p "$dir/deploy/production" "$dir/deploy/observability"
  cp deploy/production/compose.yaml "$dir/deploy/production/"
  cp deploy/observability/alloy.alloy "$dir/deploy/observability/"
  printf 'O1_BACKEND_IMAGE=ghcr.io/example/o1-backend@sha256:%s\nO1_WEB_IMAGE=ghcr.io/example/o1-web@sha256:%s\n' \
    "$HEX64" "$HEX64" >"$dir/images.env"
}

mkdir -p "$ROOT/state"
printf 'O1_DOMAIN=o1.example.com\n' >"$ROOT/.env"
chmod 600 "$ROOT/.env"

# --- 1. release validation -------------------------------------------------

expect_status 0 "validate: production final tag on main allowed" \
  bash deploy/scripts/validate-release.sh production v1.2.3 refs/heads/main
expect_status 3 "validate: production rejects pre-release" \
  bash deploy/scripts/validate-release.sh production v1.2.3-rc.1 refs/heads/main
expect_status 4 "validate: production rejects non-main ref" \
  bash deploy/scripts/validate-release.sh production v1.2.3 refs/heads/feature
expect_status 0 "validate: staging allows pre-release" \
  bash deploy/scripts/validate-release.sh staging v1.2.3-rc.1 refs/heads/main
expect_status 2 "validate: unknown environment rejected" \
  bash deploy/scripts/validate-release.sh bogus v1.2.3 refs/heads/main

# --- 2. successful deploy --------------------------------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-new
export DEPLOY_HEALTH_TIMEOUT=0
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-new"
[[ $STATUS -eq 0 ]] || fail "deploy: success exit $STATUS, expected 0"
assert_link "$ROOT/current" releases/release-new
assert_link "$ROOT/previous" releases/old
grep -q 'https://o1.example.com/readyz' "$FAKE_DIR/curl.log" \
  || fail "deploy: health URL missing from curl.log"
pass "deploy: success switches current and previous"

# --- 3. unhealthy deploy rolls back ---------------------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-new
export DEPLOY_HEALTH_TIMEOUT=0
export FAKE_CURL_EXIT=1
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-new"
[[ $STATUS -eq 1 ]] || fail "deploy: unhealthy exit $STATUS, expected 1"
assert_link "$ROOT/current" releases/old
ups="$(grep -c ' compose .* up ' "$FAKE_DIR/docker.log" || true)"
[[ $ups -eq 2 ]] || fail "deploy: expected two compose up calls, saw $ups"
pass "deploy: unhealthy release restores current and re-ups previous"

# --- 4. invalid images fail before pull -----------------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-bad
printf 'O1_BACKEND_IMAGE=ghcr.io/example/o1-backend@sha256:deadbeef\nO1_WEB_IMAGE=ghcr.io/example/o1-web@sha256:%s\n' \
  "$HEX64" >"$ROOT/incoming/release-bad/images.env"
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-bad"
[[ $STATUS -eq 1 ]] || fail "deploy: bad digest exit $STATUS, expected 1"
assert_link "$ROOT/current" releases/old
[[ ! -e "$ROOT/releases/release-bad" ]] || fail "deploy: bad release was activated"
if [[ -f "$FAKE_DIR/docker.log" ]] && grep -q ' compose .*pull' "$FAKE_DIR/docker.log"; then
  fail "deploy: pulled images before rejecting a bad digest"
fi
pass "deploy: bad digest rejected before pull and current unchanged"

# --- 5. first deploy failure leaves the release active --------------------

reset_root
seed_incoming release-first
export DEPLOY_HEALTH_TIMEOUT=0
export FAKE_CURL_EXIT=1
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-first"
[[ $STATUS -eq 1 ]] || fail "deploy: first unhealthy exit $STATUS, expected 1"
assert_link "$ROOT/current" releases/release-first
assert_no_link "$ROOT/previous"
pass "deploy: first deploy failure keeps the new release with no previous"

# --- 6. dry run mutates nothing -------------------------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-new
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-new" --dry-run
[[ $STATUS -eq 0 ]] || fail "deploy: dry-run exit $STATUS, expected 0"
assert_link "$ROOT/current" releases/old
[[ ! -e "$ROOT/releases/release-new" ]] || fail "deploy: dry run activated the release"
[[ -d "$ROOT/incoming/release-new" ]] || fail "deploy: dry run consumed the incoming bundle"
pass "deploy: dry run leaves files and links unchanged"

# --- 7. locked host --------------------------------------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-new
export FAKE_FLOCK_EXIT=1
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-new"
[[ $STATUS -eq 3 ]] || fail "deploy: lock contention exit $STATUS, expected 3"
assert_link "$ROOT/current" releases/old
[[ ! -e "$ROOT/releases/release-new" ]] || fail "deploy: lock contention activated the release"
pass "deploy: lock contention exits 3 without touching state"

# --- 8. pull failure uses the recovery path -------------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-new
export DEPLOY_HEALTH_TIMEOUT=0
export FAKE_PULL_EXIT=1
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-new"
[[ $STATUS -eq 1 ]] || fail "deploy: pull failure exit $STATUS, expected 1"
assert_link "$ROOT/current" releases/old
grep -q 'releases/old/.* up ' "$FAKE_DIR/docker.log" \
  || fail "deploy: pull failure did not bring the previous release up"
pass "deploy: pull failure restores and re-ups the previous release"

# --- 9. compose up failure uses the recovery path -------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-new
export DEPLOY_HEALTH_TIMEOUT=0
export FAKE_UP_EXIT=1
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-new"
[[ $STATUS -eq 1 ]] || fail "deploy: up failure exit $STATUS, expected 1"
assert_link "$ROOT/current" releases/old
grep -q 'releases/old/.* up ' "$FAKE_DIR/docker.log" \
  || fail "deploy: up failure did not bring the previous release up"
ups="$(grep -c ' compose .* up ' "$FAKE_DIR/docker.log" || true)"
[[ $ups -eq 2 ]] || fail "deploy: up failure expected two compose up calls, saw $ups"
pass "deploy: compose up failure restores and re-ups the previous release"

# --- 10. GHCR login failure uses the recovery path ------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-new
printf 'O1_DOMAIN=o1.example.com\nGHCR_USER=deploy-bot\nGHCR_READ_TOKEN=super-secret\n' >"$ROOT/.env"
chmod 600 "$ROOT/.env"
export DEPLOY_HEALTH_TIMEOUT=0
export FAKE_LOGIN_EXIT=1
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-new"
[[ $STATUS -eq 1 ]] || fail "deploy: login failure exit $STATUS, expected 1"
assert_link "$ROOT/current" releases/old
grep -q 'releases/old/.* up ' "$FAKE_DIR/docker.log" \
  || fail "deploy: login failure did not bring the previous release up"
pass "deploy: login failure restores and re-ups the previous release"

# --- 11. rollback ----------------------------------------------------------

reset_root
seed_release old
seed_release new
ln -s releases/new "$ROOT/current"
ln -s releases/old "$ROOT/previous"
export DEPLOY_HEALTH_TIMEOUT=0
run_script bash deploy/scripts/rollback.sh
[[ $STATUS -eq 0 ]] || fail "rollback: exit $STATUS, expected 0"
assert_link "$ROOT/current" releases/old
assert_link "$ROOT/previous" releases/new
pass "rollback: swaps current and previous"

# --- 12. credential hygiene -------------------------------------------------

reset_root
seed_release old
ln -s releases/old "$ROOT/current"
seed_incoming release-new
printf 'O1_DOMAIN=o1.example.com\nGHCR_USER=deploy-bot\nGHCR_READ_TOKEN=super-secret\n' >"$ROOT/.env"
chmod 600 "$ROOT/.env"
export DEPLOY_HEALTH_TIMEOUT=0
run_script bash deploy/scripts/deploy.sh "$ROOT/incoming/release-new"
[[ $STATUS -eq 0 ]] || fail "deploy: login case exit $STATUS, expected 0"
grep -q 'super-secret' "$FAKE_DIR/login.stdin" || fail "deploy: token was not sent to docker login"
if grep -q 'super-secret' "$FAKE_DIR/docker.log"; then
  fail "deploy: token appeared in docker arguments"
fi
pass "deploy: token goes to login stdin and never to the command line"

echo "PASS: deploy script tests"
