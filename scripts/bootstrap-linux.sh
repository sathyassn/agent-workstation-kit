#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

for arg in "$@"; do
  case "$arg" in
    --apply) APPLY_CHANGES=true ;;
    --help|-h) usage_mode; exit 0 ;;
    *) die "Unknown option: $arg" ;;
  esac
done

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

packages=(
  apt-transport-https build-essential ca-certificates curl fail2ban
  git git-lfs gnupg jq openssh-server python3 rsync shellcheck tmux ufw
  unattended-upgrades unzip zip zsh htop
)

log "Mode: $([[ "$APPLY_CHANGES" == true ]] && printf APPLY || printf PREVIEW)"
run apt-get update
run env DEBIAN_FRONTEND=noninteractive apt-get install -y "${packages[@]}"
run systemctl enable --now fail2ban
run systemctl enable --now apt-daily.timer apt-daily-upgrade.timer
run git lfs install --system

cat <<'EOF'

Manual checkpoints after this script:
1. Provision named-user SSH keys, configure Tailscale, and verify console/KVM
   recovery. This script installs but deliberately does not enable SSH.
2. Install the verified licensed NoMachine Enterprise Desktop package.
3. Run setup-accounts-linux.sh and install-agentctl-linux.sh.
4. Install user-space tooling as the agent-NN account.
5. Preview and apply harden-remote-access-linux.sh only while a tested recovery
   path is open. It enables key-only SSH and permits SSH/NoMachine on tailscale0.
EOF
