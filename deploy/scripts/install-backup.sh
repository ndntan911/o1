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

# The default is /etc/systemd/system. Tests and unprivileged dry runs override
# it so they never write to the host's unit directory.
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
timeout_sed=
if [[ -n ${O1_BACKUP_TIMEOUT:-} ]]; then
  [[ $O1_BACKUP_TIMEOUT != *$'\n'* ]] ||
    bl_die "O1_BACKUP_TIMEOUT must not contain a newline"
  timeout_sed=$(sed_replacement "$O1_BACKUP_TIMEOUT")
fi

# --- environment and password ---------------------------------------------

# env_key NAME: read one key from backup.env without executing the file.
env_key() {
  bl_env_get "$backup_env" "$1" || true
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
  local repo repo_path candidate init_output key value
  local -a init_env

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

  # Confirm a local repository has no config before generating anything.
  repo_path=$repo
  case $repo_path in
    local:*) repo_path=${repo_path#local:} ;;
  esac
  if [[ $repo_path != *:* && -e $repo_path/config ]]; then
    bl_die "repository $repo already exists; restore $restic_pass from the password manager instead of generating a new password"
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

  init_env=(RESTIC_REPOSITORY="$repo" RESTIC_PASSWORD_FILE="$password_candidate")
  for key in RESTIC_CACHE_DIR AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY; do
    value=$(env_key "$key")
    if [[ -n $value ]]; then
      init_env+=("$key=$value")
    fi
  done
  unset value

  if ! init_output=$(env -u RESTIC_PASSWORD -u RESTIC_PASSWORD_COMMAND \
    "${init_env[@]}" restic init 2>&1); then
    rm -f -- "$password_candidate"
    password_candidate=
    if grep -qiE 'already (exists|initialized)' <<<"$init_output"; then
      bl_die "restic reports that the repository already exists; removed the generated password. Restore the real $restic_pass from the password manager and re-run without --init-new-repo"
    fi
    bl_log error "restic init failed"
    if [[ -n $init_output ]]; then
      bl_log error "$init_output"
    fi
    bl_die "removed the generated password; no password was installed at $restic_pass"
  fi

  # Only pair the new password with the repository after init succeeds.
  mv -f -- "$password_candidate" "$restic_pass"
  password_candidate=

  bl_log info "initialized restic repository $repo"
  bl_log info "copy $restic_pass offsite now; its contents are never printed"
}

# --- enable checks ---------------------------------------------------------

require_enable_config() {
  local ping failure alert
  ping=$(env_key O1_BACKUP_PING_URL)
  if [[ -z $ping ]]; then
    bl_die "--enable requires O1_BACKUP_PING_URL in $backup_env; the units were installed but the timers were not enabled"
  fi
  failure=$(env_key O1_BACKUP_FAILURE_URL)
  alert=$(env_key O1_BACKUP_ALERT_EMAIL)
  if [[ -z $failure && -z $alert ]]; then
    bl_die "--enable requires O1_BACKUP_FAILURE_URL or O1_BACKUP_ALERT_EMAIL in $backup_env; the units were installed but the timers were not enabled"
  fi
}

# --- install ---------------------------------------------------------------

install -d -m 0700 "$root" "$root/backup" "$root/backup/cache" "$root/bin"
install -d -m 0755 "$unit_dir"

ensure_backup_env
ensure_password

for script in "${runtime_scripts[@]}"; do
  install -m 0755 "$source_dir/scripts/$script" "$root/bin/$script"
done
bl_log info "installed backup scripts in $root/bin"

for unit in "${units[@]}"; do
  if [[ $unit == o1-backup.service && -n ${O1_BACKUP_TIMEOUT:-} ]]; then
    render_file "$source_dir/backup/systemd/$unit" "$unit_dir/$unit" 0644 \
      "$O1_BACKUP_TIMEOUT"
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
