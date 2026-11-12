# Production Log Shipping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the production containers' logs to Grafana Cloud Loki through the existing Alloy agent, with bounded labels and surviving deploys.

**Architecture:** Mount the Docker socket read-only into the `alloy` service, discover the Compose project's containers with `discovery.docker`, tail them with `loki.source.docker`, extract `level` with `loki.process`, and remote-write to Grafana Cloud Loki. Metrics scraping is unchanged.

**Tech Stack:** Grafana Alloy v1.21.3, Docker Compose, Grafana Cloud Loki, Bash test scripts.

**Spec:** `docs/superpowers/specs/2026-11-12-loki-log-shipping-design.md`

## Global Constraints

- Socket mount must be read-only: `/var/run/docker.sock:/var/run/docker.sock:ro`.
- Only containers in `O1_COMPOSE_PROJECT` are collected.
- Labels stay bounded: `env`, `service`, `container`, `level`. No IDs, URLs, emails, or request bodies.
- All six Grafana variables are required when the `observability` profile runs: `GRAFANA_REMOTE_WRITE_URL`, `_USER`, `_TOKEN`, `GRAFANA_LOKI_URL`, `_USER`, `_TOKEN`. Missing values exit with the names printed.
- The Alloy UI stays unpublished.
- A normal deploy does not start or restart Alloy; the profile command remains the operator's step.
- Commits use conventional prefixes.

## Review Focus

1. **Socket mounted writable**: must be `:ro`. Pinned by the compose-render assertion in Task 1.
2. **Collecting other projects' containers**: must filter by `O1_COMPOSE_PROJECT`. Pinned by the rendered environment assertion and the discovery filter in Task 1.
3. **Unbounded label cardinality**: no IDs/URLs/emails. Pinned by a structural check that the Alloy config's label rules only set `service`, `container`, and `level` (Task 1).
4. **Silent agent misconfiguration**: a missing Loki variable must fail startup and name it. Pinned by the command-text assertion in Task 1.
5. **Invalid Alloy config reaching production**: pinned by `alloy validate` in the compose test (Task 1).

---

### Task 1: Alloy log pipeline and Compose wiring

**Files:**
- Modify: `deploy/observability/alloy.alloy`
- Modify: `deploy/production/compose.yaml`
- Modify: `deploy/scripts/test-compose-config.sh`

**Interfaces:**
- Consumes: `O1_COMPOSE_PROJECT`, `O1_ENV`, `GRAFANA_LOKI_URL`, `GRAFANA_LOKI_USER`, `GRAFANA_LOKI_TOKEN` from the Compose environment.
- Produces: the log pipeline contract documented in Task 2.

- [ ] **Step 1: Add the failing Compose and Alloy checks**

Append to `deploy/scripts/test-compose-config.sh`, before the final `echo "PASS..."`:

```bash
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
cmd = a["command"][0] if isinstance(a["command"], list) else a["command"]
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash deploy/scripts/test-compose-config.sh`
Expected: FAIL — no Docker socket mount / missing Loki variables.

- [ ] **Step 3: Wire the Compose service**

In `deploy/production/compose.yaml`, in the `alloy` service:

1. Extend the entrypoint check to all six variables (keep exiting on the first missing one but list it):
   ```yaml
   for name in GRAFANA_REMOTE_WRITE_URL GRAFANA_REMOTE_WRITE_USER GRAFANA_REMOTE_WRITE_TOKEN \
               GRAFANA_LOKI_URL GRAFANA_LOKI_USER GRAFANA_LOKI_TOKEN; do
   ```
2. Add to `environment`:
   ```yaml
         O1_COMPOSE_PROJECT: ${O1_COMPOSE_PROJECT:-o1-production}
         GRAFANA_LOKI_URL: ${GRAFANA_LOKI_URL:-}
         GRAFANA_LOKI_USER: ${GRAFANA_LOKI_USER:-}
         GRAFANA_LOKI_TOKEN: ${GRAFANA_LOKI_TOKEN:-}
   ```
3. Add to `volumes`:
   ```yaml
         - /var/run/docker.sock:/var/run/docker.sock:ro
   ```
4. Update the error text to mention all required variables.

- [ ] **Step 4: Add the Alloy log pipeline**

Append to `deploy/observability/alloy.alloy`:

```alloy
discovery.docker "o1" {
  host = "unix:///var/run/docker.sock"

  filter {
    name   = "label"
    values = ["com.docker.compose.project=" + sys.env("O1_COMPOSE_PROJECT")]
  }
}

discovery.relabel "o1_logs" {
  targets = []

  rule {
    source_labels = ["__meta_docker_container_label_com_docker_compose_service"]
    target_label  = "service"
  }

  rule {
    source_labels = ["__meta_docker_container_name"]
    regex         = "/(.*)"
    target_label  = "container"
  }
}

loki.source.docker "o1" {
  host          = "unix:///var/run/docker.sock"
  targets       = discovery.docker.o1.targets
  labels        = {}
  relabel_rules = discovery.relabel.o1_logs.rules
  forward_to    = [loki.process.o1.receiver]
}

loki.process "o1" {
  forward_to = [loki.write.grafana_cloud.receiver]

  stage.json {
    expressions = {
      level = "level",
    }
  }

  stage.labels {
    values = {
      level = "",
    }
  }
}

loki.write "grafana_cloud" {
  endpoint {
    url = sys.env("GRAFANA_LOKI_URL")

    basic_auth {
      username = sys.env("GRAFANA_LOKI_USER")
      password = sys.env("GRAFANA_LOKI_TOKEN")
    }
  }

  external_labels = {
    env = sys.env("O1_ENV"),
  }
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run:
```bash
bash deploy/scripts/test-compose-config.sh
bash deploy/scripts/test-bootstrap.sh
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add deploy/observability/alloy.alloy deploy/production/compose.yaml deploy/scripts/test-compose-config.sh
git commit -m "feat: ship production logs to Grafana Loki"
```

---

### Task 2: Environment example and documentation

**Files:**
- Modify: `deploy/production/env.example`
- Modify: `deploy/observability/README.md`
- Modify: `deploy/production/README.md`
- Modify: `deploy/CICD.md`

**Interfaces:**
- Consumes: the variable names and pipeline behavior from Task 1.

- [ ] **Step 1: Add the variables to `env.example`**

Under the observability section, next to `GRAFANA_REMOTE_WRITE_*`:

```dotenv
# Grafana Cloud Loki for production log shipping. All three are required when
# the observability profile runs, together with the remote-write variables.
# The token needs logs:write. The URL looks like
# https://logs-prod-XX.grafana.net/loki/api/v1/push
# GRAFANA_LOKI_URL=
# GRAFANA_LOKI_USER=
# GRAFANA_LOKI_TOKEN=
```

- [ ] **Step 2: Document the logs pipeline**

`deploy/observability/README.md`, new "Logs" section after the metrics section:

- Grafana Cloud Loki setup: endpoint, numeric user, token with `logs:write`.
- Queries: `{service="api"}`, `{service="api"} |= "ERROR"`, `{env="production"} | json`.
- Labels: `env`, `service`, `container`, `level`.
- The Docker socket is mounted read-only, and Alloy only collects the
  configured Compose project; read access is root-equivalent and accepted for
  the official image with no published UI.
- Add the profile command and note that a deploy does not restart Alloy.

`deploy/production/README.md` §12:

- List `GRAFANA_LOKI_URL`, `_USER`, `_TOKEN`.
- State that the observability profile now requires all six Grafana variables.
- Note logs survive deploys only while the profile is running.

`deploy/CICD.md` §9:

- Mention log shipping in the Alloy description and the restart command.
- Note that a release changing `alloy.alloy` needs a manual Alloy restart.

- [ ] **Step 3: Verify the documents**

Run:
```bash
grep -n "GRAFANA_LOKI_URL" deploy/production/env.example deploy/production/README.md deploy/observability/README.md deploy/CICD.md
grep -n "docker.sock" deploy/observability/README.md
```
Expected: all four files name the URL variable; the observability README mentions the socket.

- [ ] **Step 4: Commit**

```bash
git add deploy/production/env.example deploy/observability/README.md deploy/production/README.md deploy/CICD.md
git commit -m "docs: document Loki log shipping"
```

---

### Task 3: Full verification

**Files:** none.

- [ ] **Step 1: Run the gates**

```bash
bash -n deploy/scripts/*.sh
bash deploy/scripts/test-compose-config.sh
bash deploy/scripts/test-bootstrap.sh
bash deploy/scripts/test-workflows.sh
bash deploy/scripts/test-deploy.sh
cd backend && go vet ./... && golangci-lint run && go test -short ./...
```

Expected: all pass.

- [ ] **Step 2: Confirm the profile renders with and without credentials**

```bash
# With every variable set: renders.
O1_COMPOSE_PROJECT=o1-staging O1_ENV=production \
GRAFANA_REMOTE_WRITE_URL=https://x GRAFANA_REMOTE_WRITE_USER=1 GRAFANA_REMOTE_WRITE_TOKEN=x \
GRAFANA_LOKI_URL=https://x GRAFANA_LOKI_USER=1 GRAFANA_LOKI_TOKEN=x \
docker compose --env-file deploy/production/env.example \
  -f deploy/production/compose.yaml --profile observability config --quiet && echo PROFILE_OK

# Missing one Loki variable: the rendered command still names it, so startup fails loudly.
O1_COMPOSE_PROJECT=o1-staging \
docker compose --env-file deploy/production/env.example \
  -f deploy/production/compose.yaml --profile observability config \
  | grep -q GRAFANA_LOKI_TOKEN && echo MISSING_VAR_NAMED
```

- [ ] **Step 3: Commit any verification fixups**

Only if Steps 1–2 required changes.

---

## Post-Implementation (operator)

Add the three `GRAFANA_LOKI_*` values to `/opt/o1/.env`, re-run
`--profile observability up -d alloy` from the active release, and confirm
entries in Grafana Explore → Loki for `service` = `api`, `scheduler`, `worker`,
and `web`. Rollback is configuration-only: remove the three values and stop
Alloy, or leave the profile off on the next deploy.
