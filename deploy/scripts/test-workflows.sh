#!/usr/bin/env bash
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

for job in backend integration frontend e2e; do
  grep -Eq "^  ${job}:" "$ci_file" || fail "$ci_file lost the '$job' job"
done
check "$ci_file keeps the backend, integration, frontend and e2e jobs"

grep -Eq 'govulncheck@v1\.9\.0 \./\.\.\.' "$ci_file" ||
  fail "$ci_file does not run govulncheck"
check "$ci_file runs govulncheck"

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
check "$release_file verifies with the reusable CI workflow"

grep -Fq 'aquasecurity/setup-trivy@v0.3.1' "$release_file" ||
  fail "$release_file does not install Trivy via aquasecurity/setup-trivy"
check "$release_file installs Trivy"

grep -Fq 'trivy fs --scanners secret,misconfig --exit-code 1 .' "$release_file" ||
  fail "$release_file does not scan the repository for secrets and misconfiguration"
check "$release_file scans repository secrets and misconfiguration"

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

grep -Fq "tr '[:upper:]' '[:lower:]'" "$release_file" ||
  fail "$release_file does not lowercase the repository owner for GHCR"
check "release lowercases the repository owner for GHCR"

grep -Eq 'sbom=true' "$release_file" && grep -Eq 'provenance=true' "$release_file" ||
  fail "$release_file does not attach SBOM and provenance on push"
check "release pushes with SBOM and provenance"

# --- dependabot.yml: monitored ecosystems --------------------------------

[[ -f $dependabot_file ]] || fail "$dependabot_file is missing"

for ecosystem in github-actions gomod npm; do
  grep -Eq "^[[:space:]]+(- )?package-ecosystem: ${ecosystem}$" "$dependabot_file" ||
    fail "$dependabot_file does not watch $ecosystem"
done
check "$dependabot_file watches github-actions, gomod and npm"

echo "PASS: $checks workflow structure checks"
