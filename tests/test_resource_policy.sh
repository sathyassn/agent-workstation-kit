#!/usr/bin/env bash

# Fixture tests for apply-resource-policy-linux.sh. Every host path the script
# reads or writes is redirected into a fixture tree through
# AWK_RESOURCE_TEST_ROOT, and systemctl, loginctl, id and uname are stubs, so
# nothing on the machine running the tests is read or changed.

set -Eeuo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
script="$repo_dir/scripts/apply-resource-policy-linux.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() {
  printf 'test_resource_policy: %s\n' "$*" >&2
  exit 1
}

stubs="$work/stubs"
mkdir -p "$stubs"
printf '%s\n' '#!/bin/sh' 'if [ "${1:-}" = "-s" ]; then echo Linux; else /usr/bin/uname "$@"; fi' >"$stubs/uname"
printf '%s\n' '#!/bin/sh' \
  'case "${1:-}" in -u) [ "${2:-}" = agent-01 ] && echo 1001 || /usr/bin/id -u ;; agent-01) exit 0 ;; *) exit 1 ;; esac' >"$stubs/id"
# systemctl: answers the detection queries and records state-changing calls.
cat >"$stubs/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$STUB_STATE/systemctl.log"
case "$*" in
  --version) echo "systemd ${STUB_SYSTEMD_VERSION:-255} (255.4-1ubuntu8)" ;;
  "show --property=DelegateControllers --value user@1001.service") echo "${STUB_DELEGATE-cpu memory pids}" ;;
  "is-enabled systemd-oomd.service") [ -f "$STUB_STATE/oomd-enabled" ] && echo enabled || echo disabled ;;
  "enable --now systemd-oomd.service") touch "$STUB_STATE/oomd-enabled" ;;
  "disable --now systemd-oomd.service") rm -f "$STUB_STATE/oomd-enabled" ;;
  "is-active --quiet user@1001.service") exit 0 ;;
esac
exit 0
EOF
cat >"$stubs/loginctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$STUB_STATE/loginctl.log"
case "$1" in
  enable-linger) touch "$AWK_RESOURCE_TEST_ROOT/var/lib/systemd/linger/$2" ;;
  disable-linger) rm -f "$AWK_RESOURCE_TEST_ROOT/var/lib/systemd/linger/$2" ;;
esac
EOF
chmod 0755 "$stubs"/*

# make_host DIR MEM_GIB: a fixture host with disk swap, tmpfs /tmp from the
# packaged tmp.mount, oomd installed but disabled, and an old user-slice policy.
make_host() {
  local root=$1 mem_gib=$2
  mkdir -p "$root"/{proc/sys/vm,run/systemd/system,sys/fs/cgroup,etc/sysctl.d,etc/systemd/system/user-1001.slice.d,usr/lib/systemd/system,var/lib/systemd/linger}
  printf 'MemTotal:       %d kB\nMemFree:        1024 kB\n' $((mem_gib * 1024 * 1024)) >"$root/proc/meminfo"
  printf 'Filename\tType\tSize\tUsed\tPriority\n/swap.img\tfile\t8388604\t0\t-2\n' >"$root/proc/swaps"
  printf '60\n' >"$root/proc/sys/vm/swappiness"
  printf 'cpuset cpu io memory hugetlb pids rdma misc\n' >"$root/sys/fs/cgroup/cgroup.controllers"
  printf '/dev/root / ext4 rw 0 0\ntmpfs /tmp tmpfs rw,nosuid,nodev 0 0\n' >"$root/proc/mounts"
  printf '# fstab\nUUID=0000 / ext4 defaults 0 1\n' >"$root/etc/fstab"
  printf '[Mount]\nWhat=tmpfs\nWhere=/tmp\nType=tmpfs\n' >"$root/usr/lib/systemd/system/tmp.mount"
  printf '#!/bin/sh\n' >"$root/usr/lib/systemd/systemd-oomd"
  chmod 0755 "$root/usr/lib/systemd/systemd-oomd"
  printf '[Slice]\nMemoryHigh=1K\n' >"$root/etc/systemd/system/user-1001.slice.d/50-agent-workstation.conf"
  printf 'vm.swappiness = 30\n' >"$root/etc/sysctl.d/60-agent-workstation-swap.conf"
  chmod 0640 "$root/etc/sysctl.d/60-agent-workstation-swap.conf"
}

# snapshot DIR: every path with its type, mode and content checksum or link.
snapshot() {
  (
    cd "$1"
    find . -print | LC_ALL=C sort | while IFS= read -r path; do
      if [[ -L "$path" ]]; then
        printf 'L %s -> %s\n' "$path" "$(readlink "$path")"
      elif [[ -f "$path" ]]; then
        printf 'F %s %s %s\n' "$path" "$(ls -l "$path" | cut -c1-10)" "$(cksum <"$path")"
      else
        printf 'D %s\n' "$path"
      fi
    done
  )
}

# policy DIR ARGS...: run the script against the fixture DIR.
policy() {
  local root=$1
  shift
  STUB_STATE="$root.state" AWK_RESOURCE_TEST_ROOT="$root" PATH="$stubs:$PATH" \
    "$BASH" "$script" --agent agent-01 "$@"
}

new_host() {
  local root="$work/$1"
  make_host "$root" "$2"
  mkdir -p "$root.state"
  printf '%s\n' "$root"
}

# 1. Preview changes nothing, and names every planned change.
host=$(new_host preview 64)
before=$(snapshot "$host")
preview=$(policy "$host")
[[ "$(snapshot "$host")" == "$before" ]] || fail 'preview changed the fixture host'
if grep -Eq '^(enable|disable|daemon-reload|--user)' "$host.state/systemctl.log"; then
  fail 'preview ran a state-changing systemctl command'
fi
[[ ! -e "$host.state/loginctl.log" ]] || fail 'preview ran loginctl'
for expected in 'Would create /etc/systemd/user/agent-work.slice' \
  'Would replace /etc/sysctl.d/60-agent-workstation-swap.conf' \
  'Would remove /etc/systemd/system/user-1001.slice.d/50-agent-workstation.conf' \
  'Would mask tmp.mount' 'Would set vm.swappiness from 60 to 10' \
  'Would run loginctl enable-linger agent-01' 'Would run systemctl enable --now systemd-oomd.service' \
  'Preview only'; do
  grep -Fq "$expected" <<<"$preview" || fail "preview is missing: $expected"
done

# An inherited APPLY_CHANGES must not turn a preview into an apply.
APPLY_CHANGES=true policy "$host" >/dev/null
[[ "$(snapshot "$host")" == "$before" ]] || fail 'inherited APPLY_CHANGES changed the fixture host'

# 2. Apply writes the shares computed from RAM, a receipt and backups.
host=$(new_host apply 64)
before=$(snapshot "$host")
policy "$host" --apply >/dev/null
total=$((64 * 1024 * 1024))
slice="$host/etc/systemd/user/agent-work.slice"
for line in 'ConditionUser=agent-01' "MemoryHigh=$((total * 70 / 100))K" \
  "MemoryMax=$((total * 80 / 100))K" "MemorySwapMax=$((total * 10 / 100))K" \
  'TasksMax=8192' 'CPUWeight=90' 'ManagedOOMMemoryPressure=kill' 'ManagedOOMMemoryPressureLimit=60%'; do
  grep -Fxq "$line" "$slice" || fail "slice is missing: $line"
done
# The phase verifier must expect exactly the file the script wrote.
python3 - "$repo_dir/scripts/lib" "$slice" "$total" <<'EOF' || fail 'the phase verifier renders a different slice'
import sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
from phase_verification import agent_slice_text, resource_values
profile = {"resources": {"os_memory_reserve_gib": 8}}
command = ["apply-resource-policy-linux.sh", "--agent", "agent-01", "--memory-reserve-gib", "8"]
expected = agent_slice_text("agent-01", resource_values(profile, int(sys.argv[3]), command), True)
raise SystemExit(Path(sys.argv[2]).read_text() != expected)
EOF
[[ -L "$host/etc/systemd/system/tmp.mount" &&$(readlink "$host/etc/systemd/system/tmp.mount") == /dev/null ]] ||
  fail 'tmp.mount was not masked'
[[ ! -e "$host/etc/systemd/system/user-1001.slice.d/50-agent-workstation.conf" ]] || fail 'old user-slice policy remains'
[[ $(cat "$host/proc/sys/vm/swappiness") == 10 ]] || fail 'swappiness was not set'
[[ -f "$host/var/lib/systemd/linger/agent-01" ]] || fail 'linger was not enabled'
[[ -f "$host.state/oomd-enabled" ]] || fail 'systemd-oomd was not enabled'
grep -Fxq 'daemon-reload' "$host.state/systemctl.log" || fail 'systemd was not reloaded'
grep -Fxq -- '--user --machine=agent-01@ daemon-reload' "$host.state/systemctl.log" || fail 'user manager was not reloaded'
receipt="$host/var/lib/agent-workstation-kit/resource-policy/receipt.tsv"
grep -Fq $'file\t/etc/sysctl.d/60-agent-workstation-swap.conf\treplaced' "$receipt" || fail 'receipt lacks the replaced file'
grep -Fq $'sysctl\tvm.swappiness\t60\t10' "$receipt" || fail 'receipt lacks the swappiness change'
cmp -s "$host/var/lib/agent-workstation-kit/resource-policy/backups/_etc_sysctl.d_60-agent-workstation-swap.conf" \
  "$work/preview/etc/sysctl.d/60-agent-workstation-swap.conf" || fail 'backup differs from the original file'

# A repeat apply changes nothing.
after=$(snapshot "$host")
repeat=$(policy "$host" --apply)
grep -Fq 'already applied' <<<"$repeat" || fail 'repeat apply did not report an applied policy'
[[ "$(snapshot "$host")" == "$after" ]] || fail 'repeat apply changed the fixture host'

# A different policy over an existing receipt is refused.
if policy "$host" --memory-max-percent 85 --apply >/dev/null 2>&1; then
  fail 'expected a changed policy over an existing receipt to be refused'
fi

# 3. Revert preview changes nothing; revert apply restores byte-identical files.
policy "$host" --revert >/dev/null
[[ "$(snapshot "$host")" == "$after" ]] || fail 'revert preview changed the fixture host'
policy "$host" --revert --apply >/dev/null
[[ "$(snapshot "$host")" == "$before" ]] || {
  diff <(printf '%s\n' "$before") <(snapshot "$host") >&2 || true
  fail 'revert did not restore the fixture host exactly'
}
[[ ! -f "$host.state/oomd-enabled" ]] || fail 'revert left systemd-oomd enabled'
grep -Fxq 'disable-linger agent-01' "$host.state/loginctl.log" || fail 'revert did not disable linger'

# A revert is refused when a managed file changed after apply.
host=$(new_host edited 64)
policy "$host" --apply >/dev/null
printf 'MemoryMax=infinity\n' >>"$host/etc/systemd/user/agent-work.slice"
edited=$(snapshot "$host")
if policy "$host" --revert --apply >/dev/null 2>&1; then
  fail 'expected a revert over an edited file to be refused'
fi
[[ "$(snapshot "$host")" == "$edited" ]] || fail 'a refused revert changed the fixture host'

# 4. Shares follow RAM and flags; zram planning switches swappiness to 100.
host=$(new_host shares 128)
mkdir -p "$host/usr/lib/systemd/system-generators"
printf '#!/bin/sh\n' >"$host/usr/lib/systemd/system-generators/zram-generator"
chmod 0755 "$host/usr/lib/systemd/system-generators/zram-generator"
policy "$host" --memory-high-percent 60 --memory-max-percent 75 --swap-max-percent 0 \
  --zram-percent 25 --tmp keep --apply >/dev/null
total=$((128 * 1024 * 1024))
grep -Fxq "MemoryHigh=$((total * 60 / 100))K" "$host/etc/systemd/user/agent-work.slice" || fail 'flag share not used for MemoryHigh'
grep -Fxq "MemoryMax=$((total * 75 / 100))K" "$host/etc/systemd/user/agent-work.slice" || fail 'flag share not used for MemoryMax'
grep -Fxq 'MemorySwapMax=0K' "$host/etc/systemd/user/agent-work.slice" || fail 'flag share not used for MemorySwapMax'
grep -Fxq 'zram-size = ram * 25 / 100' "$host/etc/systemd/zram-generator.conf" || fail 'zram size not written'
grep -Fxq 'vm.swappiness = 100' "$host/etc/sysctl.d/60-agent-workstation-swap.conf" || fail 'zram swappiness not used'
[[ ! -e "$host/etc/systemd/system/tmp.mount" ]] || fail '--tmp keep masked tmp.mount'

# Without oomd, the slice carries no ManagedOOM keys and the preview says why.
host=$(new_host no-oomd 64)
rm "$host/usr/lib/systemd/systemd-oomd"
no_oomd=$(policy "$host" --apply)
grep -Fq 'kernel OOM killer inside the slice only' <<<"$no_oomd" || fail 'missing oomd fallback note'
if grep -q '^ManagedOOM' "$host/etc/systemd/user/agent-work.slice"; then
  fail 'ManagedOOM keys written without oomd'
fi

# 5. Refusal paths.
refuses() {
  local label=$1 pattern=$2 root output
  shift 2
  root=$(new_host "refuse-$label" "${REFUSE_GIB:-64}")
  [[ -z ${REFUSE_SETUP:-} ]] || "$REFUSE_SETUP" "$root"
  if output=$(policy "$root" "$@" --apply 2>&1); then
    fail "expected refusal: $label"
  fi
  grep -Fq "$pattern" <<<"$output" || fail "refusal $label printed: $output"
  [[ ! -e "$root/var/lib/agent-workstation-kit" ]] || fail "refusal $label wrote a receipt"
}
REFUSE_GIB=16 refuses small-ram 'leaves less than the 8 GiB reserve'
refuses high-over-max 'must be greater than' --memory-high-percent 80 --memory-max-percent 80
STUB_SYSTEMD_VERSION=245 refuses old-systemd 'too old'
cgroup_v1() { rm "$1/sys/fs/cgroup/cgroup.controllers"; }
REFUSE_SETUP=cgroup_v1 refuses cgroup-v1 'cgroup v2'
STUB_DELEGATE='pids' refuses no-delegation 'does not delegate the memory controller'
fstab_tmp() { printf 'tmpfs /tmp tmpfs defaults 0 0\n' >>"$1/etc/fstab"; }
REFUSE_SETUP=fstab_tmp refuses fstab-tmp 'tmpfs from fstab' --tmp disk
refuses no-zram-generator 'systemd-zram-generator' --zram-percent 25
no_init() { rm -r "$1/run/systemd/system"; }
REFUSE_SETUP=no_init refuses no-systemd 'not the running init system'
host=$(new_host no-receipt 64)
if output=$(policy "$host" --revert --apply 2>&1); then
  fail 'expected revert without a receipt to be refused'
fi
grep -Fq 'nothing to revert' <<<"$output" || fail "revert without receipt printed: $output"
if output=$(STUB_STATE="$work" PATH="$stubs:$PATH" "$BASH" "$script" --agent agent-01 --apply 2>&1); then
  fail 'expected a non-root apply without a fixture root to be refused'
fi
grep -Fq 'requires root' <<<"$output" || fail "non-root apply printed: $output"

printf 'Resource policy tests passed.\n'
