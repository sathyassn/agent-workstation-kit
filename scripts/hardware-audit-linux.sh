#!/usr/bin/env bash

set -Eeuo pipefail

[[ $(uname -s) == Linux ]] || { printf 'Linux only.\n' >&2; exit 1; }

printf 'Hardware audit at %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'Hostname: %s\n' "$(hostnamectl --static 2>/dev/null || hostname)"
printf 'Kernel: %s\n' "$(uname -r)"
printf 'Secure Boot: '
if command -v mokutil >/dev/null 2>&1; then mokutil --sb-state 2>&1; else printf 'UNKNOWN (mokutil unavailable)\n'; fi
printf '\nSystem identity (do not publish this output)\n'
for field in sys_vendor product_name product_version product_serial board_name board_serial; do
  path="/sys/class/dmi/id/$field"
  [[ -r "$path" ]] && printf '%-18s %s\n' "$field:" "$(<"$path")"
done
printf '\nCPU and memory\n'
lscpu | sed -n -e '/^Architecture:/p' -e '/^CPU(s):/p' -e '/^Model name:/p'
awk '/^MemTotal:/ {printf "MemTotal: %.1f GiB\n", $2/1024/1024}' /proc/meminfo
printf '\nNetwork controllers and drivers\n'
lspci -nnk | awk '/Ethernet controller|Network controller/{show=1} show{print} show && /^$/{show=0}'
printf '\nInterfaces\n'
ip -brief link
for interface in /sys/class/net/*; do
  name=${interface##*/}
  [[ "$name" == lo ]] && continue
  printf '\n[%s]\n' "$name"
  ethtool -i "$name" 2>/dev/null || true
  ethtool "$name" 2>/dev/null | sed -n -e '/Speed:/p' -e '/Duplex:/p' -e '/Link detected:/p' || true
done
printf '\nStorage\n'
lsblk -o NAME,TYPE,SIZE,MODEL,SERIAL,FSTYPE,MOUNTPOINTS
printf '\nThis is a read-only inventory report. Treat serials and asset data as private fleet information.\n'
