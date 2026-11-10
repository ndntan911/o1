#!/usr/bin/env bash
# One-time host preparation for a registry-deployed o1 host.
#
#   bootstrap-host.sh [--root DIR] [--source DIR]
#
# Creates the host layout under /opt/o1 (or --root), installs the stable deploy
# scripts, seeds .env from deploy/production/env.example when it is absent,
# validates the production Compose file with inline placeholder image refs, and
# optionally logs in to ghcr.io with GHCR_USER/GHCR_READ_TOKEN from .env.
#
# The repository root is inferred from this script's path; pass --source when
# running the script from somewhere other than a checkout. The stack is never
# started here: the first release is activated by deploy.sh.
set -euo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
default_source="$(cd -- "$script_dir/../.." && pwd)"

root=/opt/o1
source_dir=$default_source

usage() {
  echo "usage: bootstrap-host.sh [--root DIR] [--source DIR]" >&2
}

while (($# > 0)); do
  case $1 in
    --root)
      (($# >= 2)) || { echo "bootstrap: --root requires a directory" >&2; usage; exit 2; }
      root=$2
      shift 2
      ;;
    --source)
      (($# >= 2)) || { echo "bootstrap: --source requires a directory" >&2; usage; exit 2; }
      source_dir=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "bootstrap: unknown option: $1" >&2
      usage
      exit 2
      ;;
  esac
done

if [[ -z $root ]]; then
  echo "bootstrap: --root must not be empty" >&2
  exit 2
fi

# Read one KEY=value line without sourcing the file. Strips one layer of
# matching single or double quotes.
env_get() { # env_get FILE KEY
  local file=$1 key=$2 value
  value=$(grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d= -f2- || true)
  case $value in
    \"*\") value=${value#\"}; value=${value%\"} ;;
    \'*\') value=${value#\'}; value=${value%\'} ;;
  esac
  printf '%s' "$value"
}

# Check the source tree and the required tools before touching the host so a
# failed bootstrap never leaves a partial root behind.
for cmd in docker curl flock; do
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "bootstrap: required command not found: $cmd" >&2
    exit 1
  }
done
if ! docker compose version >/dev/null 2>&1; then
  echo "bootstrap: the docker compose plugin is required (docker compose version failed)" >&2
  exit 1
fi

if [[ ! -d $source_dir ]]; then
  echo "bootstrap: source directory not found: $source_dir" >&2
  exit 1
fi

required=(
  deploy/production/compose.yaml
  deploy/production/env.example
  deploy/observability/alloy.alloy
  deploy/scripts/deploy.sh
  deploy/scripts/rollback.sh
  deploy/scripts/healthcheck.sh
  deploy/scripts/validate-release.sh
)
for path in "${required[@]}"; do
  if [[ ! -f $source_dir/$path ]]; then
    echo "bootstrap: source tree is missing $path (looked under $source_dir)" >&2
    exit 1
  fi
done

install -d -m 0700 "$root" "$root/bin" "$root/releases" "$root/incoming" "$root/state"
echo "bootstrap: prepared $root/{bin,releases,incoming,state}"

for name in deploy.sh rollback.sh healthcheck.sh validate-release.sh; do
  install -m 0755 "$source_dir/deploy/scripts/$name" "$root/bin/$name"
done
echo "bootstrap: installed deploy.sh, rollback.sh, healthcheck.sh, validate-release.sh"

env_created=0
if [[ -f $root/.env ]]; then
  echo "bootstrap: keeping existing $root/.env"
else
  install -m 0600 "$source_dir/deploy/production/env.example" "$root/.env"
  env_created=1
  echo "bootstrap: created $root/.env from env.example"
fi

# Validate the Compose file without persisting image refs on the host. The
# placeholder digests keep the config check independent of a release.
placeholder=$(printf '0%.0s' {1..64})
if ! O1_BACKEND_IMAGE="example.invalid/o1-backend@sha256:$placeholder" \
  O1_WEB_IMAGE="example.invalid/o1-web@sha256:$placeholder" \
  docker compose --env-file "$root/.env" \
    -f "$source_dir/deploy/production/compose.yaml" config --quiet; then
  echo "bootstrap: compose configuration is invalid; check $root/.env" >&2
  exit 1
fi
echo "bootstrap: compose configuration is valid"

ghcr_user=$(env_get "$root/.env" GHCR_USER)
ghcr_token=$(env_get "$root/.env" GHCR_READ_TOKEN)
if [[ -n $ghcr_user && -n $ghcr_token ]]; then
  if ! printf '%s' "$ghcr_token" | docker login ghcr.io -u "$ghcr_user" --password-stdin >/dev/null; then
    echo "bootstrap: GHCR login failed; check GHCR_USER and GHCR_READ_TOKEN in $root/.env" >&2
    exit 1
  fi
  echo "bootstrap: logged in to ghcr.io as $ghcr_user"
elif [[ -n $ghcr_user || -n $ghcr_token ]]; then
  echo "bootstrap: GHCR_USER and GHCR_READ_TOKEN must both be set to log in; skipping GHCR login" >&2
fi

if ((env_created)); then
  echo "bootstrap: WARNING: edit $root/.env before the first deploy; it still contains example values" >&2
fi

echo "bootstrap: host is ready; the stack was not started"
echo "next steps:"
echo "  1. edit $root/.env (domain, database password, keys, mail settings, and optional GHCR credentials)"
echo "  2. dispatch the release workflow for the target environment, or copy a release bundle into $root/incoming and run $root/bin/deploy.sh $root/incoming/<release>"
echo "  3. for a local build instead, run from a checkout: docker compose --env-file deploy/production/env.example -f deploy/production/compose.yaml -f deploy/production/compose.build.yaml up -d --build"
