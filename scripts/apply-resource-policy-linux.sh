#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

agent_account=''
memory_reserve_gib=8
remove_policy=false
while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agent) agent_account=${2:?Missing agent account}; shift 2 ;;
    --memory-reserve-gib) memory_reserve_gib=${2:?Missing memory reserve}; shift 2 ;;
    --remove) remove_policy=true; shift ;;
    --help|-h)
      printf 'Usage: apply-resource-policy-linux.sh --agent agent-01 [--memory-reserve-gib 8] [--remove] [--apply]\n'
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -n "$agent_account" ]] || die '--agent is required.'
validate_unix_name "$agent_account"
[[ "$memory_reserve_gib" =~ ^[0-9]+$ ]] || die '--memory-reserve-gib must be an integer.'
((memory_reserve_gib >= 4 && memory_reserve_gib <= 32)) || die '--memory-reserve-gib must be from 4 through 32.'
id "$agent_account" >/dev/null 2>&1 || die "Unknown account: $agent_account"
require_root_for_apply

total_kib=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
[[ "$total_kib" =~ ^[0-9]+$ ]] || die 'Cannot read total memory.'
gib_kib=$((1024 * 1024))
read -r memory_high memory_max < <(
  calculate_memory_limits "$total_kib" "$memory_reserve_gib"
) || die 'Machine has insufficient memory for the balanced policy.'

uid=$(id -u "$agent_account")
dropin_dir="/etc/systemd/system/user-$uid.slice.d"
dropin_file="$dropin_dir/50-agent-workstation.conf"
legacy_dropin="$dropin_dir/50-agent-fleet.conf"

if [[ -e "$legacy_dropin" ]]; then
  die "Legacy policy exists at $legacy_dropin. Review and remove it through an approved schema-v2 migration."
fi

if [[ "$remove_policy" == true ]]; then
  log "Would remove $dropin_file and reload systemd. Existing sessions keep their current limits until logout/reboot."
  if [[ "$APPLY_CHANGES" == true ]]; then
    rm -f -- "$dropin_file"
    systemctl daemon-reload
    log 'Resource policy removed. Reboot or end all target-user sessions before validating rollback.'
  fi
  exit 0
fi

tasks_max=$(calculate_tasks_max "$total_kib")

cat <<EOF
Balanced policy preview for $agent_account (UID $uid)
  Host memory:       $((total_kib / gib_kib)) GiB
  MemoryHigh:        $((memory_high / gib_kib)) GiB
  MemoryMax:         $((memory_max / gib_kib)) GiB
  CPUWeight:         90 of 100
  IOWeight:          90 of 100
  TasksMax:          $tasks_max

MemoryHigh is the preferred pressure threshold. MemoryMax is only an emergency ceiling.
Tune these values after burn-in; do not copy them blindly to unlike hardware.
EOF

if [[ "$APPLY_CHANGES" == true ]]; then
  tmp=$(mktemp)
  trap 'rm -f "$tmp"' EXIT
  cat >"$tmp" <<EOF
[Slice]
CPUWeight=90
IOWeight=90
MemoryHigh=${memory_high}K
MemoryMax=${memory_max}K
TasksMax=$tasks_max
EOF
  install -d -o root -g root -m 0755 "$dropin_dir"
  install -o root -g root -m 0644 "$tmp" "$dropin_file"
  systemctl daemon-reload
  log "Installed $dropin_file. It applies fully to new sessions after logout/reboot."
else
  log "Would install $dropin_file"
fi
