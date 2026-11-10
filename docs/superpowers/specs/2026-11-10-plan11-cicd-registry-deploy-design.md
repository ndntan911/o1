# Plan 11 — CI/CD, Registry Publishing, and Automated Deploy Design

**Status:** Draft for approval
**Date:** 2026-11-10
**Depends on:** Plan 10 (`deploy/production/` Compose stack, runtime Caddy address, production email)
**Scope owner:** o1 uptime monitor

## 1. Intent

Plan 10 produced a production Compose stack that is built and deployed manually
from a checked-out repository. Plan 11 makes releases reproducible and
deployable without a laptop: CI verifies the tree, a tag builds and scans
container images, images are published to GHCR, and a GitHub Actions deploy job
pulls those exact images onto a host over SSH, runs a health check, and rolls
back to the previous release when the new one does not become healthy.

The pipeline is built and documented in this repository. Wiring a real GitHub
remote, host, domain, and secrets is deliberately left to the operator. That
means the design must be verifiable locally and must not assume any particular
host exists.

## 2. Current State

- `.github/workflows/ci.yml` already runs, on every push and PR: `go vet`,
  short tests, golangci-lint, Go builds, both Docker image builds, full
  integration tests, frontend format/lint/typecheck/unit/build, and a Compose
  E2E.
- `deploy/production/compose.yaml` builds API/scheduler/worker/web from source
  (`build: ../../backend`, `build: ../../web`). It cannot pull published images.
- `deploy/production/README.md` documents a manual first deploy.
- The application applies goose, River, and probe-partition migrations at
  startup in `backend/internal/app/app.go`, guarded by database advisory locks.
- The repository has no git remote and no tags. `gh` is authenticated to
  GitHub. From the operator's side, a remote, environments, and secrets still
  have to be created.
- The original foundation design (§8.5–8.6) asks for GitHub Actions build,
  scan, GHCR push, SSH + Compose deploy, migration, health check, rollback,
  branch protection, and local/staging/production environments, with images
  pinned by digest.

## 3. Goals

1. Keep the existing PR checks and make them reusable by the release workflow.
2. On a `v*` tag: re-run checks, scan the built images, and publish immutable
   images to GHCR only after the scans pass.
3. Publish a backend image (one image, three entrypoints) and a web image, each
   tagged with the release version and a commit-derived immutable tag, with
   SBOM and provenance attached.
4. Convert the production stack to registry images pinned by digest, while
   keeping a local build override for checked-out bootstrap and offline
   recovery.
5. Add a host-side release bundle and deploy script that pulls, deploys, waits
   for readiness, and restores the previous config-and-image pair on failure.
6. Add a GitHub Actions deploy workflow that uploads a release bundle and runs
   the host script over SSH, gated by per-environment secrets and production
   approval.
7. Define staging as a second environment using the same compose/scripts and a
   separate host and env file.
8. Document branch protection, tag protection, required checks, environments,
   secrets, GHCR visibility, first deploy, and rollback drills.
9. Verify everything that can be verified without a live host: workflow lint,
   shell lint, Compose config, and a fake-command deploy/rollback test.

## 4. Non-Goals

- Creating the GitHub repository, VPS, DNS, or secrets on the operator's behalf.
- Backups, WAL archiving, and restore drills (Plan 12).
- Rollups, retention, and data lifecycle (Plan 13).
- Load tests, penetration testing, and a formal security review (Plan 14).
- Managed PostgreSQL, multiple regions, Kubernetes, service mesh, canary or
  blue/green deploys, and autoscaling.
- Image signing with cosign and policy enforcement (possible follow-up; SBOM and
  provenance are included, signing is not).
- Generic CI for other projects; this pipeline is specific to o1.

## 5. Anti-Goals

- A deploy that reports success while `/readyz` is unhealthy.
- Publishing an image that failed a high/critical vulnerability scan.
- Deploying `latest` or any mutable tag to production.
- Storing `.env`, SSH keys, registry tokens, or host details in git or images.
- Requiring a full git checkout, Go toolchain, or Node toolchain on the host.
- Running schema migrations differently in CI than in normal startup.
- A workflow with blanket write permissions.
- A rollback that pretends to undo database migrations.
- A rollback that restores images but leaves newer compose/config in place.

## 6. Approaches Considered

### A. One workflow

Extend `ci.yml` with publish and deploy jobs. Fewest files, but every PR
workflow carries publish/deploy definitions and permissions, and tag runs
repeat unrelated work. Rejected: poor permission separation and harder review.

### B. Split workflows with host-side release bundles (chosen)

Keep `ci.yml` as the reusable verification workflow. Add `release.yml` for
tag-driven scan/publish and `deploy.yml` for environment-gated SSH deploys.
Convert the production Compose to image references and deploy immutable release
bundles on the host. This matches the existing CI, gives each workflow only the
permissions it needs, and keeps deploy logic testable outside GitHub.

### C. Managed deploy platform

Use Coolify, Dokploy, Watchtower, or Kubernetes. Less custom scripting, but it
adds a platform dependency, hides rollback and env behavior, and conflicts with
the deliberately simple Compose-on-VPS design. Rejected.

## 7. Decisions

### 7.1 Workflow layout

- **`.github/workflows/ci.yml`** remains the verification workflow. Add
  `workflow_call:` to its triggers so `release.yml` can reuse it. Jobs stay:
  `backend`, `integration`, `frontend`, `e2e`. These names become the required
  status checks.
- **`.github/workflows/release.yml`** triggers on `v*` tags. It calls `ci.yml`
  as a reusable workflow (`verify` job), then runs `publish`.
- **`.github/workflows/deploy.yml`** is `workflow_dispatch` only, with inputs
  `environment` (`staging` or `production`), `version` (tag), and `dry_run`.
  It uses a GitHub Environment of the same name for secrets and approvals.
  Production requires reviewers and is restricted to the `main` workflow ref;
  staging need not be.
- Deploy jobs check out the requested version tag, so the compose file, Alloy
  config, and scripts in the release bundle match the images. A deploy
  dispatched from `main` for `v1.2.3` uses `v1.2.3`'s bundle, not `main`'s.

Using `workflow_dispatch` rather than auto-deploying on tag keeps production a
deliberate action while still forbidding laptop deploys: the only supported path
is the Actions job.

### 7.2 Verification reuse

`release.yml` calls the existing `ci.yml` so a tag cannot publish code that
would fail the PR gate. Re-running the whole suite, including E2E, is slower
than a bespoke release check but removes drift between "tested" and "released".
Build caches make the later image build fast.

Add `govulncheck ./...` to the backend CI job. This is a new gate; if it is too
noisy for the current dependency set, the plan may narrow it to
`-scan=module` and document the decision.

### 7.3 Registry and image model

- Registry: GitHub Container Registry.
- Backend image: `ghcr.io/<owner>/o1-backend`. One image, three binaries;
  `api`, `scheduler`, and `worker` select an entrypoint.
- Web image: `ghcr.io/<owner>/o1-web`.
- `<owner>` is `${{ github.repository_owner }}`; nothing is hard-coded.
- Tags per image:
  - `vX.Y.Z` — release tag, immutable by policy.
  - `sha-<12-char-commit>` — immutable, useful for staging and hotfixes.
  - `X.Y` — convenience only, never deployed.
  - No `latest` is required; if published, it is never used by deploy.
- Deploys pin images by digest (`repo@sha256:...`), per the foundation design.
  The deploy workflow resolves digests from the version tag and writes them into
  the release bundle.
- Platform: `linux/amd64`, matching the VPS target. Multi-arch is out of scope.
- Build cache: BuildKit `type=gha` with per-image scopes.
- SBOM and provenance: enabled on the pushed images.
- GHCR packages are private by default. Public packages are allowed; the docs
  state the trade-off and let the operator omit host pull credentials.

Publishing uses the automatic `GITHUB_TOKEN` with `packages: write`; no
long-lived registry credential is stored in the repository. Digest resolution
in the deploy workflow uses `packages: read`.

### 7.4 Scan before push

The publish job builds each image locally (`load: true`, `push: false`), scans
it with Trivy, and only then pushes. A failed scan leaves the registry untouched.

- Trivy image scan: `HIGH,CRITICAL`, `ignore-unfixed: true`, `exit-code: 1`.
- Trivy filesystem scan: secret and misconfiguration scanners on the repository.
- `govulncheck` runs in the reusable verification job.
- Scan output is written to the job summary. A `.trivyignore` is allowed only
  with a comment naming the CVE, the reason, and a review date.

### 7.5 Production Compose image references

`deploy/production/compose.yaml` stops using `build:` for the four app services
and uses required image variables:

```yaml
services:
  api:
    image: ${O1_BACKEND_IMAGE:?set O1_BACKEND_IMAGE}
  scheduler:
    image: ${O1_BACKEND_IMAGE:?set O1_BACKEND_IMAGE}
  worker:
    image: ${O1_BACKEND_IMAGE:?set O1_BACKEND_IMAGE}
  web:
    image: ${O1_WEB_IMAGE:?set O1_WEB_IMAGE}
```

`deploy/production/compose.build.yaml` restores the current `build:` contexts
for a checked-out repository: local bootstrap, deliberate offline recovery on a
developer machine, or a host that keeps a source checkout. It is **not**
uploaded to a registry-deployed host; that host recovers by switching to a
previous release bundle.

The stack supports `O1_COMPOSE_PROJECT` (default `o1-production`) so staging can
run the same files with its own project name. Staging still needs a separate host
because both environments publish ports 80/443.

### 7.6 Host layout, image state, and release bundles

The host separates durable secrets, stable scripts, and immutable releases:

```text
/opt/o1/
  .env                         # secrets + O1_DOMAIN; operator-managed, never written by CI
  bin/
    deploy.sh                  # installed by bootstrap; stable across releases
    rollback.sh
    healthcheck.sh
  releases/
    <version>-<sha>/
      deploy/production/compose.yaml
      deploy/observability/alloy.alloy
      images.env
  current -> releases/<version>-<sha>
  previous -> releases/<version>-<sha>
  incoming/<version>-<sha>/    # upload target, consumed by deploy.sh
  state/deploy.lock
```

`images.env` contains digest-pinned references:

```sh
O1_BACKEND_IMAGE=ghcr.io/<owner>/o1-backend@sha256:...
O1_WEB_IMAGE=ghcr.io/<owner>/o1-web@sha256:...
```

Compose is invoked against the active release:

```sh
docker compose \
  --env-file /opt/o1/.env \
  --env-file /opt/o1/current/images.env \
  -f /opt/o1/current/deploy/production/compose.yaml \
  up -d --remove-orphans
```

The mirrored `deploy/...` tree keeps the Alloy bind mount
(`../../deploy/observability/alloy.alloy`) resolving on the host. The release
directory is the unit of rollback: switching `current` restores the compose
file and image digests together. `.env` is intentionally outside the release
because secrets change independently and must survive rollback.

### 7.7 Deploy script behavior

`deploy/scripts/deploy.sh` runs from `/opt/o1/bin` and receives an incoming
bundle path. It:

1. Validates required commands, files, and variables without echoing secrets.
2. Takes an exclusive `flock` so two deploys cannot overlap.
3. Validates the bundle contains `deploy/production/compose.yaml`,
   `deploy/observability/alloy.alloy`, and `images.env`, then runs
   `docker compose ... config --quiet` against the bundle.
4. Moves the bundle to `releases/<id>`, moves the old `current` link to
   `previous`, and points `current` at the new release.
5. Optionally logs in to GHCR with `GHCR_USER`/`GHCR_READ_TOKEN` from `.env`
   when packages are private.
6. Pulls only the app images, then runs `up -d --remove-orphans`.
7. Polls `https://$O1_DOMAIN/readyz` until healthy or the configurable timeout
   expires (default 300s; the first deploy includes ACME issuance and
   migrations).
8. On failure, prints bounded container logs, points `current` back at
   `previous`, brings that release up, re-checks health, and exits non-zero.
   With no `previous` (first deploy), it leaves the containers running for
   inspection, reports that there is nothing to roll back to, and exits non-zero.
9. On success, prints `docker compose ps` and the deployed digests.

`O1_DOMAIN` is read from `/opt/o1/.env` and is the single source of truth for
the health URL; CI does not pass it. A missing or empty domain fails validation
before any container is touched.

`deploy/scripts/rollback.sh` takes the same `flock`, switches `current` to
`previous`, brings the stack up, and runs the same health check. It also accepts
a named release to roll forward. Migrations are not rolled back; the runbook
states that image/config rollback is only safe for backward-compatible schema
changes.

`deploy/scripts/bootstrap-host.sh` performs the one-time host setup: Docker and
Compose presence, `/opt/o1` layout and permissions, `.env` creation from the
example, installation of the stable scripts, optional GHCR login, and a
`docker compose config --quiet` validation using placeholder image variables
written inline (never persisted). It does not start the stack.

All scripts use `set -euo pipefail`, quote expansions, disable shell tracing
around credentials, and have shellcheck-clean code.

The optional Alloy container is not in the default Compose profile, so a
deploy's `up -d --remove-orphans` leaves a running Alloy untouched. The
runbook states that an Alloy config change requires an explicit
`--profile observability up -d` after the deploy.

### 7.8 Deploy workflow steps

1. Validate that `version` is an existing `v*` tag reachable from `main`, and
   reject pre-release tags for production.
2. Check out `ref: ${{ inputs.version }}` so the release bundle matches the
   images.
3. Log in to GHCR and resolve the backend and web digests with
   `docker buildx imagetools inspect`.
4. Write `images.env` and assemble `incoming/<version>-<sha>/` containing
   `deploy/production/compose.yaml`, `deploy/observability/alloy.alloy`, and
   `images.env`.
5. Start `ssh-agent`, install `SSH_KNOWN_HOSTS`, and copy the bundle to
   `/opt/o1/incoming/<version>-<sha>/` with `scp`.
6. Run `/opt/o1/bin/deploy.sh /opt/o1/incoming/<version>-<sha>` over SSH, with
   `--dry-run` when requested.
7. Upload a short job summary with the environment, version, and digests.

Use host-key verification (`StrictHostKeyChecking=yes`). The deploy job needs
`contents: read` and `packages: read`. It never builds images.

### 7.9 Staging

Staging uses the same workflows, compose file, and scripts with:

- GitHub Environment `staging` and its own `SSH_*` secrets/variables.
- A separate host (both environments bind 80/443).
- Its own `/opt/o1/.env`, project name, and `O1_DOMAIN`.
- Permission to deploy `v*-rc*` or `sha-*` images; production accepts only
  final `vX.Y.Z` tags.

No live staging host is provisioned by this plan; the docs show exactly what to
create.

### 7.10 Migrations

Keep migrations in application startup. The API's readiness check gates the
deploy; if migration fails, readiness fails and the script restores the previous
release. No separate migration container is introduced.

Consequences documented in the runbook: schema changes must be forward
compatible, a failed migration may leave the schema ahead of the rolled-back
code, and operators must inspect migrations before tagging a release.

### 7.11 Branch and tag protection

Documented, because these are repository settings rather than code:

- `main` requires a pull request, the four CI checks, linear history, and no
  force pushes; direct pushes are disabled.
- Release tags `v*` are restricted to maintainers.
- Production Environment requires reviewers, allows only the `main` workflow
  ref, and rejects pre-release versions in the workflow itself.
- Only the deploy workflow may reach production; there is no laptop path.

### 7.12 Required secrets and variables

GitHub Environment secrets/variables, duplicated for `staging` and `production`:

| Name | Kind | Purpose |
|------|------|---------|
| `SSH_HOST` | secret | Deploy host |
| `SSH_USER` | secret | Deploy user |
| `SSH_PRIVATE_KEY` | secret | Deploy key |
| `SSH_KNOWN_HOSTS` | secret | Host-key pinning |
| `SSH_PORT` | variable | Default 22 |

Host `/opt/o1/.env`, operator-managed and never written by CI:

- Required: `O1_DOMAIN`, `POSTGRES_PASSWORD`, `O1_SECRET_KEY`,
  `O1_ENCRYPTION_KEY`, `O1_EMAIL_FROM`, `O1_RESEND_API_KEY`.
- Optional: `O1_EMAIL_REPLY_TO`, `O1_RESEND_RPS`, `O1_RESEND_BURST`,
  `O1_BOOTSTRAP_ADMIN_EMAIL`, observability values.
- Private GHCR packages: `GHCR_USER` and `GHCR_READ_TOKEN` (`read:packages`).
  Public packages omit them.

Repository-level variables: `VITE_SENTRY_DSN` (optional). `VITE_RELEASE` is
derived from the release tag. The automatic `GITHUB_TOKEN` handles publishing.

### 7.13 Dependency and action updates

Add `.github/dependabot.yml` for GitHub Actions, Go modules, and npm, weekly.
This is small, keeps pinned actions current, and complements Trivy. It does not
enable auto-merge.

## 8. Constraints

- GitHub Actions and GHCR are the CI/CD platform.
- Deploy target is a single Linux host with Docker Compose, per the foundation
  design; no orchestrator.
- Only the web container publishes 80/443; Plan 10's exposure rules still hold.
- Images must run as the existing non-root/distroless backend user and the
  existing Caddy web image.
- The pipeline must not require secrets to run PR checks.
- A host deploy must work with only Docker, Compose, curl, and the deployment
  directory; no Go/Node/git checkout.
- No database backup or restore work is introduced here.
- Time and cost: free-tier Actions minutes are sufficient if caches are used;
  the E2E job dominates runtime and is reused unchanged.

## 9. Deliverables

```text
.github/
  workflows/
    ci.yml                     # modify: add workflow_call, govulncheck
    release.yml                # create: verify -> scan -> publish GHCR
    deploy.yml                 # create: environment-gated SSH deploy
  dependabot.yml               # create
deploy/
  CICD.md                      # create: operator setup, branch protection, drills
  production/
    compose.yaml               # modify: image refs + project name
    compose.build.yaml         # create: local/checkout build override
    env.example               # modify: image variables + GHCR pull notes
    README.md                  # modify: CI vs manual paths, rollback caveats
  scripts/
    bootstrap-host.sh          # create
    deploy.sh                  # create
    rollback.sh                # create
    healthcheck.sh             # create (shared by deploy/rollback)
    test-deploy.sh             # create: fake-command tests
README.md                      # modify: link CI/CD docs
```

## 10. Verification

Without a live host:

- `actionlint` passes on all workflows.
- `shellcheck` and `bash -n` pass on all scripts.
- `docker compose --env-file <fixture> -f deploy/production/compose.yaml config`
  succeeds with placeholder digest refs and fails when an image variable is
  missing.
- The build override still validates and preserves the Plan 10 behavior.
- `deploy/scripts/test-deploy.sh` uses fake `docker`, `curl`, and `flock`
  binaries on `PATH` to assert:
  - a successful deploy activates the new release, writes `current`, and waits
    for readiness;
  - an unhealthy deploy restores `current` to the previous release (config and
    images together) and exits non-zero;
  - a failed validation or pull does not switch `current`;
  - a first deploy with no previous release fails without switching links;
  - `--dry-run` changes nothing.
- Existing backend, frontend, and E2E suites stay green in dev Compose.

With a live host (operator acceptance, documented as a checklist):

- Push a `vX.Y.Z-rc.1` tag; confirm images are published and scanned.
- Deploy staging; confirm the digest in the job summary matches the running
  containers.
- Force an unhealthy image; confirm rollback restores the previous digests and
  compose file.
- Deploy production through the protected Environment.

## 11. Decisions Left to the Builder

- Exact action versions and whether to use a third-party SSH agent action or
  plain `ssh`/`scp` with `ssh-agent`.
- Whether `govulncheck` is a blocking gate immediately or starts as a report.
- The precise fake-command harness for `test-deploy.sh` (PATH shims versus a
  small Go test).
- Whether Trivy scans the repository filesystem before or after the image scan.
- The exact health-check retry cadence inside the default 300s budget.

## 12. Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| A bad image passes the scan but fails at runtime | Readiness check + release rollback |
| Migration succeeds, new code fails, rollback leaves schema ahead | Forward-only migration rule, documented; migrations reviewed before tagging |
| A release changes compose/config and rollback restores only images | Release directories roll back compose and images together |
| GHCR packages private and host cannot pull | Documented `GHCR_READ_TOKEN`, optional public packages, bootstrap login |
| SSH host key changes | Pinned `SSH_KNOWN_HOSTS`, deploy fails closed |
| Two deploys overlap | Host `flock` plus workflow concurrency |
| Secrets leak through logs | No `set -x` around credentials, password via stdin, workflow masks secrets |
| Staging shares a host and fights over 80/443 | Explicit separate-host requirement |
| First deploy has no previous release | Defined no-rollback behavior, longer timeout, operator checklist |
| Scan failures block a security release | Documented, time-boxed `.trivyignore` exceptions |

## 13. Rollout

1. Land workflows, compose image support, scripts, and docs.
2. Validate locally with the verification list in §10.
3. Operator creates the remote, pushes `main`, configures environments,
   secrets, branch/tag protection, and GHCR visibility.
4. Operator provisions the host with `bootstrap-host.sh` and a real `.env`.
5. Cut `v0.1.0-rc.1`, deploy staging, run the rollback drill, then cut
   `v0.1.0` and deploy production.
