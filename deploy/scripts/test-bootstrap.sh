#!/usr/bin/env bash
# Exercise bootstrap-host.sh without root, Docker, or a real host.
#
# The script under test runs from this checkout with a temporary --source tree
# and a temporary --root. A fake docker records compose validation and the GHCR
# login so the test can assert that the token never reaches the command line.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
bootstrap="$repo_root/deploy/scripts/bootstrap-host.sh"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/o1-bootstrap-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

FAKE_DIR="$tmp/fake"
mkdir -p "$FAKE_DIR"
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
  if [[ ${2:-} == version ]]; then
    exit 0
  fi
  printf 'compose-env O1_BACKEND_IMAGE=%s O1_WEB_IMAGE=%s\n' \
    "${O1_BACKEND_IMAGE:-}" "${O1_WEB_IMAGE:-}" >>"$FAKE_DIR/docker.log"
  exit "${FAKE_COMPOSE_EXIT:-0}"
fi

exit 0
FAKE_DOCKER

# macOS has no flock; the bootstrap only needs to find it on PATH.
cat >"$FAKE_DIR/flock" <<'FAKE_FLOCK'
#!/usr/bin/env bash
exit "${FAKE_FLOCK_EXIT:-0}"
FAKE_FLOCK

# The bootstrap only checks that curl exists; keep the test independent of the
# host and of minimal container images.
cat >"$FAKE_DIR/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
exit 0
FAKE_CURL

chmod +x "$FAKE_DIR/docker" "$FAKE_DIR/flock" "$FAKE_DIR/curl"

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

run_bootstrap() { # run_bootstrap ARGS...
  set +e
  bash "$bootstrap" "$@" >"$tmp/last.out" 2>&1
  STATUS=$?
  set -e
}

mode_of() { # mode_of PATH
  if [[ $(uname -s) == Darwin ]]; then
    stat -f %Lp "$1"
  else
    stat -c %a "$1"
  fi
}

# --- temporary source tree -------------------------------------------------

SRC="$tmp/src"
mkdir -p "$SRC/deploy/production" "$SRC/deploy/observability" "$SRC/deploy/scripts"
cp "$repo_root/deploy/production/compose.yaml" "$SRC/deploy/production/"
cp "$repo_root/deploy/production/env.example" "$SRC/deploy/production/"
cp "$repo_root/deploy/observability/alloy.alloy" "$SRC/deploy/observability/"
for name in deploy.sh rollback.sh healthcheck.sh validate-release.sh; do
  cp "$repo_root/deploy/scripts/$name" "$SRC/deploy/scripts/$name"
done

# --- 1. first run creates the host layout ---------------------------------

ROOT="$tmp/host"
run_bootstrap --root "$ROOT" --source "$SRC"
[[ $STATUS -eq 0 ]] || fail "first run exited $STATUS, expected 0"

for name in bin releases incoming state; do
  [[ -d "$ROOT/$name" ]] || fail "first run did not create $ROOT/$name"
  mode="$(mode_of "$ROOT/$name")"
  [[ $mode == 700 ]] || fail "$name has mode $mode, expected 700"
done
pass "first run creates bin, releases, incoming, state as 0700"

for name in deploy.sh rollback.sh healthcheck.sh validate-release.sh; do
  path="$ROOT/bin/$name"
  [[ -f $path ]] || fail "first run did not install $path"
  [[ -x $path ]] || fail "$path is not executable"
  mode="$(mode_of "$path")"
  [[ $mode == 755 ]] || fail "$name has mode $mode, expected 755"
  cmp -s "$path" "$SRC/deploy/scripts/$name" \
    || fail "installed $name differs from the source tree"
done
pass "first run installs the four scripts as 0755"

[[ -f "$ROOT/.env" ]] || fail "first run did not create $ROOT/.env"
mode="$(mode_of "$ROOT/.env")"
[[ $mode == 600 ]] || fail ".env has mode $mode, expected 600"
cmp -s "$ROOT/.env" "$SRC/deploy/production/env.example" \
  || fail ".env does not match env.example"
pass "first run creates .env as 0600 from env.example"

[[ ! -e "$ROOT/images.env" ]] || fail "bootstrap persisted an images.env"
if grep -q '^docker login' "$FAKE_DIR/docker.log"; then
  fail "bootstrap logged in to GHCR without credentials"
fi
pass "first run keeps image refs inline and skips optional login"

grep -q 'compose .*config --quiet' "$FAKE_DIR/docker.log" \
  || fail "bootstrap did not validate the compose file"
zeros="$(printf '0%.0s' {1..64})"
grep -q "compose-env O1_BACKEND_IMAGE=example.invalid/o1-backend@sha256:$zeros O1_WEB_IMAGE=example.invalid/o1-web@sha256:$zeros" \
  "$FAKE_DIR/docker.log" \
  || fail "compose validation did not use the inline placeholder image refs"
pass "compose validation uses inline 64-zero digest placeholders"

# --- 2. a second run preserves an edited .env ------------------------------

printf 'O1_DOMAIN=edited.example.com\n' >>"$ROOT/.env"
run_bootstrap --root "$ROOT" --source "$SRC"
[[ $STATUS -eq 0 ]] || fail "second run exited $STATUS, expected 0"
grep -qx 'O1_DOMAIN=edited.example.com' "$ROOT/.env" \
  || fail "second run overwrote the edited .env"
mode="$(mode_of "$ROOT/.env")"
[[ $mode == 600 ]] || fail "second run left .env at mode $mode, expected 600"
pass "second run keeps the edited .env and its mode"

# --- 3. missing or incomplete source creates nothing ----------------------

MISSING_ROOT="$tmp/host-missing-source"
run_bootstrap --root "$MISSING_ROOT" --source "$tmp/does-not-exist"
[[ $STATUS -ne 0 ]] || fail "missing source exited 0, expected nonzero"
[[ ! -e "$MISSING_ROOT" ]] || fail "missing source created $MISSING_ROOT"
pass "missing source fails without creating the root"

PARTIAL_SRC="$tmp/src-partial"
mkdir -p "$PARTIAL_SRC/deploy/production"
cp "$repo_root/deploy/production/compose.yaml" "$PARTIAL_SRC/deploy/production/"
PARTIAL_ROOT="$tmp/host-partial-source"
run_bootstrap --root "$PARTIAL_ROOT" --source "$PARTIAL_SRC"
[[ $STATUS -ne 0 ]] || fail "incomplete source exited 0, expected nonzero"
[[ ! -e "$PARTIAL_ROOT" ]] || fail "incomplete source created $PARTIAL_ROOT"
pass "incomplete source fails without creating the root"

# --- 4. GHCR login uses stdin, never argv ---------------------------------

GHCR_ROOT="$tmp/host-ghcr"
run_bootstrap --root "$GHCR_ROOT" --source "$SRC"
[[ $STATUS -eq 0 ]] || fail "GHCR setup run exited $STATUS, expected 0"
printf 'GHCR_USER=deploy-bot\nGHCR_READ_TOKEN=super-secret-token\n' >>"$GHCR_ROOT/.env"
rm -f "$FAKE_DIR/login.stdin"

run_bootstrap --root "$GHCR_ROOT" --source "$SRC"
[[ $STATUS -eq 0 ]] || fail "GHCR login run exited $STATUS, expected 0"
[[ -f "$FAKE_DIR/login.stdin" ]] || fail "docker login did not receive the token on stdin"
grep -q 'super-secret-token' "$FAKE_DIR/login.stdin" \
  || fail "token was not written to docker login stdin"
if grep -q 'super-secret-token' "$FAKE_DIR/docker.log"; then
  fail "token appeared in the docker command line"
fi
pass "GHCR token goes to docker login stdin and never to argv"

echo "PASS: bootstrap host tests"
