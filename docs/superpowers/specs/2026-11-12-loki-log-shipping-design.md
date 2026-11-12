# Production Log Shipping to Grafana Cloud Loki — Design

**Status:** Draft for approval
**Date:** 2026-11-12
**Depends on:** Plan 9 (observability bundle), Plan 11 (registry deploys)
**Scope owner:** o1 uptime monitor

## 1. Intent

Application logs currently live only in Docker's local JSON files on the VPS.
Every deploy recreates the containers, so the logs of a failed release
disappear exactly when they are most valuable. Ship the stdout/stderr of `api`,
`scheduler`, `worker`, and `web` to Grafana Cloud Loki through the Alloy agent
that already runs for metrics, so logs survive deploys and can be queried from
Grafana.

## 2. Current State

- `deploy/production/compose.yaml` runs `grafana/alloy` under the
  `observability` profile. It mounts `alloy.alloy` and `alloy-data`, has no
  Docker socket, and requires `GRAFANA_REMOTE_WRITE_URL`, `_USER`, and `_TOKEN`.
- `deploy/observability/alloy.alloy` only scrapes `api:8083`,
  `scheduler:8081`, and `worker:8082` and remote-writes metrics.
- The compose entrypoint refuses to start when a required variable is empty and
  prints its name.
- There is no log shipping, no Loki configuration, and no log query surface.
  `docker compose logs` is the only way to read logs, and those files belong to
  containers that a deploy replaces.
- `deploy/observability/README.md` documents metrics, Sentry, tracing, and the
  dead-man's switch, but not logs.

## 3. Goals

1. Alloy discovers the Compose project's containers and tails their logs.
2. Each entry carries bounded labels: `env`, `service`, `container`, and
   `level`.
3. Logs are remote-written to Grafana Cloud Loki with basic auth.
4. Alloy refuses to start with a clear message when a Loki variable is missing.
5. The Docker socket is mounted read-only and only the project's containers are
   collected.
6. `alloy validate` and a Compose render check run in CI.

## 4. Non-Goals

- Self-hosted Loki, Grafana, or a log database on the VPS.
- Log-derived alert rules; alerts stay metric-based.
- Shipping host, Docker daemon, or systemd logs.
- Application-side log redaction changes; the app already sanitises secrets.
- Log retention, sampling, or cost tuning beyond Grafana Cloud defaults.

## 5. Approaches

**A. Alloy reads the Docker API (chosen).** Mount
`/var/run/docker.sock:ro`, use `discovery.docker` filtered by the Compose
project, `loki.source.docker`, and `loki.write`. Container names, Compose
service labels, and restarts are handled by Docker, and a deploy's new
containers are picked up automatically.

**B. Alloy tails Docker's JSON log files (rejected).** Mount
`/var/lib/docker/containers` and parse the file format. Avoids the socket but
depends on Docker's internal file layout and on manually mapping container IDs
to service names; rotation and renames make it fragile.

**C. A second `alloy-logs` service and profile (rejected).** Keeps metrics-only
installs working unchanged, but doubles the agent containers and configuration
for a deployment that will normally run both. The selected approach accepts the
extra credential requirement instead.

## 6. Design

### 6.1 Compose

In the `alloy` service:

- Add `- /var/run/docker.sock:/var/run/docker.sock:ro` to `volumes`.
- Add `O1_COMPOSE_PROJECT: ${O1_COMPOSE_PROJECT:-o1-production}`.
- Add `GRAFANA_LOKI_URL`, `GRAFANA_LOKI_USER`, `GRAFANA_LOKI_TOKEN`, all defaulting
  to empty.
- Extend the startup check to all six Grafana variables and print every missing
  name before exiting.

The profile stays `observability`; the service is still not started by a normal
deploy.

### 6.2 Alloy configuration

Add three stages to `deploy/observability/alloy.alloy`:

1. `discovery.docker "o1"` on `unix:///var/run/docker.sock`, filtered by the
   label `com.docker.compose.project=<O1_COMPOSE_PROJECT>`.
2. `discovery.relabel "o1_logs"` copies
   `__meta_docker_container_label_com_docker_compose_service` to `service` and
   the container name (without the leading slash) to `container`.
3. `loki.source.docker "o1"` uses the Docker host, the discovery targets, and
   the relabel rules, forwarding to `loki.process.o1`.

`loki.process "o1"`:

- `stage.json` extracts `level` from the application's JSON log line.
- `stage.labels` promotes `level`.
- Malformed lines (for example Caddy's plain-text errors) pass through without
  a `level` label.

`loki.write "grafana_cloud"`:

- `url = sys.env("GRAFANA_LOKI_URL")`.
- `basic_auth` with `GRAFANA_LOKI_USER` and `GRAFANA_LOKI_TOKEN`.
- `external_labels = { env = sys.env("O1_ENV") }`.

Label policy: `env` (three values), `service` (four), `container` (stable
Compose names), `level` (bounded log levels). Trace IDs, user IDs, monitor IDs,
URLs, and request bodies are not labels.

### 6.3 Documentation

- `deploy/observability/README.md`: new "Logs" section with Grafana Cloud Loki
  setup (URL `https://logs-prod-XX.grafana.net/loki/api/v1/push`, numeric user,
  token with `logs:write`), the three variables, enabling the profile, and
  example queries (`{service="api"} |= "ERROR"`, `{service="web"}`,
  `{env="production"} | json`). Note the Docker socket access and its
  root-equivalent risk.
- `deploy/production/README.md` §12: list the log variables, note that both
  credentials sets are required for the profile, and that a deploy does not
  restart Alloy.
- `deploy/CICD.md` §9: add Loki to the Alloy description and the restart
  command.
- `deploy/production/env.example`: add the three `GRAFANA_LOKI_*` placeholders.

### 6.4 Tests

Extend `deploy/scripts/test-compose-config.sh` (it already requires Docker):

- Render `--profile observability` and assert Alloy mounts the Docker socket
  read-only, receives `O1_COMPOSE_PROJECT`, and receives all three Loki
  variables.
- Run `grafana/alloy:v1.21.3 validate /etc/alloy/config.alloy` against the
  shipped file so a syntax error fails CI.
- Assert the missing-variable branch names every required variable by rendering
  with one Loki variable empty and checking the command text.

`deploy/scripts/test-bootstrap.sh` and `test-deploy.sh` are unaffected.

## 7. Security

- The socket is mounted read-only, and the agent only reads containers in the
  configured Compose project.
- Read access to `docker.sock` is effectively root-equivalent on the host. This
  is accepted for the official Alloy image and documented; the container runs
  no application code and exposes no port.
- The Loki token lives in `/opt/o1/.env` only, is never logged, and never enters
  a release bundle.
- Alloy's UI (`:12345`) remains unpublished.

## 8. Rollout

1. Add `GRAFANA_LOKI_URL`, `GRAFANA_LOKI_USER`, and `GRAFANA_LOKI_TOKEN` to
   `/opt/o1/.env`; the metrics variables stay.
2. Re-run `--profile observability up -d alloy` from the active release.
3. Confirm Grafana Explore → Loki returns entries from all four services.
4. A later deploy keeps Alloy running with its old config; re-run the profile
   command after a release that changes `alloy.alloy`.

## 9. Risks

- **Log volume:** the worker logs one line per probe. Grafana Cloud free-tier
  log allowances are finite; the design documents raising the scrape/batch
  settings or filtering debug lines if usage is high.
- **Cardinality:** labels are limited to four bounded values; the app's JSON
  fields stay in the log body.
- **Socket exposure:** mitigated by read-only mount, project filter, and no
  published UI.
- **Agent failure hides logs:** the existing `O1MetricsMissing` alert covers the
  scrape path; a Loki-specific alert is out of scope, and the dead-man's switch
  still covers a dead host.

## 10. Decisions Left to the Builder

- Whether to drop `web` logs or keep Caddy's access/error lines; the design
  keeps them.
- Whether to add a `stream` label; Alloy may add one automatically.
- Exact query examples in the runbook.

## 11. Acceptance

- Loki shows entries for `api`, `scheduler`, `worker`, and `web` with `env`,
  `service`, `container`, and `level` labels.
- `{service="api"} |= "ERROR"` returns application errors after a deploy.
- Removing `GRAFANA_LOKI_TOKEN` makes Alloy exit and name the missing variable.
- `test-compose-config.sh`, `alloy validate`, and the Go suites pass.
