#!/usr/bin/env bash

set -Eeuo pipefail

agent_account=${1:-}

printf 'Resource assessment at %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '========================================\n'

case "$(uname -s)" in
  Linux)
    printf '\nMemory\n'
    free -h
    printf '\nPressure stall information\n'
    for file in /proc/pressure/cpu /proc/pressure/memory /proc/pressure/io; do
      [[ -r "$file" ]] && { printf '%s: ' "${file##*/}"; tr '\n' ' ' <"$file"; printf '\n'; }
    done
    printf '\nLoad\n'
    uptime
    printf '\nFilesystem\n'
    df -h /
    printf '\nTop processes by RSS\n'
    ps -eo user,pid,ppid,%cpu,%mem,rss,etime,comm --sort=-rss | head -n 21
    if [[ -n "$agent_account" ]]; then
      printf '\nProcesses for %s\n' "$agent_account"
      ps -u "$agent_account" -o pid,ppid,%cpu,%mem,rss,etime,comm --sort=-rss | head -n 31 || true
      uid=$(id -u "$agent_account" 2>/dev/null || true)
      if [[ -n "$uid" ]] && command -v systemctl >/dev/null 2>&1; then
        printf '\nAgent user slice\n'
        systemctl show "user-$uid.slice" \
          -p CPUUsageNSec -p MemoryCurrent -p MemoryHigh -p MemoryMax -p TasksCurrent -p TasksMax 2>/dev/null || true
      fi
    fi
    ;;
  Darwin)
    printf '\nMemory pressure\n'
    memory_pressure 2>/dev/null || true
    printf '\nLoad\n'
    uptime
    printf '\nFilesystem\n'
    df -h /
    printf '\nTop processes by RSS\n'
    ps -axo user,pid,ppid,%cpu,%mem,rss,etime,comm -r | head -n 21
    ;;
  *) printf 'Unsupported operating system.\n' >&2; exit 1 ;;
esac

printf '\nThis assessment is read-only. Interpret trends over time; one snapshot is not a capacity test.\n'
