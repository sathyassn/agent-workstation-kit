#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

install_agents=false
install_gws=false

while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agents) install_agents=true; shift ;;
    --gws) install_gws=true; shift ;;
    --help|-h)
      cat <<'EOF'
Usage: install-user-tooling.sh [--agents] [--gws] [--apply]

Run as the target non-root account. --gws is optional and must be explicitly selected.
EOF
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ ${EUID:-$(id -u)} -ne 0 ]] || die 'Run as the target human or agent-NN account, never root.'
command_exists mise || die 'mise is required. Install it using the verified official installer or OS package first.'
[[ "$install_agents" == true ]] || die '--agents is required by this baseline.'

repo_dir=$(cd "$script_dir/.." && pwd)
managed_config="$repo_dir/config/mise.toml"
global_config="$HOME/.config/mise/config.toml"
desired_config=$(mktemp)
trap 'rm -f "$desired_config"' EXIT
cp -- "$managed_config" "$desired_config"
if [[ "$install_gws" == true ]]; then
  printf '\n# Optional profile-selected tool.\n"npm:@googleworkspace/cli" = "latest"\n' >>"$desired_config"
fi

log "Mode: $([[ "$APPLY_CHANGES" == true ]] && printf APPLY || printf PREVIEW)"
if [[ -e "$global_config" ]] && ! cmp -s "$desired_config" "$global_config"; then
  if [[ "$APPLY_CHANGES" == true ]]; then
    die "$global_config already exists and differs from the reviewed fleet baseline. Merge it manually; no overwrite is allowed."
  fi
  warn "$global_config differs from the managed baseline; apply will stop until it is reviewed and merged."
fi
run install -d -m 0755 "$HOME/.config/mise"
if [[ ! -e "$global_config" ]]; then
  run install -m 0644 "$desired_config" "$global_config"
fi
run env MISE_GLOBAL_CONFIG_FILE="$global_config" mise --yes install
run env MISE_GLOBAL_CONFIG_FILE="$global_config" mise lock

if [[ "$APPLY_CHANGES" == true ]]; then
  mise doctor || warn 'mise doctor reported findings; installation completed, but review its diagnostics before acceptance.'
fi

cat <<EOF

Installed selection:
  baseline tools: yes
  agent CLIs:     $install_agents
  optional gws:   $install_gws

Next:
- Review the exact versions written to the user's global mise config and record
  the accepted baseline after the pilot.
- On work nodes, disable agent-CLI background self-updates when the provider
  supports it; stage reviewed upgrades on one node before fleet rollout.
- Authenticate gh, glab, Codex, Claude, Grok, and gws separately with approved identities.
- Do not place tokens in shell startup files.
- Grok Build is a required manual vendor-install gate. Use xAI's reviewed
  macOS/Linux installer from https://docs.x.ai/build/overview; do not substitute
  an unverified npm package. Record its version and rerun the host audit.
EOF
