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
under the agent account's user manager, enables systemd-oomd for it, and
applies the swap, zram and /tmp policy. Preview by default; --apply changes
the machine. Apply backs up every file it touches and writes a receipt;
--revert restores exactly what the receipt lists.

Reserve-based sizing is the default (read at run time):
  Memory reserve = fixed part (8 GiB desktop, 2 GiB headless) + 2% of RAM
  MemoryMax      = RAM - reserve (refused below 2 GiB)
  MemoryHigh     = MemoryMax - min(10% of MemoryMax, 8 GiB)
  MemorySwapMax  = min(25% of total swap, 8 GiB); 0 when there is no swap
  CPUWeight      = 50 (system and desktop keep 100)
  CPUQuota       = (cores - clamp(ceil(cores * 0.05), 2, 4)) * 100%

Options and explicit overrides:
  --reserve-fixed-gib N      Override the fixed RAM reserve in GiB (positive number)
  --measure                  Measure system/session/app slices x 1.5; use when larger than role default
  --cpu-weight N             CPUWeight for agent-work.slice, 1..10000 (default 50)
  --reserve-cores N          Override reserved CPU cores for CPUQuota (integer >= 0)
  --memory-high-percent N    Explicit override: MemoryHigh as % of RAM (10..90)
  --memory-max-percent N     Explicit override: MemoryMax as % of RAM (20..95)
  --swap-max-percent N       Explicit override: MemorySwapMax as % of RAM (0..50)
  --memory-reserve-percent N Explicit override: reserve as % of RAM (5..50)
  --oomd-pressure-percent N  ManagedOOMMemoryPressureLimit for the slice (default 60)
  --swappiness auto|keep|N   auto: 100 with zram, 10 with disk swap only (default auto)
  --zram-percent N           zram size as a share of RAM; 0 leaves zram alone (default 0)
  --tmp auto|disk|keep       auto: disk-backed /tmp when tmp.mount makes it tmpfs (default auto)
  --revert                   Restore the state recorded in the receipt (alias: --remove)
EOF
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
oomd_pressure_percent=60
swappiness_mode=auto
zram_percent=0
tmp_mode=auto
revert=false
while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agent) agent_account=${2:?Missing agent account}; shift 2 ;;
    --reserve-fixed-gib) reserve_fixed_gib=${2:?Missing fixed memory reserve}; shift 2 ;;
    --measure) measure=true; shift ;;
    --cpu-weight) cpu_weight=${2:?Missing CPU weight}; shift 2 ;;
    --reserve-cores) reserve_cores=${2:?Missing reserved cores}; shift 2 ;;
    --memory-reserve-percent) memory_reserve_percent=${2:?Missing memory reserve}; shift 2 ;;
    --memory-reserve-gib) die '--memory-reserve-gib is replaced by --reserve-fixed-gib (fixed reserve GiB) or --memory-reserve-percent (explicit RAM share).' ;;
    --memory-high-percent) memory_high_percent=${2:?Missing share}; shift 2 ;;
    --memory-max-percent) memory_max_percent=${2:?Missing share}; shift 2 ;;
    --swap-max-percent) swap_max_percent=${2:?Missing share}; shift 2 ;;
    --oomd-pressure-percent) oomd_pressure_percent=${2:?Missing limit}; shift 2 ;;
    --swappiness) swappiness_mode=${2:?Missing swappiness}; shift 2 ;;
    --zram-percent) zram_percent=${2:?Missing share}; shift 2 ;;
    --tmp) tmp_mode=${2:?Missing /tmp mode}; shift 2 ;;
    --revert|--remove) revert=true; shift ;;
    --help|-h) usage; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done

require_int() {
  local name=$1 value=$2 lower=$3 upper=$4
  [[ "$value" =~ ^[0-9]+$ ]] || die "$name must be an integer."
  ((value >= lower && value <= upper)) || die "$name must be from $lower through $upper."
}

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -n "$agent_account" ]] || die '--agent is required.'
validate_unix_name "$agent_account"
if [[ -n "$reserve_fixed_gib" && -n "$memory_reserve_percent" ]]; then
  die 'Cannot combine --reserve-fixed-gib with --memory-reserve-percent; choose one reserve override.'
fi
if [[ -n "$reserve_fixed_gib" ]]; then
  gib_to_kib "$reserve_fixed_gib" >/dev/null || die '--reserve-fixed-gib must be a positive number.'
fi
if [[ -n "$memory_reserve_percent" ]]; then
  require_int --memory-reserve-percent "$memory_reserve_percent" 5 50
fi
if [[ -n "$memory_high_percent" ]]; then
  require_int --memory-high-percent "$memory_high_percent" 10 90
fi
if [[ -n "$memory_max_percent" ]]; then
  require_int --memory-max-percent "$memory_max_percent" 20 95
fi
if [[ -n "$memory_high_percent" && -n "$memory_max_percent" ]]; then
  ((memory_max_percent > memory_high_percent)) || die '--memory-max-percent must be greater than --memory-high-percent.'
fi
if [[ -n "$swap_max_percent" ]]; then
  require_int --swap-max-percent "$swap_max_percent" 0 50
fi
if [[ -n "$cpu_weight" ]]; then
  require_int --cpu-weight "$cpu_weight" 1 10000
fi
if [[ -n "$reserve_cores" ]]; then
  require_int --reserve-cores "$reserve_cores" 0 1024
fi
require_int --oomd-pressure-percent "$oomd_pressure_percent" 10 99
require_int --zram-percent "$zram_percent" 0 100
case "$swappiness_mode" in
  auto|keep) ;;
  *) require_int --swappiness "$swappiness_mode" 0 200 ;;
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
if [[ -n ${AWK_RESOURCE_TEST_ROOT:-} ]]; then
  [[ ${EUID:-$(id -u)} -ne 0 ]] || die 'AWK_RESOURCE_TEST_ROOT is for unprivileged fixture tests only.'
  [[ "$AWK_RESOURCE_TEST_ROOT" == /* && -d "$AWK_RESOURCE_TEST_ROOT" ]] || die 'AWK_RESOURCE_TEST_ROOT must be an existing absolute directory.'
  fs_root=$AWK_RESOURCE_TEST_ROOT
  owner_args=()
else
  require_root_for_apply
fi

host_path() {
  printf '%s%s' "$fs_root" "$1"
}

slice_name=agent-work.slice
slice_path=/etc/systemd/user/$slice_name
sysctl_path=/etc/sysctl.d/60-agent-workstation-swap.conf
zram_path=/etc/systemd/zram-generator.conf
tmp_mask_path=/etc/systemd/system/tmp.mount
old_dropin=/etc/systemd/system/user-$uid.slice.d/50-agent-workstation.conf
legacy_dropin=/etc/systemd/system/user-$uid.slice.d/50-agent-fleet.conf
state_parent=/var/lib/agent-workstation-kit
state_dir=$state_parent/resource-policy
receipt=$state_dir/receipt.tsv

user_manager_active() {
  systemctl is-active --quiet "user@$uid.service" 2>/dev/null
}

reload_managers() {
  systemctl daemon-reload
  if user_manager_active; then
    systemctl --user --machine="$agent_account@" daemon-reload
  fi
}

record() {
  local IFS=$'\t'
  printf '%s\n' "$*" >>"$(host_path "$receipt")"
}

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

# ---------------------------------------------------------------------------
# Revert: restore exactly what the receipt lists, newest change first.
# ---------------------------------------------------------------------------
if [[ "$revert" == true ]]; then
  [[ -f "$(host_path "$receipt")" ]] || die "No receipt at $receipt; there is nothing to revert."
  mapfile -t entries <"$(host_path "$receipt")"
  [[ "${entries[0]:-}" == $'version\t1' ]] || die "Unsupported receipt format in $receipt."
  [[ "${entries[1]:-}" == $'agent\t'"$agent_account" ]] || die "The receipt belongs to another agent account; refusing."

  problems=()
  for entry in "${entries[@]:2}"; do
    IFS=$'\t' read -r kind path action backup installed <<<"$entry"
    # Apply records each step before making it, so an interrupted apply can
    # leave its last step recorded but not made. Accept both states.
    current=$(host_path "$path")
    case "$kind" in
      file)
        if [[ -L "$current" ]]; then
          problems+=("$path is now a symbolic link")
        elif [[ ! -e "$current" ]]; then
          [[ "$action" != replaced ]] || problems+=("$path was deleted after apply")
        elif [[ "$action" == created ]]; then
          { [[ -f "$(host_path "$state_dir/installed/$installed")" ]] && cmp -s "$(host_path "$state_dir/installed/$installed")" "$current"; } ||
            problems+=("$path changed after apply")
        elif [[ "$action" == removed ]]; then
          cmp -s "$(host_path "$state_dir/backups/$backup")" "$current" || problems+=("$path was recreated after apply")
        else
          { cmp -s "$(host_path "$state_dir/installed/$installed")" "$current" 2>/dev/null ||
              cmp -s "$(host_path "$state_dir/backups/$backup")" "$current"; } || problems+=("$path changed after apply")
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
  for ((index = ${#entries[@]} - 1; index >= 2; index--)); do
    IFS=$'\t' read -r kind path action backup installed <<<"${entries[index]}"
    case "$kind:$action" in
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

  for ((index = ${#entries[@]} - 1; index >= 2; index--)); do
    IFS=$'\t' read -r kind path action backup installed <<<"${entries[index]}"
    case "$kind:$action" in
      file:created) rm -f -- "$(host_path "$path")" ;;
      file:replaced|file:removed) cp -p -- "$(host_path "$state_dir/backups/$backup")" "$(host_path "$path")" ;;
      mask:*) rm -f -- "$(host_path "$path")" ;;
      dir:*)
        if [[ -d "$(host_path "$path")" ]]; then
          rmdir -- "$(host_path "$path")" 2>/dev/null || warn "$path is not empty; left in place."
        fi
        ;;
      sysctl:*) printf '%s\n' "$action" >"$(host_path /proc/sys/vm/swappiness)" ;;
      linger:*) loginctl disable-linger "$path" ;;
      service:*) systemctl disable --now "$path" ;;
    esac
  done
  reload_managers
  rm -rf -- "$(host_path "$state_dir")"
  rmdir -- "$(host_path "$state_parent")" 2>/dev/null || true
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
grep -qw memory "$(host_path /sys/fs/cgroup/cgroup.controllers)" 2>/dev/null ||
  die 'The unified cgroup v2 hierarchy with the memory controller is not available.'
delegated=$(systemctl show --property=DelegateControllers --value "user@$uid.service" 2>/dev/null) || true
grep -qw memory <<<"$delegated" ||
  die "user@$uid.service does not delegate the memory controller, so a user-level slice cannot hold a memory ceiling."

gib() {
  awk -v kib="$1" 'BEGIN {printf "%.1f GiB", kib / 1048576}'
}

total_kib=$(awk '$1 == "MemTotal:" {print $2}' "$(host_path /proc/meminfo)" 2>/dev/null) || true
[[ "$total_kib" =~ ^[0-9]+$ ]] || die 'Cannot read total memory from /proc/meminfo.'
tasks_max=$(calculate_tasks_max "$total_kib")

cpu_cores=$(awk '/^processor[[:space:]]*:/ {c++} END {print c + 0}' "$(host_path /proc/cpuinfo)" 2>/dev/null) || true
if [[ ! "$cpu_cores" =~ ^[0-9]+$ || "$cpu_cores" -lt 1 ]] && [[ -z "$fs_root" ]]; then
  cpu_cores=$(nproc 2>/dev/null || true)
fi
[[ "$cpu_cores" =~ ^[0-9]+$ && "$cpu_cores" -ge 1 ]] || die 'Cannot read CPU core count from /proc/cpuinfo.'

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
if [[ "$desktop_present" == true ]]; then
  role_label=desktop
  role_default_gib=8
else
  role_label=headless
  role_default_gib=2
fi
role_default_kib=$((role_default_gib * 1024 * 1024))

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
swap_total_kib=0
while read -r swap_name _ swap_size _; do
  case "$swap_name" in
    Filename|'') ;;
    /dev/zram*)
      zram_swaps=$((zram_swaps + 1))
      [[ "$swap_size" =~ ^[0-9]+$ ]] && swap_total_kib=$((swap_total_kib + swap_size))
      ;;
    *)
      disk_swaps=$((disk_swaps + 1))
      [[ "$swap_size" =~ ^[0-9]+$ ]] && swap_total_kib=$((swap_total_kib + swap_size))
      ;;
  esac
done <"$(host_path /proc/swaps)"

# Calculate memory reserve, MemoryMax, MemoryHigh, MemorySwapMax and CPU controls.
min_slice_kib=$((2 * 1024 * 1024))
measured_raw_kib=''
measured_scaled_kib=''
if [[ "$measure" == true ]]; then
  read -r measured_raw_kib measured_scaled_kib < <(
    measure_non_agent_memory_kib "$(host_path /sys/fs/cgroup)"
  ) || die '--measure could not read memory.current from /sys/fs/cgroup/system.slice.'
fi

if [[ -n "$memory_reserve_percent" ]]; then
  reserve_kib=$((total_kib * memory_reserve_percent / 100))
  reserve_derivation="${memory_reserve_percent}% of RAM (override: --memory-reserve-percent)"
else
  if [[ -n "$reserve_fixed_gib" ]]; then
    fixed_kib=$(gib_to_kib "$reserve_fixed_gib")
    fixed_desc="${reserve_fixed_gib} GiB override (--reserve-fixed-gib)"
  elif [[ "$measure" == true ]] && ((measured_scaled_kib > role_default_kib)); then
    fixed_kib=$measured_scaled_kib
    fixed_desc="$(gib "$measured_scaled_kib") measured baseline (1.5x $(gib "$measured_raw_kib") > ${role_default_gib} GiB ${role_label} default)"
  elif [[ "$measure" == true ]]; then
    fixed_kib=$role_default_kib
    fixed_desc="${role_default_gib} GiB ${role_label} default (>= $(gib "$measured_scaled_kib") measured 1.5x of $(gib "$measured_raw_kib"))"
  else
    fixed_kib=$role_default_kib
    fixed_desc="${role_default_gib} GiB ${role_label} default"
  fi
  reserve_kib=$(calculate_memory_reserve_kib "$total_kib" "$fixed_kib")
  reserve_derivation="${fixed_desc} + 2% of RAM"
fi

if [[ -n "$memory_max_percent" ]]; then
  memory_max=$((total_kib * memory_max_percent / 100))
  if [[ -n "$memory_reserve_percent" ]] && ((total_kib - memory_max < reserve_kib)); then
    die "A ${memory_max_percent}% ceiling leaves less than the ${memory_reserve_percent}% reserve outside agent work; lower --memory-max-percent or the reserve."
  fi
  ((memory_max >= min_slice_kib)) || die "MemoryMax ($(gib "$memory_max"), from --memory-max-percent ${memory_max_percent}%) is below 2.0 GiB; this machine is too small for an agent-work slice."
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

if [[ -n "$swap_max_percent" ]]; then
  swap_max=$((total_kib * swap_max_percent / 100))
  swap_max_derivation="${swap_max_percent}% of $(gib "$total_kib") RAM (override: --swap-max-percent)"
else
  swap_max=$(calculate_swap_max_kib "$swap_total_kib")
  if ((swap_total_kib == 0)); then
    swap_max_derivation='no swap configured (formula: min(25% of swap, 8 GiB))'
  else
    swap_max_derivation="min(25% of $(gib "$swap_total_kib") swap, 8 GiB)"
  fi
fi

if [[ -n "$cpu_weight" ]]; then
  cpu_weight_source="override: --cpu-weight $cpu_weight"
else
  cpu_weight=50
  cpu_weight_source='default: 50 (system and desktop keep 100)'
fi

if [[ -n "$reserve_cores" ]]; then
  ((reserve_cores < cpu_cores)) || die "--reserve-cores ($reserve_cores) must be less than host CPU cores ($cpu_cores)."
  reserved_cores=$reserve_cores
  cpu_quota_percent=$(calculate_cpu_quota_percent "$cpu_cores" "$reserved_cores")
  cpu_quota_derivation="(${cpu_cores} cores - ${reserved_cores} reserved) x 100% (override: --reserve-cores ${reserved_cores})"
else
  reserved_cores=$(calculate_cpu_reserve_cores "$cpu_cores")
  cpu_quota_percent=$(calculate_cpu_quota_percent "$cpu_cores" "$reserved_cores")
  cpu_quota_derivation="(${cpu_cores} cores - ${reserved_cores} reserved) x 100% (formula: clamp(ceil(${cpu_cores} x 0.05), 2, 4))"
fi
zram_generator=false
[[ -x "$(host_path /usr/lib/systemd/system-generators/zram-generator)" ]] && zram_generator=true
if ((zram_percent > 0)) && [[ "$zram_generator" != true ]]; then
  die '--zram-percent needs systemd-zram-generator; install it through the packages scope first.'
fi

current_swappiness=$(cat "$(host_path /proc/sys/vm/swappiness)" 2>/dev/null) || true
[[ "$current_swappiness" =~ ^[0-9]+$ ]] || die 'Cannot read vm.swappiness.'
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

for path in "$slice_path" "$sysctl_path" "$zram_path" "$old_dropin" "${oomd_vendor_dropins[@]}"; do
  [[ ! -L "$(host_path "$path")" ]] || die "$path is a symbolic link; review it by hand."
done

# ---------------------------------------------------------------------------
# Render the desired files.
# ---------------------------------------------------------------------------
cat >"$work_dir/slice" <<EOF
# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.
[Unit]
Description=Agent work ceiling for $agent_account
ConditionUser=$agent_account

[Slice]
CPUWeight=$cpu_weight
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
  Slice:              $slice_name in the user manager of $agent_account
  Host memory:        $(gib "$total_kib")
  Host CPUs:          $cpu_cores
  Host role:          $role_label ($desktop_reason)
  systemd:            $systemd_version
  systemd-oomd:       $oomd_summary
  Swap:               $(gib "$swap_total_kib") ($swap_summary)
  /tmp:               ${tmp_type:-not a separate mount}$([[ "$tmp_source" != none ]] && printf ' (%s)' "$tmp_source")
EOF
if [[ "$measure" == true ]]; then
  printf '  Measured baseline:  %s current x 1.5 = %s (role default: %s.0 GiB %s)\n' \
    "$(gib "$measured_raw_kib")" "$(gib "$measured_scaled_kib")" "$role_default_gib" "$role_label"
fi
cat <<EOF
  MemoryMax:          $(gib "$memory_max") = ${memory_max_derivation}
  MemoryHigh:         $(gib "$memory_high") = ${memory_high_derivation}
  MemorySwapMax:      $(gib "$swap_max") = ${swap_max_derivation}
  Outside the slice:  $(gib $((total_kib - memory_max))) (reserve $(gib "$reserve_kib"): ${reserve_derivation})
  CPUWeight:          ${cpu_weight} (${cpu_weight_source})
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

if [[ -f "$(host_path "$receipt")" ]]; then
  if ((${#changes[@]} == 0)); then
    log "Policy already applied as recorded in $receipt; nothing to change."
    exit 0
  fi
  die "A receipt from an earlier apply exists at $receipt and the policy differs. Run --revert first, then preview and apply again."
fi

if ((${#changes[@]} == 0)); then
  log 'Nothing on this host differs from the policy.'
fi
for change in "${changes[@]}"; do
  IFS='|' read -r kind path action _ <<<"$change"
  case "$kind" in
    file) log "Would ${action%d} $path (backup first)" ;;
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
# Apply: back up, record, then change; one step at a time.
# ---------------------------------------------------------------------------
install -d -m 0700 "${owner_args[@]}" "$(host_path "$state_dir")" \
  "$(host_path "$state_dir/backups")" "$(host_path "$state_dir/installed")"
printf 'version\t1\nagent\t%s\n' "$agent_account" >"$(host_path "$receipt")"
chmod 0600 "$(host_path "$receipt")"
trap 'warn "Apply stopped part way. Undo what it changed with: apply-resource-policy-linux.sh --agent $agent_account --revert --apply"' ERR

ensure_dir() {
  local dir=$1 missing=() index
  while [[ ! -d "$(host_path "$dir")" ]]; do
    missing+=("$dir")
    dir=$(dirname "$dir")
  done
  for ((index = ${#missing[@]} - 1; index >= 0; index--)); do
    record dir "${missing[index]}" created - -
    install -d -m 0755 "${owner_args[@]}" "$(host_path "${missing[index]}")"
  done
}

for change in "${changes[@]}"; do
  IFS='|' read -r kind path action source <<<"$change"
  key=$(printf '%s' "$path" | tr '/' '_')
  case "$kind:$action" in
    file:created)
      ensure_dir "$(dirname "$path")"
      cp -- "$source" "$(host_path "$state_dir/installed/$key")"
      record file "$path" created - "$key"
      install -m 0644 "${owner_args[@]}" "$source" "$(host_path "$path")"
      ;;
    file:replaced)
      cp -p -- "$(host_path "$path")" "$(host_path "$state_dir/backups/$key")"
      cp -- "$source" "$(host_path "$state_dir/installed/$key")"
      record file "$path" replaced "$key" "$key"
      install -m 0644 "${owner_args[@]}" "$source" "$(host_path "$path")"
      ;;
    file:removed)
      cp -p -- "$(host_path "$path")" "$(host_path "$state_dir/backups/$key")"
      record file "$path" removed "$key" -
      rm -f -- "$(host_path "$path")"
      ;;
    mask:*)
      ensure_dir "$(dirname "$path")"
      record mask "$path" created - -
      ln -s /dev/null "$(host_path "$path")"
      ;;
    sysctl:*)
      record sysctl "$path" "$action" "$source" -
      printf '%s\n' "$source" >"$(host_path /proc/sys/vm/swappiness)"
      ;;
    linger:*)
      record linger "$path" enabled - -
      loginctl enable-linger "$path"
      ;;
    service:*)
      record service "$path" enabled - -
      systemctl enable --now "$path"
      ;;
  esac
  log "Changed: $kind $path"
done
reload_managers
log "Receipt: $receipt. Undo with: apply-resource-policy-linux.sh --agent $agent_account --revert --apply"
log "Start agent services with Slice=$slice_name; agentctl starts its sessions there. zram and /tmp changes take effect at the next reboot."
