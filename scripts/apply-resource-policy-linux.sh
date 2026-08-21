#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

agent_account=''
remove_policy=false
while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agent) agent_account=${2:?Missing agent account}; shift 2 ;;
    --remove) remove_policy=true; shift ;;
    --help|-h)
      printf 'Usage: apply-resource-policy-linux.sh --agent agt-ai-01 [--remove] [--apply]\n'
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -n "$agent_account" ]] || die '--agent is required.'
validate_unix_name "$agent_account"
id "$agent_account" >/dev/null 2>&1 || die "Unknown account: $agent_account"
require_root_for_apply

total_kib=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
[[ "$total_kib" =~ ^[0-9]+$ ]] || die 'Cannot read total memory.'
gib_kib=$((1024 * 1024))
soft_reserve=$((total_kib / 8))
hard_reserve=$((total_kib / 16))
((soft_reserve >= 8 * gib_kib)) || soft_reserve=$((8 * gib_kib))
((hard_reserve >= 4 * gib_kib)) || hard_reserve=$((4 * gib_kib))
memory_high=$((total_kib - soft_reserve))
memory_max=$((total_kib - hard_reserve))
((memory_high > 0 && memory_max > memory_high)) || die 'Machine has insufficient memory for the balanced policy.'

uid=$(id -u "$agent_account")
dropin_dir="/etc/systemd/system/user-$uid.slice.d"
dropin_file="$dropin_dir/50-agent-fleet.conf"

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
