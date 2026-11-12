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

# SMTP deployments do not need a Resend key.
smtp_env=$(mktemp)
grep -v '^O1_RESEND_API_KEY=' "$env_file" > "$smtp_env"
env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  O1_EMAIL_PROVIDER=smtp O1_SMTP_ADDR=smtp.example.net:587 \
  O1_SMTP_USERNAME=user O1_SMTP_PASSWORD=pass O1_SMTP_TLS=starttls \
  docker compose --env-file "$smtp_env" --env-file "$images_file" \
  -f "$compose_file" config --format json \
  | python3 -c '
import json, sys
env = json.load(sys.stdin)["services"]["api"]["environment"]
assert env["O1_EMAIL_PROVIDER"] == "smtp", env["O1_EMAIL_PROVIDER"]
assert env["O1_SMTP_ADDR"] == "smtp.example.net:587", env["O1_SMTP_ADDR"]
assert env["O1_SMTP_TLS"] == "starttls", env["O1_SMTP_TLS"]
assert env["O1_RESEND_API_KEY"] == "", env["O1_RESEND_API_KEY"]
'
rm -f "$smtp_env"
check "smtp email settings render without a Resend key"

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


# Alloy collects this project's container logs and ships them to Grafana Loki.
obs_env=$(mktemp)
cat > "$obs_env" <<'ENV'
O1_COMPOSE_PROJECT=o1-staging
O1_ENV=production
GRAFANA_REMOTE_WRITE_URL=https://example.invalid/api/prom/push
GRAFANA_REMOTE_WRITE_USER=1
GRAFANA_REMOTE_WRITE_TOKEN=x
GRAFANA_LOKI_URL=https://example.invalid/loki/api/v1/push
GRAFANA_LOKI_USER=1
GRAFANA_LOKI_TOKEN=x
ENV
env -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$env_file" --env-file "$obs_env" \
  -f "$compose_file" --profile observability config --format json \
  | python3 -c '
import json, sys
a = json.load(sys.stdin)["services"]["alloy"]
env = a["environment"]
assert env["O1_COMPOSE_PROJECT"] == "o1-staging", env.get("O1_COMPOSE_PROJECT")
for key in ("GRAFANA_LOKI_URL", "GRAFANA_LOKI_USER", "GRAFANA_LOKI_TOKEN"):
    assert env[key], key
mounts = {m["target"]: m for m in a["volumes"]}
sock = mounts.get("/var/run/docker.sock")
assert sock is not None, sorted(mounts)
assert sock.get("read_only") is True, sock
cmd = " ".join(a["command"]) if isinstance(a["command"], list) else a["command"]
for key in ("GRAFANA_REMOTE_WRITE_URL", "GRAFANA_REMOTE_WRITE_USER",
            "GRAFANA_REMOTE_WRITE_TOKEN", "GRAFANA_LOKI_URL",
            "GRAFANA_LOKI_USER", "GRAFANA_LOKI_TOKEN"):
    assert key in cmd, key
'
rm -f "$obs_env"
check "alloy mounts the Docker socket read-only and requires every Grafana variable"

for label in 'target_label  = "service"' 'target_label  = "container"' 'level = ""'; do
  grep -Fq "$label" deploy/observability/alloy.alloy ||
    fail "alloy.alloy is missing label rule: $label"
done
if grep -Eq 'target_label[[:space:]]*=[[:space:]]*"(user_id|monitor_id|workspace_id|url|email)"' deploy/observability/alloy.alloy; then
  fail "alloy.alloy promotes a high-cardinality field to a label"
fi
check "alloy labels stay bounded to service, container and level"

docker run --rm \
  -v "$repo_root/deploy/observability/alloy.alloy:/etc/alloy/config.alloy:ro" \
  -e O1_ENV=production -e O1_COMPOSE_PROJECT=o1-staging \
  -e GRAFANA_REMOTE_WRITE_URL=https://example.invalid/api/prom/push \
  -e GRAFANA_REMOTE_WRITE_USER=1 -e GRAFANA_REMOTE_WRITE_TOKEN=x \
  -e GRAFANA_LOKI_URL=https://example.invalid/loki/api/v1/push \
  -e GRAFANA_LOKI_USER=1 -e GRAFANA_LOKI_TOKEN=x \
  grafana/alloy:v1.21.3 validate /etc/alloy/config.alloy >/dev/null
check "alloy config validates"

echo "PASS: $checks compose config checks"
