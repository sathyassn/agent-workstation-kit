#!/usr/bin/env bash

set -Eeuo pipefail

# Apply mode must be reachable only through an explicit command-line flag in the
# current process. Never inherit it from a caller's shell or fleet runner.
APPLY_CHANGES=false

# As root, never trust the caller's environment: sudo on macOS keeps PATH, and
# interpreter variables can load code. Use system tools and an isolated Python.
# This protects a staged helper; it cannot protect a checkout someone edited.
# shellcheck disable=SC2034 # used by scripts that source this library
KIT_PYTHON=(python3)

# Succeed only when this library is sourced by a script file that can be re-run
# as itself. From `bash -c` or an interactive shell, $0 is a label or the shell,
# and re-running it could execute an unrelated file.
# Recorded at source time: the file that sourced this library, if any.
KIT_SOURCED_BY=${BASH_SOURCE[1]:-}
kit_can_reexec_clean() {
  [[ -n "$KIT_SOURCED_BY" && "$KIT_SOURCED_BY" == "$0" && -f "$0" && ! -L "$0" ]]
}

if [[ ${EUID:-$(/usr/bin/id -u)} -eq 0 ]]; then
  # Re-run once with an empty environment, the standard design for privileged
  # scripts; the unsets below are the fallback when the marker is preset or the
  # library is not sourced by a re-runnable script file. Only
  # SSH_CONNECTION (a remote-login signal) and SUDO_USER, which load no code,
  # are carried over.
  if [[ -z ${AWK_CLEAN_ROOT_ENV:-} ]] && kit_can_reexec_clean; then
    root_home=/root
    [[ $(/usr/bin/uname -s) != Darwin ]] || root_home=/var/root
    exec /usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$root_home" \
      TERM="${TERM:-dumb}" LC_ALL=C AWK_CLEAN_ROOT_ENV=1 \
      SSH_CONNECTION="${SSH_CONNECTION:-}" SUDO_USER="${SUDO_USER:-}" /bin/bash "$0" "$@"
  fi
  PATH=/usr/bin:/bin:/usr/sbin:/sbin
  export PATH
  unset BASH_ENV ENV CDPATH PYTHONPATH PYTHONHOME PYTHONSTARTUP PYTHONUSERBASE PYTHONINSPECT
  # -B as well as -I: isolated mode ignores PYTHONDONTWRITEBYTECODE.
  # shellcheck disable=SC2034
  KIT_PYTHON=(/usr/bin/python3 -I -B)
fi

log() {
  printf '[agent-workstation] %s\n' "$*"
}

warn() {
  printf '[agent-workstation] WARNING: %s\n' "$*" >&2
}

die() {
  printf '[agent-workstation] ERROR: %s\n' "$*" >&2
  exit 1
}

quote_command() {
  local arg
  for arg in "$@"; do
    printf '%q ' "$arg"
  done
  printf '\n'
}

run() {
  printf '[agent-workstation] RUN: '
  quote_command "$@"
  if [[ "$APPLY_CHANGES" == true ]]; then
    "$@"
  fi
}

require_root_for_apply() {
  if [[ "$APPLY_CHANGES" == true && ${EUID:-$(id -u)} -ne 0 ]]; then
    die 'Apply mode requires root. Re-run the reviewed command with sudo.'
  fi
}

require_macos_for_apply() {
  if [[ "$APPLY_CHANGES" == true && $(uname -s) != Darwin ]]; then
    die 'This script applies only to macOS.'
  fi
}

validate_unix_name() {
  local value=$1
  [[ "$value" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die "Invalid Unix account/group name: $value"
}

validate_session_name() {
  local value=$1
  [[ "$value" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$ ]] || die "Invalid session name: $value"
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

ufw_rule_is_unsafe() {
  local rule=$1 nomachine_port=${2:-4000}
  case "$rule" in
    'ufw allow out '*|'ufw limit out '*)
      return 1
      ;;
    'ufw allow '*|'ufw limit '*|'ufw route allow '*|'ufw route limit '*)
      [[ "$rule" == "ufw allow in on tailscale0 to any port 22 proto tcp"* ]] && return 1
      [[ "$rule" == "ufw allow in on tailscale0 to any port $nomachine_port proto tcp"* ]] && return 1
      [[ "$rule" == "ufw allow in on tailscale0 to any port $nomachine_port proto udp"* ]] && return 1
      return 0
      ;;
  esac
  return 1
}

first_unsafe_ufw_rule() {
  local nomachine_port=$1 rule
  while IFS= read -r rule; do
    if ufw_rule_is_unsafe "$rule" "$nomachine_port"; then
      printf '%s\n' "$rule"
      return 0
    fi
  done
  return 1
}

# Read `ufw status` (plain or verbose) on stdin; succeed when RULE is allowed
# inbound on INTERFACE. Plain output omits the IN direction for incoming rules.
ufw_status_allows_in() {
  local rule=$1 interface=$2
  awk -v rule="$rule" -v iface="$interface" '
    $1 == rule && $2 == "on" && $3 == iface && $4 == "ALLOW" && $5 != "OUT" && $5 != "FWD" { found = 1 }
    END { exit !found }'
}

ufw_default_policies_are_safe() {
  local defaults=$1
  grep -Eq '^[[:space:]]*DEFAULT_INPUT_POLICY="?DROP"?[[:space:]]*$' <<<"$defaults" &&
    grep -Eq '^[[:space:]]*DEFAULT_FORWARD_POLICY="?DROP"?[[:space:]]*$' <<<"$defaults"
}

sudo_policy_has_command_grants() {
  grep -Fq 'may run the following commands' <<<"$1"
}

authorized_keys_has_usable_key() {
  local path=$1
  grep -Eq '^[[:space:]]*([^#].*[[:space:]])?((ssh-(rsa|ed25519)|ecdsa-sha2-nistp(256|384|521))(-cert-v01@openssh\.com)?|sk-(ssh-ed25519|ecdsa-sha2-nistp256)(-cert-v01)?@openssh\.com)[[:space:]]' "$path"
}

calculate_tasks_max() {
  local total_kib=$1 gib_kib=$((1024 * 1024)) result
  [[ "$total_kib" =~ ^[0-9]+$ ]] || return 2
  result=$(((total_kib / gib_kib) * 128))
  ((result >= 4096)) || result=4096
  ((result <= 16384)) || result=16384
  printf '%d\n' "$result"
}

# Print MemoryHigh, MemoryMax and MemorySwapMax in KiB as shares (percent) of
# total RAM. Fail when the ceiling would leave less than the reserve share.
calculate_memory_shares() {
  local total_kib=$1 high_percent=$2 max_percent=$3 swap_percent=$4 reserve_percent=$5
  local memory_high memory_max swap_max value
  for value in "$@"; do
    [[ "$value" =~ ^[0-9]+$ ]] || return 2
  done
  ((max_percent > high_percent && max_percent <= 100)) || return 2
  memory_high=$((total_kib * high_percent / 100))
  memory_max=$((total_kib * max_percent / 100))
  swap_max=$((total_kib * swap_percent / 100))
  ((memory_high > 0 && total_kib - memory_max >= total_kib * reserve_percent / 100)) || return 1
  printf '%d %d %d\n' "$memory_high" "$memory_max" "$swap_max"
}

# Print a whole-number flag value, checked before any arithmetic: digits only,
# at most six of them, and no leading zero. Rejects -1, 08, abc and ''.
normalize_int() {
  local value=$1
  [[ "$value" =~ ^(0|[1-9][0-9]{0,5})$ ]] || return 2
  printf '%d\n' "$((10#$value))"
}

# Print a positive GiB value (at most six integer digits and six decimals, no
# leading zero) without trailing decimal zeros: 6.50 -> 6.5, 8.0 -> 8.
normalize_gib() {
  local value=$1 whole fraction
  [[ "$value" =~ ^(0|[1-9][0-9]{0,5})([.]([0-9]{1,6}))?$ ]] || return 2
  whole=${BASH_REMATCH[1]}
  fraction=${BASH_REMATCH[3]}
  while [[ "$fraction" == *0 ]]; do fraction=${fraction%0}; done
  [[ "$whole" != 0 || -n "$fraction" ]] || return 2
  printf '%s%s\n' "$whole" "${fraction:+.$fraction}"
}

# Convert a positive integer or decimal GiB value to KiB (rounded to nearest KiB).
gib_to_kib() {
  local value=$1 kib
  value=$(normalize_gib "$value") || return 2
  kib=$(awk -v gib="$value" 'BEGIN { if (gib <= 0) exit 1; printf "%.0f\n", gib * 1048576 }') || return 2
  [[ "$kib" =~ ^[0-9]+$ && "$kib" -gt 0 ]] || return 2
  printf '%d\n' "$kib"
}

# Print memory reserve in KiB: fixed part + 2% of RAM, never below fixed part.
calculate_memory_reserve_kib() {
  local total_kib=$1 fixed_kib=$2 pct_kib reserve_kib
  [[ "$total_kib" =~ ^[0-9]+$ && "$fixed_kib" =~ ^[0-9]+$ ]] || return 2
  ((total_kib > 0)) || return 2
  pct_kib=$((total_kib * 2 / 100))
  reserve_kib=$((fixed_kib + pct_kib))
  ((reserve_kib >= fixed_kib)) || reserve_kib=$fixed_kib
  printf '%d\n' "$reserve_kib"
}

# Print MemoryHigh in KiB: MemoryMax - min(10% of MemoryMax, 8 GiB).
calculate_memory_high_kib() {
  local memory_max_kib=$1 cap_kib=$((8 * 1024 * 1024)) margin_kib memory_high_kib
  [[ "$memory_max_kib" =~ ^[0-9]+$ ]] || return 2
  ((memory_max_kib > 0)) || return 2
  margin_kib=$((memory_max_kib * 10 / 100))
  ((margin_kib <= cap_kib)) || margin_kib=$cap_kib
  memory_high_kib=$((memory_max_kib - margin_kib))
  ((memory_high_kib > 0 && memory_high_kib < memory_max_kib)) || return 1
  printf '%d\n' "$memory_high_kib"
}

# Print MemorySwapMax in KiB: min(25% of total swap, 8 GiB); 0 when no swap.
calculate_swap_max_kib() {
  local swap_total_kib=$1 cap_kib=$((8 * 1024 * 1024)) swap_max_kib
  [[ "$swap_total_kib" =~ ^[0-9]+$ ]] || return 2
  if ((swap_total_kib == 0)); then
    printf '0\n'
    return 0
  fi
  swap_max_kib=$((swap_total_kib * 25 / 100))
  ((swap_max_kib <= cap_kib)) || swap_max_kib=$cap_kib
  printf '%d\n' "$swap_max_kib"
}

# Print reserved CPU cores: clamp(ceil(cores * 0.05), 2, 4), leaving at least 1 core.
calculate_cpu_reserve_cores() {
  local cores=$1 reserved
  [[ "$cores" =~ ^[0-9]+$ && "$cores" -ge 1 ]] || return 2
  reserved=$(((cores + 19) / 20))
  ((reserved >= 2)) || reserved=2
  ((reserved <= 4)) || reserved=4
  if ((cores <= reserved)); then
    reserved=$((cores - 1))
  fi
  printf '%d\n' "$reserved"
}

# Print CPUQuota percentage: (cores - reserved) * 100.
calculate_cpu_quota_percent() {
  local cores=$1 reserved=$2
  [[ "$cores" =~ ^[0-9]+$ && "$reserved" =~ ^[0-9]+$ ]] || return 2
  ((cores >= 1 && reserved < cores)) || return 1
  printf '%d\n' $(((cores - reserved) * 100))
}

# Read memory.current (bytes) of every top-level cgroup (init.scope,
# system.slice, user.slice, machine.slice and any other) and subtract the
# agent's agent.slice subtree in user@UID.service. Print
# "<raw_kib> <scaled_1_5x_kib>". The reading is informational only.
measure_non_agent_memory_kib() {
  local cgroup_root=$1 uid=$2 total_bytes=0 value file found=false agent_file
  [[ "$uid" =~ ^[0-9]+$ ]] || return 2
  for file in "$cgroup_root"/*/memory.current; do
    [[ -r "$file" ]] || continue
    value=$(cat "$file" 2>/dev/null) || continue
    [[ "$value" =~ ^[0-9]{1,19}$ ]] || continue
    total_bytes=$((total_bytes + value))
    found=true
  done
  [[ "$found" == true ]] || return 1
  agent_file="$cgroup_root/user.slice/user-$uid.slice/user@$uid.service/agent.slice/memory.current"
  if [[ -r "$agent_file" ]]; then
    value=$(cat "$agent_file" 2>/dev/null) || value=0
    [[ "$value" =~ ^[0-9]{1,19}$ ]] || value=0
    ((value <= total_bytes)) && total_bytes=$((total_bytes - value))
  fi
  printf '%d %d\n' $((total_bytes / 1024)) $(((total_bytes * 3) / 2048))
}

# Print the last value of KEY in a systemd unit FILE, as systemd reads it.
unit_value() {
  awk -F= -v key="$2" '$1 == key {value = substr($0, length(key) + 2)} END {print value}' "$1" 2>/dev/null
}

# Print one line per problem with the installed resource-policy slices: WORK is
# agent-work.slice (memory ceiling and CPUQuota) and PARENT is the agent.slice
# drop-in (CPUWeight). Prints nothing when both are sound. Values are read as
# text and checked by length before any arithmetic.
resource_slice_problems() {
  local work=$1 parent=$2 agent=$3 high max swap quota tasks weight
  if [[ ! -f "$work" || -L "$work" || -L "$(dirname -- "$work")" ]]; then
    printf 'agent-work.slice is missing or is a symbolic link\n'
  else
    grep -Fxq "ConditionUser=$agent" "$work" || printf 'agent-work.slice lacks ConditionUser=%s\n' "$agent"
    max=$(unit_value "$work" MemoryMax)
    high=$(unit_value "$work" MemoryHigh)
    swap=$(unit_value "$work" MemorySwapMax)
    quota=$(unit_value "$work" CPUQuota)
    tasks=$(unit_value "$work" TasksMax)
    if [[ ! "$max" =~ ^[0-9]{1,12}K$ ]] || ((${max%K} < 2097152)); then
      printf 'agent-work.slice MemoryMax is missing or below 2 GiB (%s)\n' "${max:-unset}"
      max=''
    fi
    if [[ ! "$high" =~ ^[0-9]{1,12}K$ ]] || [[ -z "$max" ]] || ((${high%K} >= ${max%K})); then
      printf 'agent-work.slice MemoryHigh is missing or not below MemoryMax (%s)\n' "${high:-unset}"
    fi
    [[ "$swap" =~ ^[0-9]{1,12}K$ ]] || printf 'agent-work.slice MemorySwapMax is missing (%s)\n' "${swap:-unset}"
    if [[ ! "$quota" =~ ^[0-9]{1,7}%$ ]] || ((${quota%\%} < 100)); then
      printf 'agent-work.slice CPUQuota is missing or below 100%% (%s)\n' "${quota:-unset}"
    fi
    if [[ ! "$tasks" =~ ^[0-9]{1,6}$ ]] || ((tasks < 4096 || tasks > 16384)); then
      printf 'agent-work.slice TasksMax is missing or outside 4096-16384 (%s)\n' "${tasks:-unset}"
    fi
    [[ -z "$(unit_value "$work" CPUWeight)" ]] || printf 'agent-work.slice sets CPUWeight, which belongs on agent.slice\n'
  fi
  if [[ ! -f "$parent" || -L "$parent" || -L "$(dirname -- "$parent")" ]]; then
    printf 'agent.slice drop-in is missing or is a symbolic link\n'
  else
    [[ -z "$(unit_value "$parent" ConditionUser)" ]] ||
      printf "agent.slice drop-in sets ConditionUser, which must be omitted so CPUWeight applies to every user's agent.slice\n"
    weight=$(unit_value "$parent" CPUWeight)
    if [[ ! "$weight" =~ ^[1-9][0-9]{0,4}$ ]] || ((weight > 10000)); then
      printf 'agent.slice drop-in CPUWeight is missing or outside 1-10000 (%s)\n' "${weight:-unset}"
    fi
  fi
}

# Print "PID NAME CGROUP" for each agent process of account UID that runs
# outside agent-work.slice: a claude, codex or grok process, or any process in
# an agent manager's pty slice (orca_ptys_*.slice). PROC_ROOT is /proc, or a
# fixture tree in tests. Processes that exit while being read are skipped.
agent_processes_outside_slice() {
  local proc_root=$1 uid=$2 dir owner name cgroup argv0 argv1
  for dir in "$proc_root"/[0-9]*; do
    owner=$(awk '$1 == "Uid:" {print $2}' "$dir/status" 2>/dev/null) || continue
    [[ "$owner" == "$uid" ]] || continue
    cgroup=$(sed -n 1p "$dir/cgroup" 2>/dev/null) || continue
    case "$cgroup" in */agent-work.slice/*) continue ;; esac
    name=$(cat "$dir/comm" 2>/dev/null) || name=''
    argv0='' argv1=''
    { IFS= read -r -d '' argv0 && IFS= read -r -d '' argv1; } <"$dir/cmdline" 2>/dev/null || true
    case "$name:${argv0##*/}:${argv1##*/}" in
      claude:*|codex:*|grok*:*|*:claude:*|*:codex:*|*:grok*:*|*:*:claude|*:*:codex|*:*:grok*) ;;
      *) [[ "$cgroup" == */orca_ptys_* ]] || continue ;;
    esac
    printf '%s %s %s\n' "${dir##*/}" "${name:-${argv0##*/}}" "${cgroup#0::}"
  done
}

usage_mode() {
  cat <<'EOF'
Scripts are preview-only by default. Add --apply after reviewing every action.
No script accepts or writes credentials.
EOF
}

find_toolkit_python() {
  local candidate
  if [[ -n ${AWK_PYTHON:-} ]]; then
    "$AWK_PYTHON" -c 'import sys; raise SystemExit(sys.version_info < (3, 11))' || \
      die 'AWK_PYTHON must select Python 3.11 or newer for source/package tooling.'
    printf '%s\n' "$AWK_PYTHON"
    return
  fi
  for candidate in python3 /opt/homebrew/bin/python3 /usr/local/bin/python3; do
    command -v "$candidate" >/dev/null 2>&1 || continue
    if "$candidate" -c 'import sys; raise SystemExit(sys.version_info < (3, 11))' >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return
    fi
  done
  die 'Python 3.11+ is required; complete the OS day-zero prerequisite or select AWK_PYTHON explicitly.'
}
