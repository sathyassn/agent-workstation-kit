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
    --formula) package_args+=(--formula "${2:?Missing formula}"); shift 2 ;;
    --cask) package_args+=(--cask "${2:?Missing cask}"); shift 2 ;;
    --check) check_only=true; shift ;;
    --help|-h) usage_mode; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done
[[ "$check_only" != true || "$APPLY_CHANGES" != true ]] || die '--check and --apply are exclusive.'

require_macos_for_apply
[[ $(uname -s) == Darwin ]] || die 'This script supports macOS only.'

if ! xcode-select -p >/dev/null 2>&1; then
  die 'Install Xcode Command Line Tools as a named human, then rerun.'
fi

if ! command_exists brew; then
  die 'Homebrew is required but is not installed. Install it from brew.sh after reviewing the official installer.'
fi

log "Mode: $([[ "$APPLY_CHANGES" == true ]] && printf APPLY || printf PREVIEW)"
python_bin=$(find_toolkit_python)
if [[ "$check_only" == true ]]; then
  "$python_bin" "$script_dir/install-package-set.py" --platform macos ${package_args[@]+"${package_args[@]}"} --check
  exit
fi
if [[ "$APPLY_CHANGES" == true ]]; then package_args+=(--apply); fi
"$python_bin" "$script_dir/install-package-set.py" --platform macos ${package_args[@]+"${package_args[@]}"}
run git lfs install

cat <<'EOF'

Manual macOS checkpoints:
1. FileVault, recovery key, Screen Sharing/NoMachine, Remote Login, firewall, and MDM.
2. Privacy permissions for terminals, editors, browsers, and agent applications.
3. Xcode license, runtimes, simulators, and project signing identities.
4. Continue the machine scope for selected local-account prompts or MDM account verification before shared operation.
5. Install the pinned Herdr release through the user-tooling phase, then verify
   detach/reattach before relying on it for long-lived work.
EOF
