#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: apply-resource-policy-linux.sh --agent ACCOUNT [options] [--apply]
       apply-resource-policy-linux.sh --agent ACCOUNT --revert [--apply]

Sets a machine-wide memory and CPU ceiling for agent work in agent-work.slice
under the agent account's user manager, gives its parent agent.slice a lower
CPU weight, enables systemd-oomd for it, and applies the swap, zram and /tmp
policy. Preview by default; --apply changes the machine. Apply backs up every
file it touches and writes a receipt; --revert restores exactly what the
receipt lists.

Reserve-based sizing is the default (read at run time):
  Memory reserve = fixed part (8 GiB desktop, 2 GiB headless) + 2% of RAM
  MemoryMax      = RAM - reserve (refused below 2 GiB)
  MemoryHigh     = MemoryMax - min(10% of MemoryMax, 8 GiB)
  MemorySwapMax  = min(25% of (disk swap + planned zram, or active swap), 8 GiB); 0 with no swap
  CPUWeight      = 50 on agent.slice (app.slice and session.slice keep 100)
  CPUQuota       = (cores - clamp(ceil(cores * 0.05), 2, 4)) * 100%

Options and explicit overrides (whole numbers: at most six digits, no
leading zeros):
  --reserve-fixed-gib N      Override the fixed RAM reserve in GiB (positive number)
  --measure                  Show 1.5x the memory in use outside agent work; informational, never applied
  --cpu-weight N             CPUWeight for agent.slice, 1..10000 (default 50)
  --reserve-cores N          Override reserved CPU cores for CPUQuota (0..1024)
  --memory-high-percent N    Explicit override: MemoryHigh as % of RAM (10..90)
  --memory-max-percent N     Explicit override: MemoryMax as % of RAM (20..95)
  --allow-small-reserve      Accept a --memory-max-percent that leaves less than the reserve or 2 GiB
  --swap-max-percent N       Explicit override: MemorySwapMax as % of RAM (0..50)
  --memory-reserve-percent N Explicit override: reserve as % of RAM (5..50)
  --oomd-pressure-percent N  ManagedOOMMemoryPressureLimit for the slice (default 60)
  --swappiness auto|keep|N   auto: 100 with zram, 10 with disk swap only (default auto)
  --zram-percent N           zram size as a share of RAM; 0 leaves zram alone (default 0)
  --tmp auto|disk|keep       auto: disk-backed /tmp when tmp.mount makes it tmpfs (default auto)
  --revert                   Restore the state recorded in the receipt (alias: --remove)
EOF
}

need_value() {
  (($# >= 2)) || die "Missing value for $1."
  [[ -n "$2" ]] || die "$1 needs a value; it was given an empty one."
}

agent_account=''
reserve_fixed_gib=''
measure=false
cpu_weight=''
reserve_cores=''
memory_reserve_percent=''
memory_high_percent=''
memory_max_percent=''
swap_max_percent=''
allow_small_reserve=false
oomd_pressure_percent=60
swappiness_mode=auto
zram_percent=0
tmp_mode=auto
revert=false
while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agent) need_value "$@"; agent_account=$2; shift 2 ;;
    --reserve-fixed-gib) need_value "$@"; reserve_fixed_gib=$2; shift 2 ;;
    --measure) measure=true; shift ;;
    --cpu-weight) need_value "$@"; cpu_weight=$2; shift 2 ;;
    --reserve-cores) need_value "$@"; reserve_cores=$2; shift 2 ;;
    --memory-reserve-percent) need_value "$@"; memory_reserve_percent=$2; shift 2 ;;
    --memory-reserve-gib) die '--memory-reserve-gib is replaced by --reserve-fixed-gib (fixed reserve GiB) or --memory-reserve-percent (explicit RAM share).' ;;
    --memory-high-percent) need_value "$@"; memory_high_percent=$2; shift 2 ;;
    --memory-max-percent) need_value "$@"; memory_max_percent=$2; shift 2 ;;
    --allow-small-reserve) allow_small_reserve=true; shift ;;
    --swap-max-percent) need_value "$@"; swap_max_percent=$2; shift 2 ;;
    --oomd-pressure-percent) need_value "$@"; oomd_pressure_percent=$2; shift 2 ;;
    --swappiness) need_value "$@"; swappiness_mode=$2; shift 2 ;;
    --zram-percent) need_value "$@"; zram_percent=$2; shift 2 ;;
    --tmp) need_value "$@"; tmp_mode=$2; shift 2 ;;
    --revert|--remove) revert=true; shift ;;
    --help|-h) usage; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done

# check_int VAR FLAG LOWER UPPER: validate the text of VAR before any
# arithmetic, check its range, and store it normalised.
check_int() {
  local var=$1 name=$2 lower=$3 upper=$4 value
  value=$(normalize_int "${!var}") ||
    die "$name must be a whole number from $lower through $upper, with at most six digits and no leading zeros (got '${!var}')."
  ((value >= lower && value <= upper)) || die "$name must be from $lower through $upper (got $value)."
  printf -v "$var" '%s' "$value"
}

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -n "$agent_account" ]] || die '--agent is required.'
validate_unix_name "$agent_account"
if [[ -n "$reserve_fixed_gib" && -n "$memory_reserve_percent" ]]; then
  die 'Cannot combine --reserve-fixed-gib with --memory-reserve-percent; choose one reserve override.'
fi
if [[ -n "$reserve_fixed_gib" ]]; then
  value=$(normalize_gib "$reserve_fixed_gib") ||
    die "--reserve-fixed-gib must be a positive number with at most six digits before and after the point and no leading zeros (got '$reserve_fixed_gib')."
  reserve_fixed_gib=$value
fi
[[ -z "$memory_reserve_percent" ]] || check_int memory_reserve_percent --memory-reserve-percent 5 50
[[ -z "$memory_high_percent" ]] || check_int memory_high_percent --memory-high-percent 10 90
[[ -z "$memory_max_percent" ]] || check_int memory_max_percent --memory-max-percent 20 95
if [[ -n "$memory_high_percent" && -n "$memory_max_percent" ]]; then
  ((memory_max_percent > memory_high_percent)) || die '--memory-max-percent must be greater than --memory-high-percent.'
fi
[[ -z "$swap_max_percent" ]] || check_int swap_max_percent --swap-max-percent 0 50
[[ -z "$cpu_weight" ]] || check_int cpu_weight --cpu-weight 1 10000
[[ -z "$reserve_cores" ]] || check_int reserve_cores --reserve-cores 0 1024
check_int oomd_pressure_percent --oomd-pressure-percent 10 99
check_int zram_percent --zram-percent 0 100
case "$swappiness_mode" in
  auto|keep) ;;
  *) check_int swappiness_mode --swappiness 0 200 ;;
esac
case "$tmp_mode" in auto|disk|keep) ;; *) die '--tmp must be auto, disk or keep.' ;; esac
id "$agent_account" >/dev/null 2>&1 || die "Unknown account: $agent_account"
uid=$(id -u "$agent_account")
[[ "$uid" =~ ^[0-9]+$ ]] || die "Cannot read the UID of $agent_account."

# Unprivileged fixture tests point every host path at a fixture tree. Root
# never honours it: common.sh re-runs root with an empty environment, and this
# check covers the case where that re-run could not happen.
fs_root=''
owner_args=(-o root -g root)
stop_after=''
if [[ -n ${AWK_RESOURCE_TEST_ROOT:-} ]]; then
  [[ ${EUID:-$(id -u)} -ne 0 ]] || die 'AWK_RESOURCE_TEST_ROOT is for unprivileged fixture tests only.'
  [[ "$AWK_RESOURCE_TEST_ROOT" == /* && -d "$AWK_RESOURCE_TEST_ROOT" ]] || die 'AWK_RESOURCE_TEST_ROOT must be an existing absolute directory.'
  fs_root=$AWK_RESOURCE_TEST_ROOT
  owner_args=()
  # Fault injection for the fixture tests: stop dead (SIGKILL, no traps) right
  # after the Nth change to the host, before its completion is recorded.
  if [[ -n ${AWK_RESOURCE_TEST_STOP_AFTER:-} ]]; then
    [[ "$AWK_RESOURCE_TEST_STOP_AFTER" =~ ^[1-9][0-9]{0,3}$ ]] || die 'AWK_RESOURCE_TEST_STOP_AFTER must be a small positive number.'
    stop_after=$AWK_RESOURCE_TEST_STOP_AFTER
  fi
else
  require_root_for_apply
fi

host_path() {
  printf '%s%s' "$fs_root" "$1"
}

changes_made=0
stop_point() {
  changes_made=$((changes_made + 1))
  if [[ -n "$stop_after" ]] && ((changes_made == stop_after)); then
    kill -KILL "$$"
  fi
}

slice_name=agent-work.slice
slice_path=/etc/systemd/user/$slice_name
parent_slice_dir=/etc/systemd/user/agent.slice.d
parent_slice_path=$parent_slice_dir/50-agent-workstation-kit.conf
sysctl_path=/etc/sysctl.d/60-agent-workstation-swap.conf
zram_path=/etc/systemd/zram-generator.conf
tmp_mask_path=/etc/systemd/system/tmp.mount
old_dropin=/etc/systemd/system/user-$uid.slice.d/50-agent-workstation.conf
legacy_dropin=/etc/systemd/system/user-$uid.slice.d/50-agent-fleet.conf
state_parent=/var/lib/agent-workstation-kit
lock_file=$state_parent/resource-policy.lock
state_dir=$state_parent/resource-policy
receipt=$state_dir/receipt.tsv
tmp_suffix=.agent-workstation-tmp

user_manager_active() {
  systemctl is-active --quiet "user@$uid.service" 2>/dev/null
}

reload_managers() {
  systemctl daemon-reload
  if user_manager_active; then
    systemctl --user --machine="$agent_account@" daemon-reload
  fi
}

# The state directory, lock file, receipt and every parent must be real
# directories and files: a link could redirect root's writes or a revert's restores.
refuse_state_links() {
  local path
  for path in /var /var/lib "$state_parent" "$lock_file" "$state_dir" "$state_dir/backups" "$state_dir/installed" "$receipt"; do
    [[ ! -L "$(host_path "$path")" ]] || die "$path is a symbolic link; the resource-policy state must not be. Review it by hand."
  done
}

# check_trusted_dir PATH: require PATH to be a real directory (not a symlink),
# owned by root (UID 0), and not group- or world-writable.
check_trusted_dir() {
  local path=$1 target stat_out owner mode ftype
  target=$(host_path "$path")
  [[ ! -L "$target" ]] || die "$path is a symbolic link; the resource-policy state must not be. Review it by hand."
  [[ -d "$target" ]] || die "$path is not a real directory; review it by hand."
  stat_out=$(stat -c '%u %a %F' -- "$target") || die "Cannot inspect $path; review it by hand."
  IFS=' ' read -r owner mode ftype <<<"$stat_out"
  [[ "$ftype" == directory ]] || die "$path is not a real directory ($ftype); review it by hand."
  [[ "$owner" == 0 ]] || die "$path is owned by UID $owner, not root (0); review it by hand."
  [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die "Cannot read permissions of $path; review it by hand."
  (( (8#$mode & 8#022) == 0 )) || die "$path is group- or world-writable (mode $mode); review it by hand."
}

# check_trusted_lock_file PATH: an existing lock file must be a regular file
# owned by root (UID 0), never a symlink or special file.
check_trusted_lock_file() {
  local path=$1 target stat_out owner ftype
  target=$(host_path "$path")
  [[ ! -L "$target" ]] || die "$path is a symbolic link; the resource-policy state must not be. Review it by hand."
  [[ ! -d "$target" ]] || die "$path is a directory; review it by hand."
  stat_out=$(stat -c '%u %F' -- "$target") || die "Cannot inspect $path; review it by hand."
  IFS=' ' read -r owner ftype <<<"$stat_out"
  [[ "$ftype" == 'regular file' || "$ftype" == 'regular empty file' ]] ||
    die "$path is not a regular file ($ftype); review it by hand."
  [[ "$owner" == 0 ]] || die "$path is owned by UID $owner, not root (0); review it by hand."
}

# verify_lock_fd EXPECTED_PATH: after opening fd 9, confirm through
# /proc/self/fd/9 (Linux) that it resolves to EXPECTED_PATH, and through
# stat -L that it is a regular file owned by root.
verify_lock_fd() {
  local expected_path=$1 fd_path stat_out fd_owner fd_ftype
  if [[ -z "$fs_root" || -L /proc/self/fd/9 ]]; then
    [[ -L /proc/self/fd/9 ]] || { exec 9>&-; die "Cannot verify open lock descriptor /proc/self/fd/9; review $lock_file by hand."; }
    fd_path=$(readlink -- /proc/self/fd/9) || { exec 9>&-; die "Cannot read /proc/self/fd/9; review $lock_file by hand."; }
    [[ "$fd_path" == "$expected_path" ]] ||
      { exec 9>&-; die "Lock descriptor resolved to $fd_path instead of $expected_path; review $lock_file by hand."; }
    stat_out=$(stat -L -c '%u %F' -- /proc/self/fd/9) ||
      { exec 9>&-; die "Cannot inspect /proc/self/fd/9; review $lock_file by hand."; }
  else
    # Unprivileged fixture test on a non-Linux host (macOS) without /proc/self/fd.
    stat_out=$(stat -L -c '%u %F' -- /dev/fd/9) ||
      { exec 9>&-; die "Cannot inspect /dev/fd/9; review $lock_file by hand."; }
  fi
  IFS=' ' read -r fd_owner fd_ftype <<<"$stat_out"
  [[ "$fd_ftype" == 'regular file' || "$fd_ftype" == 'regular empty file' ]] ||
    { exec 9>&-; die "Lock descriptor is not a regular file ($fd_ftype); review $lock_file by hand."; }
  [[ "$fd_owner" == 0 ]] ||
    { exec 9>&-; die "Lock descriptor is owned by UID $fd_owner, not root (0); review $lock_file by hand."; }
}

# create_durable_dir PATH MODE: create PATH and flush both the new folder and
# its parent to disk when it does not exist yet.
create_durable_dir() {
  local path=$1 mode=$2 target
  target=$(host_path "$path")
  if [[ ! -d "$target" ]]; then
    [[ ! -e "$target" && ! -L "$target" ]] || die "$path exists and is not a directory; review it by hand."
    install -d -m "$mode" "${owner_args[@]}" "$target"
    sync -- "$target" "$(dirname -- "$target")"
  fi
}

# Hold an exclusive lock on a persistent file outside the state directory until
# this process exits, so an apply and a revert never run at the same time and
# deleting the state directory on revert keeps the lock valid.
lock_held=false
lock_state() {
  local parent_target target lock_name expected_path old_pwd old_umask created_lock=false
  [[ "$lock_held" != true ]] || return 0
  command -v flock >/dev/null 2>&1 || die 'flock (util-linux) is required to lock the resource-policy state.'
  refuse_state_links
  check_trusted_dir /var/lib
  parent_target=$(host_path "$state_parent")
  if [[ -e "$parent_target" || -L "$parent_target" ]]; then
    check_trusted_dir "$state_parent"
  else
    create_durable_dir "$state_parent" 0755
    check_trusted_dir "$state_parent"
  fi
  refuse_state_links
  target=$(host_path "$lock_file")
  lock_name=${lock_file##*/}
  if [[ -e "$target" || -L "$target" ]]; then
    check_trusted_lock_file "$lock_file"
  else
    created_lock=true
  fi
  old_pwd=$PWD
  old_umask=$(umask)
  umask 077
  cd -P -- "$parent_target" || { umask "$old_umask"; die "Cannot enter $state_parent; review it by hand."; }
  expected_path=$PWD/$lock_name
  if [[ -z "$fs_root" && "$expected_path" != "$lock_file" ]]; then
    cd -- "$old_pwd" || true
    umask "$old_umask"
    die "$lock_file resolved to $expected_path instead of $lock_file; review it by hand."
  fi
  exec 9>>"$lock_name"
  cd -- "$old_pwd" || { exec 9>&-; umask "$old_umask"; die "Cannot return to $old_pwd."; }
  umask "$old_umask"
  verify_lock_fd "$expected_path"
  flock -n 9 || { exec 9>&-; die "Another apply or revert holds the lock on $lock_file; wait for it to finish."; }
  chmod 0600 "$target"
  if [[ "$created_lock" == true ]]; then
    sync -- "$target" "$(dirname -- "$target")"
  fi
  lock_held=true
}

# write_atomic SOURCE DEST [MODE]: copy SOURCE to a temporary file in DEST's
# folder, flush it to disk, then rename it over DEST in one step. Without MODE
# the copy keeps SOURCE's mode and owner (used to restore backups).
write_atomic() {
  local source=$1 dest=$2 mode=${3:-} tmp
  tmp=$dest$tmp_suffix
  rm -f -- "$tmp"
  if [[ -n "$mode" ]]; then
    install -m "$mode" "${owner_args[@]}" "$source" "$tmp"
  else
    cp -p -- "$source" "$tmp"
  fi
  sync -- "$tmp"
  mv -T -- "$tmp" "$dest"
  sync -- "${dest%/*}"
}

# record FIELD...: append one tab-separated line to the receipt atomically.
record() {
  local IFS=$'\t' file=$fs_root$receipt
  { [[ ! -f "$file" ]] || cat -- "$file"; printf '%s\n' "$*"; } >"$work_dir/receipt"
  write_atomic "$work_dir/receipt" "$file" 0600
}

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

# ---------------------------------------------------------------------------
# Receipt. Version 2, formula 2 lines (tab-separated, '-' for an unset value):
#   version 2
#   agent ACCOUNT
#   formula 2            formula version that sized this policy
#   input KEY VALUE      approved inputs (flags, normalised)
#   reading KEY VALUE    host readings the values were derived from
#   value KEY VALUE      every resolved value written to the host
#   begin ID KIND PATH ACTION BACKUP INSTALLED   recorded before a change
#   done ID              recorded after the change
#   reverting ID         recorded before revert restores it
#   undone ID            recorded after revert restored it
#   state complete       the apply finished
# Receipts from an earlier receipt version or formula version are refused.
# ---------------------------------------------------------------------------
current_receipt_version=2
current_formula_version=2
earlier_receipt_msg="This host's resource policy was applied by an earlier version. Revert it with the version that applied it, then apply again."
declare -A r_input=() r_reading=() r_value=()
declare -A step_kind=() step_path=() step_action=() step_backup=() step_installed=() step_done=() step_undone=()
declare -A step_reverting=()
step_ids=()
receipt_version=''
receipt_formula=''
receipt_complete=false

load_receipt() {
  local file line kind a b c d e f first_kind first_val first_extra
  r_input=() r_reading=() r_value=()
  step_kind=() step_path=() step_action=() step_backup=() step_installed=() step_done=() step_undone=()
  step_reverting=()
  step_ids=()
  receipt_version=''
  receipt_formula=''
  receipt_complete=false
  file=$(host_path "$receipt")
  mapfile -t receipt_lines <"$file"
  IFS=$'\t' read -r first_kind first_val first_extra <<<"${receipt_lines[0]:-}"
  [[ "$first_kind" == version && "$first_val" =~ ^[1-9][0-9]{0,5}$ && -z "$first_extra" ]] ||
    die "Unsupported receipt format in $receipt."
  receipt_version=$first_val
  if ((receipt_version < current_receipt_version)); then
    die "$earlier_receipt_msg"
  fi
  if ((receipt_version > current_receipt_version)); then
    die "Unsupported receipt format in $receipt (version $receipt_version)."
  fi
  [[ "${receipt_lines[1]:-}" == $'agent\t'"$agent_account" ]] || die 'The receipt belongs to another agent account; refusing.'
  for line in "${receipt_lines[@]:2}"; do
    IFS=$'\t' read -r kind a b _ <<<"$line"
    if [[ "$kind" == formula ]]; then
      [[ -z "$receipt_formula" && "$a" =~ ^[1-9][0-9]{0,5}$ && -z "$b" ]] ||
        die "Unsupported receipt formula marker in $receipt."
      receipt_formula=$a
    fi
  done
  if [[ -z "$receipt_formula" ]] || ((receipt_formula < current_formula_version)); then
    die "$earlier_receipt_msg"
  fi
  if ((receipt_formula > current_formula_version)); then
    die "Unsupported receipt formula version in $receipt ($receipt_formula)."
  fi
  for line in "${receipt_lines[@]:2}"; do
    IFS=$'\t' read -r kind a b c d e f <<<"$line"
    case "$kind" in
      formula) ;;
      input) r_input[$a]=$b ;;
      reading) r_reading[$a]=$b ;;
      value) r_value[$a]=$b ;;
      begin)
        step_ids+=("$a")
        step_kind[$a]=$b step_path[$a]=$c step_action[$a]=$d step_backup[$a]=$e step_installed[$a]=$f
        ;;
      done) step_done[$a]=true ;;
      reverting) step_reverting[$a]=true ;;
      undone) step_undone[$a]=true ;;
      state)
        case "$a" in
          complete) receipt_complete=true ;;
          *) die "Unknown state in $receipt: $a" ;;
        esac
        ;;
      *) die "Unknown line in $receipt: $kind" ;;
    esac
  done
}

# ---------------------------------------------------------------------------
# Revert: restore exactly what the receipt lists, newest change first.
# ---------------------------------------------------------------------------
if [[ "$revert" == true ]]; then
  refuse_state_links
  if [[ "$APPLY_CHANGES" == true && ( -e "$(host_path "$lock_file")" || -L "$(host_path "$lock_file")" ) ]]; then
    lock_state
  fi
  [[ -f "$(host_path "$receipt")" ]] || die "No receipt at $receipt; there is nothing to revert."
  load_receipt
  if [[ "$APPLY_CHANGES" == true && "$lock_held" != true ]]; then
    lock_state
    [[ -f "$(host_path "$receipt")" ]] || die "The receipt at $receipt disappeared while waiting for the lock."
    load_receipt
  fi

  problems=()
  for id in "${step_ids[@]}"; do
    [[ -z ${step_undone[$id]:-} ]] || continue
    kind=${step_kind[$id]} path=${step_path[$id]} action=${step_action[$id]}
    backup=$(host_path "$state_dir/backups/${step_backup[$id]}")
    installed=$(host_path "$state_dir/installed/${step_installed[$id]}")
    current=$(host_path "$path")
    # A step recorded but not finished, or one a stopped revert had begun to
    # restore, may be in its earlier or later state; a finished step must
    # still be exactly what apply left.
    finished=${step_done[$id]:-false}
    [[ -z ${step_reverting[$id]:-} ]] || finished=false
    case "$kind" in
      file)
        if [[ -L "$current" ]]; then
          problems+=("$path is now a symbolic link")
        elif [[ ! -e "$current" ]]; then
          [[ "$action" != replaced ]] || problems+=("$path was deleted after apply")
        elif [[ "$action" == created ]]; then
          { [[ -f "$installed" ]] && cmp -s "$installed" "$current"; } || problems+=("$path changed after apply")
        elif [[ "$action" == removed ]]; then
          [[ "$finished" != true ]] && cmp -s "$backup" "$current" || problems+=("$path was recreated after apply")
        elif [[ "$finished" == true ]]; then
          cmp -s "$installed" "$current" || problems+=("$path changed after apply")
        else
          { cmp -s "$installed" "$current" 2>/dev/null || cmp -s "$backup" "$current"; } || problems+=("$path changed after apply")
        fi
        ;;
      mask)
        [[ ! -e "$current" && ! -L "$current" ]] ||
          [[ -L "$current" && $(readlink "$current") == /dev/null ]] || problems+=("$path is no longer the mask link")
        ;;
      dir|sysctl|linger|service) ;;
      *) problems+=("unknown receipt entry: $kind") ;;
    esac
  done
  if ((${#problems[@]})); then
    for problem in "${problems[@]}"; do warn "$problem"; done
    die 'The machine changed since apply; review these paths by hand. Nothing was reverted.'
  fi

  log "Revert of the resource policy for $agent_account, from $receipt:"
  for ((index = ${#step_ids[@]} - 1; index >= 0; index--)); do
    id=${step_ids[index]}
    [[ -z ${step_undone[$id]:-} ]] || continue
    path=${step_path[$id]} action=${step_action[$id]}
    case "${step_kind[$id]}:$action" in
      file:created) log "  remove $path" ;;
      file:replaced|file:removed) log "  restore $path from its backup" ;;
      mask:*) log "  unmask: remove $path" ;;
      dir:*) log "  remove the empty directory $path" ;;
      sysctl:*) log "  set $path back to $action" ;;
      linger:*) log "  loginctl disable-linger $path" ;;
      service:*) log "  systemctl disable --now $path" ;;
    esac
  done
  log "  remove $state_dir"
  if [[ "$APPLY_CHANGES" != true ]]; then
    log 'Preview only. Re-run with --apply to revert.'
    exit 0
  fi

  for ((index = ${#step_ids[@]} - 1; index >= 0; index--)); do
    id=${step_ids[index]}
    [[ -z ${step_undone[$id]:-} ]] || continue
    path=${step_path[$id]} action=${step_action[$id]}
    current=$(host_path "$path")
    record reverting "$id"
    case "${step_kind[$id]}:$action" in
      file:created)
        rm -f -- "$current" "$current$tmp_suffix"
        if [[ -d "$(dirname -- "$current")" ]]; then
          sync -- "$(dirname -- "$current")"
        fi
        ;;
      file:replaced|file:removed)
        write_atomic "$(host_path "$state_dir/backups/${step_backup[$id]}")" "$current"
        ;;
      mask:*)
        rm -f -- "$current"
        if [[ -d "$(dirname -- "$current")" ]]; then
          sync -- "$(dirname -- "$current")"
        fi
        ;;
      dir:*)
        if [[ -d "$current" ]]; then
          if rmdir -- "$current" 2>/dev/null; then
            sync -- "$(dirname -- "$current")"
          else
            warn "$path is not empty; left in place."
          fi
        fi
        ;;
      sysctl:*) printf '%s\n' "$action" >"$(host_path /proc/sys/vm/swappiness)" ;;
      linger:*) loginctl disable-linger "$path" ;;
      service:*) systemctl disable --now "$path" ;;
    esac
    stop_point
    record undone "$id"
  done
  reload_managers
  rm -rf -- "$(host_path "$state_dir")"
  sync -- "$(host_path "$state_parent")"
  log 'Resource policy reverted. Running agent work keeps its cgroup until it restarts; zram and /tmp return to their earlier state at the next reboot.'
  exit 0
fi

# ---------------------------------------------------------------------------
# Detect what this host has. Refuse when something the policy needs is missing.
# ---------------------------------------------------------------------------
if [[ -e "$(host_path "$legacy_dropin")" ]]; then
  die "Legacy policy exists at $legacy_dropin. Review and remove it through an approved schema-v2 migration."
fi

[[ -d "$(host_path /run/systemd/system)" ]] || die 'systemd is not the running init system; this policy needs it.'
systemd_version=$(systemctl --version 2>/dev/null | awk 'NR == 1 && $1 == "systemd" {print $2}') || true
[[ "$systemd_version" =~ ^[0-9]+$ ]] || die 'Cannot read the systemd version from systemctl --version.'
((systemd_version >= 248)) || die "systemd $systemd_version is too old; the agent-work slice needs 248 or newer for oomd on user units."
controllers=$(cat "$(host_path /sys/fs/cgroup/cgroup.controllers)" 2>/dev/null) || true
grep -qw memory <<<"$controllers" ||
  die 'The unified cgroup v2 hierarchy with the memory controller is not available.'
grep -qw cpu <<<"$controllers" ||
  die 'The cgroup v2 cpu controller is not available, so agent.slice cannot hold CPUWeight or agent-work.slice CPUQuota.'
delegated=$(systemctl show --property=DelegateControllers --value "user@$uid.service" 2>/dev/null) || true
grep -qw memory <<<"$delegated" ||
  die "user@$uid.service does not delegate the memory controller, so a user-level slice cannot hold a memory ceiling."
grep -qw cpu <<<"$delegated" ||
  die "user@$uid.service does not delegate the cpu controller, so agent.slice cannot hold CPUWeight and agent-work.slice cannot hold CPUQuota."
refuse_state_links

gib() {
  awk -v kib="$1" 'BEGIN {printf "%.1f GiB", kib / 1048576}'
}

total_kib=$(awk '$1 == "MemTotal:" {print $2}' "$(host_path /proc/meminfo)" 2>/dev/null) || true
[[ "$total_kib" =~ ^[0-9]{1,12}$ ]] || die 'Cannot read total memory from /proc/meminfo.'

cpu_cores=$(awk '/^processor[[:space:]]*:/ {c++} END {print c + 0}' "$(host_path /proc/cpuinfo)" 2>/dev/null) || true
if [[ ! "$cpu_cores" =~ ^[0-9]+$ || "$cpu_cores" -lt 1 ]] && [[ -z "$fs_root" ]]; then
  cpu_cores=$(nproc 2>/dev/null || true)
fi
[[ "$cpu_cores" =~ ^[0-9]{1,5}$ && "$cpu_cores" -ge 1 ]] || die 'Cannot read CPU core count from /proc/cpuinfo.'

desktop_present=false
desktop_reason='headless (no graphical target or display manager active)'
if systemctl is-active --quiet graphical.target 2>/dev/null; then
  desktop_present=true
  desktop_reason='graphical.target active'
else
  for dm in display-manager.service gdm.service gdm3.service sddm.service lightdm.service; do
    if systemctl is-active --quiet "$dm" 2>/dev/null; then
      desktop_present=true
      desktop_reason="$dm active"
      break
    fi
  done
fi
role_label=headless
[[ "$desktop_present" != true ]] || role_label=desktop

oomd_available=false
for candidate in /usr/lib/systemd/systemd-oomd /lib/systemd/systemd-oomd; do
  [[ -x "$(host_path "$candidate")" ]] && oomd_available=true
done
oomd_enabled=false
if [[ "$oomd_available" == true ]]; then
  case $(systemctl is-enabled systemd-oomd.service 2>/dev/null || true) in
    enabled|enabled-runtime|static|alias) oomd_enabled=true ;;
  esac
fi
# Vendor drop-ins (Ubuntu's systemd-oomd package ships some) can let oomd act
# on every user manager or on the root slice. A drop-in in /etc with the same
# name replaces the vendor one, so the policy overrides each with ManagedOOM
# set to auto, leaving agent-work.slice as the only cgroup oomd acts on.
oomd_vendor_dropins=()
if [[ "$oomd_available" == true ]]; then
  for vendor_dir in /usr/lib/systemd/system /lib/systemd/system; do
    for unit_dir in 'user@.service.d' '-.slice.d'; do
      for vendor in "$(host_path "$vendor_dir/$unit_dir")"/*oomd*.conf; do
        [[ -f "$vendor" ]] || continue
        override="/etc/systemd/system/$unit_dir/${vendor##*/}"
        [[ " ${oomd_vendor_dropins[*]} " == *" $override "* ]] || oomd_vendor_dropins+=("$override")
      done
    done
  done
fi

zram_swaps=0
disk_swaps=0
disk_swap_kib=0
zram_swap_kib=0
while read -r swap_name _ swap_size _; do
  case "$swap_name" in
    Filename|'') ;;
    /dev/zram*)
      zram_swaps=$((zram_swaps + 1))
      [[ "$swap_size" =~ ^[0-9]{1,15}$ ]] && zram_swap_kib=$((zram_swap_kib + swap_size))
      ;;
    *)
      disk_swaps=$((disk_swaps + 1))
      [[ "$swap_size" =~ ^[0-9]{1,15}$ ]] && disk_swap_kib=$((disk_swap_kib + swap_size))
      ;;
  esac
done <"$(host_path /proc/swaps)"
swap_total_kib=$((disk_swap_kib + zram_swap_kib))

# Whenever --zram-percent is set, its planned size replaces any active zram
# in the swap total so MemorySwapMax is sized for the swap after reboot.
zram_planned_kib=0
if ((zram_percent > 0)); then
  zram_planned_kib=$((total_kib * zram_percent / 100))
fi

current_swappiness=$(cat "$(host_path /proc/sys/vm/swappiness)" 2>/dev/null) || true
[[ "$current_swappiness" =~ ^[0-9]{1,3}$ ]] || die 'Cannot read vm.swappiness.'
case "$swappiness_mode" in
  keep) swappiness='' ;;
  auto)
    if ((zram_percent > 0 || zram_swaps > 0)); then
      swappiness=100
    elif ((disk_swaps > 0)); then
      swappiness=10
    else
      swappiness=''
    fi
    ;;
  *) swappiness=$swappiness_mode ;;
esac

# ---------------------------------------------------------------------------
# An existing receipt: a repeat apply with the same approved inputs reuses the
# readings it recorded, so a changed memory or swap reading never resizes the
# policy without a revert and a new review.
# ---------------------------------------------------------------------------
input_keys=(reserve_fixed_gib memory_reserve_percent memory_high_percent memory_max_percent swap_max_percent
  cpu_weight reserve_cores oomd_pressure_percent swappiness zram_percent tmp allow_small_reserve)
declare -A inputs=(
  [reserve_fixed_gib]=${reserve_fixed_gib:--} [memory_reserve_percent]=${memory_reserve_percent:--}
  [memory_high_percent]=${memory_high_percent:--} [memory_max_percent]=${memory_max_percent:--}
  [swap_max_percent]=${swap_max_percent:--} [cpu_weight]=${cpu_weight:--} [reserve_cores]=${reserve_cores:--}
  [oomd_pressure_percent]=$oomd_pressure_percent [swappiness]=$swappiness_mode [zram_percent]=$zram_percent
  [tmp]=$tmp_mode [allow_small_reserve]=$allow_small_reserve
)
if [[ "$APPLY_CHANGES" == true && ( -e "$(host_path "$lock_file")" || -L "$(host_path "$lock_file")" ) ]]; then
  lock_state
fi
receipt_present=false
reuse_readings=false
if [[ -e "$(host_path "$receipt")" ]]; then
  receipt_present=true
  load_receipt
  [[ "$receipt_complete" == true ]] ||
    die "An earlier apply stopped part way ($receipt). Run --revert --apply first, then preview and apply again."
  inputs_differ=()
  for key in "${input_keys[@]}"; do
    [[ "${r_input[$key]:-}" == "${inputs[$key]}" ]] || inputs_differ+=("$key: receipt ${r_input[$key]:-unset}, now ${inputs[$key]}")
  done
  if ((${#inputs_differ[@]})); then
    for difference in "${inputs_differ[@]}"; do warn "approved input differs: $difference"; done
    die "The options differ from the approved inputs in $receipt. Run --revert --apply first, then preview and apply again."
  fi
  reuse_readings=true
  for key in ram_kib swap_kib zram_kib zram_planned_kib cpu_cores; do
    [[ "${r_reading[$key]:-}" =~ ^[0-9]{1,15}$ ]] || die "The receipt at $receipt lacks the reading $key; review it by hand."
  done
  ((r_reading[zram_kib] <= r_reading[swap_kib])) || die "The receipt at $receipt has zram_kib above swap_kib; review it by hand."
  ((r_reading[zram_planned_kib] == r_reading[ram_kib] * zram_percent / 100)) ||
    die "The receipt at $receipt has zram_planned_kib (${r_reading[zram_planned_kib]}) that does not match --zram-percent ($zram_percent); review it by hand."
  [[ "${r_reading[role]:-}" == desktop || "${r_reading[role]:-}" == headless ]] ||
    die "The receipt at $receipt lacks the host role; review it by hand."
  for key in ram_kib swap_kib zram_kib cpu_cores; do
    case "$key" in
      ram_kib) now=$total_kib ;;
      swap_kib) now=$swap_total_kib ;;
      zram_kib) now=$zram_swap_kib ;;
      cpu_cores) now=$cpu_cores ;;
    esac
    [[ "$now" == "${r_reading[$key]}" ]] ||
      warn "The host now reads $key=$now; the approved reading ${r_reading[$key]} from the receipt is kept. Revert and apply again to resize."
  done
  total_kib=${r_reading[ram_kib]}
  swap_total_kib=${r_reading[swap_kib]}
  zram_swap_kib=${r_reading[zram_kib]}
  disk_swap_kib=$((swap_total_kib - zram_swap_kib))
  zram_planned_kib=${r_reading[zram_planned_kib]}
  cpu_cores=${r_reading[cpu_cores]}
  if [[ "${r_reading[role]}" != "$role_label" ]]; then
    warn "The host role now reads $role_label; the approved role ${r_reading[role]} from the receipt is kept."
    role_label=${r_reading[role]}
    desktop_reason='approved in the receipt'
  fi
  swappiness=${r_value[swappiness]:--}
  [[ "$swappiness" != - ]] || swappiness=''
fi

role_default_gib=2
[[ "$role_label" != desktop ]] || role_default_gib=8
role_default_kib=$((role_default_gib * 1024 * 1024))
tasks_max=$(calculate_tasks_max "$total_kib")

# ---------------------------------------------------------------------------
# Calculate the memory reserve, MemoryMax, MemoryHigh, MemorySwapMax and CPU.
# ---------------------------------------------------------------------------
min_slice_kib=$((2 * 1024 * 1024))
measured_raw_kib=''
measured_scaled_kib=''
if [[ "$measure" == true ]]; then
  read -r measured_raw_kib measured_scaled_kib < <(
    measure_non_agent_memory_kib "$(host_path /sys/fs/cgroup)" "$uid"
  ) || die '--measure could not read memory.current from the top-level cgroups.'
fi

if [[ -n "$memory_reserve_percent" ]]; then
  reserve_kib=$((total_kib * memory_reserve_percent / 100))
  reserve_derivation="${memory_reserve_percent}% of RAM (override: --memory-reserve-percent)"
else
  if [[ -n "$reserve_fixed_gib" ]]; then
    fixed_kib=$(gib_to_kib "$reserve_fixed_gib")
    fixed_desc="${reserve_fixed_gib} GiB override (--reserve-fixed-gib)"
  else
    fixed_kib=$role_default_kib
    fixed_desc="${role_default_gib} GiB ${role_label} default"
  fi
  reserve_kib=$(calculate_memory_reserve_kib "$total_kib" "$fixed_kib")
  reserve_derivation="${fixed_desc} + 2% of RAM"
fi

reserve_note="reserve $(gib "$reserve_kib"): ${reserve_derivation}"
if [[ -n "$memory_max_percent" ]]; then
  memory_max=$((total_kib * memory_max_percent / 100))
  ((memory_max >= min_slice_kib)) || die "MemoryMax ($(gib "$memory_max"), from --memory-max-percent ${memory_max_percent}%) is below 2.0 GiB; this machine is too small for an agent-work slice."
  outside_kib=$((total_kib - memory_max))
  reserve_note="reserve overridden by --memory-max-percent; the reserve would be $(gib "$reserve_kib"): ${reserve_derivation}"
  if ((outside_kib < reserve_kib || outside_kib < min_slice_kib)); then
    [[ "$allow_small_reserve" == true ]] ||
      die "A ${memory_max_percent}% ceiling leaves $(gib "$outside_kib") outside agent work, less than the reserve ($(gib "$reserve_kib"): ${reserve_derivation}) or 2.0 GiB. Lower --memory-max-percent, or accept it with --allow-small-reserve."
    reserve_note="$reserve_note; less than the reserve, accepted with --allow-small-reserve"
  fi
  memory_max_derivation="${memory_max_percent}% of $(gib "$total_kib") RAM (override: --memory-max-percent)"
else
  if ((total_kib <= reserve_kib || total_kib - reserve_kib < min_slice_kib)); then
    die "Host RAM ($(gib "$total_kib")) minus reserve ($(gib "$reserve_kib")) is below 2.0 GiB; this machine is too small for an agent-work slice."
  fi
  memory_max=$((total_kib - reserve_kib))
  memory_max_derivation="$(gib "$total_kib") RAM - $(gib "$reserve_kib") reserve (${reserve_derivation})"
fi

if [[ -n "$memory_high_percent" ]]; then
  memory_high=$((total_kib * memory_high_percent / 100))
  ((memory_high > 0 && memory_high < memory_max)) || die "MemoryHigh ($(gib "$memory_high"), from --memory-high-percent ${memory_high_percent}%) must be positive and below MemoryMax ($(gib "$memory_max"))."
  memory_high_derivation="${memory_high_percent}% of $(gib "$total_kib") RAM (override: --memory-high-percent)"
else
  memory_high=$(calculate_memory_high_kib "$memory_max") || die 'Cannot calculate MemoryHigh below MemoryMax.'
  high_margin=$((memory_max - memory_high))
  memory_high_derivation="$(gib "$memory_max") MemoryMax - $(gib "$high_margin") (min(10% of MemoryMax, 8 GiB))"
fi

if ((zram_planned_kib > 0)); then
  swap_basis_kib=$((disk_swap_kib + zram_planned_kib))
else
  swap_basis_kib=$swap_total_kib
fi
if [[ -n "$swap_max_percent" ]]; then
  swap_max=$((total_kib * swap_max_percent / 100))
  swap_max_derivation="${swap_max_percent}% of $(gib "$total_kib") RAM (override: --swap-max-percent)"
else
  swap_max=$(calculate_swap_max_kib "$swap_basis_kib")
  if ((swap_basis_kib == 0)); then
    swap_max_derivation='no swap configured (formula: min(25% of swap, 8 GiB))'
  elif ((zram_planned_kib > 0 && zram_swap_kib > 0)); then
    swap_max_derivation="min(25% of ($(gib "$disk_swap_kib") disk swap + $(gib "$zram_planned_kib") planned zram at ${zram_percent}% of RAM, replacing $(gib "$zram_swap_kib") active zram), 8 GiB)"
  elif ((zram_planned_kib > 0)); then
    swap_max_derivation="min(25% of ($(gib "$disk_swap_kib") swap + $(gib "$zram_planned_kib") planned zram at ${zram_percent}% of RAM), 8 GiB)"
  else
    swap_max_derivation="min(25% of $(gib "$swap_total_kib") swap, 8 GiB)"
  fi
fi

if [[ -n "$cpu_weight" ]]; then
  cpu_weight_value=$cpu_weight
  cpu_weight_source="override: --cpu-weight $cpu_weight"
else
  cpu_weight_value=50
  cpu_weight_source='default: 50; app.slice and session.slice keep 100'
fi

if [[ -n "$reserve_cores" ]]; then
  ((reserve_cores < cpu_cores)) || die "--reserve-cores ($reserve_cores) must be less than host CPU cores ($cpu_cores)."
  reserved_cores=$reserve_cores
  cpu_quota_percent=$(calculate_cpu_quota_percent "$cpu_cores" "$reserved_cores")
  cpu_quota_derivation="(${cpu_cores} cores - ${reserved_cores} reserved) x 100% (override: --reserve-cores ${reserved_cores})"
else
  reserved_cores=$(calculate_cpu_reserve_cores "$cpu_cores")
  cpu_quota_percent=$(calculate_cpu_quota_percent "$cpu_cores" "$reserved_cores")
  cpu_quota_derivation="(${cpu_cores} cores - ${reserved_cores} reserved) x 100% (formula: clamp(ceil(${cpu_cores} x 0.05), 2, 4)"
  if ((reserved_cores < 2)); then
    cpu_quota_derivation+=", lowered to ${reserved_cores} so agent work keeps one core"
  fi
  cpu_quota_derivation+=')'
fi

declare -A values=(
  [reserve_kib]=$reserve_kib [memory_max_kib]=$memory_max [memory_high_kib]=$memory_high
  [swap_max_kib]=$swap_max [cpu_weight]=$cpu_weight_value [cpu_quota_percent]=$cpu_quota_percent
  [tasks_max]=$tasks_max [oomd_pressure_percent]=$oomd_pressure_percent [swappiness]=${swappiness:--}
  [zram_percent]=$zram_percent
)
value_keys=(reserve_kib memory_max_kib memory_high_kib swap_max_kib cpu_weight cpu_quota_percent tasks_max
  oomd_pressure_percent swappiness zram_percent)
if [[ "$reuse_readings" == true ]]; then
  for key in "${value_keys[@]}"; do
    [[ "${r_value[$key]:-}" == "${values[$key]}" ]] ||
      die "The receipt value $key (${r_value[$key]:-unset}) does not match its approved inputs and readings (${values[$key]}); review $receipt by hand."
  done
fi

zram_generator=false
[[ -x "$(host_path /usr/lib/systemd/system-generators/zram-generator)" ]] && zram_generator=true
if ((zram_percent > 0)) && [[ "$zram_generator" != true ]]; then
  die '--zram-percent needs systemd-zram-generator; install it through the packages scope first.'
fi

tmp_type=$(awk '$2 == "/tmp" {type = $3} END {print type}' "$(host_path /proc/mounts)")
tmp_source=none
if [[ "$tmp_type" == tmpfs ]]; then
  if awk '$1 !~ /^#/ && $2 == "/tmp" {found = 1} END {exit !found}' "$(host_path /etc/fstab)" 2>/dev/null; then
    tmp_source=fstab
  elif [[ -e "$(host_path "$tmp_mask_path")" && ! -L "$(host_path "$tmp_mask_path")" ]]; then
    tmp_source=local-unit
  elif [[ -f "$(host_path /usr/lib/systemd/system/tmp.mount)" || -f "$(host_path /lib/systemd/system/tmp.mount)" ]]; then
    tmp_source=tmp.mount
  else
    tmp_source=unknown
  fi
fi
mask_tmp=false
case "$tmp_mode:$tmp_type:$tmp_source" in
  keep:*|*:*:none) ;;
  *:tmpfs:tmp.mount) mask_tmp=true ;;
  disk:*) die "/tmp is tmpfs from $tmp_source; this script only masks the packaged tmp.mount. Change $tmp_source by hand." ;;
  auto:*) warn "/tmp is tmpfs from $tmp_source; leaving it. Agent writes to /tmp count against the ceiling." ;;
esac
if [[ "$mask_tmp" == true && -L "$(host_path "$tmp_mask_path")" ]]; then
  [[ $(readlink "$(host_path "$tmp_mask_path")") == /dev/null ]] || die "$tmp_mask_path is an unexpected link; review it by hand."
  mask_tmp=false
fi

for path in "$parent_slice_dir" "$parent_slice_path" "$slice_path" "$sysctl_path" "$zram_path" "$old_dropin" "${oomd_vendor_dropins[@]}"; do
  [[ ! -L "$(host_path "$path")" ]] || die "$path is a symbolic link; review it by hand."
done
[[ ! -e "$(host_path "$parent_slice_dir")" || -d "$(host_path "$parent_slice_dir")" ]] ||
  die "$parent_slice_dir exists and is not a directory; review it by hand."

# ---------------------------------------------------------------------------
# Render the desired files.
# ---------------------------------------------------------------------------
# agent.slice is the parent systemd implies from the name agent-work.slice. It
# sits beside app.slice and session.slice in the agent's user manager, so its
# CPUWeight is what competes with the desktop there. Write it as a drop-in so
# other definitions or vendor settings on agent.slice are not shadowed. Omit
# ConditionUser= so CPUWeight applies to every user's agent.slice; that slice
# exists only as the implicit parent of agent-work.slice.
cat >"$work_dir/parent-slice" <<EOF
# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.
[Slice]
CPUWeight=$cpu_weight_value
EOF
cat >"$work_dir/slice" <<EOF
# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.
[Unit]
Description=Agent work ceiling for $agent_account
ConditionUser=$agent_account

[Slice]
CPUQuota=${cpu_quota_percent}%
TasksMax=$tasks_max
MemoryHigh=${memory_high}K
MemoryMax=${memory_max}K
MemorySwapMax=${swap_max}K
EOF
if [[ "$oomd_available" == true ]]; then
  printf '%s\n' 'ManagedOOMMemoryPressure=kill' "ManagedOOMMemoryPressureLimit=${oomd_pressure_percent}%" >>"$work_dir/slice"
fi
if [[ -n "$swappiness" ]]; then
  printf '%s\n' '# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.' \
    "vm.swappiness = $swappiness" >"$work_dir/sysctl"
fi
if ((zram_percent > 0)); then
  printf '%s\n' '# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.' \
    '[zram0]' "zram-size = ram * $zram_percent / 100" 'compression-algorithm = zstd' >"$work_dir/zram"
fi

for index in "${!oomd_vendor_dropins[@]}"; do
  section=Slice
  [[ "${oomd_vendor_dropins[index]}" == */user@.service.d/* ]] && section=Service
  printf '%s\n' '# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.' \
    '# Replaces the vendor oomd default so oomd acts only on agent-work.slice.' \
    "[$section]" 'ManagedOOMMemoryPressure=auto' 'ManagedOOMSwap=auto' >"$work_dir/oomd-$index"
done

changes=()
plan_file() {
  local path=$1 source=$2
  [[ -f "$source" ]] || return 0
  if [[ ! -e "$(host_path "$path")" ]]; then
    changes+=("file|$path|created|$source")
  elif ! cmp -s "$source" "$(host_path "$path")"; then
    changes+=("file|$path|replaced|$source")
  fi
}
plan_file "$parent_slice_path" "$work_dir/parent-slice"
plan_file "$slice_path" "$work_dir/slice"
plan_file "$sysctl_path" "$work_dir/sysctl"
plan_file "$zram_path" "$work_dir/zram"
for index in "${!oomd_vendor_dropins[@]}"; do
  plan_file "${oomd_vendor_dropins[index]}" "$work_dir/oomd-$index"
done
[[ -e "$(host_path "$old_dropin")" ]] && changes+=("file|$old_dropin|removed|-")
[[ "$mask_tmp" == true ]] && changes+=("mask|$tmp_mask_path|created|-")
[[ -n "$swappiness" && "$swappiness" != "$current_swappiness" ]] && changes+=("sysctl|vm.swappiness|$current_swappiness|$swappiness")
[[ -e "$(host_path "/var/lib/systemd/linger/$agent_account")" ]] || changes+=("linger|$agent_account|enabled|-")
[[ "$oomd_available" == true && "$oomd_enabled" != true ]] && changes+=("service|systemd-oomd.service|enabled|-")

swap_summary="zram devices: $zram_swaps, disk swap areas: $disk_swaps"
oomd_summary=unavailable
[[ "$oomd_available" == true ]] && oomd_summary="available, $([[ "$oomd_enabled" == true ]] && echo enabled || echo not enabled)"
cat <<EOF
Agent work ceiling for $agent_account (UID $uid)
  Slice:              $slice_name in the user manager of $agent_account, under agent.slice
  Host memory:        $(gib "$total_kib")
  Host CPUs:          $cpu_cores
  Host role:          $role_label ($desktop_reason)
  systemd:            $systemd_version
  systemd-oomd:       $oomd_summary
  Swap:               $(gib "$swap_total_kib") ($swap_summary)
  /tmp:               ${tmp_type:-not a separate mount}$([[ "$tmp_source" != none ]] && printf ' (%s)' "$tmp_source")
EOF
if [[ "$reuse_readings" == true ]]; then
  printf '  Readings:           approved values reused from %s\n' "$receipt"
fi
if [[ "$measure" == true ]]; then
  printf '  Measured baseline:  %s in use outside agent work x 1.5 = %s (informational, not applied; role default: %s GiB %s)\n' \
    "$(gib "$measured_raw_kib")" "$(gib "$measured_scaled_kib")" "$role_default_gib" "$role_label"
fi
cat <<EOF
  MemoryMax:          $(gib "$memory_max") = ${memory_max_derivation}
  MemoryHigh:         $(gib "$memory_high") = ${memory_high_derivation}
  MemorySwapMax:      $(gib "$swap_max") = ${swap_max_derivation}
  Outside the slice:  $(gib $((total_kib - memory_max))) (${reserve_note})
  CPUWeight:          ${cpu_weight_value} on agent.slice (${cpu_weight_source})
  CPUQuota:           ${cpu_quota_percent}% = ${cpu_quota_derivation}
  TasksMax:           $tasks_max (formula: clamp(RAM GiB x 128, 4096, 16384))
EOF
if [[ "$oomd_available" == true ]]; then
  printf '  oomd:               kill in the slice above %s%% memory pressure\n' "$oomd_pressure_percent"
  printf '  oomd scope:         agent-work.slice only; overrides %s vendor default drop-in(s)\n' "${#oomd_vendor_dropins[@]}"
  printf '\n  Current oomd settings (systemctl cat, ManagedOOM lines):\n'
  { systemctl cat -- "user@$uid.service" -.slice 2>/dev/null | grep -E '^# /|ManagedOOM' ||
      printf '(no ManagedOOM settings found)\n'; } | sed 's/^/    /'
  printf '  oomctl dump (first 20 lines):\n'
  { oomctl dump 2>/dev/null || printf '(oomctl unavailable or systemd-oomd not running)\n'; } | sed -n '1,20s/^/    /p'
  printf '\n'
else
  printf '  oomd:               unavailable; MemoryHigh throttles and MemoryMax runs the kernel OOM killer inside the slice only\n'
fi
printf '  vm.swappiness:      %s\n' "${swappiness:-unchanged ($current_swappiness)}"
printf '  zram:               %s\n' "$( ((zram_percent > 0)) && printf 'zram0 at %s%% of RAM from the next boot' "$zram_percent" || printf 'unchanged')"
printf '  /tmp policy:        %s\n\n' "$([[ "$mask_tmp" == true ]] && printf 'disk-backed from the next boot (mask tmp.mount)' || printf 'unchanged')"

if [[ "$receipt_present" == true ]]; then
  if ((${#changes[@]} == 0)); then
    log "Policy already applied as recorded in $receipt; nothing to change."
    exit 0
  fi
  die "A receipt from an earlier apply exists at $receipt and the host no longer matches it. Run --revert first, then preview and apply again."
fi

if ((${#changes[@]} == 0)); then
  log 'Nothing on this host differs from the policy.'
fi
for change in "${changes[@]}"; do
  IFS='|' read -r kind path action _ <<<"$change"
  case "$kind" in
    file)
      if [[ "$path" == "$parent_slice_path" ]]; then
        log "Would ${action%d} $path (backup first; applies CPUWeight=$cpu_weight_value to every user's agent.slice)"
      else
        log "Would ${action%d} $path (backup first)"
      fi
      ;;
    mask) log "Would mask tmp.mount: link $path to /dev/null" ;;
    sysctl) log "Would set vm.swappiness from $action to ${change##*|}" ;;
    linger) log "Would run loginctl enable-linger $path so its user manager runs at boot" ;;
    service) log "Would run systemctl enable --now $path" ;;
  esac
done
log "Would write the receipt $receipt"

if [[ "$APPLY_CHANGES" != true ]]; then
  log 'Preview only. Re-run with --apply after review.'
  exit 0
fi

# ---------------------------------------------------------------------------
# Apply: lock, flush state dirs, back up, record intent, change, record done;
# one step at a time, every file written atomically.
# ---------------------------------------------------------------------------
lock_state
create_durable_dir "$state_dir" 0700
create_durable_dir "$state_dir/backups" 0700
create_durable_dir "$state_dir/installed" 0700
refuse_state_links
trap 'warn "Apply stopped part way. Undo what it changed with: apply-resource-policy-linux.sh --agent $agent_account --revert --apply"' ERR

step=0
[[ ! -e "$(host_path "$receipt")" ]] || die "A receipt appeared at $receipt after the preview; preview again."
{
  printf 'version\t%s\nagent\t%s\nformula\t%s\n' "$current_receipt_version" "$agent_account" "$current_formula_version"
  for key in "${input_keys[@]}"; do printf 'input\t%s\t%s\n' "$key" "${inputs[$key]}"; done
  printf 'reading\tram_kib\t%s\nreading\tswap_kib\t%s\nreading\tzram_kib\t%s\nreading\tzram_planned_kib\t%s\nreading\tcpu_cores\t%s\nreading\trole\t%s\n' \
    "$total_kib" "$swap_total_kib" "$zram_swap_kib" "$zram_planned_kib" "$cpu_cores" "$role_label"
  for key in "${value_keys[@]}"; do printf 'value\t%s\t%s\n' "$key" "${values[$key]}"; done
} >"$work_dir/receipt"
write_atomic "$work_dir/receipt" "$(host_path "$receipt")" 0600

ensure_dir() {
  local dir=$1 missing=() index target
  while [[ ! -d "$(host_path "$dir")" ]]; do
    missing+=("$dir")
    dir=$(dirname "$dir")
  done
  for ((index = ${#missing[@]} - 1; index >= 0; index--)); do
    step=$((step + 1))
    record begin "$step" dir "${missing[index]}" created - -
    target=$(host_path "${missing[index]}")
    install -d -m 0755 "${owner_args[@]}" "$target"
    sync -- "$target" "$(dirname -- "$target")"
    stop_point
    record 'done' "$step"
  done
}

for change in "${changes[@]}"; do
  IFS='|' read -r kind path action source <<<"$change"
  key=$(printf '%s' "$path" | tr '/' '_')
  dest=$(host_path "$path")
  case "$kind:$action" in
    file:created)
      ensure_dir "$(dirname "$path")"
      [[ ! -e "$dest" && ! -L "$dest" ]] || die "$path appeared after the preview; preview again."
      write_atomic "$source" "$(host_path "$state_dir/installed/$key")" 0600
      step=$((step + 1))
      record begin "$step" file "$path" created - "$key"
      write_atomic "$source" "$dest" 0644
      ;;
    file:replaced)
      write_atomic "$dest" "$(host_path "$state_dir/backups/$key")"
      write_atomic "$source" "$(host_path "$state_dir/installed/$key")" 0600
      step=$((step + 1))
      record begin "$step" file "$path" replaced "$key" "$key"
      write_atomic "$source" "$dest" 0644
      ;;
    file:removed)
      write_atomic "$dest" "$(host_path "$state_dir/backups/$key")"
      step=$((step + 1))
      record begin "$step" file "$path" removed "$key" -
      rm -f -- "$dest"
      sync -- "$(dirname -- "$dest")"
      ;;
    mask:*)
      ensure_dir "$(dirname "$path")"
      step=$((step + 1))
      record begin "$step" mask "$path" created - -
      ln -s /dev/null "$dest"
      sync -- "$(dirname -- "$dest")"
      ;;
    sysctl:*)
      step=$((step + 1))
      record begin "$step" sysctl "$path" "$action" "$source" -
      printf '%s\n' "$source" >"$(host_path /proc/sys/vm/swappiness)"
      ;;
    linger:*)
      step=$((step + 1))
      record begin "$step" linger "$path" enabled - -
      loginctl enable-linger "$path"
      ;;
    service:*)
      step=$((step + 1))
      record begin "$step" service "$path" enabled - -
      systemctl enable --now "$path"
      ;;
  esac
  stop_point
  record 'done' "$step"
  log "Changed: $kind $path"
done
reload_managers
record state complete
log "Receipt: $receipt. Undo with: apply-resource-policy-linux.sh --agent $agent_account --revert --apply"
log "Start agent services with Slice=$slice_name; agentctl starts its sessions there. zram and /tmp changes take effect at the next reboot."
