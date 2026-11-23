#!/usr/bin/env bash

# Fixture tests for apply-resource-policy-linux.sh. Every host path the script
# reads or writes is redirected into a fixture tree through
# AWK_RESOURCE_TEST_ROOT, and systemctl, loginctl, id, uname, flock, mv and
# sync are stubs, so nothing on the machine running the tests is read or
# changed.

set -Eeuo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
script="$repo_dir/scripts/apply-resource-policy-linux.sh"
work=$(mktemp -d)
lock_pid=''
trap '[[ -z "$lock_pid" ]] || kill "$lock_pid" 2>/dev/null || true; rm -rf "$work"' EXIT

fail() {
  printf 'test_resource_policy: %s\n' "$*" >&2
  exit 1
}

stubs="$work/stubs"
mkdir -p "$stubs" "$work/tmp"
cat >"$stubs/uname" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-s" ]; then echo Linux; else /usr/bin/uname "$@"; fi
EOF
cat >"$stubs/id" <<'EOF'
#!/bin/sh
case "${1:-}" in -u) [ "${2:-}" = agent-01 ] && echo 1001 || /usr/bin/id -u ;; agent-01) exit 0 ;; *) exit 1 ;; esac
EOF
# systemctl: answers the detection queries and records state-changing calls.
cat >"$stubs/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$STUB_STATE/systemctl.log"
case "$*" in
  --version) echo "systemd ${STUB_SYSTEMD_VERSION:-255} (255.4-1ubuntu8)" ;;
  "show --property=DelegateControllers --value user@1001.service") echo "${STUB_DELEGATE-cpu memory pids}" ;;
  "is-enabled systemd-oomd.service")
    if [ -n "${STUB_OOMD_STATE:-}" ]; then echo "$STUB_OOMD_STATE"
    elif [ -f "$STUB_STATE/oomd-enabled" ]; then echo enabled; else echo disabled; fi ;;
  "cat -- user@1001.service -.slice")
    printf '%s\n' '# /usr/lib/systemd/system/user@.service.d/10-oomd-user-service-defaults.conf' \
      '[Service]' 'ManagedOOMMemoryPressure=kill' 'ManagedOOMMemoryPressureLimit=50%' ;;
  "enable --now systemd-oomd.service") touch "$STUB_STATE/oomd-enabled" ;;
  "disable --now systemd-oomd.service") rm -f "$STUB_STATE/oomd-enabled" ;;
  "is-active --quiet user@1001.service") exit 0 ;;
  "is-active --quiet graphical.target")
    [ "${STUB_GRAPHICAL_TARGET:-1}" = "1" ] && exit 0 || exit 3 ;;
  "is-active --quiet display-manager.service"|"is-active --quiet gdm.service"|"is-active --quiet gdm3.service"|"is-active --quiet sddm.service"|"is-active --quiet lightdm.service")
    unit=${3:-}
    [ "${STUB_DISPLAY_MANAGER:-}" = "$unit" ] && exit 0 || exit 3 ;;
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
# flock [-n] FD: lock an inherited descriptor as util-linux flock does (macOS
# has no flock command). The lock stays with the caller's open descriptor.
cat >"$stubs/flock" <<'EOF'
#!/bin/sh
exec python3 -c '
import fcntl, sys
args = sys.argv[1:]
flags = fcntl.LOCK_EX
if args and args[0] == "-n":
    flags |= fcntl.LOCK_NB
    args = args[1:]
try:
    fcntl.flock(int(args[0]), flags)
except BlockingIOError:
    sys.exit(1)
' "$@"
EOF
# mv -T SOURCE DEST where mv lacks -T (macOS): never move into a directory.
# GNU mv (Linux) runs as it is.
if ! mv --version >/dev/null 2>&1; then
  cat >"$stubs/mv" <<'EOF'
#!/bin/sh
[ "${1:-}" = -T ] || exec /bin/mv "$@"
shift
[ "$1" = -- ] && shift
if [ -d "$2" ] && [ ! -L "$2" ]; then echo "mv: $2 is a directory" >&2; exit 1; fi
exec /bin/mv -f -- "$1" "$2"
EOF
fi
chmod 0755 "$stubs"/*
# sync FILE: a flush cannot be observed in a fixture; skip the host-wide sync.
ln -s /usr/bin/true "$stubs/sync"

# make_host DIR MEM_GIB [SWAP_GIB] [CORES]: a fixture host with configurable
# RAM, swap and CPU cores, tmpfs /tmp from the packaged tmp.mount, oomd
# installed but disabled with Ubuntu-style vendor drop-ins, and an old
# user-slice policy.
make_host() {
  local root=$1 mem_gib=$2 swap_gib=${3:-8} cores=${4:-16} i
  mkdir -p "$root"/{proc/sys/vm,run/systemd/system,sys/fs/cgroup,etc/sysctl.d,etc/systemd/system/user-1001.slice.d,usr/lib/systemd/system,var/lib/systemd/linger}
  printf 'MemTotal:       %d kB\nMemFree:        1024 kB\n' $((mem_gib * 1024 * 1024)) >"$root/proc/meminfo"
  : >"$root/proc/cpuinfo"
  for ((i = 0; i < cores; i++)); do
    printf 'processor\t: %d\n\n' "$i" >>"$root/proc/cpuinfo"
  done
  printf 'Filename\tType\tSize\tUsed\tPriority\n' >"$root/proc/swaps"
  if ((swap_gib > 0)); then
    printf '/swap.img\tfile\t%d\t0\t-2\n' $((swap_gib * 1024 * 1024)) >>"$root/proc/swaps"
  fi
  printf '60\n' >"$root/proc/sys/vm/swappiness"
  printf 'cpuset cpu io memory hugetlb pids rdma misc\n' >"$root/sys/fs/cgroup/cgroup.controllers"
  printf '/dev/root / ext4 rw 0 0\ntmpfs /tmp tmpfs rw,nosuid,nodev 0 0\n' >"$root/proc/mounts"
  printf '# fstab\nUUID=0000 / ext4 defaults 0 1\n' >"$root/etc/fstab"
  printf '[Mount]\nWhat=tmpfs\nWhere=/tmp\nType=tmpfs\n' >"$root/usr/lib/systemd/system/tmp.mount"
  printf '#!/bin/sh\n' >"$root/usr/lib/systemd/systemd-oomd"
  chmod 0755 "$root/usr/lib/systemd/systemd-oomd"
  mkdir -p "$root/usr/lib/systemd/system/user@.service.d" "$root/usr/lib/systemd/system/-.slice.d"
  printf '[Service]\nManagedOOMMemoryPressure=kill\nManagedOOMMemoryPressureLimit=50%%\n' \
    >"$root/usr/lib/systemd/system/user@.service.d/10-oomd-user-service-defaults.conf"
  printf '[Slice]\nManagedOOMSwap=kill\n' >"$root/usr/lib/systemd/system/-.slice.d/10-oomd-root-slice-defaults.conf"
  printf '[Slice]\nMemoryHigh=1K\n' >"$root/etc/systemd/system/user-1001.slice.d/50-agent-workstation.conf"
  printf 'vm.swappiness = 30\n' >"$root/etc/sysctl.d/60-agent-workstation-swap.conf"
  chmod 0640 "$root/etc/sysctl.d/60-agent-workstation-swap.conf"
}

add_zram_generator() {
  mkdir -p "$1/usr/lib/systemd/system-generators"
  printf '#!/bin/sh\n' >"$1/usr/lib/systemd/system-generators/zram-generator"
  chmod 0755 "$1/usr/lib/systemd/system-generators/zram-generator"
}

# snapshot DIR: every path with its type, mode and content checksum or link.
snapshot() {
  (
    cd "$1"
    find . -print | LC_ALL=C sort | while IFS= read -r path; do
      if [[ -L "$path" ]]; then
        printf 'L %s -> %s\n' "$path" "$(readlink "$path")"
      elif [[ -f "$path" ]]; then
        printf 'F %s %s %s\n' "$path" "$(stat -c %a "$path" 2>/dev/null || stat -f %Lp "$path")" "$(cksum <"$path")"
      else
        printf 'D %s\n' "$path"
      fi
    done
  )
}

# assert_reverted DIR BEFORE: revert deleted the state folder, kept the
# persistent lock file (0600 in 0755 parent), and restored everything else
# byte-identically to BEFORE.
file_mode() {
  stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"
}
file_inode() {
  stat -c %i "$1" 2>/dev/null || stat -f %i "$1"
}
strip_persistent_lock() {
  grep -v -E '^(D \./var/lib/agent-workstation-kit|F \./var/lib/agent-workstation-kit/resource-policy\.lock 600 4294967295 0)$' || true
}
assert_reverted() {
  local root=$1 before=$2 label=${3:-revert} actual
  [[ ! -e "$root/var/lib/agent-workstation-kit/resource-policy" ]] || fail "$label left the state folder behind"
  [[ -d "$root/var/lib/agent-workstation-kit" && ! -L "$root/var/lib/agent-workstation-kit" ]] || fail "$label removed the lock parent directory"
  [[ $(file_mode "$root/var/lib/agent-workstation-kit") == 755 ]] || fail "$label left wrong mode on lock parent: $(file_mode "$root/var/lib/agent-workstation-kit")"
  [[ -f "$root/var/lib/agent-workstation-kit/resource-policy.lock" && ! -L "$root/var/lib/agent-workstation-kit/resource-policy.lock" ]] || fail "$label removed the persistent lock file"
  [[ $(file_mode "$root/var/lib/agent-workstation-kit/resource-policy.lock") == 600 ]] || fail "$label left wrong mode on lock file: $(file_mode "$root/var/lib/agent-workstation-kit/resource-policy.lock")"
  actual=$(snapshot "$root" | strip_persistent_lock)
  before=$(printf '%s\n' "$before" | strip_persistent_lock)
  [[ "$actual" == "$before" ]] || {
    diff <(printf '%s\n' "$before") <(printf '%s\n' "$actual") >&2 || true
    fail "$label did not restore the fixture host exactly"
  }
}

# policy DIR ARGS...: run the script against the fixture DIR. EXTRA_STUBS
# names a directory of stubs that take precedence, to make a step fail.
policy() {
  local root=$1
  shift
  TMPDIR="$work/tmp" STUB_STATE="$root.state" AWK_RESOURCE_TEST_ROOT="$root" PATH="${EXTRA_STUBS:+$EXTRA_STUBS:}$stubs:$PATH" \
    "$BASH" "$script" --agent agent-01 "$@"
}

new_host() {
  local root="$work/$1"
  make_host "$root" "$2" "${3:-8}" "${4:-16}"
  mkdir -p "$root.state"
  printf '%s\n' "$root"
}

receipt_of() {
  printf '%s/var/lib/agent-workstation-kit/resource-policy/receipt.tsv' "$1"
}

# verify_slice_matches_verifier SLICE TOTAL SWAP CORES DESKTOP [FLAGS...]:
# the phase verifier renders agent-work.slice and its parent agent.slice drop-in
# exactly as apply wrote them. PLANNED_ZRAM_KIB and ACTIVE_ZRAM_KIB mirror zram.
verify_slice_matches_verifier() {
  local slice=$1 total=$2 swap_kib=$3 cores=$4 desktop=$5
  shift 5
  python3 - "$repo_dir/scripts/lib" "$slice" "$total" "$swap_kib" "$cores" "$desktop" "${PLANNED_ZRAM_KIB:-0}" "${ACTIVE_ZRAM_KIB:-0}" "$@" <<'EOF' || fail "phase verifier disagrees with $slice"
import sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
from phase_verification import agent_parent_slice_text, agent_slice_text, resource_values
slice_path = Path(sys.argv[2])
total = int(sys.argv[3])
swap_kib = int(sys.argv[4])
cores = int(sys.argv[5])
desktop = sys.argv[6] == "1"
zram = int(sys.argv[7])
active_zram = int(sys.argv[8])
command = ["apply-resource-policy-linux.sh", "--agent", "agent-01", *sys.argv[9:]]
values = resource_values({"resources": {}}, total, command, swap_total_kib=swap_kib, cpu_cores=cores, desktop=desktop,
                         planned_zram_kib=zram, active_zram_kib=active_zram)
for path, expected in ((slice_path, agent_slice_text("agent-01", values, True)),
                       (slice_path.parent / "agent.slice.d/50-agent-workstation-kit.conf", agent_parent_slice_text("agent-01", values))):
    if path.read_text() != expected:
        sys.stderr.write("EXPECTED:\n" + expected + "\nACTUAL:\n" + path.read_text())
        raise SystemExit(1)
EOF
}

# verify_receipt_matches_verifier ROOT [FLAGS...]: the phase verifier derives
# both slices from the receipt's approved inputs and readings, never from a
# fresh host reading.
verify_receipt_matches_verifier() {
  local root=$1
  shift
  python3 - "$repo_dir/scripts/lib" "$root" "$@" <<'EOF' || fail "receipt verifier disagrees with $root"
import sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
from phase_verification import agent_parent_slice_text, agent_slice_text, read_resource_receipt, receipt_resource_values
root = Path(sys.argv[2])
command = ["apply-resource-policy-linux.sh", "--agent", "agent-01", *sys.argv[3:]]
receipt = read_resource_receipt(root / "var/lib/agent-workstation-kit/resource-policy/receipt.tsv")
values = receipt_resource_values(receipt, {"resources": {}}, command, "agent-01")
for name, expected in (("agent-work.slice", agent_slice_text("agent-01", values, True)),
                       ("agent.slice.d/50-agent-workstation-kit.conf", agent_parent_slice_text("agent-01", values))):
    actual = (root / "etc/systemd/user" / name).read_text()
    if actual != expected:
        sys.stderr.write("EXPECTED:\n" + expected + "\nACTUAL:\n" + actual)
        raise SystemExit(1)
EOF
}

# 1. Preview changes nothing, shows derivations, and names every planned change.
host=$(new_host preview 60 16 32)
before=$(snapshot "$host")
preview=$(policy "$host")
[[ "$(snapshot "$host")" == "$before" ]] || fail 'preview changed the fixture host'
if grep -Eq '^(enable|disable|daemon-reload|--user)' "$host.state/systemctl.log"; then
  fail 'preview ran a state-changing systemctl command'
fi
[[ ! -e "$host.state/loginctl.log" ]] || fail 'preview ran loginctl'
for expected in \
  'Host memory:        60.0 GiB' \
  'Host CPUs:          32' \
  'Host role:          desktop (graphical.target active)' \
  'MemoryMax:          50.8 GiB = 60.0 GiB RAM - 9.2 GiB reserve (8 GiB desktop default + 2% of RAM)' \
  'MemoryHigh:         45.7 GiB = 50.8 GiB MemoryMax - 5.1 GiB (min(10% of MemoryMax, 8 GiB))' \
  'MemorySwapMax:      4.0 GiB = min(25% of 16.0 GiB swap, 8 GiB)' \
  'Outside the slice:  9.2 GiB (reserve 9.2 GiB: 8 GiB desktop default + 2% of RAM)' \
  'CPUWeight:          50 on agent.slice (default: 50; app.slice and session.slice keep 100)' \
  'CPUQuota:           3000% = (32 cores - 2 reserved) x 100% (formula: clamp(ceil(32 x 0.05), 2, 4))' \
  'Would create /etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf' \
  'Would create /etc/systemd/user/agent-work.slice' \
  'Would replace /etc/sysctl.d/60-agent-workstation-swap.conf' \
  'Would remove /etc/systemd/system/user-1001.slice.d/50-agent-workstation.conf' \
  'Would mask tmp.mount' 'Would set vm.swappiness from 60 to 10' \
  'Would run loginctl enable-linger agent-01' 'Would run systemctl enable --now systemd-oomd.service' \
  'Would create /etc/systemd/system/user@.service.d/10-oomd-user-service-defaults.conf' \
  'Would create /etc/systemd/system/-.slice.d/10-oomd-root-slice-defaults.conf' \
  'oomd scope:         agent-work.slice only; overrides 2 vendor default drop-in(s)' \
  'ManagedOOMMemoryPressureLimit=50%' '(oomctl unavailable or systemd-oomd not running)' \
  'Preview only'; do
  grep -Fq "$expected" <<<"$preview" || fail "preview is missing: $expected"
done

# An inherited APPLY_CHANGES must not turn a preview into an apply.
APPLY_CHANGES=true policy "$host" >/dev/null
[[ "$(snapshot "$host")" == "$before" ]] || fail 'inherited APPLY_CHANGES changed the fixture host'

# 2. Formula matrix: 16, 60, 128, 512 GiB desktop and 16 GiB headless.
# CPUWeight sits on agent.slice, the parent beside app.slice and session.slice.
check_formula_case() {
  local label=$1 mem_gib=$2 swap_gib=$3 cores=$4 desktop=$5
  local exp_max=$6 exp_high=$7 exp_swap=$8 exp_quota=$9 exp_tasks=${10}
  local root slice parent
  root=$(new_host "matrix-$label" "$mem_gib" "$swap_gib" "$cores")
  STUB_GRAPHICAL_TARGET=$desktop policy "$root" --apply >/dev/null
  slice="$root/etc/systemd/user/agent-work.slice"
  parent="$root/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf"
  [[ ! -e "$root/etc/systemd/user/agent.slice" ]] || fail "matrix $label wrote a whole agent.slice instead of a drop-in"
  for line in 'ConditionUser=agent-01' 'CPUWeight=50'; do
    grep -Fxq "$line" "$parent" || fail "matrix $label agent.slice drop-in missing: $line (got: $(cat "$parent"))"
  done
  if grep -q '^CPUWeight=' "$slice"; then
    fail "matrix $label agent-work.slice still sets CPUWeight"
  fi
  for line in \
    'ConditionUser=agent-01' \
    "CPUQuota=${exp_quota}%" \
    "TasksMax=${exp_tasks}" \
    "MemoryHigh=${exp_high}K" \
    "MemoryMax=${exp_max}K" \
    "MemorySwapMax=${exp_swap}K" \
    'ManagedOOMMemoryPressure=kill' \
    'ManagedOOMMemoryPressureLimit=60%'; do
    grep -Fxq "$line" "$slice" || fail "matrix $label slice missing: $line (got: $(cat "$slice"))"
  done
  verify_slice_matches_verifier "$slice" $((mem_gib * 1048576)) $((swap_gib * 1048576)) "$cores" "$desktop"
  verify_receipt_matches_verifier "$root"
}

# 16 GiB desktop: reserve = 8 GiB + 2%(16 GiB) = 8724152 K; max = 8053064 K; high = 7247758 K; swap = 2 GiB; 16 cores -> 1400%
check_formula_case desktop-16 16 8 16 1 8053064 7247758 2097152 1400 4096
# 60 GiB desktop: reserve = 8 GiB + 2%(60 GiB) = 9646899 K; max = 53267661 K; high = 47940895 K; swap = 4 GiB; 32 cores -> 3000%
check_formula_case desktop-60 60 16 32 1 53267661 47940895 4194304 3000 7680
# 128 GiB desktop: reserve = 8 GiB + 2%(128 GiB) = 11072962 K; max = 123144766 K; high = max - 8 GiB = 114756158 K; swap capped at 8 GiB; 64 cores -> 6000%
check_formula_case desktop-128 128 64 64 1 123144766 114756158 8388608 6000 16384
# 512 GiB desktop: reserve = 8 GiB + 2%(512 GiB) = 19126026 K; max = 517744886 K; high = max - 8 GiB = 509356278 K; swap capped at 8 GiB; 128 cores -> 12400%
check_formula_case desktop-512 512 64 128 1 517744886 509356278 8388608 12400 16384
# 16 GiB headless: reserve = 2 GiB + 2%(16 GiB) = 2432696 K; max = 14344520 K; high = 12910068 K; swap = 2 GiB; 8 cores -> 600%
check_formula_case headless-16 16 8 8 0 14344520 12910068 2097152 600 4096

# Display manager active without graphical.target counts as desktop (8 GiB default).
host=$(new_host dm-desktop 16 8 16)
dm_preview=$(STUB_GRAPHICAL_TARGET=0 STUB_DISPLAY_MANAGER=gdm3.service policy "$host")
grep -Fq 'Host role:          desktop (gdm3.service active)' <<<"$dm_preview" || fail 'display manager was not detected as desktop'
grep -Fq '8 GiB desktop default + 2% of RAM' <<<"$dm_preview" || fail 'display manager did not use 8 GiB desktop default'

# On 1 or 2 CPUs the core reserve is lowered below the clamp, and the preview says so.
host=$(new_host two-cores 16 8 2)
out=$(STUB_GRAPHICAL_TARGET=0 policy "$host")
grep -Fq 'CPUQuota:           100% = (2 cores - 1 reserved) x 100% (formula: clamp(ceil(2 x 0.05), 2, 4), lowered to 1 so agent work keeps one core)' <<<"$out" ||
  fail "2-core preview does not name the lowered clamp: $(grep CPUQuota <<<"$out")"
host=$(new_host one-core 16 8 1)
out=$(STUB_GRAPHICAL_TARGET=0 policy "$host")
grep -Fq 'lowered to 0 so agent work keeps one core' <<<"$out" || fail '1-core preview does not name the lowered clamp'

# 3. --measure is informational: it reads every top-level cgroup (init.scope
# included) minus the agent's agent.slice subtree, shows 1.5x next to the role
# default, and never changes the reserve, whether larger or smaller.
cg=/sys/fs/cgroup
manager=user.slice/user-1001.slice/user@1001.service
host=$(new_host measure-large 60 8 16)
mkdir -p "$host$cg/init.scope" "$host$cg/system.slice" "$host$cg/$manager/agent.slice/agent-work.slice"
# init 1 + system 3 + user.slice 22 (20 of it in agent.slice) - 20 = 6 GiB -> 1.5x = 9 GiB (> 8 GiB default).
printf '%d\n' $((1 << 30)) >"$host$cg/init.scope/memory.current"
printf '%d\n' $((3 << 30)) >"$host$cg/system.slice/memory.current"
printf '%d\n' $((22 << 30)) >"$host$cg/user.slice/memory.current"
printf '%d\n' $((20 << 30)) >"$host$cg/$manager/agent.slice/memory.current"
measured_preview=$(policy "$host" --measure --apply)
grep -Fq 'Measured baseline:  6.0 GiB in use outside agent work x 1.5 = 9.0 GiB (informational, not applied; role default: 8 GiB desktop)' <<<"$measured_preview" ||
  fail "measured baseline line missing: $measured_preview"
grep -Fq '8 GiB desktop default + 2% of RAM' <<<"$measured_preview" || fail 'a larger measured value changed the reserve derivation'
# 60 GiB desktop default: 62914560 - (8388608 + 1258291) = 53267661 K, as without --measure.
grep -Fxq 'MemoryMax=53267661K' "$host/etc/systemd/user/agent-work.slice" || fail 'a larger measured value changed MemoryMax'
verify_receipt_matches_verifier "$host" --measure

host=$(new_host measure-small 60 8 16)
mkdir -p "$host$cg/system.slice"
printf '%d\n' $((2 << 30)) >"$host$cg/system.slice/memory.current"
measured_small=$(policy "$host" --measure --apply)
grep -Fq 'Measured baseline:  2.0 GiB in use outside agent work x 1.5 = 3.0 GiB (informational, not applied; role default: 8 GiB desktop)' <<<"$measured_small" ||
  fail 'small measured baseline line missing'
grep -Fxq 'MemoryMax=53267661K' "$host/etc/systemd/user/agent-work.slice" || fail 'a smaller measured value changed MemoryMax'

# 4. No swap gives MemorySwapMax=0K.
host=$(new_host no-swap 64 0 16)
no_swap_out=$(policy "$host" --apply)
grep -Fxq 'MemorySwapMax=0K' "$host/etc/systemd/user/agent-work.slice" || fail 'no swap did not produce MemorySwapMax=0K'
grep -Fq 'no swap configured' <<<"$no_swap_out" || fail 'no swap derivation missing from preview'

# Planned zram counts toward the swap total: 8 GiB disk swap + 25% of 64 GiB
# zram = 24 GiB -> MemorySwapMax = 6 GiB (6291456 K), not 2 GiB.
host=$(new_host zram-swap 64 8 16)
add_zram_generator "$host"
zram_out=$(policy "$host" --zram-percent 25 --apply)
grep -Fxq 'MemorySwapMax=6291456K' "$host/etc/systemd/user/agent-work.slice" ||
  fail "planned zram was not counted in the swap total: $(grep MemorySwapMax "$host/etc/systemd/user/agent-work.slice")"
grep -Fq 'MemorySwapMax:      6.0 GiB = min(25% of (8.0 GiB swap + 16.0 GiB planned zram at 25% of RAM), 8 GiB)' <<<"$zram_out" ||
  fail "planned zram is not named in the derivation: $(grep MemorySwapMax <<<"$zram_out")"
PLANNED_ZRAM_KIB=$((16 * 1048576)) verify_slice_matches_verifier "$host/etc/systemd/user/agent-work.slice" \
  $((64 * 1048576)) $((8 * 1048576)) 16 1 --zram-percent 25
verify_receipt_matches_verifier "$host" --zram-percent 25

# Existing zram device resized UP: 8 GiB disk swap + 4 GiB active zram, resized
# to 25% of 64 GiB (16 GiB). Active 4 GiB is replaced (not added, not kept):
# 8 + 16 = 24 GiB -> MemorySwapMax = 6 GiB (6291456 K).
host=$(new_host zram-resize-up 64 8 16)
add_zram_generator "$host"
printf '/dev/zram0\tpartition\t%d\t0\t100\n' $((4 * 1048576)) >>"$host/proc/swaps"
zram_up=$(policy "$host" --zram-percent 25 --apply)
grep -Fxq 'MemorySwapMax=6291456K' "$host/etc/systemd/user/agent-work.slice" ||
  fail "zram resize up did not replace active zram: $(grep MemorySwapMax "$host/etc/systemd/user/agent-work.slice")"
grep -Fq 'MemorySwapMax:      6.0 GiB = min(25% of (8.0 GiB disk swap + 16.0 GiB planned zram at 25% of RAM, replacing 4.0 GiB active zram), 8 GiB)' <<<"$zram_up" ||
  fail "zram resize up derivation wrong: $(grep MemorySwapMax <<<"$zram_up")"
PLANNED_ZRAM_KIB=$((16 * 1048576)) ACTIVE_ZRAM_KIB=$((4 * 1048576)) verify_slice_matches_verifier \
  "$host/etc/systemd/user/agent-work.slice" $((64 * 1048576)) $((12 * 1048576)) 16 1 --zram-percent 25
verify_receipt_matches_verifier "$host" --zram-percent 25

# Existing zram device resized DOWN: 4 GiB disk swap + 28 GiB active zram,
# resized to 25% of 64 GiB (16 GiB). Active 28 GiB is replaced:
# 4 + 16 = 20 GiB -> MemorySwapMax = 5 GiB (5242880 K), not 8 GiB.
host=$(new_host zram-resize-down 64 4 16)
add_zram_generator "$host"
printf '/dev/zram0\tpartition\t%d\t0\t100\n' $((28 * 1048576)) >>"$host/proc/swaps"
zram_down=$(policy "$host" --zram-percent 25 --apply)
grep -Fxq 'MemorySwapMax=5242880K' "$host/etc/systemd/user/agent-work.slice" ||
  fail "zram resize down did not replace active zram: $(grep MemorySwapMax "$host/etc/systemd/user/agent-work.slice")"
grep -Fq 'MemorySwapMax:      5.0 GiB = min(25% of (4.0 GiB disk swap + 16.0 GiB planned zram at 25% of RAM, replacing 28.0 GiB active zram), 8 GiB)' <<<"$zram_down" ||
  fail "zram resize down derivation wrong: $(grep MemorySwapMax <<<"$zram_down")"
PLANNED_ZRAM_KIB=$((16 * 1048576)) ACTIVE_ZRAM_KIB=$((28 * 1048576)) verify_slice_matches_verifier \
  "$host/etc/systemd/user/agent-work.slice" $((64 * 1048576)) $((32 * 1048576)) 16 1 --zram-percent 25
verify_receipt_matches_verifier "$host" --zram-percent 25

# 5. Apply writes backups and receipt; repeat apply changes nothing; revert restores byte-identical state.
host=$(new_host apply 64 8 16)
before=$(snapshot "$host")
policy "$host" --apply >/dev/null
[[ -L "$host/etc/systemd/system/tmp.mount" && $(readlink "$host/etc/systemd/system/tmp.mount") == /dev/null ]] ||
  fail 'tmp.mount was not masked'
[[ ! -e "$host/etc/systemd/system/user-1001.slice.d/50-agent-workstation.conf" ]] || fail 'old user-slice policy remains'
for override in 'user@.service.d/10-oomd-user-service-defaults.conf' '-.slice.d/10-oomd-root-slice-defaults.conf'; do
  grep -Fxq 'ManagedOOMMemoryPressure=auto' "$host/etc/systemd/system/$override" || fail "oomd default not overridden: $override"
  grep -Fxq 'ManagedOOMSwap=auto' "$host/etc/systemd/system/$override" || fail "oomd swap default not overridden: $override"
done
grep -Fxq '[Service]' "$host/etc/systemd/system/user@.service.d/10-oomd-user-service-defaults.conf" || fail 'user@ override lacks [Service]'
grep -Fxq '[Slice]' "$host/etc/systemd/system/-.slice.d/10-oomd-root-slice-defaults.conf" || fail 'root slice override lacks [Slice]'
[[ $(cat "$host/proc/sys/vm/swappiness") == 10 ]] || fail 'swappiness was not set'
[[ -f "$host/var/lib/systemd/linger/agent-01" ]] || fail 'linger was not enabled'
[[ -f "$host.state/oomd-enabled" ]] || fail 'systemd-oomd was not enabled'
grep -Fxq 'daemon-reload' "$host.state/systemctl.log" || fail 'systemd was not reloaded'
grep -Fxq -- '--user --machine=agent-01@ daemon-reload' "$host.state/systemctl.log" || fail 'user manager was not reloaded'
receipt=$(receipt_of "$host")
grep -Fq $'file\t/etc/sysctl.d/60-agent-workstation-swap.conf\treplaced' "$receipt" || fail 'receipt lacks the replaced file'
grep -Fq $'dir\t/etc/systemd/user/agent.slice.d\tcreated' "$receipt" || fail 'receipt lacks agent.slice.d creation'
grep -Fq $'file\t/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf\tcreated' "$receipt" || fail 'receipt lacks agent.slice drop-in'
grep -Fq $'sysctl\tvm.swappiness\t60\t10' "$receipt" || fail 'receipt lacks the swappiness change'
# The receipt keeps the approved inputs, the readings and every written value.
for line in $'input\tcpu_weight\t-' $'input\tmemory_max_percent\t-' $'input\tzram_percent\t0' \
  $'reading\tram_kib\t67108864' $'reading\tswap_kib\t8388608' $'reading\tzram_kib\t0' $'reading\tzram_planned_kib\t0' \
  $'reading\tcpu_cores\t16' $'reading\trole\tdesktop' $'value\tmemory_max_kib\t57378079' \
  $'value\tcpu_weight\t50' $'value\tcpu_quota_percent\t1400' $'value\tswap_max_kib\t2097152' \
  $'value\tswappiness\t10' $'state\tcomplete'; do
  grep -Fxq "$line" "$receipt" || fail "receipt lacks: ${line//$'\t'/ }"
done
# Every recorded intent has its completion.
[[ $(grep -c '^begin' "$receipt") -eq $(grep -c '^done' "$receipt") ]] || fail 'receipt has an intent without its completion'
cmp -s "$host/var/lib/agent-workstation-kit/resource-policy/backups/_etc_sysctl.d_60-agent-workstation-swap.conf" \
  "$work/preview/etc/sysctl.d/60-agent-workstation-swap.conf" || fail 'backup differs from the original file'
if find "$host" -name '*.agent-workstation-tmp' | grep -q .; then
  fail 'apply left a temporary file behind'
fi

# A repeat apply changes nothing.
after=$(snapshot "$host")
repeat=$(policy "$host" --apply)
grep -Fq 'already applied' <<<"$repeat" || fail 'repeat apply did not report an applied policy'
[[ "$(snapshot "$host")" == "$after" ]] || fail 'repeat apply changed the fixture host'

# A different policy over an existing receipt is refused.
if output=$(policy "$host" --memory-max-percent 85 --apply 2>&1); then
  fail 'expected a changed policy over an existing receipt to be refused'
fi
grep -Fq 'differ from the approved inputs' <<<"$output" || fail "changed policy printed: $output"

# Revert preview changes nothing; revert apply reloads both managers and restores byte-identical files.
policy "$host" --revert >/dev/null
[[ "$(snapshot "$host")" == "$after" ]] || fail 'revert preview changed the fixture host'
: >"$host.state/systemctl.log"
policy "$host" --revert --apply >/dev/null
grep -Fxq 'daemon-reload' "$host.state/systemctl.log" || fail 'revert did not reload systemd'
grep -Fxq -- '--user --machine=agent-01@ daemon-reload' "$host.state/systemctl.log" || fail 'revert did not reload the user manager'
assert_reverted "$host" "$before" 'revert'
[[ ! -f "$host.state/oomd-enabled" ]] || fail 'revert left systemd-oomd enabled'
grep -Fxq 'disable-linger agent-01' "$host.state/loginctl.log" || fail 'revert did not disable linger'

# Pre-existing agent.slice, sibling drop-in, and existing 50-agent-workstation-kit.conf:
# apply backs up and replaces our drop-in without touching agent.slice or sibling drop-ins,
# does not record creating agent.slice.d, and revert restores the earlier drop-in and keeps .d.
host=$(new_host existing-dropin 64 8 16)
mkdir -p "$host/etc/systemd/user/agent.slice.d"
printf '[Unit]\nDescription=Vendor agent slice\n' >"$host/etc/systemd/user/agent.slice"
printf '[Slice]\nIOWeight=80\n' >"$host/etc/systemd/user/agent.slice.d/10-local.conf"
printf '[Slice]\nCPUWeight=80\n' >"$host/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf"
chmod 0640 "$host/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf"
before=$(snapshot "$host")
policy "$host" --apply >/dev/null
grep -Fxq 'Description=Vendor agent slice' "$host/etc/systemd/user/agent.slice" || fail 'apply altered pre-existing agent.slice'
grep -Fxq 'IOWeight=80' "$host/etc/systemd/user/agent.slice.d/10-local.conf" || fail 'apply altered sibling drop-in'
grep -Fxq 'CPUWeight=50' "$host/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf" || fail 'apply did not replace existing drop-in'
if grep -Fq $'dir\t/etc/systemd/user/agent.slice.d\tcreated' "$(receipt_of "$host")"; then
  fail 'receipt recorded creating a pre-existing agent.slice.d'
fi
grep -Fq $'file\t/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf\treplaced' "$(receipt_of "$host")" ||
  fail 'receipt did not record replacing existing drop-in'
policy "$host" --revert --apply >/dev/null
assert_reverted "$host" "$before" 'existing drop-in revert'

# Pre-existing empty agent.slice.d: revert removes the drop-in but leaves the directory.
host=$(new_host existing-dropin-dir 64 8 16)
mkdir -p "$host/etc/systemd/user/agent.slice.d"
before=$(snapshot "$host")
policy "$host" --apply >/dev/null
if grep -Fq $'dir\t/etc/systemd/user/agent.slice.d\tcreated' "$(receipt_of "$host")"; then
  fail 'receipt recorded creating a pre-existing empty agent.slice.d'
fi
policy "$host" --revert --apply >/dev/null
assert_reverted "$host" "$before" 'pre-existing empty drop-in dir revert'
[[ -d "$host/etc/systemd/user/agent.slice.d" ]] || fail 'revert removed a pre-existing agent.slice.d'

# Migration from an earlier run of this branch that installed a whole /etc/systemd/user/agent.slice.
# Helper: simulate an earlier run on HOST (optionally with an existing whole agent.slice beforehand).
simulate_earlier_branch_apply() {
  local root=$1 had_old_slice=$2 old_body=$3
  policy "$root" --apply >/dev/null
  local sd="$root/var/lib/agent-workstation-kit/resource-policy"
  local rc="$sd/receipt.tsv"
  # Replace the drop-in step in the receipt and on disk with the earlier whole agent.slice.
  rm -f "$root/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf"
  rmdir "$root/etc/systemd/user/agent.slice.d" 2>/dev/null || true
  cat >"$root/etc/systemd/user/agent.slice" <<'EOF'
# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.
[Unit]
Description=Agent work CPU share for agent-01
ConditionUser=agent-01

[Slice]
CPUWeight=50
EOF
  cp "$root/etc/systemd/user/agent.slice" "$sd/installed/_etc_systemd_user_agent.slice"
  rm -f "$sd/installed/_etc_systemd_user_agent.slice.d_50-agent-workstation-kit.conf"
  local action=created backup=-
  if [[ "$had_old_slice" == yes ]]; then
    action=replaced
    backup=_etc_systemd_user_agent.slice
    printf '%s' "$old_body" >"$sd/backups/$backup"
  fi
  python3 - "$rc" "$action" "$backup" <<'EOF'
import sys
from pathlib import Path
rc, action, backup = Path(sys.argv[1]), sys.argv[2], sys.argv[3]
drop_dir_id = None
out = []
for line in rc.read_text().splitlines():
    parts = line.split("\t")
    if parts[:2] == ["reading", "zram_kib"]:
        continue
    if parts[0] == "begin" and len(parts) >= 4 and parts[2] == "dir" and parts[3] == "/etc/systemd/user/agent.slice.d":
        drop_dir_id = parts[1]
        continue
    if drop_dir_id and parts == ["done", drop_dir_id]:
        continue
    if parts[0] == "begin" and len(parts) >= 4 and parts[3] == "/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf":
        out.append("\t".join(["begin", parts[1], "file", "/etc/systemd/user/agent.slice", action, backup, "_etc_systemd_user_agent.slice"]))
        continue
    out.append(line)
rc.write_text("\n".join(out) + "\n")
EOF
}

# Subcase A: earlier run created whole agent.slice -> preview names migration, apply removes it and writes drop-in, revert restores original host.
host=$(new_host migrate-created 64 8 16)
before=$(snapshot "$host")
simulate_earlier_branch_apply "$host" no ''
pre_mig=$(snapshot "$host")
mig_preview=$(policy "$host")
[[ "$(snapshot "$host")" == "$pre_mig" ]] || fail 'migration preview changed the host'
grep -Fq 'Would migrate: remove /etc/systemd/user/agent.slice (installed by an earlier run)' <<<"$mig_preview" ||
  fail "migration preview missing remove notice: $mig_preview"
grep -Fq 'Would create /etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf' <<<"$mig_preview" ||
  fail "migration preview missing drop-in creation: $mig_preview"
policy "$host" --apply >/dev/null
[[ ! -e "$host/etc/systemd/user/agent.slice" ]] || fail 'migration did not remove earlier kit-installed agent.slice'
grep -Fxq 'CPUWeight=50' "$host/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf" || fail 'migration did not write drop-in'
verify_receipt_matches_verifier "$host"
repeat=$(policy "$host" --apply)
grep -Fq 'already applied' <<<"$repeat" || fail 'repeat apply after migration did not report already applied'
policy "$host" --revert --apply >/dev/null
assert_reverted "$host" "$before" 'revert after migration (created)'

# Subcase B: earlier run replaced a pre-existing whole agent.slice -> migration restores backup before writing drop-in, revert keeps original agent.slice.
host=$(new_host migrate-replaced 64 8 16)
mkdir -p "$host/etc/systemd/user"
orig_slice=$'[Unit]\nDescription=Existing admin slice\n'
printf '%s' "$orig_slice" >"$host/etc/systemd/user/agent.slice"
before=$(snapshot "$host")
simulate_earlier_branch_apply "$host" yes "$orig_slice"
mig_preview=$(policy "$host")
grep -Fq 'Would migrate: restore /etc/systemd/user/agent.slice from its backup (installed by an earlier run)' <<<"$mig_preview" ||
  fail "migration preview missing restore notice: $mig_preview"
policy "$host" --apply >/dev/null
[[ "$(cat "$host/etc/systemd/user/agent.slice")" == "$(printf '%s' "$orig_slice")" ]] ||
  fail 'migration did not restore backed-up agent.slice'
grep -Fxq 'CPUWeight=50' "$host/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf" || fail 'migration did not write drop-in (replaced case)'
verify_receipt_matches_verifier "$host"
policy "$host" --revert --apply >/dev/null
assert_reverted "$host" "$before" 'revert after migration (replaced)'

# Subcase C: hand-edited earlier whole agent.slice -> migration refuses and changes nothing.
host=$(new_host migrate-edited 64 8 16)
simulate_earlier_branch_apply "$host" no ''
printf 'CPUWeight=99\n' >>"$host/etc/systemd/user/agent.slice"
edited=$(snapshot "$host")
if output=$(policy "$host" --apply 2>&1); then
  fail 'expected migration over hand-edited agent.slice to be refused'
fi
grep -Fq '/etc/systemd/user/agent.slice changed after apply' <<<"$output" || fail "edited migration printed: $output"
[[ "$(snapshot "$host")" == "$edited" ]] || fail 'refused migration changed the host'

# Changed memory and swap readings between apply, repeat apply and verify: the
# repeat apply reuses the approved readings and changes nothing, and the
# verifier derives the values from the receipt instead of the host.
host=$(new_host reread 64 8 16)
policy "$host" --apply >/dev/null
printf 'MemTotal:       %d kB\nMemFree:        1024 kB\n' $((63 * 1024 * 1024 + 4321)) >"$host/proc/meminfo"
printf 'Filename\tType\tSize\tUsed\tPriority\n/swap.img\tfile\t%d\t0\t-2\n' $((4 * 1024 * 1024)) >"$host/proc/swaps"
after=$(snapshot "$host")
if ! repeat=$(policy "$host" --apply 2>&1); then
  fail "repeat apply after a changed memory reading was refused: $repeat"
fi
grep -Fq 'already applied' <<<"$repeat" || fail 'repeat apply after a changed reading did not report an applied policy'
grep -Fq 'the approved reading 67108864 from the receipt is kept' <<<"$repeat" || fail 'changed memory reading was not reported'
grep -Fq 'Host memory:        64.0 GiB' <<<"$repeat" || fail 'repeat apply did not reuse the approved memory reading'
[[ "$(snapshot "$host")" == "$after" ]] || fail 'repeat apply after a changed reading changed the fixture host'
printf 'MemTotal:       %d kB\n' $((65 * 1024 * 1024)) >"$host/proc/meminfo"
verify_receipt_matches_verifier "$host"

# A revert is refused when a managed file changed after apply.
host=$(new_host edited 64)
policy "$host" --apply >/dev/null
printf 'MemoryMax=infinity\n' >>"$host/etc/systemd/user/agent-work.slice"
edited=$(snapshot "$host")
if policy "$host" --revert --apply >/dev/null 2>&1; then
  fail 'expected a revert over an edited file to be refused'
fi
[[ "$(snapshot "$host")" == "$edited" ]] || fail 'a refused revert changed the fixture host'

# 6. Overrides win and are labelled; zram planning switches swappiness to 100.
host=$(new_host overrides-pct 128 32 16)
add_zram_generator "$host"
out=$(policy "$host" --memory-high-percent 60 --memory-max-percent 75 --swap-max-percent 0 \
  --cpu-weight 75 --reserve-cores 4 --zram-percent 25 --tmp keep --apply)
total=$((128 * 1024 * 1024))
grep -Fxq "MemoryHigh=$((total * 60 / 100))K" "$host/etc/systemd/user/agent-work.slice" || fail 'flag share not used for MemoryHigh'
grep -Fxq "MemoryMax=$((total * 75 / 100))K" "$host/etc/systemd/user/agent-work.slice" || fail 'flag share not used for MemoryMax'
grep -Fxq 'MemorySwapMax=0K' "$host/etc/systemd/user/agent-work.slice" || fail 'flag share not used for MemorySwapMax'
grep -Fxq 'CPUWeight=75' "$host/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf" || fail '--cpu-weight override not written to agent.slice drop-in'
grep -Fxq 'CPUQuota=1200%' "$host/etc/systemd/user/agent-work.slice" || fail '--reserve-cores override not written'
grep -Fxq 'zram-size = ram * 25 / 100' "$host/etc/systemd/zram-generator.conf" || fail 'zram size not written'
grep -Fxq 'vm.swappiness = 100' "$host/etc/sysctl.d/60-agent-workstation-swap.conf" || fail 'zram swappiness not used'
[[ ! -e "$host/etc/systemd/system/tmp.mount" ]] || fail '--tmp keep masked tmp.mount'
for label in \
  '(override: --memory-max-percent)' \
  '(override: --memory-high-percent)' \
  '(override: --swap-max-percent)' \
  '(override: --cpu-weight 75)' \
  '(override: --reserve-cores 4)' \
  'reserve overridden by --memory-max-percent'; do
  grep -Fq "$label" <<<"$out" || fail "override label missing from preview: $label"
done
PLANNED_ZRAM_KIB=$((total / 4)) verify_slice_matches_verifier "$host/etc/systemd/user/agent-work.slice" "$total" $((32 * 1048576)) 16 1 \
  --memory-high-percent 60 --memory-max-percent 75 --swap-max-percent 0 --cpu-weight 75 --reserve-cores 4
verify_receipt_matches_verifier "$host" --memory-high-percent 60 --memory-max-percent 75 --swap-max-percent 0 \
  --cpu-weight 75 --reserve-cores 4 --zram-percent 25 --tmp keep

# --reserve-fixed-gib (decimal GiB, normalised) and --memory-reserve-percent overrides win and are labelled.
host=$(new_host override-fixed 60 8 16)
out=$(policy "$host" --reserve-fixed-gib 6.50 --apply)
grep -Fq '6.5 GiB override (--reserve-fixed-gib) + 2% of RAM' <<<"$out" || fail 'fixed reserve override not labelled'
grep -Fxq $'input\treserve_fixed_gib\t6.5' "$(receipt_of "$host")" || fail '--reserve-fixed-gib 6.50 was not normalised to 6.5'
# 60 GiB - (6.5 GiB + 2% of 60 GiB) = 62914560 - (6815744 + 1258291) = 54840525 K
grep -Fxq 'MemoryMax=54840525K' "$host/etc/systemd/user/agent-work.slice" || fail '--reserve-fixed-gib not applied'
verify_slice_matches_verifier "$host/etc/systemd/user/agent-work.slice" $((60 * 1048576)) $((8 * 1048576)) 16 1 \
  --reserve-fixed-gib 6.5
verify_receipt_matches_verifier "$host" --reserve-fixed-gib 6.50

# Partial override: --memory-max-percent alone wins for MemoryMax while MemoryHigh stays on formula.
host=$(new_host override-partial 60 8 41)
out=$(policy "$host" --memory-max-percent 75 --apply)
grep -Fq '(override: --memory-max-percent)' <<<"$out" || fail 'partial MemoryMax override not labelled'
grep -Fq '(min(10% of MemoryMax, 8 GiB))' <<<"$out" || fail 'MemoryHigh did not stay on formula'
# 41 cores -> ceil(41 * 0.05) = 3 reserved -> 3800%
grep -Fxq 'CPUQuota=3800%' "$host/etc/systemd/user/agent-work.slice" || fail '41-core CPUQuota formula mismatch'
verify_slice_matches_verifier "$host/etc/systemd/user/agent-work.slice" $((60 * 1048576)) $((8 * 1048576)) 41 1 \
  --memory-max-percent 75

host=$(new_host override-res-pct 60 8 16)
out=$(policy "$host" --memory-reserve-percent 15 --apply)
grep -Fq '15% of RAM (override: --memory-reserve-percent)' <<<"$out" || fail '--memory-reserve-percent override not labelled'
# 60 GiB - 15% of 60 GiB = 62914560 - 9437184 = 53477376 K
grep -Fxq 'MemoryMax=53477376K' "$host/etc/systemd/user/agent-work.slice" || fail '--memory-reserve-percent not applied'
verify_slice_matches_verifier "$host/etc/systemd/user/agent-work.slice" $((60 * 1048576)) $((8 * 1048576)) 16 1 \
  --memory-reserve-percent 15

# A --memory-max-percent that leaves less than the reserve applies only with
# --allow-small-reserve, and the preview says so. 64 GiB desktop at 90%
# leaves 6.4 GiB, below the 9.3 GiB reserve: MemoryMax = 60397977 K.
host=$(new_host small-reserve-allowed 64 8 16)
out=$(policy "$host" --memory-max-percent 90 --allow-small-reserve --apply)
grep -Fxq 'MemoryMax=60397977K' "$host/etc/systemd/user/agent-work.slice" || fail '--allow-small-reserve did not apply the ceiling'
grep -Fq 'Outside the slice:  6.4 GiB (reserve overridden by --memory-max-percent; the reserve would be 9.3 GiB: 8 GiB desktop default + 2% of RAM; less than the reserve, accepted with --allow-small-reserve)' <<<"$out" ||
  fail "small reserve not labelled: $(grep 'Outside the slice' <<<"$out")"
verify_receipt_matches_verifier "$host" --memory-max-percent 90 --allow-small-reserve

# Without oomd, the slice carries no ManagedOOM keys and the preview says why.
host=$(new_host no-oomd 64)
rm "$host/usr/lib/systemd/systemd-oomd"
no_oomd=$(policy "$host" --apply)
grep -Fq 'kernel OOM killer inside the slice only' <<<"$no_oomd" || fail 'missing oomd fallback note'
if grep -q '^ManagedOOM' "$host/etc/systemd/user/agent-work.slice"; then
  fail 'ManagedOOM keys written without oomd'
fi

# 7. Refusal paths.
refuses() {
  local label=$1 pattern=$2 root output
  shift 2
  root=$(new_host "refuse-$label" "${REFUSE_GIB:-64}" "${REFUSE_SWAP:-8}" "${REFUSE_CORES:-16}")
  [[ -z ${REFUSE_SETUP:-} ]] || "$REFUSE_SETUP" "$root"
  if output=$(STUB_GRAPHICAL_TARGET="${REFUSE_DESKTOP:-1}" policy "$root" "$@" --apply 2>&1); then
    fail "expected refusal: $label"
  fi
  grep -Fq "$pattern" <<<"$output" || fail "refusal $label printed: $output"
  [[ ! -e "$root/var/lib/agent-workstation-kit" ]] || fail "refusal $label wrote a receipt"
}
REFUSE_GIB=8 refuses tiny-desktop 'below 2.0 GiB'
REFUSE_GIB=4 REFUSE_DESKTOP=0 refuses tiny-headless 'below 2.0 GiB'
refuses reserve-conflict 'Cannot combine --reserve-fixed-gib with --memory-reserve-percent' --reserve-fixed-gib 8 --memory-reserve-percent 10
refuses reserve-pct 'less than the reserve (6.4 GiB: 10% of RAM' --memory-max-percent 95 --memory-reserve-percent 10
# Override safety: the ceiling may not eat the formula reserve or the last 2 GiB.
refuses max-over-reserve 'A 90% ceiling leaves 6.4 GiB outside agent work, less than the reserve (9.3 GiB' --memory-max-percent 90
REFUSE_GIB=16 REFUSE_DESKTOP=0 refuses max-over-2gib 'A 95% ceiling leaves 0.8 GiB outside agent work' --memory-max-percent 95
refuses reserve-gib 'replaced by --reserve-fixed-gib' --memory-reserve-gib 8
refuses high-over-max 'must be greater than' --memory-high-percent 80 --memory-max-percent 80
refuses too-many-reserved-cores 'must be less than host CPU cores' --reserve-cores 16
STUB_SYSTEMD_VERSION=245 refuses old-systemd 'too old'
cgroup_v1() { rm "$1/sys/fs/cgroup/cgroup.controllers"; }
REFUSE_SETUP=cgroup_v1 refuses cgroup-v1 'cgroup v2'
no_cpu_controller() { printf 'cpuset io memory hugetlb pids rdma misc\n' >"$1/sys/fs/cgroup/cgroup.controllers"; }
REFUSE_SETUP=no_cpu_controller refuses no-cpu-controller 'cpu controller is not available'
STUB_DELEGATE='pids' refuses no-delegation 'does not delegate the memory controller'
STUB_DELEGATE='memory pids' refuses memory-only-delegation 'does not delegate the cpu controller'
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

# Integer flags are checked as text before any arithmetic: an overflowing
# value, a negative, letters, a leading zero and an empty value are refused.
host=$(new_host bad-integers 64)
for flag in --cpu-weight --reserve-cores --memory-reserve-percent --memory-high-percent --memory-max-percent \
  --swap-max-percent --oomd-pressure-percent --swappiness --zram-percent --reserve-fixed-gib; do
  for bad in 18446744073709551666 -1 abc 08 ''; do
    pattern='must be a whole number'
    [[ "$flag" != --reserve-fixed-gib ]] || pattern='must be a positive number'
    [[ -n "$bad" ]] || pattern='needs a value'
    if output=$(policy "$host" "$flag" "$bad" --apply 2>&1); then
      fail "expected $flag '$bad' to be refused"
    fi
    grep -Fq -- "$flag $pattern" <<<"$output" || fail "$flag '$bad' printed: $output"
  done
done
[[ ! -e "$host/var/lib/agent-workstation-kit" ]] || fail 'a refused integer flag wrote a receipt'

# 8. oomd states static, alias and enabled-runtime count as enabled.
for state in static alias enabled-runtime; do
  host=$(new_host "oomd-$state" 64)
  output=$(STUB_OOMD_STATE=$state policy "$host")
  if grep -Fq 'systemctl enable --now systemd-oomd.service' <<<"$output"; then
    fail "oomd state $state was not treated as enabled"
  fi
done

# 9. An interrupted apply never blocks revert, and revert restores the host.
interrupt() {
  local label=$1 command=$2 root before output
  root=$(new_host "interrupt-$label" 64)
  mkdir -p "$work/fail-$label"
  printf '#!/bin/sh\nexit 1\n' >"$work/fail-$label/$command"
  chmod 0755 "$work/fail-$label/$command"
  before=$(snapshot "$root")
  if output=$(EXTRA_STUBS="$work/fail-$label" policy "$root" --apply 2>&1); then
    fail "expected apply to stop when $command fails"
  fi
  grep -Fq 'Apply stopped part way' <<<"$output" || fail "interrupted apply printed: $output"
  policy "$root" --revert --apply >/dev/null || fail "revert after an interrupted $label apply failed"
  assert_reverted "$root" "$before" "revert after an interrupted $label apply"
}
# The mask step is recorded, then ln fails: the receipt lists a missing link.
interrupt mask ln
# lingering is recorded, then loginctl fails, after most files changed.
interrupt linger loginctl

# Fault injection: kill the apply (SIGKILL, so no trap runs) right after each
# change to the host in turn, before its completion is recorded. A new apply
# refuses the half-applied receipt, and revert restores the original bytes.
stop=1
while :; do
  root=$(new_host "stop-apply-$stop" 64)
  before=$(snapshot "$root")
  if AWK_RESOURCE_TEST_STOP_AFTER=$stop policy "$root" --apply >/dev/null 2>&1; then
    break
  fi
  ! grep -Fxq $'state\tcomplete' "$(receipt_of "$root")" || fail "a stop after change $stop left a complete receipt"
  if ((stop == 3)); then
    if output=$(policy "$root" --apply 2>&1); then
      fail 'expected an apply over a half-applied receipt to be refused'
    fi
    grep -Fq 'stopped part way' <<<"$output" || fail "apply over a half-applied receipt printed: $output"
  fi
  policy "$root" --revert --apply >/dev/null 2>&1 || fail "revert after a stop at change $stop failed"
  assert_reverted "$root" "$before" "revert after a stop at change $stop"
  [[ ! -f "$root.state/oomd-enabled" ]] || fail "revert after a stop at change $stop left systemd-oomd enabled"
  stop=$((stop + 1))
done
((stop > 13)) || fail "fault injection covered only $((stop - 1)) changes"

# A revert killed after a restore can be run again to finish. Sampled at the
# first, middle and last of the 14 restores to keep the run short.
for stop in 1 5 9 14; do
  root=$(new_host "stop-revert-$stop" 64)
  before=$(snapshot "$root")
  policy "$root" --apply >/dev/null
  if AWK_RESOURCE_TEST_STOP_AFTER=$stop policy "$root" --revert --apply >/dev/null 2>&1; then
    fail "revert was not stopped after restore $stop"
  fi
  policy "$root" --revert --apply >/dev/null 2>&1 || fail "revert after a revert stopped at restore $stop failed"
  assert_reverted "$root" "$before" "revert after a revert stopped at restore $stop"
done

# Persistent lock at /var/lib/agent-workstation-kit/resource-policy.lock:
# held for the whole apply or revert, refuses a second apply and a revert,
# and stays valid even after revert deletes the state folder.
hold_lock() {
  local target=$1 ready="$work/lock-ready"
  rm -f "$ready"
  python3 -c 'import fcntl, os, sys, time
fd = os.open(sys.argv[1], os.O_CREAT | os.O_RDWR, 0o600)
fcntl.flock(fd, fcntl.LOCK_EX)
open(sys.argv[2], "w").close()
time.sleep(120)' "$target" "$ready" &
  lock_pid=$!
  until [[ -e "$ready" ]]; do sleep 0.1; done
}
release_lock() {
  kill "$lock_pid" 2>/dev/null || true
  wait "$lock_pid" 2>/dev/null || true
  lock_pid=''
}
host=$(new_host locked-apply 64)
mkdir -p "$host/var/lib/agent-workstation-kit"
hold_lock "$host/var/lib/agent-workstation-kit/resource-policy.lock"
before=$(snapshot "$host")
if output=$(policy "$host" --apply 2>&1); then
  fail 'expected an apply to be refused while lock is held'
fi
release_lock
grep -Fq 'holds the lock' <<<"$output" || fail "locked apply printed: $output"
[[ ! -e "$(receipt_of "$host")" ]] || fail 'a locked apply wrote a receipt'
host=$(new_host locked-revert 64)
policy "$host" --apply >/dev/null
hold_lock "$host/var/lib/agent-workstation-kit/resource-policy.lock"
before=$(snapshot "$host")
if output=$(policy "$host" --revert --apply 2>&1); then
  fail 'expected a revert to be refused while lock is held'
fi
release_lock
grep -Fq 'holds the lock' <<<"$output" || fail "locked revert printed: $output"
[[ "$(snapshot "$host")" == "$before" ]] || fail 'a locked revert changed the fixture host'

# Revert that deletes the state folder keeps the persistent lock valid while
# revert is still running and afterwards on the same inode.
host=$(new_host lock-survives-revert 64)
policy "$host" --apply >/dev/null
lock_path="$host/var/lib/agent-workstation-kit/resource-policy.lock"
lock_inode=$(file_inode "$lock_path")
mkdir -p "$work/lock-hook"
cat >"$work/lock-hook/sync" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "--" ] && [ "${2:-}" = "$LOCK_HOOK_PARENT" ] && [ ! -e "$LOCK_HOOK_STATE_DIR" ]; then
  [ -f "$LOCK_HOOK_FILE" ] || { echo "lock file missing after state_dir deletion" >&2; exit 1; }
  if output=$(TMPDIR="$LOCK_HOOK_TMP" STUB_STATE="$LOCK_HOOK_STUB_STATE" AWK_RESOURCE_TEST_ROOT="$LOCK_HOOK_ROOT" \
      PATH="$LOCK_HOOK_STUBS:$PATH" "$LOCK_HOOK_BASH" "$LOCK_HOOK_SCRIPT" --agent agent-01 --apply 2>&1); then
    echo "concurrent apply succeeded after revert deleted state_dir" >&2
    exit 1
  fi
  printf '%s\n' "$output" >"$LOCK_HOOK_MARKER"
fi
exit 0
EOF
chmod 0755 "$work/lock-hook/sync"
LOCK_HOOK_PARENT="$host/var/lib/agent-workstation-kit" \
  LOCK_HOOK_STATE_DIR="$host/var/lib/agent-workstation-kit/resource-policy" \
  LOCK_HOOK_FILE="$lock_path" \
  LOCK_HOOK_TMP="$work/tmp" \
  LOCK_HOOK_STUB_STATE="$host.state" \
  LOCK_HOOK_ROOT="$host" \
  LOCK_HOOK_STUBS="$stubs" \
  LOCK_HOOK_BASH="$BASH" \
  LOCK_HOOK_SCRIPT="$script" \
  LOCK_HOOK_MARKER="$work/lock-hook/checked" \
  EXTRA_STUBS="$work/lock-hook" policy "$host" --revert --apply >/dev/null
[[ -f "$work/lock-hook/checked" ]] || fail 'post-delete lock check hook never ran'
grep -Fq 'holds the lock' "$work/lock-hook/checked" ||
  fail "concurrent apply after state_dir deletion printed: $(cat "$work/lock-hook/checked")"
[[ ! -e "$host/var/lib/agent-workstation-kit/resource-policy" ]] || fail 'revert did not delete state_dir'
[[ -f "$lock_path" && $(file_inode "$lock_path") == "$lock_inode" ]] || fail 'revert replaced or removed the lock file inode'
[[ $(file_mode "$lock_path") == 600 ]] || fail 'lock file mode changed after revert'
policy "$host" --apply >/dev/null || fail 'fresh apply after revert failed'
[[ $(file_inode "$lock_path") == "$lock_inode" ]] || fail 'fresh apply replaced the persistent lock file inode'

# Durability: each newly created state folder and its parent are flushed via
# sync before the first destination policy file is written.
host=$(new_host durability 64 8 16)
mkdir -p "$work/sync-record"
cat >"$work/sync-record/sync" <<'EOF'
#!/bin/sh
phase=before_dest
if [ -e "$SYNC_HOST/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf" ] ||
   [ -e "$SYNC_HOST/etc/systemd/user/agent-work.slice" ] ||
   [ -L "$SYNC_HOST/etc/systemd/system/tmp.mount" ] ||
   [ ! -e "$SYNC_HOST/etc/systemd/system/user-1001.slice.d/50-agent-workstation.conf" ] ||
   ! grep -Fxq 'vm.swappiness = 30' "$SYNC_HOST/etc/sysctl.d/60-agent-workstation-swap.conf"; then
  phase=after_dest
fi
printf '%s %s\n' "$phase" "$*" >>"$SYNC_LOG"
exit 0
EOF
chmod 0755 "$work/sync-record/sync"
SYNC_HOST="$host" SYNC_LOG="$work/sync-record/log" EXTRA_STUBS="$work/sync-record" policy "$host" --apply >/dev/null
for pair in \
  "$host/var/lib/agent-workstation-kit $host/var/lib" \
  "$host/var/lib/agent-workstation-kit/resource-policy $host/var/lib/agent-workstation-kit" \
  "$host/var/lib/agent-workstation-kit/resource-policy/backups $host/var/lib/agent-workstation-kit/resource-policy" \
  "$host/var/lib/agent-workstation-kit/resource-policy/installed $host/var/lib/agent-workstation-kit/resource-policy"; do
  grep -Fxq "before_dest -- $pair" "$work/sync-record/log" ||
    fail "durability sync missing before first destination change: $pair (log: $(cat "$work/sync-record/log"))"
done

# A symbolic link in the state path (parent, lock file, or receipt) is refused.
host=$(new_host state-link 64)
mkdir -p "$work/state-elsewhere"
ln -s "$work/state-elsewhere" "$host/var/lib/agent-workstation-kit"
if output=$(policy "$host" --apply 2>&1); then
  fail 'expected a symlinked state directory to be refused'
fi
grep -Fq '/var/lib/agent-workstation-kit is a symbolic link' <<<"$output" || fail "symlinked state printed: $output"
[[ -z "$(ls -A "$work/state-elsewhere")" ]] || fail 'apply wrote through a symlinked state directory'
host=$(new_host lock-link 64)
mkdir -p "$host/var/lib/agent-workstation-kit"
ln -s "$work/state-elsewhere/lock" "$host/var/lib/agent-workstation-kit/resource-policy.lock"
if output=$(policy "$host" --apply 2>&1); then
  fail 'expected a symlinked lock file to be refused'
fi
grep -Fq 'resource-policy.lock is a symbolic link' <<<"$output" || fail "symlinked lock printed: $output"
host=$(new_host receipt-link 64)
policy "$host" --apply >/dev/null
receipt=$(receipt_of "$host")
mv "$receipt" "$work/receipt-elsewhere.tsv"
ln -s "$work/receipt-elsewhere.tsv" "$receipt"
before=$(snapshot "$host")
if output=$(policy "$host" --revert --apply 2>&1); then
  fail 'expected a symlinked receipt to be refused'
fi
grep -Fq 'receipt.tsv is a symbolic link' <<<"$output" || fail "symlinked receipt printed: $output"
[[ "$(snapshot "$host")" == "$before" ]] || fail 'a refused revert through a symlinked receipt changed the host'

# 10. Agent processes outside agent-work.slice are found; others are not.
# make_proc DIR PID UID COMM CGROUP [ARGV...]
make_proc() {
  local dir="$1/$2" uid=$3 comm=$4 cgroup=$5
  shift 5
  mkdir -p "$dir"
  printf 'Name:\t%s\nUid:\t%s\t%s\t%s\t%s\n' "$comm" "$uid" "$uid" "$uid" "$uid" >"$dir/status"
  printf '%s\n' "$comm" >"$dir/comm"
  printf '0::%s\n' "$cgroup" >"$dir/cgroup"
  printf '%s\0' "${@:-$comm}" >"$dir/cmdline"
}
manager=/user.slice/user-1001.slice/user@1001.service
proc="$work/proc-pass"
make_proc "$proc" 101 1001 claude "$manager/agent.slice/agent-work.slice/run-r1.scope"
make_proc "$proc" 102 1001 zsh "$manager/agent.slice/agent-work.slice/run-r2.scope"
make_proc "$proc" 103 1001 nautilus "$manager/app.slice/app-nautilus.scope"
make_proc "$proc" 104 1000 claude /user.slice/user-1000.slice/session-2.scope
# Path is resolved from this test at runtime.
# shellcheck disable=SC1091
source "$repo_dir/scripts/lib/common.sh"
[[ -z "$(agent_processes_outside_slice "$proc" 1001)" ]] || fail 'placement check failed a correctly placed host'
cp -R "$proc" "$work/proc-fail"
proc="$work/proc-fail"
make_proc "$proc" 201 1001 node "$manager/app.slice/app-term.scope" node /opt/tools/codex exec
make_proc "$proc" 202 1001 zsh "$manager/orca_ptys_orcad_123.slice/pty-1.scope"
make_proc "$proc" 203 1001 grok "$manager/app.slice/app-term.scope"
outside=$(agent_processes_outside_slice "$proc" 1001)
for pid in 201 202 203; do
  grep -q "^$pid " <<<"$outside" || fail "placement check missed PID $pid"
done
[[ $(wc -l <<<"$outside") -eq 3 ]] || fail "placement check flagged too much: $outside"
# The phase verifier uses the same predicate and fails closed.
python3 - "$repo_dir" "$work/proc-pass" "$work/proc-fail" <<'EOF' || fail 'phase verifier placement check'
import sys
from pathlib import Path
sys.path.insert(0, sys.argv[1] + "/scripts/lib")
from phase_verification import agent_processes_outside_slice
root = Path(sys.argv[1])
assert agent_processes_outside_slice(root, 1001, sys.argv[2]) == []
assert len(agent_processes_outside_slice(root, 1001, sys.argv[3])) == 3
EOF

# The host audit (validate-host.sh) checks both slices through
# resource_slice_problems: an applied host passes, and each unsafe value fails.
applied="$work/matrix-desktop-16/etc/systemd/user"
[[ -z "$(resource_slice_problems "$applied/agent-work.slice" "$applied/agent.slice.d/50-agent-workstation-kit.conf" agent-01)" ]] ||
  fail "an applied host has slice problems: $(resource_slice_problems "$applied/agent-work.slice" "$applied/agent.slice.d/50-agent-workstation-kit.conf" agent-01)"
slice_problem() {
  local label=$1 pattern=$2 edit=$3 target=${4:-work} dir="$work/audit-$1"
  mkdir -p "$dir/agent.slice.d"
  cp "$applied/agent-work.slice" "$dir/"
  cp "$applied/agent.slice.d/50-agent-workstation-kit.conf" "$dir/agent.slice.d/"
  if [[ "$target" == work ]]; then
    sed "$edit" "$applied/agent-work.slice" >"$dir/agent-work.slice"
  else
    sed "$edit" "$applied/agent.slice.d/50-agent-workstation-kit.conf" >"$dir/agent.slice.d/50-agent-workstation-kit.conf"
  fi
  grep -Fq "$pattern" <<<"$(resource_slice_problems "$dir/agent-work.slice" "$dir/agent.slice.d/50-agent-workstation-kit.conf" agent-01)" ||
    fail "audit $label did not report: $pattern"
}
slice_problem max-below-2gib 'MemoryMax is missing or below 2 GiB' 's/^MemoryMax=.*/MemoryMax=1048576K/'
slice_problem high-equals-max 'MemoryHigh is missing or not below MemoryMax' 's/^MemoryHigh=.*/MemoryHigh=8053064K/'
slice_problem quota-below-100 'CPUQuota is missing or below 100%' 's/^CPUQuota=.*/CPUQuota=50%/'
slice_problem quota-overflow 'CPUQuota is missing or below 100%' 's/^CPUQuota=.*/CPUQuota=18446744073709551666%/'
slice_problem no-swap-max 'MemorySwapMax is missing' '/^MemorySwapMax=/d'
slice_problem weight-on-work 'sets CPUWeight, which belongs on agent.slice' 's/^ManagedOOMMemoryPressure=kill$/CPUWeight=50/'
slice_problem no-parent-weight 'agent.slice drop-in CPUWeight is missing' '/^CPUWeight=/d' parent
mkdir -p "$work/audit-link-dir"
ln -s "$applied/agent.slice.d" "$work/audit-link-dir/agent.slice.d"
grep -Fq 'agent.slice drop-in is missing or is a symbolic link' \
  <<<"$(resource_slice_problems "$applied/agent-work.slice" "$work/audit-link-dir/agent.slice.d/50-agent-workstation-kit.conf" agent-01)" ||
  fail 'audit did not report symlinked agent.slice.d'

# 11. The terminal wrapper runs the shell directly when the policy is absent,
# and falls back to the plain shell with a warning when the scope fails.
wrap_dir="$work/wrapper"
mkdir -p "$wrap_dir/bin"
printf 'x\n' >"$wrap_dir/agent-work.slice"
printf '0::/user.slice/user-1001.slice/user@1001.service/app.slice/x.scope\n' >"$wrap_dir/cgroup"
[[ $(AGENT_WORK_SLICE_FILE="$wrap_dir/absent" SHELL=/bin/echo "$repo_dir/scripts/agent-work-shell" hello) == '-l hello' ]] ||
  fail 'agent-work-shell did not exec the shell without the policy'
printf '#!/bin/sh\nexit 1\n' >"$wrap_dir/bin/systemd-run"
chmod +x "$wrap_dir/bin/systemd-run"
out=$(PATH="$wrap_dir/bin:$PATH" AGENT_WORK_SLICE_FILE="$wrap_dir/agent-work.slice" AGENT_WORK_CGROUP_FILE="$wrap_dir/cgroup" \
  SHELL=/bin/echo "$repo_dir/scripts/agent-work-shell" hello 2>"$wrap_dir/err") || fail 'agent-work-shell failed when the scope failed'
[[ "$out" == '-l hello' ]] || fail "agent-work-shell did not fall back to the shell: $out"
grep -Fq 'runs outside the memory ceiling' "$wrap_dir/err" || fail 'agent-work-shell gave no warning on fallback'
# With a working systemd-run, the wrapper launches the shell inside the scope.
# shellcheck disable=SC2016 # the stub's own $1 and $@, not this script'
printf '#!/bin/sh\nwhile [ "$1" != "--" ]; do shift; done; shift; exec "$@"\n' >"$wrap_dir/bin/systemd-run"
out=$(PATH="$wrap_dir/bin:$PATH" AGENT_WORK_SLICE_FILE="$wrap_dir/agent-work.slice" AGENT_WORK_CGROUP_FILE="$wrap_dir/cgroup" \
  SHELL=/bin/echo "$repo_dir/scripts/agent-work-shell" hello 2>"$wrap_dir/err") || fail 'agent-work-shell failed with a working scope'
[[ "$out" == '-l hello' && ! -s "$wrap_dir/err" ]] || fail "agent-work-shell did not run in the scope: $out"

printf 'Resource policy tests passed.\n'
