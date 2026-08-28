#!/usr/bin/env bash

set -Eeuo pipefail

case ${BASH_SOURCE[0]} in
  */*) source_dir=${BASH_SOURCE[0]%/*} ;;
  *) source_dir=. ;;
esac
script_dir=$(cd -- "$source_dir" && pwd -P)
repo_dir=$(cd -- "$script_dir/.." && pwd -P)

# This phase installs code that sudo will later execute as another account.
# Verify the privileged trust boundary before loading any repository helper.
requested_apply=false
for trust_arg in "$@"; do
  [[ "$trust_arg" == --apply ]] && requested_apply=true
done
if [[ "$requested_apply" == true && ${EUID:-$(/usr/bin/id -u)} -eq 0 ]]; then
  if [[ "$repo_dir" != /opt/agent-workstation-kit ]]; then
    printf '[agent-workstation] ERROR: Apply must run from /opt/agent-workstation-kit.\n' >&2
    exit 1
  fi
  for source_path in "$repo_dir" "$script_dir" "$script_dir/lib" \
    "$script_dir/lib/common.sh" "$repo_dir/agentctl" \
    "$repo_dir/agentctl/agentctl" "$repo_dir/agentctl/agentctl-session" \
    "$repo_dir/agentctl/agentctl-observe" "$repo_dir/agentctl/agentctl-policy"; do
    if [[ -L "$source_path" || ! -e "$source_path" ]]; then
      printf '[agent-workstation] ERROR: Missing or symlinked privileged source: %s\n' "$source_path" >&2
      exit 1
    fi
    owner=$(/usr/bin/stat -c '%U' -- "$source_path")
    mode=$(/usr/bin/stat -c '%a' -- "$source_path")
    if [[ "$owner" != root || $((8#$mode & 8#022)) -ne 0 ]]; then
      printf '[agent-workstation] ERROR: Privileged source must be root-owned and not group/world writable: %s\n' "$source_path" >&2
      exit 1
    fi
  done
fi
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
      printf 'Usage: install-agentctl-linux.sh --agent agent-01 --target acme-ws-001 [--apply]\n'
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

if [[ "$APPLY_CHANGES" == true ]]; then
  log 'Verified the root-owned /opt staging tree before loading repository helpers.'
else
  log 'Would require /opt/agent-workstation-kit and verify privileged sources before loading repository helpers.'
fi

log "Install agentctl for $agent_account as target $target"
run install -d -o root -g root -m 0755 /etc/agentctl /usr/local/libexec
run install -o root -g root -m 0755 "$repo_dir/agentctl/agentctl" /usr/local/bin/agentctl
run install -o root -g root -m 0755 "$repo_dir/agentctl/agentctl-session" /usr/local/libexec/agentctl-session
run install -o root -g root -m 0755 "$repo_dir/agentctl/agentctl-observe" /usr/local/libexec/agentctl-observe
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
%$operator_group ALL=($agent_account) NOPASSWD: /usr/local/libexec/agentctl-observe *
%$viewer_group ALL=($agent_account) NOPASSWD: /usr/local/libexec/agentctl-observe *
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
  %$operator_group ALL=($agent_account) NOPASSWD: /usr/local/libexec/agentctl-observe *
  %$viewer_group ALL=($agent_account) NOPASSWD: /usr/local/libexec/agentctl-observe *
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
