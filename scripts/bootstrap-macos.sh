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

require_macos_for_apply
[[ $(uname -s) == Darwin ]] || die 'This script supports macOS only.'

if ! xcode-select -p >/dev/null 2>&1; then
  die 'Install Xcode Command Line Tools as a named human, then rerun.'
fi

if ! command_exists brew; then
  die 'Homebrew is required but is not installed. Install it from brew.sh after reviewing the official installer.'
fi

formulae=(git git-lfs gh glab jq mise python@3.13 shellcheck tmux zsh-completions)
casks=(ghostty google-chrome visual-studio-code orbstack)

log "Mode: $([[ "$APPLY_CHANGES" == true ]] && printf APPLY || printf PREVIEW)"
run brew update
run brew install "${formulae[@]}"
run brew install --cask "${casks[@]}"
run git lfs install

cat <<'EOF'

Manual macOS checkpoints:
1. FileVault, recovery key, Screen Sharing/NoMachine, Remote Login, firewall, and MDM.
2. Privacy permissions for terminals, editors, browsers, and agent applications.
3. Xcode license, runtimes, simulators, and project signing identities.
4. Create named/admin-NN/agent-NN accounts using the macOS guide before shared operation.
5. Install the pinned Herdr release through the user-tooling phase, then verify
   detach/reattach before relying on it for long-lived work.
EOF
