#!/usr/bin/env bash
# shellcheck disable=SC2034

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

agent_account=''
while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agent) agent_account=${2:?Missing agent account}; shift 2 ;;
    --help|-h)
      printf 'Usage: install-workloads-linux.sh --agent agt-ai-01 [--apply]\n'
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -r /etc/os-release ]] || die 'Cannot identify Linux distribution.'
# shellcheck disable=SC1091
source /etc/os-release
[[ ${ID:-} == ubuntu ]] || die "Production baseline is Ubuntu; detected ${ID:-unknown}."
[[ -n "$agent_account" ]] || die '--agent is required.'
validate_unix_name "$agent_account"
id "$agent_account" >/dev/null 2>&1 || die "Unknown account: $agent_account"
require_root_for_apply

if id -nG "$agent_account" | tr ' ' '\n' | grep -Fxq docker; then
  die "$agent_account is in the docker group, which is root-equivalent. Remove that membership before continuing."
fi
grep -Eq "^${agent_account}:" /etc/subuid || \
  die "$agent_account has no subordinate UID range. Allocate a non-overlapping range through the approved account-management process before installing workloads."
grep -Eq "^${agent_account}:" /etc/subgid || \
  die "$agent_account has no subordinate GID range. Allocate a non-overlapping range through the approved account-management process before installing workloads."

packages=(
  chromium-browser fuse-overlayfs podman podman-compose podman-docker
  slirp4netns uidmap xvfb
)

cat <<EOF
Linux workload baseline for $agent_account
  Browser:        Ubuntu Chromium package (headed through the agt-* desktop)
  Containers:     rootless Podman with Docker CLI compatibility
  Headless GUI:   Xvfb
  Playwright:     installed and pinned inside each project, not globally

The shared agent account is never added to the root-equivalent docker group.
EOF

run apt-get update
run env DEBIAN_FRONTEND=noninteractive apt-get install -y "${packages[@]}"

cat <<EOF

Acceptance gates:
- Log in to the $agent_account desktop and confirm chromium-browser opens visibly.
- As $agent_account, run 'podman info' and a disposable rootless container.
- In each project, commit its Playwright version and run its own browser install
  (for example, 'npx playwright install chromium') before headless/headed tests.
- Validate headed tests in NoMachine and headless tests with Xvfb.
EOF
