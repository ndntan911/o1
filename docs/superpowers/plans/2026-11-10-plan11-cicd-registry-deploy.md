# Plan 11 — CI/CD, Registry Publishing, and Automated Deploy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a tag-driven pipeline that verifies, scans, and publishes digest-pinned images to GHCR, then deploys immutable release bundles to a Compose host over SSH with a readiness check and config-plus-image rollback.

**Architecture:** `ci.yml` becomes a reusable verification workflow. `release.yml` calls it on `v*` tags, builds and scans both images before pushing them to GHCR. `deploy.yml` dispatches a release bundle to a host over SSH; `/opt/o1/bin/deploy.sh` activates it as an immutable release directory, waits for `/readyz`, and swaps back to the previous release on failure.

**Tech Stack:** GitHub Actions, GHCR, Docker Buildx, Trivy, govulncheck, Docker Compose, Caddy, Bash, Python 3 (test assertions), shellcheck, actionlint.

**Spec:** `docs/superpowers/specs/2026-11-10-plan11-cicd-registry-deploy-design.md`

## Global Constraints

- CI/CD platform is GitHub Actions; registry is GHCR. No other registry or deploy platform.
- Images: `ghcr.io/<owner>/o1-backend` (one image, `api`/`scheduler`/`worker` entrypoints) and `ghcr.io/<owner>/o1-web`. `<owner>` is always lowercased from `github.repository_owner`; GHCR paths reject uppercase.
- Platform `linux/amd64`; no multi-arch builds.
- Deploys pin images by digest (`repo@sha256:<64 hex>`), never by a mutable tag.
- Publish only after both images pass Trivy (`HIGH,CRITICAL`, `ignore-unfixed: true`) and the reusable verification workflow passes.
- Production accepts only final `vMAJOR.MINOR.PATCH` tags; staging may deploy `v*-rc*` and `sha-*`.
- Production Environment requires reviewers and allows only the `main` workflow ref.
- Secrets live only in GitHub Environment secrets and the host `/opt/o1/.env`. Never in git, images, workflow logs, or command lines.
- The host needs only Docker, Compose, curl, and `flock`; no git checkout, Go, or Node.
- Migrations stay in application startup; rollback restores code and config, never schema.
- Only the web service publishes 80/443; Plan 10 exposure rules are unchanged.
- No live GitHub remote, host, domain, or secret is created in this plan.
- Scripts use `set -euo pipefail`, quote expansions, are shellcheck-clean, and never enable `set -x` around credentials.
- Existing backend, frontend, and E2E behavior must not regress.

## Review Focus

1. **Missing or invalid release input.** A deploy with no `.env`, no image file, missing digests, or a malformed version must fail before `current` changes. Tested in Task 2.
2. **Unhealthy release and first deploy.** An unhealthy new release restores the previous compose file and digests together and re-passes health; a first deploy with no previous release leaves containers inspectable and reports that rollback is impossible. Tested in Task 2.
3. **Concurrent deploys.** A second deploy or rollback while the host lock is held exits nonzero without touching state. Tested in Task 2.
4. **Production dispatch rules.** A pre-release version or a non-`main` workflow ref cannot deploy to production. Tested in Task 2 and Task 5.
5. **Scan and secret containment.** No image is pushed when a scan fails, and a GHCR token never appears in process arguments or logs. Tested in Tasks 2 and 4.

---

## File Structure

```text
.github/
  dependabot.yml                 # create: actions, gomod, npm
  workflows/
    ci.yml                       # modify: workflow_call + govulncheck
    release.yml                  # create: verify -> build -> scan -> push
    deploy.yml                   # create: dispatch -> digest -> bundle -> ssh
deploy/
  CICD.md                        # create: operator setup and drills
  production/
    compose.yaml                 # modify: registry images, project name
    compose.build.yaml           # create: local build override
    images.env.example           # create: digest fixture for validation
    env.example                  # modify: GHCR + images notes
    README.md                    # modify: CI and rollback paths
  scripts/
    bootstrap-host.sh            # create
    deploy.sh                    # create
    healthcheck.sh               # create
    rollback.sh                  # create
    validate-release.sh          # create
    test-compose-config.sh       # create
    test-deploy.sh               # create
    test-bootstrap.sh            # create
    test-workflows.sh            # create
README.md                        # modify: CI/CD section and link
```

Scripts own host behavior; workflows own orchestration; docs own operator setup.

---

### Task 1: Registry-image production Compose and build override

**Files:**
- Modify: `deploy/production/compose.yaml`
- Create: `deploy/production/compose.build.yaml`
- Create: `deploy/production/images.env.example`
- Modify: `deploy/production/env.example`
- Test: `deploy/scripts/test-compose-config.sh`

**Interfaces:**
- Produces: `compose.yaml` reads `O1_BACKEND_IMAGE` (default `o1-backend:local`), `O1_WEB_IMAGE` (default `o1-web:local`), and `O1_COMPOSE_PROJECT` (default `o1-production`).
- Produces: `compose.build.yaml` adds `build.context` for `api`, `scheduler`, `worker`, and `web`.
- Produces: `images.env.example` with 64-hex digest placeholders.

- [ ] **Step 1: Write the failing Compose validation test**

Create `deploy/scripts/test-compose-config.sh` (executable). It resolves the repo root from the script path, then uses `docker compose config --format json` and `python3` to assert:

```bash
# Base file has no build contexts.
! grep -Eq '^[[:space:]]+build:' deploy/production/compose.yaml

# Registry config uses the fixture digests and the default project name.
docker compose --env-file deploy/production/env.example \
  --env-file deploy/production/images.env.example \
  -f deploy/production/compose.yaml config --format json \
  | python3 -c '
import json, sys
c = json.load(sys.stdin)
assert c["name"] == "o1-production", c["name"]
assert c["services"]["api"]["image"].startswith("ghcr.io/example/o1-backend@sha256:")
assert c["services"]["scheduler"]["image"] == c["services"]["api"]["image"]
assert c["services"]["worker"]["image"] == c["services"]["api"]["image"]
assert c["services"]["web"]["image"].startswith("ghcr.io/example/o1-web@sha256:")
'

# Project name is overridable.
O1_COMPOSE_PROJECT=o1-staging docker compose --env-file deploy/production/env.example \
  --env-file deploy/production/images.env.example \
  -f deploy/production/compose.yaml config --format json \
  | python3 -c 'import json,sys; assert json.load(sys.stdin)["name"] == "o1-staging"'

# Build override adds contexts and keeps local image names.
docker compose --env-file deploy/production/env.example \
  -f deploy/production/compose.yaml -f deploy/production/compose.build.yaml \
  config --format json \
  | python3 -c '
import json, sys
c = json.load(sys.stdin)
assert c["services"]["api"]["image"] == "o1-backend:local"
assert "context" in c["services"]["api"]["build"]
assert "context" in c["services"]["web"]["build"]
'
```

Use the real `docker compose` command in each placeholder above; only the arguments after `docker compose` vary.

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash deploy/scripts/test-compose-config.sh`
Expected: FAIL because the base file still has `build:` and no image variables.

- [ ] **Step 3: Convert the base Compose file**

In `deploy/production/compose.yaml`:

- Change `name: o1-production` to `name: ${O1_COMPOSE_PROJECT:-o1-production}`.
- Replace each app service's `build:` block with:
  - `api`, `scheduler`, `worker`: `image: ${O1_BACKEND_IMAGE:-o1-backend:local}`
  - `web`: `image: ${O1_WEB_IMAGE:-o1-web:local}`
- Keep entrypoints, environment, ports, volumes, healthchecks, and the Alloy service unchanged.

Compose interpolates `:?` in the base file before merging overrides, so the defaults are `:-`; `deploy.sh` enforces digest-pinned refs before deploying.

- [ ] **Step 4: Add the build override and fixture**

Create `deploy/production/compose.build.yaml`:

```yaml
services:
  api:
    build:
      context: ../../backend
  scheduler:
    build:
      context: ../../backend
  worker:
    build:
      context: ../../backend
  web:
    build:
      context: ../../web
      args:
        VITE_SENTRY_DSN: ${VITE_SENTRY_DSN:-}
        VITE_RELEASE: ${VITE_RELEASE:-production}
```

Create `deploy/production/images.env.example`:

```sh
# Fixture used by config validation and documentation. Deploys write real
# digests to /opt/o1/<release>/images.env; never put this file on a host as-is.
O1_BACKEND_IMAGE=ghcr.io/example/o1-backend@sha256:0000000000000000000000000000000000000000000000000000000000000000
O1_WEB_IMAGE=ghcr.io/example/o1-web@sha256:0000000000000000000000000000000000000000000000000000000000000000
```

- [ ] **Step 5: Update `env.example`**

Add a short section:

```sh
# --- Registry pull credentials (private GHCR packages only) --------------
# The image refs are not set here. They live in the host images.env written by
# the deploy script. Keep private-package credentials out of git.
# GHCR_USER=your-github-user
# GHCR_READ_TOKEN=ghp_token_with_read_packages
```

Also note that `docker compose ... -f compose.build.yaml` builds locally from a checkout.

- [ ] **Step 6: Run the test to verify it passes**

Run: `bash deploy/scripts/test-compose-config.sh`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add deploy/production/compose.yaml deploy/production/compose.build.yaml \
  deploy/production/images.env.example deploy/production/env.example \
  deploy/scripts/test-compose-config.sh
git commit -m "feat: deploy production stack from registry images"
```

---

### Task 2: Host deploy, rollback, health, and release validation scripts

**Files:**
- Create: `deploy/scripts/healthcheck.sh`
- Create: `deploy/scripts/validate-release.sh`
- Create: `deploy/scripts/deploy.sh`
- Create: `deploy/scripts/rollback.sh`
- Test: `deploy/scripts/test-deploy.sh`

**Interfaces:**
- `healthcheck.sh URL [TIMEOUT_SECONDS] [INTERVAL_SECONDS]` — defaults 300 and 5; exits 0 only after a 2xx response.
- `validate-release.sh ENVIRONMENT VERSION WORKFLOW_REF` — exits 0 when allowed; 2 for bad arguments/version, 3 for production pre-release, 4 for production non-`main` ref.
- `deploy.sh INCOMING_DIR [--dry-run]` — `O1_ROOT` defaults to `/opt/o1`; reads `O1_DOMAIN`, `GHCR_USER`, `GHCR_READ_TOKEN` from `$O1_ROOT/.env`.
- `rollback.sh [RELEASE_ID]` — no argument targets `previous`; an argument targets `releases/<id>`.
- All four write nothing to stdout containing credentials.

- [ ] **Step 1: Write the failing script test**

Create `deploy/scripts/test-deploy.sh` (executable). It builds a temporary root and a fake `PATH` containing `docker`, `curl`, and `flock`:

- `docker` logs every invocation to `$FAKE_DIR/docker.log`; `compose ... config --quiet` succeeds; `compose ... pull` exits `${FAKE_PULL_EXIT:-0}`; `compose ... up`, `ps`, and `logs` succeed; `login ... --password-stdin` saves stdin to `$FAKE_DIR/login.stdin` and succeeds.
- `curl` appends its URL to `$FAKE_DIR/curl.log` and exits `${FAKE_CURL_EXIT:-0}`.
- `flock` exits `${FAKE_FLOCK_EXIT:-0}`.

Seed the root environment before the deploy cases:

```bash
mkdir -p "$ROOT/state"
printf 'O1_DOMAIN=o1.example.com\n' > "$ROOT/.env"
chmod 600 "$ROOT/.env"
```

Seed helper:

```bash
HEX64=$(printf 'a%.0s' {1..64})
seed_release() { # seed_release NAME
  local dir="$ROOT/releases/$1"
  mkdir -p "$dir/deploy/production" "$dir/deploy/observability"
  cp deploy/production/compose.yaml "$dir/deploy/production/"
  cp deploy/observability/alloy.alloy "$dir/deploy/observability/"
  printf 'O1_BACKEND_IMAGE=ghcr.io/example/o1-backend@sha256:%s\nO1_WEB_IMAGE=ghcr.io/example/o1-web@sha256:%s\n' \
    "$HEX64" "$HEX64" > "$dir/images.env"
}
```

Assertions, each printing `PASS` or failing the script:

1. `validate-release.sh production v1.2.3 refs/heads/main` → 0; `production v1.2.3-rc.1 refs/heads/main` → 3; `production v1.2.3 refs/heads/feature` → 4; `staging v1.2.3-rc.1 refs/heads/main` → 0; `bogus ...` → 2.
2. Successful deploy: seed `old`; make `current -> releases/old`; run `deploy.sh incoming/release-new`; assert exit 0, `current` resolves to `releases/release-new`, `previous` resolves to `releases/old`, and `curl.log` contains `https://o1.example.com/readyz`.
3. Unhealthy deploy: `FAKE_CURL_EXIT=1`; assert exit 1, `current` resolves back to `releases/old`, and `docker.log` contains two `compose ... up` invocations.
4. Invalid images: remove `images.env` or corrupt the digest; assert exit 1, `current` unchanged, and no `pull` in `docker.log`.
5. First deploy: no `current`; `FAKE_CURL_EXIT=1`; assert exit 1, `current` resolves to the new release, and `previous` is absent.
6. Dry run: `deploy.sh incoming/release-new --dry-run`; assert exit 0, `current` unchanged, and `releases/release-new` absent.
7. Lock busy: `FAKE_FLOCK_EXIT=1`; assert exit 3 and `current` unchanged.
8. Rollback: seed `current -> releases/new`, `previous -> releases/old`; run `rollback.sh`; assert `current -> releases/old` and `previous -> releases/new`.
9. Credential hygiene: put `GHCR_USER` and `GHCR_READ_TOKEN=super-secret` in `.env`; assert `login.stdin` contains the token and `docker.log` does not.

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash deploy/scripts/test-deploy.sh`
Expected: FAIL because the scripts do not exist.

- [ ] **Step 3: Implement `healthcheck.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail
url=${1:?usage: healthcheck.sh URL [TIMEOUT_SECONDS] [INTERVAL_SECONDS]}
timeout=${2:-300}
interval=${3:-5}
deadline=$((SECONDS + timeout))
until curl -fsS --max-time 5 "$url" >/dev/null 2>&1; do
  (( SECONDS < deadline )) || { echo "healthcheck: $url not ready after ${timeout}s" >&2; exit 1; }
  sleep "$interval"
done
```

- [ ] **Step 4: Implement `validate-release.sh`**

Usage validation and checks:

```bash
[[ ${1:-} =~ ^(staging|production)$ ]] || exit 2
[[ ${2:-} =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || exit 2
if [[ $1 == production ]]; then
  [[ $2 != *-* ]] || exit 3
  [[ ${3:-} == refs/heads/main ]] || exit 4
fi
```

- [ ] **Step 5: Implement `deploy.sh`**

Required behavior:

- `set -euo pipefail`, `umask 077`.
- Read only the keys it needs with a helper that greps `^KEY=` from `$O1_ROOT/.env` and strips one layer of single/double quotes; do not `source` the file.
- Require `docker`, `curl`, `flock`, `O1_ROOT/.env`, and an incoming directory containing `deploy/production/compose.yaml`, `deploy/observability/alloy.alloy`, and `images.env`.
- Require both image refs to match `^.+@sha256:[0-9a-f]{64}$`.
- Take an exclusive lock: `exec 9>"$O1_ROOT/state/deploy.lock"; flock -n 9 || exit 3`.
- Validate with `docker compose --env-file "$O1_ROOT/.env" --env-file "$incoming/images.env" -f "$incoming/deploy/production/compose.yaml" config --quiet`.
- On `--dry-run`, print the planned release, compose command, and health URL, then exit 0 without moving files or links.
- Release id is `$(basename "$incoming")`; refuse if `$O1_ROOT/releases/$id` already exists.
- Move the bundle to `releases/$id`.
- Atomically switch links with a temporary symlink and `mv -f`:

```bash
link() { # link TARGET LINKPATH
  rm -f "$2.new"
  ln -s "$1" "$2.new"
  mv -f "$2.new" "$2"
}
```

  Save the old `current` target as `previous` before pointing `current` at the new release.
- Optional GHCR login before pull:

```bash
if [[ -n $ghcr_user && -n $ghcr_token ]]; then
  printf '%s' "$ghcr_token" | docker login ghcr.io -u "$ghcr_user" --password-stdin >/dev/null
fi
```

- Pull `api` and `web` (they cover both images), then `up -d --remove-orphans`.
- Health check `https://$domain/readyz` with timeout `${DEPLOY_HEALTH_TIMEOUT:-300}`, taken from the environment or `.env`.
- On failure: `logs --tail=100`, restore `current` to the saved target if one exists, `up -d --remove-orphans` on it, health-check again, exit 1. If no target exists, leave the new release active, report no rollback target, exit 1.
- On success: `compose ps` and print the two digest refs.
- Never echo `ghcr_token` or the contents of `.env`.

- [ ] **Step 6: Implement `rollback.sh`**

- Same `flock`, `.env` parsing, image validation, compose helpers, and health check.
- Target is `releases/$1` when given, otherwise the `previous` symlink target.
- Refuse when the target is missing or `current` is absent.
- Validate the target's compose config, switch `current` to the target, set `previous` to the release that was current, run `pull`/`up`, and health-check.
- On health failure, restore the pre-rollback `current` and bring it up; exit 1.
- Print the active release and digests on success.

- [ ] **Step 7: Run the tests to verify they pass**

Run:

```bash
bash deploy/scripts/test-deploy.sh
bash -n deploy/scripts/*.sh
docker run --rm -v "$PWD:/repo" -w /repo koalaman/shellcheck:v0.11.0 deploy/scripts/*.sh
```

Expected: all PASS; no shellcheck findings.

- [ ] **Step 8: Commit**

```bash
git add deploy/scripts/healthcheck.sh deploy/scripts/validate-release.sh \
  deploy/scripts/deploy.sh deploy/scripts/rollback.sh deploy/scripts/test-deploy.sh
git commit -m "feat: add host deploy and rollback scripts"
```

---

### Task 3: Host bootstrap script

**Files:**
- Create: `deploy/scripts/bootstrap-host.sh`
- Test: `deploy/scripts/test-bootstrap.sh`

**Interfaces:**
- `bootstrap-host.sh [--root DIR] [--source DIR]` — defaults `/opt/o1` and the repository root inferred from the script path.
- Consumes: `deploy/production/compose.yaml`, `deploy/production/env.example`, and the four scripts from Task 2.
- Produces: `$ROOT/{bin,releases,incoming,state}`, installed scripts, `.env` (mode 0600) when absent.

- [ ] **Step 1: Write the failing bootstrap test**

Create `deploy/scripts/test-bootstrap.sh` with a fake `docker` on `PATH` and a temporary source tree containing `deploy/production/{compose.yaml,env.example}`, `deploy/observability/alloy.alloy`, and copies of the scripts. Assert:

- First run creates `bin`, `releases`, `incoming`, `state` with mode 0700.
- `bin/deploy.sh`, `bin/rollback.sh`, `bin/healthcheck.sh`, and `bin/validate-release.sh` exist and are executable.
- `.env` exists, is mode 0600, and matches `env.example`.
- A second run does not overwrite an edited `.env`.
- A missing source tree exits nonzero without creating a partial root.
- When `GHCR_USER`/`GHCR_READ_TOKEN` are in `.env`, the fake `docker login` receives the token on stdin, not argv.

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash deploy/scripts/test-bootstrap.sh`
Expected: FAIL because `bootstrap-host.sh` does not exist.

- [ ] **Step 3: Implement `bootstrap-host.sh`**

- Parse `--root` and `--source`; validate the source contains the expected files before creating anything.
- Require `docker`, `docker compose`, `curl`, and `flock`; fail with a readable message.
- `install -d -m 0700` the four directories.
- `install -m 0755` the four scripts into `$ROOT/bin`.
- If `$ROOT/.env` is absent, `install -m 0600 "$source/deploy/production/env.example" "$ROOT/.env"` and print a warning to edit it.
- Validate compose without persisting image refs:

```bash
O1_BACKEND_IMAGE="example.invalid/o1-backend@sha256:$(printf '0%.0s' {1..64})" \
O1_WEB_IMAGE="example.invalid/o1-web@sha256:$(printf '0%.0s' {1..64})" \
  docker compose --env-file "$root/.env" \
    -f "$source/deploy/production/compose.yaml" config --quiet
```

- Optional GHCR login using the same stdin pattern as `deploy.sh`.
- Print the next steps (edit `.env`, dispatch a release deploy, or use the build override from a checkout). Do not start the stack.

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash deploy/scripts/test-bootstrap.sh && docker run --rm -v "$PWD:/repo" -w /repo koalaman/shellcheck:v0.11.0 deploy/scripts/bootstrap-host.sh`
Expected: PASS, no shellcheck findings.

- [ ] **Step 5: Commit**

```bash
git add deploy/scripts/bootstrap-host.sh deploy/scripts/test-bootstrap.sh
git commit -m "feat: add host bootstrap script"
```

---

### Task 4: Reusable CI and tag-driven scan/publish

**Files:**
- Modify: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`
- Create: `.github/dependabot.yml`
- Test: `deploy/scripts/test-workflows.sh`

**Interfaces:**
- `ci.yml` gains `workflow_call:` and a `govulncheck` step; existing job names stay `backend`, `integration`, `frontend`, `e2e`.
- `release.yml` publishes `ghcr.io/<owner>/o1-{backend,web}:<version>`, `:sha-<12>`, and `:<major>.<minor>`.
- Publish order: build both images for scanning, scan both, push both. No push before both scans pass.

- [ ] **Step 1: Write the failing workflow structure test**

Create `deploy/scripts/test-workflows.sh` (executable) that asserts with `grep`/`awk`:

- `ci.yml` contains `workflow_call:` and `govulncheck`.
- `release.yml` exists, triggers on `v*`, has `packages: write`, uses `aquasecurity/setup-trivy`, runs two `trivy image` commands before the first `--push`, and names both image repositories.
- `dependabot.yml` contains `github-actions`, `gomod`, and `npm`.

Run: `bash deploy/scripts/test-workflows.sh`
Expected: FAIL because `release.yml` and `dependabot.yml` do not exist.

- [ ] **Step 2: Make `ci.yml` reusable and add govulncheck**

- Add `workflow_call:` beside `push:` and `pull_request:`.
- In the `backend` job, after `go vet`, add:

```yaml
- name: govulncheck
  run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

- Leave every existing job, step, and version unchanged.

- [ ] **Step 3: Create `release.yml`**

Trigger and permissions:

```yaml
name: Release
on:
  push:
    tags: ["v*"]
permissions:
  contents: read
  packages: write
concurrency:
  group: release-${{ github.ref }}
  cancel-in-progress: false
```

Jobs:

```yaml
jobs:
  verify:
    uses: ./.github/workflows/ci.yml
  publish:
    needs: verify
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v4.4.1
      - uses: docker/login-action@v4.8.0
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Prepare tags
        run: |
          owner=$(printf '%s' "${{ github.repository_owner }}" | tr '[:upper:]' '[:lower:]')
          echo "REGISTRY=ghcr.io/$owner" >> "$GITHUB_ENV"
          echo "VERSION=${GITHUB_REF_NAME}" >> "$GITHUB_ENV"
          echo "SHORT_SHA=${GITHUB_SHA::12}" >> "$GITHUB_ENV"
          no_v=${GITHUB_REF_NAME#v}
          echo "MAJOR_MINOR=${no_v%.*}" >> "$GITHUB_ENV"
```

Install Trivy and scan the repository before building images:

```yaml
- uses: aquasecurity/setup-trivy@v0.3.1
  with:
    version: v0.76.2
- name: Scan repository secrets and misconfiguration
  run: trivy fs --scanners secret,misconfig --exit-code 1 .
```

Then run these steps in order. Both scans must succeed before either push:

```yaml
- name: Build backend for scan
  run: >
    docker buildx build --load --platform linux/amd64
    --cache-from type=gha,scope=backend --cache-to type=gha,mode=max,scope=backend
    -t "$REGISTRY/o1-backend:$VERSION" -f backend/Dockerfile backend
- name: Scan backend image
  run: trivy image --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 "$REGISTRY/o1-backend:$VERSION"

- name: Build web for scan
  run: >
    docker buildx build --load --platform linux/amd64
    --cache-from type=gha,scope=web --cache-to type=gha,mode=max,scope=web
    --build-arg VITE_SENTRY_DSN="${{ vars.VITE_SENTRY_DSN }}"
    --build-arg VITE_RELEASE="$VERSION"
    -t "$REGISTRY/o1-web:$VERSION" -f web/Dockerfile web
- name: Scan web image
  run: trivy image --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 "$REGISTRY/o1-web:$VERSION"

- name: Push backend
  run: >
    docker buildx build --push --platform linux/amd64 --sbom=true --provenance=true
    --cache-from type=gha,scope=backend --cache-to type=gha,mode=max,scope=backend
    -t "$REGISTRY/o1-backend:$VERSION"
    -t "$REGISTRY/o1-backend:sha-$SHORT_SHA"
    -t "$REGISTRY/o1-backend:$MAJOR_MINOR"
    -f backend/Dockerfile backend
- name: Push web
  run: >
    docker buildx build --push --platform linux/amd64 --sbom=true --provenance=true
    --cache-from type=gha,scope=web --cache-to type=gha,mode=max,scope=web
    --build-arg VITE_SENTRY_DSN="${{ vars.VITE_SENTRY_DSN }}"
    --build-arg VITE_RELEASE="$VERSION"
    -t "$REGISTRY/o1-web:$VERSION"
    -t "$REGISTRY/o1-web:sha-$SHORT_SHA"
    -t "$REGISTRY/o1-web:$MAJOR_MINOR"
    -f web/Dockerfile web

- name: Record scan results
  run: |
    {
      echo "### Container scans"
      echo "- backend: HIGH/CRITICAL scan passed before push"
      echo "- web: HIGH/CRITICAL scan passed before push"
    } >> "$GITHUB_STEP_SUMMARY"
- name: Summarize published images
  run: |
    for image in o1-backend o1-web; do
      digest=$(docker buildx imagetools inspect "$REGISTRY/$image:$VERSION" --format '{{.Manifest.Digest}}')
      echo "- \`$REGISTRY/$image@$digest\`" >> "$GITHUB_STEP_SUMMARY"
    done
```

- [ ] **Step 4: Create `dependabot.yml`**

```yaml
version: 2
updates:
  - package-ecosystem: github-actions
    directory: /
    schedule: { interval: weekly }
  - package-ecosystem: gomod
    directory: /backend
    schedule: { interval: weekly }
  - package-ecosystem: npm
    directory: /web
    schedule: { interval: weekly }
```

- [ ] **Step 5: Run the structure test and actionlint**

```bash
bash deploy/scripts/test-workflows.sh
docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.12
```

Expected: PASS and no actionlint findings.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/ci.yml .github/workflows/release.yml \
  .github/dependabot.yml deploy/scripts/test-workflows.sh
git commit -m "ci: publish scanned images to GHCR on release"
```

---

### Task 5: Environment-gated deploy workflow

**Files:**
- Create: `.github/workflows/deploy.yml`
- Modify: `deploy/scripts/test-workflows.sh`

**Interfaces:**
- Consumes: `release.yml` image tags, `validate-release.sh`, the host `deploy.sh`, and the Environment secrets from the spec.
- Inputs: `environment` (`staging` or `production`), `version` (`v*` tag), `dry_run` (boolean).
- Produces: a bundle at `/opt/o1/incoming/<version>-<sha>/` and an SSH call to `/opt/o1/bin/deploy.sh`.

- [ ] **Step 1: Extend the failing workflow test**

Add assertions that `deploy.yml`:

- triggers on `workflow_dispatch` with `environment`, `version`, and `dry_run` inputs;
- uses `environment: ${{ inputs.environment }}` and `concurrency: deploy-${{ inputs.environment }}`;
- has only `contents: read` and `packages: read`;
- checks out `ref: ${{ inputs.version }}`;
- runs `deploy/scripts/validate-release.sh` with the environment, version, and `github.ref`;
- logs in to GHCR, resolves both digests, copies the bundle with `scp`, and runs `/opt/o1/bin/deploy.sh`;
- passes `--dry-run` when requested.

Run: `bash deploy/scripts/test-workflows.sh`
Expected: FAIL because `deploy.yml` does not exist.

- [ ] **Step 2: Create `deploy.yml`**

```yaml
name: Deploy
on:
  workflow_dispatch:
    inputs:
      environment:
        type: choice
        options: [staging, production]
        required: true
      version:
        description: "Release tag, for example v1.2.3"
        required: true
      dry_run:
        type: boolean
        default: false
permissions:
  contents: read
  packages: read
concurrency:
  group: deploy-${{ inputs.environment }}
  cancel-in-progress: false
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: ${{ inputs.environment }}
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.version }}
      - name: Validate release
        run: ./deploy/scripts/validate-release.sh "${{ inputs.environment }}" "${{ inputs.version }}" "${{ github.ref }}"
      - uses: docker/setup-buildx-action@v4.4.1
      - uses: docker/login-action@v4.8.0
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Resolve digests and write bundle
        run: |
          set -euo pipefail
          owner=$(printf '%s' "${{ github.repository_owner }}" | tr '[:upper:]' '[:lower:]')
          registry="ghcr.io/$owner"
          backend_digest=$(docker buildx imagetools inspect "$registry/o1-backend:${{ inputs.version }}" --format '{{.Manifest.Digest}}')
          web_digest=$(docker buildx imagetools inspect "$registry/o1-web:${{ inputs.version }}" --format '{{.Manifest.Digest}}')
          release_id="${{ inputs.version }}-$(git rev-parse --short=12 HEAD)"
          mkdir -p "bundle/deploy/production" "bundle/deploy/observability"
          cp deploy/production/compose.yaml bundle/deploy/production/
          cp deploy/observability/alloy.alloy bundle/deploy/observability/
          printf 'O1_BACKEND_IMAGE=%s/o1-backend@%s\nO1_WEB_IMAGE=%s/o1-web@%s\n' \
            "$registry" "$backend_digest" "$registry" "$web_digest" > bundle/images.env
          echo "RELEASE_ID=$release_id" >> "$GITHUB_ENV"
```

SSH steps:

```yaml
      - uses: webfactory/ssh-agent@v0.10.0
        with:
          ssh-private-key: ${{ secrets.SSH_PRIVATE_KEY }}
      - name: Install known hosts
        run: |
          mkdir -p ~/.ssh
          printf '%s\n' "${{ secrets.SSH_KNOWN_HOSTS }}" > ~/.ssh/known_hosts
          chmod 600 ~/.ssh/known_hosts
      - name: Upload bundle and deploy
        env:
          SSH_HOST: ${{ secrets.SSH_HOST }}
          SSH_USER: ${{ secrets.SSH_USER }}
          SSH_PORT: ${{ vars.SSH_PORT || '22' }}
        run: |
          set -euo pipefail
          ssh -p "$SSH_PORT" -o StrictHostKeyChecking=yes "$SSH_USER@$SSH_HOST" \
            "mkdir -p /opt/o1/incoming/$RELEASE_ID"
          scp -P "$SSH_PORT" -r bundle/. "$SSH_USER@$SSH_HOST:/opt/o1/incoming/$RELEASE_ID/"
          flag=""
          if [[ "${{ inputs.dry_run }}" == "true" ]]; then flag="--dry-run"; fi
          ssh -p "$SSH_PORT" -o StrictHostKeyChecking=yes "$SSH_USER@$SSH_HOST" \
            "/opt/o1/bin/deploy.sh /opt/o1/incoming/$RELEASE_ID $flag"
      - name: Summarize deployment
        run: |
          {
            echo "### Deploy"
            echo "- environment: \`${{ inputs.environment }}\`"
            echo "- version: \`${{ inputs.version }}\`"
            echo "- release: \`$RELEASE_ID\`"
          } >> "$GITHUB_STEP_SUMMARY"
```

The workflow must resolve the release commit with `git rev-parse --short=12 HEAD` after checkout, because `GITHUB_SHA` in a `workflow_dispatch` is the dispatch ref's commit, not the checked-out tag's commit.

- [ ] **Step 3: Run the structure test and actionlint**

```bash
bash deploy/scripts/test-workflows.sh
docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.12
```

Expected: PASS and no actionlint findings.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/deploy.yml deploy/scripts/test-workflows.sh
git commit -m "ci: add environment-gated deploy workflow"
```

---

### Task 6: Operator docs and final verification

**Files:**
- Create: `deploy/CICD.md`
- Modify: `deploy/production/README.md`
- Modify: `README.md`

**Interfaces:**
- Consumes everything from Tasks 1–5.
- Produces: the operator setup path, release/rollback drill, and updated manual-deploy commands.

- [ ] **Step 1: Write `deploy/CICD.md`**

Cover, in this order:

1. Pipeline overview: PR checks → tag release → scan/publish → environment-gated deploy.
2. One-time GitHub setup: create/attach the remote, push `main`, create `staging` and `production` Environments, add the `SSH_*` secrets and `SSH_PORT` variable, set `VITE_SENTRY_DSN` if used.
3. Branch and tag protection: require PRs and the four checks (`backend`, `integration`, `frontend`, `e2e`), linear history, no force pushes; restrict `v*` tags; production Environment requires reviewers and the `main` ref.
4. GHCR: private packages by default; how to make them public; `GHCR_USER`/`GHCR_READ_TOKEN` on the host for private packages.
5. Host setup: Docker/Compose, firewall 80/443, run `bootstrap-host.sh`, edit `/opt/o1/.env`, optional GHCR login.
6. Release process: merge → `vX.Y.Z-rc.1` → staging deploy → rollback drill → `vX.Y.Z` → production deploy with approval.
7. Manual recovery: `rollback.sh`, build override on a checkout, re-running `/opt/o1/bin/deploy.sh`.
8. Migrations: startup-applied, forward-only, rollback does not undo schema, review migrations before tagging.
9. Observability: Alloy profile is untouched by deploys; restart it explicitly after a config change.
10. Troubleshooting: SSH host key, GHCR pull denial, failed readiness, migration failure, scan blocks, missing `.env`.
11. Security: no laptop deploys, secrets never in git/images, token scopes, host-key pinning, `.trivyignore` policy.
12. Live acceptance checklist from spec §10.

- [ ] **Step 2: Update the production runbook**

Add a "CI/CD deploys" section near the top linking to `deploy/CICD.md`, and:

- Change the manual first-deploy command to use the build override:
  `docker compose --env-file deploy/production/.env -f deploy/production/compose.yaml -f deploy/production/compose.build.yaml up -d --build`.
- Document registry deploys as `images.env` plus the base compose file.
- Add a rollback section using `/opt/o1/bin/rollback.sh` and explain the release-directory model.
- State that image/config rollback does not undo migrations.
- Keep the existing Resend, TLS, and troubleshooting content.

- [ ] **Step 3: Update the root README**

- Replace the sentence that says CI deployment is Plan 11 with the current pipeline description and a link to `deploy/CICD.md`.
- Add a short "CI/CD" subsection listing what a `v*` tag publishes and how deploys are dispatched.
- Keep the manual production section, noting the build override for manual deploys.

- [ ] **Step 4: Run the complete verification**

```bash
docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.12
docker run --rm -v "$PWD:/repo" -w /repo koalaman/shellcheck:v0.11.0 deploy/scripts/*.sh
bash -n deploy/scripts/*.sh
bash deploy/scripts/test-compose-config.sh
bash deploy/scripts/test-deploy.sh
bash deploy/scripts/test-bootstrap.sh
bash deploy/scripts/test-workflows.sh
cd backend && go test -short ./...
cd ../web && npm test -- --run
```

Expected: every command exits 0. The workflow publish/deploy jobs remain unverified against a live remote; record that in the report.

- [ ] **Step 5: Commit**

```bash
git add deploy/CICD.md deploy/production/README.md README.md
git commit -m "docs: document CI/CD setup and go-live"
```

---

## Self-Review Checklist

- [ ] Every spec section maps to a task: Compose images (§7.5–7.6) → Task 1; deploy/rollback/health/validation (§7.6–7.7) → Task 2; bootstrap (§7.7) → Task 3; CI/release/scans (§7.1–7.4) → Task 4; deploy workflow (§7.8) → Task 5; staging, secrets, protection, migrations, docs, acceptance (§7.9–7.13, §10) → Task 6.
- [ ] Review Focus items have tests: inputs → Task 2; unhealthy/first deploy → Task 2; lock → Task 2; production dispatch → Task 3 + Task 5; scan/secret → Tasks 2 and 4.
- [ ] Names/signatures match: `deploy.sh`, `rollback.sh`, `healthcheck.sh`, `validate-release.sh`, `bootstrap-host.sh`, `O1_ROOT`, `O1_BACKEND_IMAGE`, `O1_WEB_IMAGE`, `O1_COMPOSE_PROJECT`, `images.env`, `current`, `previous`.
- [ ] No task depends on a later task without saying so; each ends with a passing check and a commit.
- [ ] The plan does not add backups, retention, Kubernetes, signing, or app-code changes.
