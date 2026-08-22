#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

printf 'Agent workstation preflight\n'
printf '=================================\n'
printf 'Timestamp:        %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'Hostname:         %s\n' "$(hostname)"
printf 'Kernel:           %s\n' "$(uname -srvmo 2>/dev/null || uname -a)"
printf 'Architecture:     %s\n' "$(uname -m)"

case "$(uname -s)" in
  Linux)
    if [[ -r /etc/os-release ]]; then
      # This OS-owned file contains static key/value metadata.
      # shellcheck disable=SC1091
      source /etc/os-release
      printf 'Operating system: %s\n' "${PRETTY_NAME:-Linux}"
    fi
    printf 'Logical CPUs:     %s\n' "$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf unknown)"
    if command_exists free; then
      free -h
    fi
    if command_exists systemd-detect-virt; then
      printf 'Virtualization:   %s\n' "$(systemd-detect-virt 2>/dev/null || printf none)"
    fi
    ;;
  Darwin)
    printf 'Operating system: macOS %s\n' "$(sw_vers -productVersion)"
    printf 'Logical CPUs:     %s\n' "$(sysctl -n hw.logicalcpu)"
    printf 'Memory bytes:     %s\n' "$(sysctl -n hw.memsize)"
    printf 'FileVault:        %s\n' "$(fdesetup status 2>/dev/null || printf unknown)"
    ;;
  *) warn 'Unsupported operating system.' ;;
esac

printf '\nRoot filesystem\n'
df -h / 2>/dev/null || true

printf '\nRequired and optional commands\n'
for command_name in git ssh zsh tmux herdr tailscale docker mise node python3 gh glab codex claude grok chatgpt; do
  if command_exists "$command_name"; then
    printf '%-12s %s\n' "$command_name" "$(command -v "$command_name")"
  else
    printf '%-12s %s\n' "$command_name" MISSING
  fi
done

printf '\nThis report is read-only and contains no credential values.\n'
