#!/usr/bin/env bash
# shellcheck disable=SC2016  # grep patterns intentionally contain $VAR literally
# Structural checks for the GitHub Actions workflows.
#
# The release path (tag -> verify -> build -> scan -> push to GHCR) cannot be
# exercised locally, so these assertions pin the pieces that make it safe:
# CI stays reusable and off the publish path, images are scanned before any
# push, tags follow the documented scheme, and dependencies stay monitored.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

ci_file=".github/workflows/ci.yml"
release_file=".github/workflows/release.yml"
deploy_file=".github/workflows/deploy.yml"
dependabot_file=".github/dependabot.yml"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

checks=0
check() {
  checks=$((checks + 1))
  echo "ok $checks - $*"
}

# --- ci.yml: reusable, unchanged jobs, govulncheck -------------------------

[[ -f $ci_file ]] || fail "$ci_file is missing"

grep -Eq '^[[:space:]]+workflow_call:' "$ci_file" ||
  fail "$ci_file does not declare workflow_call trigger"
check "$ci_file is reusable via workflow_call"

for job in backend integration frontend e2e pipeline; do
  grep -Eq "^  ${job}:" "$ci_file" || fail "$ci_file lost the '$job' job"
done
check "$ci_file keeps the backend, integration, frontend, e2e and pipeline jobs"

grep -Eq 'govulncheck@v1\.9\.0 \./\.\.\.' "$ci_file" ||
  fail "$ci_file does not run govulncheck"
grep -Fq 'name: govulncheck (advisory)' "$ci_file" ||
  fail "$ci_file does not mark govulncheck advisory"
grep -A1 -F 'name: govulncheck (advisory)' "$ci_file" |
  grep -Fq 'continue-on-error: true' ||
  fail "$ci_file does not let advisory govulncheck continue on error"
check "$ci_file runs govulncheck as an advisory gate"

# The local lint and script suites are a required CI job, not just docs.
grep -Fq 'rhysd/actionlint:1.7.12' "$ci_file" ||
  fail "$ci_file does not run actionlint"
grep -Fq 'koalaman/shellcheck:v0.11.0 deploy/scripts/*.sh' "$ci_file" ||
  fail "$ci_file does not run shellcheck over the deploy scripts"
for test in test-compose-config test-deploy test-bootstrap test-workflows; do
  grep -Fq "bash deploy/scripts/${test}.sh" "$ci_file" ||
    fail "$ci_file does not run ${test}.sh"
done
check "$ci_file runs actionlint, shellcheck and the script suites"

# The backup suite needs restic on PATH and a reachable Docker daemon, so the
# pipeline job installs the pinned release before running it.
grep -Fq 'restic_0.19.1_linux_amd64.bz2' "$ci_file" ||
  fail "$ci_file does not download restic 0.19.1"
grep -Fq 'restic version' "$ci_file" ||
  fail "$ci_file does not verify the installed restic"
grep -Fq 'bash deploy/scripts/test-backup.sh' "$ci_file" ||
  fail "$ci_file does not run test-backup.sh"
awk '
  /^  [a-z]+:/ { job = $1 }
  /restic_0\.19\.1_linux_amd64\.bz2/ { restic_job = job }
  /bash deploy\/scripts\/test-backup\.sh/ { backup_job = job }
  END { exit !(restic_job == "pipeline:" && backup_job == "pipeline:") }
' "$ci_file" || fail "restic and test-backup.sh are not both in the pipeline job"
check "$ci_file installs restic 0.19.1 and runs the backup suite in pipeline"

# govulncheck belongs to the backend job, after go vet.
awk '
  /^  [a-z]+:/ { job = $1 }
  /go vet \.\/\.\.\./ { vet = NR }
  /govulncheck/ { vuln = NR; vuln_job = job }
  END { exit !(vet && vuln && vuln > vet && vuln_job == "backend:") }
' "$ci_file" || fail "govulncheck is not in the backend job after go vet"
check "govulncheck runs in the backend job after go vet"

# --- release.yml: tag driven, scanned before publishing -------------------

[[ -f $release_file ]] || fail "$release_file is missing"

grep -Fq 'tags: ["v*"]' "$release_file" ||
  fail "$release_file does not trigger on v* tags"
check "$release_file triggers on v* tags"

grep -Eq '^[[:space:]]+packages: write$' "$release_file" ||
  fail "$release_file lacks packages: write permission"
check "$release_file can push packages"

grep -Eq '^[[:space:]]+contents: read$' "$release_file" ||
  fail "$release_file lacks contents: read permission"
check "$release_file keeps contents read-only"

grep -Fq 'uses: ./.github/workflows/ci.yml' "$release_file" ||
  fail "$release_file does not call the reusable CI workflow"
grep -Eq '^[[:space:]]+needs: verify$' "$release_file" ||
  fail "$release_file does not gate publishing on the verify job"
check "$release_file verifies with the reusable CI workflow"

grep -Fq 'aquasecurity/setup-trivy@v0.3.1' "$release_file" ||
  fail "$release_file does not install Trivy via aquasecurity/setup-trivy"
check "$release_file installs Trivy"

grep -Fq 'trivy fs --scanners secret,misconfig --severity HIGH,CRITICAL --exit-code 1 .' "$release_file" ||
  fail "$release_file does not scan the repository for HIGH/CRITICAL secrets and misconfiguration"
check "$release_file scans repository secrets and misconfiguration"

grep -Fq 'refusing malformed version tag' "$release_file" ||
  fail "$release_file does not reject malformed version tags"
grep -Fq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' "$release_file" ||
  fail "$release_file does not validate the version shape"
check "$release_file validates the version tag before publishing"

[[ -f .trivyignore ]] || fail ".trivyignore is missing"
grep -Fq 'DS-0002' .trivyignore ||
  fail ".trivyignore does not record the DS-0002 exception"
grep -Fq 'Re-review:' .trivyignore ||
  fail ".trivyignore exception has no review date"
check ".trivyignore records the DS-0002 exception with a review date"

first_push="$(grep -n -- '--push' "$release_file" | head -n 1 | cut -d: -f1 || true)"
[[ -n $first_push ]] || fail "$release_file never pushes an image"

scans_before_push="$(
  awk -v stop="$first_push" 'NR < stop && /trivy image/ { n++ } END { print n + 0 }' "$release_file"
)"
[[ $scans_before_push -eq 2 ]] ||
  fail "expected 2 image scans before the first --push, found $scans_before_push"
check "both images are scanned before the first push"

for image in o1-backend o1-web; do
  grep -Eq "trivy image .*\\\$REGISTRY/${image}:" "$release_file" ||
    fail "$release_file does not scan the $image image"
done
check "$release_file scans the o1-backend and o1-web images"

grep -Fq 'trivy image --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1' "$release_file" ||
  fail "$release_file scans do not gate on HIGH/CRITICAL with --ignore-unfixed"
check "$release_file image scans gate on HIGH/CRITICAL"

for tag in '$VERSION' 'sha-$SHORT_SHA' '$MAJOR_MINOR'; do
  for image in o1-backend o1-web; do
    grep -Fq "\$REGISTRY/${image}:${tag}\"" "$release_file" ||
      fail "$release_file does not tag $image with $tag"
  done
done
check "release tags use version, sha-<12> and major.minor"

grep -Fq 'base=${no_v%%-*}' "$release_file" ||
  fail "$release_file does not strip the pre-release suffix before deriving X.Y"
grep -Fq 'MAJOR_MINOR=${base%.*}' "$release_file" ||
  fail "$release_file does not derive MAJOR_MINOR from the base version"
check "release derives X.Y from the base version, ignoring a pre-release suffix"

grep -Fq "tr '[:upper:]' '[:lower:]'" "$release_file" ||
  fail "$release_file does not lowercase the repository owner for GHCR"
check "release lowercases the repository owner for GHCR"

if ! grep -Eq 'sbom=true' "$release_file" ||
  ! grep -Eq 'provenance=true' "$release_file"; then
  fail "$release_file does not attach SBOM and provenance on push"
fi
check "release pushes with SBOM and provenance"

# --- deploy.yml: gated, digest pinned, dispatched workflow -----------------

[[ -f $deploy_file ]] || fail "$deploy_file is missing"

grep -Eq '^[[:space:]]+workflow_dispatch:' "$deploy_file" ||
  fail "$deploy_file does not trigger on workflow_dispatch"
if grep -Eq '^[[:space:]]+(push|pull_request|schedule|workflow_call):' "$deploy_file"; then
  fail "$deploy_file is not dispatch-only"
fi
check "$deploy_file is dispatch-only"

for input in environment version dry_run; do
  grep -Eq "^[[:space:]]+${input}:" "$deploy_file" ||
    fail "$deploy_file does not declare the '$input' dispatch input"
done
grep -Fq 'options: [staging, production]' "$deploy_file" ||
  fail "$deploy_file does not offer staging and production"
check "$deploy_file dispatches on environment, version and dry_run"

grep -Fq '    environment: ${{ inputs.environment }}' "$deploy_file" ||
  fail "$deploy_file does not gate the job on the selected environment"
check "$deploy_file deploys through the selected GitHub environment"

grep -Fq '  group: deploy-${{ inputs.environment }}' "$deploy_file" ||
  fail "$deploy_file does not serialize per-environment deploys"
check "$deploy_file serializes deploys per environment"

# The dispatch workflow may read source and packages, never write.
perm_count="$(
  awk '
    /^permissions:/ { in_perms = 1; next }
    in_perms && /^[^ ]/ { in_perms = 0 }
    in_perms && /^  [a-z-]+:/ { n++; keys = keys " " $1 }
    END { print n + 0 ":" keys }
  ' "$deploy_file"
)"
[[ $perm_count == "2: contents: packages:" ]] ||
  fail "expected only contents: read and packages: read, found ${perm_count#*:}"
grep -Eq '^[[:space:]]+contents: read$' "$deploy_file" ||
  fail "$deploy_file lacks contents: read"
grep -Eq '^[[:space:]]+packages: read$' "$deploy_file" ||
  fail "$deploy_file lacks packages: read"
if grep -Eq '^[[:space:]]+[a-z-]+: write$' "$deploy_file"; then
  fail "$deploy_file requests a write permission"
fi
check "$deploy_file holds only contents: read and packages: read"

grep -Fq 'ref: ${{ inputs.version }}' "$deploy_file" ||
  fail "$deploy_file does not check out the requested version"
grep -Fq 'fetch-depth: 0' "$deploy_file" ||
  fail "$deploy_file does not fetch full history for reachability checks"

checkout_line="$(grep -n 'uses: actions/checkout@v4' "$deploy_file" | head -n 1 | cut -d: -f1)"
validate_line="$(grep -n 'deploy/scripts/validate-release.sh' "$deploy_file" | head -n 1 | cut -d: -f1)"
[[ -n $checkout_line ]] || fail "$deploy_file does not check out the release tag"
[[ -n $validate_line ]] || fail "$deploy_file does not validate the release"
[[ $checkout_line -lt $validate_line ]] ||
  fail "$deploy_file validates before checking out the release tag"
check "$deploy_file checks out the release tag before validating"

grep -Fq 'ENVIRONMENT: ${{ inputs.environment }}' "$deploy_file" ||
  fail "$deploy_file does not pass the environment through env"
grep -Fq 'WORKFLOW_REF: ${{ github.ref }}' "$deploy_file" ||
  fail "$deploy_file does not pass the dispatch ref through env"
grep -Fq './deploy/scripts/validate-release.sh "$ENVIRONMENT" "$VERSION" "$WORKFLOW_REF"' "$deploy_file" ||
  fail "$deploy_file does not validate environment, version and dispatch ref"
check "$deploy_file validates the release against the dispatch ref"

# Production must prove the tag commit is on main before touching the host.
grep -Fq 'git fetch --no-tags origin main:refs/remotes/origin/main' "$deploy_file" ||
  fail "$deploy_file does not fetch origin/main for reachability"
grep -Fq 'git merge-base --is-ancestor HEAD origin/main' "$deploy_file" ||
  fail "$deploy_file does not require the tag to be an ancestor of main"
grep -Fq "if: inputs.environment == 'production'" "$deploy_file" ||
  fail "$deploy_file does not restrict the reachability check to production"
check "$deploy_file requires production tags to be reachable from main"

grep -Fq 'git rev-parse --short=12 HEAD' "$deploy_file" ||
  fail "$deploy_file does not resolve the release commit from the checkout"
if grep -Fq 'GITHUB_SHA' "$deploy_file"; then
  fail "$deploy_file uses GITHUB_SHA instead of the checked-out commit"
fi
grep -Fq 'release_id="$VERSION-$(git rev-parse --short=12 HEAD)-$RUN_ID-$RUN_ATTEMPT"' "$deploy_file" ||
  fail "$deploy_file does not make the release id unique per run attempt"
grep -Fq 'RUN_ID: ${{ github.run_id }}' "$deploy_file" ||
  fail "$deploy_file does not pass github.run_id"
grep -Fq 'RUN_ATTEMPT: ${{ github.run_attempt }}' "$deploy_file" ||
  fail "$deploy_file does not pass github.run_attempt"
check "$deploy_file derives a per-run release id from the checked-out tag"

grep -Fq 'uses: docker/login-action@v4.8.0' "$deploy_file" ||
  fail "$deploy_file does not log in to the registry"
grep -Fq 'registry: ghcr.io' "$deploy_file" ||
  fail "$deploy_file does not log in to ghcr.io"
grep -Fq "tr '[:upper:]' '[:lower:]'" "$deploy_file" ||
  fail "$deploy_file does not lowercase the repository owner for GHCR"
check "$deploy_file logs in to GHCR with a lowercase owner"

inspect_count="$(grep -c 'docker buildx imagetools inspect' "$deploy_file" || true)"
[[ $inspect_count -eq 2 ]] ||
  fail "expected 2 digest lookups, found $inspect_count"
for image in o1-backend o1-web; do
  grep -Fq "\$registry/${image}:" "$deploy_file" ||
    fail "$deploy_file does not resolve the $image tag"
  grep -Eq "$(echo "$image" | tr 'a-z-' 'A-Z_')_IMAGE=%s/${image}@%s" "$deploy_file" ||
    fail "$deploy_file does not pin $image by digest in images.env"
done
check "$deploy_file pins both images by resolved digest"

grep -Fq 'BACKEND_IMAGE=$registry/o1-backend@$backend_digest' "$deploy_file" ||
  fail "$deploy_file does not export the resolved backend image ref"
grep -Fq 'WEB_IMAGE=$registry/o1-web@$web_digest' "$deploy_file" ||
  fail "$deploy_file does not export the resolved web image ref"
grep -Fq 'echo "- backend: \`$BACKEND_IMAGE\`"' "$deploy_file" ||
  fail "$deploy_file summary does not include the backend image ref"
grep -Fq 'echo "- web: \`$WEB_IMAGE\`"' "$deploy_file" ||
  fail "$deploy_file summary does not include the web image ref"
check "$deploy_file summarizes both deployed image refs"

for artifact in 'deploy/production/compose.yaml' 'deploy/observability/alloy.alloy'; do
  grep -Fq "cp ${artifact} " "$deploy_file" ||
    fail "$deploy_file does not copy $artifact into the bundle"
done
grep -Fq 'bundle/images.env' "$deploy_file" ||
  fail "$deploy_file does not write bundle/images.env"
check "$deploy_file bundles compose.yaml, alloy.alloy and images.env"

grep -Fq 'uses: webfactory/ssh-agent@v0.10.0' "$deploy_file" ||
  fail "$deploy_file does not install the deploy key with webfactory/ssh-agent"
grep -Fq 'SSH_KNOWN_HOSTS' "$deploy_file" ||
  fail "$deploy_file does not install the pinned known hosts"
grep -Fq 'StrictHostKeyChecking=yes' "$deploy_file" ||
  fail "$deploy_file does not require strict host key checking"
grep -Fq "vars.SSH_PORT || '22'" "$deploy_file" ||
  fail "$deploy_file does not default SSH_PORT to 22"
for opt in 'BatchMode=yes' 'ConnectTimeout=10' 'ServerAliveInterval=15' 'ServerAliveCountMax=4'; do
  grep -Fq -- "-o $opt" "$deploy_file" ||
    fail "$deploy_file does not set SSH liveness option $opt"
done
grep -Fq 'timeout-minutes: 30' "$deploy_file" ||
  fail "$deploy_file does not bound the deploy job"
check "$deploy_file hardens SSH, bounds the job and defaults the port to 22"

grep -Fq 'mkdir -p /opt/o1/incoming/$RELEASE_ID' "$deploy_file" ||
  fail "$deploy_file does not create the incoming release directory"
grep -Fq 'scp -P "$SSH_PORT" "${ssh_opts[@]}" -r bundle/.' "$deploy_file" ||
  fail "$deploy_file does not upload the bundle with scp"
grep -Fq '/opt/o1/bin/deploy.sh /opt/o1/incoming/$RELEASE_ID $flag' "$deploy_file" ||
  fail "$deploy_file does not run the host deploy script with the dry-run flag"
check "$deploy_file uploads the bundle and runs the host deploy script"

grep -Fq 'flag="--dry-run"' "$deploy_file" ||
  fail "$deploy_file cannot pass --dry-run to the host deploy script"
grep -Fq 'DRY_RUN: ${{ inputs.dry_run }}' "$deploy_file" ||
  fail "$deploy_file does not read the dry_run input through env"
check "$deploy_file passes --dry-run when requested"

# Dispatch inputs, secrets and the caller ref must reach scripts through env,
# never by direct interpolation into a run: block (shell injection).
interpolated_run="$(
  awk '
    /^[[:space:]]*run:[[:space:]]*\|/ {
      in_run = 1
      run_indent = match($0, /[^ ]/) - 1
      next
    }
    in_run {
      line_indent = match($0, /[^ ]/) - 1
      if ($0 !~ /^[[:space:]]*$/ && line_indent <= run_indent) in_run = 0
    }
    /^[[:space:]]*run:/ && /\$\{\{[[:space:]]*(inputs\.|secrets\.|github\.ref)/ { print NR ": " $0 }
    in_run && /\$\{\{[[:space:]]*(inputs\.|secrets\.|github\.ref)/ { print NR ": " $0 }
  ' "$deploy_file"
)"
[[ -z $interpolated_run ]] ||
  fail "$deploy_file interpolates inputs/secrets/ref directly into a run: block: $interpolated_run"
check "$deploy_file passes dispatch values through env, not run: interpolation"

# --- dependabot.yml: monitored ecosystems --------------------------------

[[ -f $dependabot_file ]] || fail "$dependabot_file is missing"

for ecosystem in github-actions gomod npm; do
  grep -Eq "^[[:space:]]+(- )?package-ecosystem: ${ecosystem}$" "$dependabot_file" ||
    fail "$dependabot_file does not watch $ecosystem"
done
check "$dependabot_file watches github-actions, gomod and npm"

echo "PASS: $checks workflow structure checks"
