#!/usr/bin/env bash
# Switch the host to an earlier release and verify it becomes healthy.
#
#   rollback.sh [RELEASE_ID]
#
# With no argument the target is the `previous` symlink. With an argument the
# target is releases/<id>, which also allows rolling forward. Shared state is
# the same as deploy.sh: O1_ROOT (default /opt/o1), .env, state/deploy.lock.
set -euo pipefail
umask 077

O1_ROOT=${O1_ROOT:-/opt/o1}
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  echo "usage: rollback.sh [RELEASE_ID]" >&2
}

if [[ $# -gt 1 ]]; then
  usage
  exit 2
fi

release_id=${1:-}
case $release_id in
  -h|--help)
    usage
    exit 0
    ;;
  -*)
    usage
    exit 2
    ;;
esac
if [[ -n $release_id && ! $release_id =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "rollback: release id may only contain letters, digits, dot, underscore, and dash" >&2
  exit 2
fi
if [[ $release_id == . || $release_id == .. ]]; then
  echo "rollback: release id must not be '.' or '..'" >&2
  exit 2
fi

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
    echo "rollback: required command not found: $cmd" >&2
    exit 1
  }
done

if [[ ! -f $O1_ROOT/.env ]]; then
  echo "rollback: missing $O1_ROOT/.env" >&2
  exit 1
fi

domain=$(env_get "$O1_ROOT/.env" O1_DOMAIN)
if [[ -z $domain ]]; then
  echo "rollback: O1_DOMAIN is missing or empty in $O1_ROOT/.env" >&2
  exit 1
fi

mkdir -p "$O1_ROOT/state"
exec 9>"$O1_ROOT/state/deploy.lock"
if ! flock -n 9; then
  echo "rollback: another deploy or rollback holds $O1_ROOT/state/deploy.lock" >&2
  exit 3
fi

current_target=$(readlink "$O1_ROOT/current" 2>/dev/null || true)
if [[ -z $current_target ]]; then
  echo "rollback: no current release to roll back from" >&2
  exit 1
fi
current_dir=$current_target
if [[ $current_dir != /* ]]; then
  current_dir="$O1_ROOT/$current_dir"
fi
if [[ ! -d $current_dir ]]; then
  echo "rollback: current release is missing: $current_target" >&2
  exit 1
fi

if [[ -n $release_id ]]; then
  target_rel="releases/$release_id"
else
  target_rel=$(readlink "$O1_ROOT/previous" 2>/dev/null || true)
  if [[ -z $target_rel ]]; then
    echo "rollback: no previous release is recorded" >&2
    exit 1
  fi
fi

target_dir=$target_rel
if [[ $target_dir != /* ]]; then
  target_dir="$O1_ROOT/$target_dir"
fi
if [[ ! -d $target_dir ]]; then
  echo "rollback: release not found: $target_rel" >&2
  exit 1
fi

backend_ref=$(env_get "$target_dir/images.env" O1_BACKEND_IMAGE)
web_ref=$(env_get "$target_dir/images.env" O1_WEB_IMAGE)
digest_pattern='^.+@sha256:[0-9a-f]{64}$'
if [[ ! $backend_ref =~ $digest_pattern ]]; then
  echo "rollback: target O1_BACKEND_IMAGE is not pinned as <image>@sha256:<64 hex>" >&2
  exit 1
fi
if [[ ! $web_ref =~ $digest_pattern ]]; then
  echo "rollback: target O1_WEB_IMAGE is not pinned as <image>@sha256:<64 hex>" >&2
  exit 1
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

if ! compose "$target_dir" config --quiet; then
  echo "rollback: target compose configuration is invalid" >&2
  exit 1
fi

health_timeout=${DEPLOY_HEALTH_TIMEOUT:-}
if [[ -z $health_timeout ]]; then
  health_timeout=$(env_get "$O1_ROOT/.env" DEPLOY_HEALTH_TIMEOUT)
fi
health_timeout=${health_timeout:-300}
health_url="https://$domain/readyz"

old_previous=$(readlink "$O1_ROOT/previous" 2>/dev/null || true)

# Single recovery path for any post-switch failure: GHCR login, pull, up, or
# health. Restores the release that was current before this rollback and exits.
recover() {
  echo "rollback: release $target_rel failed; recent logs:" >&2
  compose "$target_dir" logs --tail=100 || true

  link "$current_target" "$O1_ROOT/current"
  if [[ -n $old_previous ]]; then
    link "$old_previous" "$O1_ROOT/previous"
  else
    rm -f "$O1_ROOT/previous"
  fi

  echo "rollback: restoring current to $current_target" >&2
  compose "$current_dir" up -d --remove-orphans || true
  if ! "$script_dir/healthcheck.sh" "$health_url" "$health_timeout"; then
    echo "rollback: restored release $current_target is also unhealthy" >&2
  fi
  exit 1
}

link "$target_rel" "$O1_ROOT/current"
link "$current_target" "$O1_ROOT/previous"

ghcr_user=$(env_get "$O1_ROOT/.env" GHCR_USER)
ghcr_token=$(env_get "$O1_ROOT/.env" GHCR_READ_TOKEN)
if [[ -n $ghcr_user && -n $ghcr_token ]]; then
  if ! printf '%s' "$ghcr_token" | docker login ghcr.io -u "$ghcr_user" --password-stdin >/dev/null; then
    echo "rollback: GHCR login failed" >&2
    recover
  fi
fi

if ! compose "$target_dir" pull api web; then
  echo "rollback: image pull failed" >&2
  recover
fi
if ! compose "$target_dir" up -d --remove-orphans; then
  echo "rollback: compose up failed" >&2
  recover
fi

if ! "$script_dir/healthcheck.sh" "$health_url" "$health_timeout"; then
  echo "rollback: target release $target_rel is unhealthy" >&2
  recover
fi

compose "$target_dir" ps
echo "rollback: release $target_rel is active"
echo "rollback: O1_BACKEND_IMAGE=$backend_ref"
echo "rollback: O1_WEB_IMAGE=$web_ref"
