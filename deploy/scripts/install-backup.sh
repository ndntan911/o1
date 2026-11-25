#!/usr/bin/env bash
# Install or upgrade the o1 backup tooling and systemd units.
#
# --source points at a copy of the deploy tree (it must contain backup/ and
# scripts/). The installer never sources backup.env: keys are read with the
# library parser. The restic password is generated only for an explicit
# --init-new-repo and is never printed.
set -Eeuo pipefail
umask 077

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=backup-lib.sh disable=SC1091
source "$script_dir/backup-lib.sh"

usage="usage: install-backup.sh --source DIR [--root /opt/o1] [--init-new-repo] [--enable]"

source_dir=
root=/opt/o1
init_new_repo=0
enable=0

while (($#)); do
  case $1 in
    --source)
      [[ $# -ge 2 ]] || bl_die "$usage"
      source_dir=$2
      shift 2
      ;;
    --root)
      [[ $# -ge 2 ]] || bl_die "$usage"
      root=$2
      shift 2
      ;;
    --init-new-repo)
      init_new_repo=1
      shift
      ;;
    --enable)
      enable=1
      shift
      ;;
    -h | --help)
      printf '%s\n' "$usage"
      exit 0
      ;;
    *)
      bl_die "unknown argument: $1"
      ;;
  esac
done

[[ -n $source_dir ]] || bl_die "$usage"
[[ -d $source_dir ]] || bl_die "source directory not found: $source_dir"
source_dir=$(cd -- "$source_dir" && pwd)

[[ -n $root ]] || bl_die "the install root must not be empty"
case $root in
  /*) ;;
  *) bl_die "--root must be an absolute path: $root" ;;
esac
while [[ $root == */ && $root != / ]]; do
  root=${root%/}
done
[[ $root != *$'\n'* ]] || bl_die "the install root must not contain a newline"

# Test-only override. Production installs always use /etc/systemd/system; the
# installer tests point this at a temporary directory.
unit_dir=${O1_SYSTEMD_UNIT_DIR:-/etc/systemd/system}
backup_env="$root/backup/backup.env"
restic_pass="$root/backup/restic.pass"

runtime_scripts=(backup.sh restore-verify.sh backup-lib.sh)
units=(o1-backup.service o1-backup.timer
  o1-backup-prune.service o1-backup-prune.timer
  o1-restore-verify.service o1-restore-verify.timer)

# --- source and command validation ----------------------------------------

required_files=("$source_dir/backup/backup.env.example")
for script in "${runtime_scripts[@]}"; do
  required_files+=("$source_dir/scripts/$script")
done
for unit in "${units[@]}"; do
  required_files+=("$source_dir/backup/systemd/$unit")
done
for file in "${required_files[@]}"; do
  [[ -f $file ]] || bl_die "missing source file: $file"
done

bl_require_cmd docker restic flock curl jq openssl systemctl timeout \
  install sed mktemp

# --- file rendering --------------------------------------------------------

tmp_file=
password_candidate=
# shellcheck disable=SC2329  # invoked by the EXIT trap
cleanup() {
  if [[ -n $tmp_file ]]; then
    rm -f -- "$tmp_file"
  fi
  if [[ -n $password_candidate ]]; then
    rm -f -- "$password_candidate"
  fi
}
trap cleanup EXIT

sed_replacement() {
  printf '%s' "$1" | sed 's/[\\|&]/\\&/g'
}

# render_file SRC DEST MODE [TIMEOUT]: rewrite /opt/o1 to the install root and
# optionally replace the backup service timeout.
render_file() {
  local src=$1 dest=$2 mode=$3 timeout=${4:-}
  local -a sed_args

  tmp_file=$(mktemp "${TMPDIR:-/tmp}/o1-backup-install.XXXXXX") ||
    bl_die "could not create a temporary file"
  sed_args=(-e "s|/opt/o1|$root_sed|g")
  if [[ -n $timeout ]]; then
    sed_args+=(-e "s|^TimeoutStartSec=.*|TimeoutStartSec=$timeout_sed|")
  fi
  sed "${sed_args[@]}" "$src" >"$tmp_file"
  install -m "$mode" "$tmp_file" "$dest"
  rm -f -- "$tmp_file"
  tmp_file=
}

root_sed=$(sed_replacement "$root")

# --- environment and password ---------------------------------------------

# env_key NAME: read one key from backup.env without executing the file.
env_key() {
  bl_env_get "$backup_env" "$1" || true
}

# resolve_timeout: a shell O1_BACKUP_TIMEOUT wins, then backup.env, then 2h.
resolve_timeout() {
  timeout_value=${O1_BACKUP_TIMEOUT:-$(env_key O1_BACKUP_TIMEOUT)}
  timeout_value=${timeout_value:-2h}
  [[ $timeout_value != *$'\n'* ]] ||
    bl_die "O1_BACKUP_TIMEOUT must not contain a newline"
  timeout_sed=$(sed_replacement "$timeout_value")
}

ensure_backup_env() {
  if [[ -e $backup_env ]]; then
    bl_log info "keeping existing $backup_env"
    return 0
  fi
  render_file "$source_dir/backup/backup.env.example" "$backup_env" 0600
  bl_log info "created $backup_env from the example; edit it before enabling timers"
}

ensure_password() {
  local repo repo_path redacted_repo candidate init_output key value

  if [[ -f $restic_pass ]]; then
    bl_log info "keeping existing restic password file: $restic_pass"
    return 0
  fi

  if ((!init_new_repo)); then
    bl_die "restic password file is missing: $restic_pass; restore it from the password manager, or re-run with --init-new-repo only for a brand-new repository"
  fi

  repo=$(env_key RESTIC_REPOSITORY)
  [[ -n $repo ]] ||
    bl_die "RESTIC_REPOSITORY is empty in $backup_env; configure it before --init-new-repo"
  redacted_repo=$(redact_repository "$repo")

  # Confirm a local repository has no config before generating anything.
  repo_path=$repo
  case $repo_path in
    local:*) repo_path=${repo_path#local:} ;;
  esac
  if [[ $repo_path != *:* && -e $repo_path/config ]]; then
    bl_die "repository $redacted_repo already exists; restore $restic_pass from the password manager instead of generating a new password"
  fi

  candidate=$(openssl rand -hex 32) ||
    bl_die "could not generate a restic password"
  password_candidate=$(mktemp "$root/backup/.restic-pass.XXXXXX") ||
    bl_die "could not create a temporary password file"
  if ! (umask 077; printf '%s\n' "$candidate" >"$password_candidate"); then
    rm -f -- "$password_candidate"
    password_candidate=
    bl_die "could not write the candidate password file"
  fi
  chmod 600 "$password_candidate"
  unset candidate
  bl_log info "generating a new restic password for $restic_pass"

  # Export inside a subshell so credentials never appear in a child's argv.
  if ! init_output=$(
    unset RESTIC_PASSWORD RESTIC_PASSWORD_COMMAND
    export RESTIC_REPOSITORY="$repo" RESTIC_PASSWORD_FILE="$password_candidate"
    for key in RESTIC_CACHE_DIR AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY; do
      value=$(env_key "$key")
      if [[ -n $value ]]; then
        export "$key=$value"
      fi
    done
    restic init 2>&1
  ); then
    rm -f -- "$password_candidate"
    password_candidate=
    if grep -qiE 'already (exists|initialized)' <<<"$init_output"; then
      bl_die "restic reports that the repository already exists; removed the generated password. Restore the real $restic_pass from the password manager and re-run without --init-new-repo"
    fi
    bl_log error "restic init failed; its output is not repeated because it may contain repository credentials"
    bl_die "removed the generated password; no password was installed at $restic_pass (run restic init manually to see the error)"
  fi

  # Only pair the new password with the repository after init succeeds.
  mv -f -- "$password_candidate" "$restic_pass"
  password_candidate=

  bl_log info "initialized restic repository $redacted_repo"
  bl_log info "copy $restic_pass offsite now; its contents are never printed"
}

# --- enable checks ---------------------------------------------------------

# warn_large_database: best-effort warning when the database is large enough
# that the default 2h timeout may be too short. An unreachable database is not
# an install failure.
warn_large_database() {
  local container compose_file env_file threshold output
  local -a pg_cmd

  threshold=$(env_key O1_BACKUP_LARGE_DB_WARN_KB)
  threshold=${threshold:-10485760}
  if [[ ! $threshold =~ ^[0-9]+$ ]]; then
    bl_log warn "O1_BACKUP_LARGE_DB_WARN_KB is not an integer: $threshold; skipping the database-size warning"
    return 0
  fi

  container=$(env_key O1_BACKUP_PG_CONTAINER)
  if [[ -n $container ]]; then
    pg_cmd=(docker exec "$container" psql -U o1 -d o1 -tAc
      "SELECT pg_database_size('o1')/1024")
  else
    compose_file=$(env_key O1_COMPOSE_FILE)
    env_file=$(env_key O1_ENV_FILE)
    if [[ -z $compose_file || -z $env_file ]]; then
      bl_log info "no Compose configuration; skipping the database-size warning"
      return 0
    fi
    pg_cmd=(docker compose --env-file "$env_file" -f "$compose_file"
      exec -T postgres psql -U o1 -d o1 -tAc
      "SELECT pg_database_size('o1')/1024")
  fi

  if ! output=$("${pg_cmd[@]}" 2>/dev/null); then
    bl_log info "database is not reachable; skipping the large-database warning"
    return 0
  fi
  output=${output//[[:space:]]/}
  if [[ ! $output =~ ^[0-9]+$ ]]; then
    bl_log warn "unexpected database size for the large-database warning: ${output:-<empty>}"
    return 0
  fi
  if ((output >= threshold)); then
    bl_log warn "database size ${output}KB is at or above ${threshold}KB; the default 2h backup timeout may be too short. Set O1_BACKUP_TIMEOUT and re-run the installer."
  else
    bl_log info "database size ${output}KB is below the ${threshold}KB warning threshold"
  fi
}

require_enable_config() {
  local ping failure alert repo password o1_env api_key email_from
  ping=$(env_key O1_BACKUP_PING_URL)
  if [[ -z $ping ]]; then
    bl_die "--enable requires O1_BACKUP_PING_URL in $backup_env; the units were installed but the timers were not enabled"
  fi
  failure=$(env_key O1_BACKUP_FAILURE_URL)
  alert=$(env_key O1_BACKUP_ALERT_EMAIL)
  if [[ -z $failure && -z $alert ]]; then
    bl_die "--enable requires O1_BACKUP_FAILURE_URL or O1_BACKUP_ALERT_EMAIL in $backup_env; the units were installed but the timers were not enabled"
  fi
  if [[ -z $failure ]]; then
    o1_env=$(env_key O1_ENV_FILE)
    api_key=
    email_from=
    if [[ -n $o1_env && -r $o1_env ]]; then
      api_key=$(bl_env_get "$o1_env" O1_RESEND_API_KEY) || api_key=
      email_from=$(bl_env_get "$o1_env" O1_EMAIL_FROM) || email_from=
    fi
    if [[ -z $api_key || -z $email_from ]]; then
      bl_die "--enable with O1_BACKUP_ALERT_EMAIL and no O1_BACKUP_FAILURE_URL requires O1_RESEND_API_KEY and O1_EMAIL_FROM in O1_ENV_FILE (${o1_env:-unset}); the units were installed but the timers were not enabled"
    fi
  fi
  repo=$(env_key RESTIC_REPOSITORY)
  if [[ -z $repo ]]; then
    bl_die "--enable requires RESTIC_REPOSITORY in $backup_env; the units were installed but the timers were not enabled"
  fi
  password=$(env_key RESTIC_PASSWORD_FILE)
  if [[ -z $password ]]; then
    bl_die "--enable requires RESTIC_PASSWORD_FILE in $backup_env; the units were installed but the timers were not enabled"
  fi
}

# --- install ---------------------------------------------------------------

install -d -m 0700 "$root" "$root/backup" "$root/backup/cache" "$root/bin"
install -d -m 0755 "$unit_dir"

ensure_backup_env
resolve_timeout
warn_large_database
ensure_password

for script in "${runtime_scripts[@]}"; do
  install -m 0755 "$source_dir/scripts/$script" "$root/bin/$script"
done
bl_log info "installed backup scripts in $root/bin"

for unit in "${units[@]}"; do
  if [[ $unit == o1-backup.service ]]; then
    render_file "$source_dir/backup/systemd/$unit" "$unit_dir/$unit" 0644 \
      "$timeout_value"
  else
    render_file "$source_dir/backup/systemd/$unit" "$unit_dir/$unit" 0644
  fi
done
bl_log info "installed systemd units in $unit_dir"

if command -v systemd-analyze >/dev/null 2>&1; then
  verify_paths=()
  for unit in "${units[@]}"; do
    verify_paths+=("$unit_dir/$unit")
  done
  if ! systemd-analyze verify "${verify_paths[@]}"; then
    bl_die "systemd-analyze verify rejected the installed units"
  fi
  bl_log info "systemd-analyze verify passed"
fi

systemctl daemon-reload

if ((enable)); then
  require_enable_config
  systemctl enable --now o1-backup.timer o1-backup-prune.timer \
    o1-restore-verify.timer
  bl_log info "enabled o1-backup.timer, o1-backup-prune.timer, and o1-restore-verify.timer"
else
  bl_log info "timers installed but not enabled; re-run with --enable after configuring $backup_env"
fi
