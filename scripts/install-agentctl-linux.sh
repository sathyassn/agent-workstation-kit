#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "$script_dir/.." && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

agent_account=''
target=''

while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agent) agent_account=${2:?Missing agent account}; shift 2 ;;
    --target) target=${2:?Missing target alias}; shift 2 ;;
    --help|-h)
      printf 'Usage: install-agentctl-linux.sh --agent agt-ai-01 --target ai-node-01 [--apply]\n'
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -n "$agent_account" && -n "$target" ]] || die '--agent and --target are required.'
validate_unix_name "$agent_account"
validate_session_name "$target"
id "$agent_account" >/dev/null 2>&1 || die "Agent account does not exist: $agent_account"
require_root_for_apply

operator_group="${agent_account}-operators"
viewer_group="${agent_account}-viewers"
getent group "$operator_group" >/dev/null 2>&1 || die "Missing group: $operator_group"
getent group "$viewer_group" >/dev/null 2>&1 || die "Missing group: $viewer_group"

log "Install agentctl for $agent_account as target $target"
run install -d -o root -g root -m 0755 /etc/agentctl /usr/local/libexec
run install -o root -g root -m 0755 "$repo_dir/agentctl/agentctl" /usr/local/bin/agentctl
run install -o root -g root -m 0755 "$repo_dir/agentctl/agentctl-session" /usr/local/libexec/agentctl-session
run install -o root -g root -m 0644 "$repo_dir/agentctl/agentctl-policy" /usr/local/libexec/agentctl-policy

if [[ "$APPLY_CHANGES" == true ]]; then
  config_tmp=$(mktemp)
  sudoers_tmp=$(mktemp)
  trap 'rm -f "$config_tmp" "$sudoers_tmp"' EXIT
  cat >"$config_tmp" <<EOF
AGENTCTL_TARGET='$target'
AGENTCTL_ACCOUNT='$agent_account'
AGENTCTL_OPERATOR_GROUP='$operator_group'
AGENTCTL_VIEWER_GROUP='$viewer_group'
EOF
  cat >"$sudoers_tmp" <<EOF
%$operator_group ALL=($agent_account) NOPASSWD: /usr/local/libexec/agentctl-session *
%$viewer_group ALL=($agent_account) NOPASSWD: /usr/local/libexec/agentctl-session *
EOF
  visudo -cf "$sudoers_tmp"
  install -o root -g root -m 0644 "$config_tmp" /etc/agentctl/target.conf
  install -o root -g root -m 0440 "$sudoers_tmp" "/etc/sudoers.d/60-agentctl-$agent_account"
  log 'Installed and validated agentctl configuration.'
else
  cat <<EOF
Would write /etc/agentctl/target.conf:
  AGENTCTL_TARGET='$target'
  AGENTCTL_ACCOUNT='$agent_account'
  AGENTCTL_OPERATOR_GROUP='$operator_group'
  AGENTCTL_VIEWER_GROUP='$viewer_group'

Would write /etc/sudoers.d/60-agentctl-$agent_account:
  %$operator_group ALL=($agent_account) NOPASSWD: /usr/local/libexec/agentctl-session *
  %$viewer_group ALL=($agent_account) NOPASSWD: /usr/local/libexec/agentctl-session *
EOF
fi

cat <<EOF

Validate from an authorized named-user shell:
  agentctl list
  agentctl status $target
  agentctl start $target smoke shell
  agentctl attach $target smoke
  agentctl detach
  agentctl stop $target smoke
EOF
