#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

install_agents=false
install_gws=false
mise_version=''
mise_sha256=''

while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agents) install_agents=true; shift ;;
    --gws) install_gws=true; shift ;;
    --mise-version) mise_version=${2:?Missing mise version}; shift 2 ;;
    --mise-sha256) mise_sha256=${2:?Missing mise checksum}; shift 2 ;;
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
# A new Mac agent shell may not yet have loaded Homebrew's environment.
if [[ $(uname -s) == Darwin ]]; then
  if [[ -x /opt/homebrew/bin/mise ]]; then export PATH="/opt/homebrew/bin:$PATH";
  elif [[ -x /usr/local/bin/mise ]]; then export PATH="/usr/local/bin:$PATH"; fi
fi
mise_args=()
if [[ -n "$mise_version" || -n "$mise_sha256" ]]; then
  [[ -n "$mise_version" && -n "$mise_sha256" ]] || die 'Both mise version and SHA-256 are required.'
  mise_args=(--version "$mise_version" --sha256 "$mise_sha256")
elif ! command_exists mise && [[ $(uname -s) == Linux ]]; then
  mise_args=(--baseline)
fi
if ((${#mise_args[@]})); then
  if [[ "$APPLY_CHANGES" == true ]]; then mise_args+=(--apply); fi
  python_bin=$(find_toolkit_python)
  "$python_bin" "$script_dir/install-mise.py" "${mise_args[@]}"
  export PATH="$HOME/.local/bin:$PATH"
fi
if ! command_exists mise; then
  if [[ "$APPLY_CHANGES" == true || ${#mise_args[@]} -eq 0 ]]; then
    die 'mise is unavailable; complete the selected verified binary or Homebrew installation.'
  fi
  log 'PREVIEW mise will be available after the approved binary installation.'
fi
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
