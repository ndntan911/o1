#!/usr/bin/env bash
# Validate the production Compose bundle.
#
# The production file must reference registry images (never build from source on
# the host); the optional override re-adds local build contexts for a checkout.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

compose_file="deploy/production/compose.yaml"
build_file="deploy/production/compose.build.yaml"
env_file="deploy/production/env.example"
images_file="deploy/production/images.env.example"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

checks=0
check() {
  checks=$((checks + 1))
  echo "ok $checks - $*"
}

# Base file has no build contexts.
if grep -Eq '^[[:space:]]+build:' "$compose_file"; then
  fail "base compose file still contains a build: block"
fi
check "base compose file has no build contexts"

# Registry config uses the fixture digests and the default project name.
env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$env_file" \
  --env-file "$images_file" \
  -f "$compose_file" config --format json \
  | python3 -c '
import json, sys
c = json.load(sys.stdin)
assert c["name"] == "o1-production", c["name"]
assert c["services"]["api"]["image"].startswith("ghcr.io/example/o1-backend@sha256:")
assert c["services"]["scheduler"]["image"] == c["services"]["api"]["image"]
assert c["services"]["worker"]["image"] == c["services"]["api"]["image"]
assert c["services"]["web"]["image"].startswith("ghcr.io/example/o1-web@sha256:")
'
check "registry images come from the fixture and the default project name is used"

# The production stack has no bundled database and takes the DSN from the env.
env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$env_file" \
  --env-file "$images_file" \
  -f "$compose_file" config --format json \
  | python3 -c '
import json, sys
c = json.load(sys.stdin)
assert "postgres" not in c["services"], sorted(c["services"])
assert "postgres-data" not in c.get("volumes", {}), sorted(c.get("volumes", {}))
dsns = {n: c["services"][n]["environment"]["O1_DATABASE_URL"]
        for n in ("api", "scheduler", "worker")}
assert len(set(dsns.values())) == 1, dsns
dsn = next(iter(dsns.values()))
assert dsn.startswith("postgres://"), dsn
assert "sslmode=disable" not in dsn, dsn
assert c["services"]["api"]["environment"]["O1_DB_MAX_CONNS"] == "10"
'
check "no bundled database; DSN and pool size come from the environment"

# Rendering without a DSN fails before any container starts.
tmp_env=$(mktemp)
trap 'rm -f "$tmp_env"' EXIT
grep -v '^O1_DATABASE_URL=' "$env_file" > "$tmp_env"
if env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$tmp_env" --env-file "$images_file" \
  -f "$compose_file" config --quiet >/dev/null 2>&1; then
  fail "compose rendered without O1_DATABASE_URL"
fi
check "compose refuses to render without O1_DATABASE_URL"

# Project name is overridable.
O1_COMPOSE_PROJECT=o1-staging \
  env -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$env_file" \
  --env-file "$images_file" \
  -f "$compose_file" config --format json \
  | python3 -c 'import json,sys; assert json.load(sys.stdin)["name"] == "o1-staging"'
check "O1_COMPOSE_PROJECT overrides the project name"

# Build override adds contexts and keeps local image names.
env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$env_file" \
  -f "$compose_file" -f "$build_file" \
  config --format json \
  | python3 -c '
import json, sys
c = json.load(sys.stdin)
assert c["services"]["api"]["image"] == "o1-backend:local"
assert "context" in c["services"]["api"]["build"]
assert "context" in c["services"]["web"]["build"]
'
check "build override restores contexts and local image names"

echo "PASS: $checks compose config checks"
