#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

package_args=()
check_only=false
while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --apt) package_args+=(--apt "${2:?Missing APT package}"); shift 2 ;;
    --check) check_only=true; shift ;;
    --help|-h) usage_mode; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done
[[ "$check_only" != true || "$APPLY_CHANGES" != true ]] || die '--check and --apply are exclusive.'

[[ $(uname -s) == Linux ]] || die 'This script supports Linux only.'
[[ -r /etc/os-release ]] || die 'Cannot identify Linux distribution.'
# shellcheck disable=SC1091
source /etc/os-release
[[ ${ID:-} == ubuntu ]] || die "Production baseline is Ubuntu; detected ${ID:-unknown}."

case "${VERSION_ID:-}" in
  24.04|26.04) ;;
  *) warn "Ubuntu ${VERSION_ID:-unknown} is outside the currently documented baseline." ;;
esac

require_root_for_apply

log "Mode: $([[ "$APPLY_CHANGES" == true ]] && printf APPLY || printf PREVIEW)"
if [[ "$check_only" == true ]]; then
  python3 "$script_dir/install-package-set.py" --platform linux "${package_args[@]}" --check
  # One unit per call: with several units, is-enabled succeeds if any is enabled.
  for unit in fail2ban apt-daily.timer apt-daily-upgrade.timer; do
    systemctl is-enabled --quiet "$unit" || die "$unit is not enabled"
  done
  exit
fi
if [[ "$APPLY_CHANGES" == true ]]; then package_args+=(--apply); fi
python3 "$script_dir/install-package-set.py" --platform linux "${package_args[@]}"
run systemctl enable --now fail2ban
run systemctl enable --now apt-daily.timer apt-daily-upgrade.timer
run git lfs install --system

cat <<'EOF'

Manual checkpoints after this script:
1. Provision named-user SSH keys, configure Tailscale, and verify console/KVM
   recovery. This script installs but deliberately does not enable SSH.
2. Install the verified licensed NoMachine Enterprise Desktop package.
3. Continue the machine scope through the single setup-workstation.sh entry.
   It provisions the declared accounts, hardening, agentctl and workload packages.
4. Run the agent scope in the actual agent-NN account for user-space tooling.
5. Keep tested recovery open for the privileged hardening step. It enables
   key-only SSH and permits SSH/NoMachine only on tailscale0.
EOF
