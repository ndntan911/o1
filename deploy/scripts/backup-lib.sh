#!/usr/bin/env bash
# Shared helpers for encrypted backups, restore drills, and host install.
#
# This file is meant to be sourced. It does not enable set -e; callers own
# their shell options. Environment files are parsed key by key and are never
# sourced, so values such as $(...) stay literal.

# --- logging ---------------------------------------------------------------

bl_utc_now() {
  date -u '+%Y-%m-%dT%H:%M:%SZ'
}

bl_log() {
  local level=${1:-info}
  shift || true
  printf '%s %-5s %s\n' "$(bl_utc_now)" "$level" "$*" >&2
}

bl_die() {
  bl_log error "$@"
  exit 1
}

# --- validation ------------------------------------------------------------

bl_require_var() {
  local name
  for name in "$@"; do
    if [[ -z ${!name:-} ]]; then
      bl_die "missing required environment variable: $name"
    fi
  done
}

bl_require_cmd() {
  local cmd
  for cmd in "$@"; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      bl_die "missing required command: $cmd"
    fi
  done
}

# --- env parsing -----------------------------------------------------------

# Trim leading and trailing whitespace without altering the value's interior.
_bl_trim() {
  local value=$1
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

# Remove one pair of matching single or double quotes.
_bl_unquote() {
  local value=$1
  if [[ ${#value} -ge 2 ]]; then
    case $value in
      \'*\') value=${value:1:${#value}-2} ;;
      \"*\") value=${value:1:${#value}-2} ;;
    esac
  fi
  printf '%s' "$value"
}

# bl_load_env FILE: parse KEY=value lines, export them, and execute nothing.
bl_load_env() {
  local file=$1 line key value
  if [[ ! -r $file ]]; then
    bl_log error "cannot read env file: $file"
    return 1
  fi

  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%$'\r'}
    [[ $line =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=(.*)$ ]] ||
      continue
    key=${BASH_REMATCH[1]}
    value=$(_bl_trim "${BASH_REMATCH[2]}")
    value=$(_bl_unquote "$value")
    printf -v "$key" '%s' "$value"
    export "${key?}"
  done <"$file"
}

# bl_env_get FILE NAME: print one parsed value. Private to this library.
bl_env_get() {
  local file=$1 name=$2 line key value
  [[ -r $file ]] || return 1

  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%$'\r'}
    [[ $line =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=(.*)$ ]] ||
      continue
    key=${BASH_REMATCH[1]}
    [[ $key == "$name" ]] || continue
    value=$(_bl_trim "${BASH_REMATCH[2]}")
    value=$(_bl_unquote "$value")
    printf '%s\n' "$value"
    return 0
  done <"$file"

  return 1
}

# bl_read_o1_env: read the Resend credentials from O1_ENV_FILE without sourcing.
bl_read_o1_env() {
  local file=${O1_ENV_FILE:-} value
  if [[ -z $file ]]; then
    bl_log warn "O1_ENV_FILE is not set"
    return 1
  fi
  if [[ ! -r $file ]]; then
    bl_log warn "cannot read O1_ENV_FILE: $file"
    return 1
  fi

  value=$(bl_env_get "$file" O1_RESEND_API_KEY) || {
    bl_log warn "O1_RESEND_API_KEY is missing from $file"
    return 1
  }
  O1_RESEND_API_KEY=$value
  export O1_RESEND_API_KEY

  value=$(bl_env_get "$file" O1_EMAIL_FROM) || {
    bl_log warn "O1_EMAIL_FROM is missing from $file"
    return 1
  }
  O1_EMAIL_FROM=$value
  export O1_EMAIL_FROM
}

# --- locking and state -----------------------------------------------------

# bl_take_lock PATH: hold an exclusive lock on fd 9; return 1 when held.
bl_take_lock() {
  local path=$1 dir
  dir=$(dirname -- "$path")
  if [[ ! -d $dir || ! -w $dir ]]; then
    bl_log error "lock directory is not writable: $dir"
    return 1
  fi

  exec 9>"$path"
  if ! flock -n 9; then
    exec 9>&-
    return 1
  fi
}

bl_release_lock() {
  exec 9>&- 2>/dev/null || true
}

# bl_write_state PATH CONTENT: replace PATH atomically with CONTENT.
bl_write_state() {
  local path=$1 content=$2 dir tmp
  dir=$(dirname -- "$path")
  tmp=$(mktemp "$dir/.bl-state.XXXXXX") || return 1

  if ! printf '%s\n' "$content" >"$tmp"; then
    rm -f -- "$tmp"
    return 1
  fi
  chmod 600 "$tmp" || {
    rm -f -- "$tmp"
    return 1
  }
  if ! mv -f -- "$tmp" "$path"; then
    rm -f -- "$tmp"
    return 1
  fi
}

# --- alerts ----------------------------------------------------------------

# bl_send_alert SUBJECT BODY: email through Resend. The API key lives in a
# mode-0600 curl config, never in argv.
bl_send_alert() {
  local subject=$1 body=$2 alert_to
  if [[ -z ${O1_BACKUP_ALERT_EMAIL:-} || -z ${O1_RESEND_API_KEY:-} ||
    -z ${O1_EMAIL_FROM:-} ]]; then
    bl_log warn "alert not configured"
    return 0
  fi
  alert_to=$O1_BACKUP_ALERT_EMAIL

  (
    set -e
    dir=$(mktemp -d "${TMPDIR:-/tmp}/o1-backup-alert.XXXXXX")
    trap 'rm -rf "$dir"' EXIT

    header="$dir/curl.conf"
    payload="$dir/body.json"
    (umask 077; : >"$header"; : >"$payload")
    chmod 600 "$header" "$payload"

    printf 'header = "Authorization: Bearer %s"\n' "$O1_RESEND_API_KEY" \
      >"$header"
    jq -n --arg from "$O1_EMAIL_FROM" --arg to "$alert_to" \
      --arg subject "$subject" --arg text "$body" \
      '{from: $from, to: [$to], subject: $subject, text: $text}' >"$payload"

    curl -fsS --config "$header" \
      -H 'Content-Type: application/json' \
      --data-binary @"$payload" \
      https://api.resend.com/emails >/dev/null
  )
}

# --- uptime pings ----------------------------------------------------------

# bl_ping [URL] STATUS: ping success or failure. With just a status, select
# O1_BACKUP_PING_URL or O1_BACKUP_FAILURE_URL. Empty URL is a no-op.
bl_ping() {
  local url=${1:-} status=${2:-success}
  if [[ $url == success || $url == failure ]]; then
    status=$url
    if [[ $status == success ]]; then
      url=${O1_BACKUP_PING_URL:-}
    else
      url=${O1_BACKUP_FAILURE_URL:-}
    fi
  fi
  if [[ -z $url ]]; then
    return 0
  fi

  case $status in
    success | failure) ;;
    *)
      bl_log warn "unknown ping status: $status"
      return 0
      ;;
  esac

  if ! curl -fsS -m 10 "$url" >/dev/null 2>&1; then
    bl_log warn "ping failed: $status"
    return 0
  fi
}
