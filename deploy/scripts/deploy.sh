#!/usr/bin/env bash
# Activate an uploaded release bundle on a registry-deployed host.
#
#   deploy.sh INCOMING_DIR [--dry-run]
#
# The host layout is /opt/o1/{.env,bin,releases,incoming,state}; set O1_ROOT to
# test with a temporary tree. The bundle is moved to releases/<id>, `current`
# and `previous` are switched atomically, the stack is pulled and started, and
# https://$O1_DOMAIN/readyz is polled. A failed health check restores the
# previous release. Secrets are read key-by-key from .env and never sourced or
# echoed.
set -euo pipefail
umask 077

O1_ROOT=${O1_ROOT:-/opt/o1}
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  echo "usage: deploy.sh INCOMING_DIR [--dry-run]" >&2
}

incoming=
dry_run=0
for arg in "$@"; do
  case $arg in
    --dry-run) dry_run=1 ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*) echo "deploy: unknown option: $arg" >&2; usage; exit 2 ;;
    *)
      if [[ -n $incoming ]]; then
        usage
        exit 2
      fi
      incoming=$arg
      ;;
  esac
done
if [[ -z $incoming ]]; then
  usage
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

link() { # link TARGET LINKPATH
  rm -f "$2.new"
  ln -s "$1" "$2.new"
  # rename(2) over a symlink-to-directory needs GNU mv -T or BSD mv -h;
  # plain mv would move the new link inside the old release directory.
  if mv -fT "$2.new" "$2" 2>/dev/null; then
    return 0
  fi
  mv -fh "$2.new" "$2"
}

for cmd in docker curl flock; do
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "deploy: required command not found: $cmd" >&2
    exit 1
  }
done

if [[ ! -f $O1_ROOT/.env ]]; then
  echo "deploy: missing $O1_ROOT/.env" >&2
  exit 1
fi
if [[ ! -d $incoming ]]; then
  echo "deploy: incoming directory not found: $incoming" >&2
  exit 1
fi
for path in deploy/production/compose.yaml deploy/observability/alloy.alloy images.env; do
  if [[ ! -f $incoming/$path ]]; then
    echo "deploy: incoming bundle is missing $path" >&2
    exit 1
  fi
done

domain=$(env_get "$O1_ROOT/.env" O1_DOMAIN)
if [[ -z $domain ]]; then
  echo "deploy: O1_DOMAIN is missing or empty in $O1_ROOT/.env" >&2
  exit 1
fi

backend_ref=$(env_get "$incoming/images.env" O1_BACKEND_IMAGE)
web_ref=$(env_get "$incoming/images.env" O1_WEB_IMAGE)
digest_pattern='^.+@sha256:[0-9a-f]{64}$'
if [[ ! $backend_ref =~ $digest_pattern ]]; then
  echo "deploy: O1_BACKEND_IMAGE must be pinned as <image>@sha256:<64 hex>" >&2
  exit 1
fi
if [[ ! $web_ref =~ $digest_pattern ]]; then
  echo "deploy: O1_WEB_IMAGE must be pinned as <image>@sha256:<64 hex>" >&2
  exit 1
fi

mkdir -p "$O1_ROOT/state" "$O1_ROOT/releases"
exec 9>"$O1_ROOT/state/deploy.lock"
if ! flock -n 9; then
  echo "deploy: another deploy or rollback holds $O1_ROOT/state/deploy.lock" >&2
  exit 3
fi

compose() { # compose RELEASE_DIR ARGS...
  local dir=$1
  shift
  docker compose \
    --env-file "$O1_ROOT/.env" \
    --env-file "$dir/images.env" \
    -f "$dir/deploy/production/compose.yaml" \
    "$@"
}

if ! compose "$incoming" config --quiet; then
  echo "deploy: compose configuration is invalid" >&2
  exit 1
fi

id=$(basename "$incoming")
target_rel="releases/$id"
target="$O1_ROOT/$target_rel"

health_timeout=${DEPLOY_HEALTH_TIMEOUT:-}
if [[ -z $health_timeout ]]; then
  health_timeout=$(env_get "$O1_ROOT/.env" DEPLOY_HEALTH_TIMEOUT)
fi
health_timeout=${health_timeout:-300}
health_url="https://$domain/readyz"

if (( dry_run )); then
  echo "deploy: dry run for release $id"
  echo "  compose: docker compose --env-file $O1_ROOT/.env --env-file $target/images.env -f $target/deploy/production/compose.yaml up -d --remove-orphans"
  echo "  health: $health_url"
  exit 0
fi

if [[ -e $target ]]; then
  echo "deploy: release $id already exists at $target" >&2
  exit 1
fi

old_current=$(readlink "$O1_ROOT/current" 2>/dev/null || true)
old_previous=$(readlink "$O1_ROOT/previous" 2>/dev/null || true)

mv "$incoming" "$target"

if [[ -n $old_current ]]; then
  link "$old_current" "$O1_ROOT/previous"
fi
link "$target_rel" "$O1_ROOT/current"

ghcr_user=$(env_get "$O1_ROOT/.env" GHCR_USER)
ghcr_token=$(env_get "$O1_ROOT/.env" GHCR_READ_TOKEN)
if [[ -n $ghcr_user && -n $ghcr_token ]]; then
  printf '%s' "$ghcr_token" | docker login ghcr.io -u "$ghcr_user" --password-stdin >/dev/null
fi

compose "$target" pull api web
compose "$target" up -d --remove-orphans

if ! "$script_dir/healthcheck.sh" "$health_url" "$health_timeout"; then
  echo "deploy: release $id is unhealthy; recent logs:" >&2
  compose "$target" logs --tail=100 || true

  if [[ -n $old_current ]]; then
    link "$old_current" "$O1_ROOT/current"
    if [[ -n $old_previous ]]; then
      link "$old_previous" "$O1_ROOT/previous"
    else
      rm -f "$O1_ROOT/previous"
    fi

    restore_dir=$old_current
    if [[ $restore_dir != /* ]]; then
      restore_dir="$O1_ROOT/$restore_dir"
    fi

    echo "deploy: restoring current to $old_current" >&2
    compose "$restore_dir" up -d --remove-orphans || true
    if ! "$script_dir/healthcheck.sh" "$health_url" "$health_timeout"; then
      echo "deploy: previous release $old_current is also unhealthy" >&2
    fi
  else
    echo "deploy: first deploy has no previous release to roll back to; containers left running for inspection" >&2
  fi
  exit 1
fi

compose "$target" ps
echo "deploy: release $id is active"
echo "deploy: O1_BACKEND_IMAGE=$backend_ref"
echo "deploy: O1_WEB_IMAGE=$web_ref"
