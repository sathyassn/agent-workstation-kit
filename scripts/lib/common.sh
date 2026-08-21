#!/usr/bin/env bash

set -Eeuo pipefail

APPLY_CHANGES=${APPLY_CHANGES:-false}

log() {
  printf '[agent-fleet] %s\n' "$*"
}

warn() {
  printf '[agent-fleet] WARNING: %s\n' "$*" >&2
}

die() {
  printf '[agent-fleet] ERROR: %s\n' "$*" >&2
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
  printf '[agent-fleet] RUN: '
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

ufw_default_policies_are_safe() {
  local defaults=$1
  grep -Eq '^[[:space:]]*DEFAULT_INPUT_POLICY="?DROP"?[[:space:]]*$' <<<"$defaults" &&
    grep -Eq '^[[:space:]]*DEFAULT_FORWARD_POLICY="?DROP"?[[:space:]]*$' <<<"$defaults"
}

sudo_policy_has_command_grants() {
  grep -Fq 'may run the following commands' <<<"$1"
}

calculate_tasks_max() {
  local total_kib=$1 gib_kib=$((1024 * 1024)) result
  [[ "$total_kib" =~ ^[0-9]+$ ]] || return 2
  result=$(((total_kib / gib_kib) * 128))
  ((result >= 4096)) || result=4096
  ((result <= 16384)) || result=16384
  printf '%d\n' "$result"
}

usage_mode() {
  cat <<'EOF'
Scripts are preview-only by default. Add --apply after reviewing every action.
No script accepts or writes credentials.
EOF
}
