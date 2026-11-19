#!/usr/bin/env bash
# shellcheck disable=SC2034

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

agent_account=''
humans=()
admins=()
operators=()
viewers=()
services=()

while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agent) agent_account=${2:?Missing agent account}; shift 2 ;;
    --human) humans+=("${2:?Missing human account}"); shift 2 ;;
    --admin) admins+=("${2:?Missing admin account}"); shift 2 ;;
    --operator) operators+=("${2:?Missing operator account}"); shift 2 ;;
    --viewer) viewers+=("${2:?Missing viewer account}"); shift 2 ;;
    --service) services+=("${2:?Missing service account}"); shift 2 ;;
    --help|-h)
      cat <<'EOF'
Usage: setup-accounts-linux.sh --agent agent-01 [--human USER] [--admin USER]
       [--operator USER] [--viewer USER] [--service svc-purpose] [--apply]

Preview is the default. The script never sets passwords or installs SSH keys.
EOF
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -n "$agent_account" ]] || die '--agent is required.'
validate_unix_name "$agent_account"
for account in "${humans[@]}" "${admins[@]}" "${operators[@]}" "${viewers[@]}" "${services[@]}"; do
  [[ -z "$account" ]] || validate_unix_name "$account"
  [[ -z "$account" || "$account" != "$agent_account" ]] || \
    die 'The agent account must not also be a human, admin, operator, or viewer.'
done
for human in "${humans[@]}"; do
  for admin in "${admins[@]}"; do
    [[ "$human" != "$admin" ]] || die "Daily human and administrator must be separate accounts: $human"
  done
done
# Inspect every existing interactive identity before creating groups or users.
for account in "$agent_account" "${humans[@]}" "${admins[@]}"; do
  if id "$account" >/dev/null 2>&1; then
    existing_uid=$(id -u "$account")
    if [[ ! "$existing_uid" =~ ^[0-9]+$ ]] || ((existing_uid < 1000)); then
      die "Refusing to reuse a system/root identity as an interactive account: $account"
    fi
  fi
done
for service in "${services[@]}"; do
  [[ "$service" == svc-* ]] || die 'Service identities must use svc-purpose.'
  for person in "${humans[@]}" "${admins[@]}"; do
    [[ "$service" != "$person" ]] || die 'Service identities must be separate from human/admin accounts.'
  done
  if id "$service" >/dev/null 2>&1; then
    service_shell=$(getent passwd "$service" | cut -d: -f7)
    [[ "$service_shell" == */nologin || "$service_shell" == */false ]] || die "Existing service $service has an interactive shell; preserve and review its purpose policy."
    if id -nG "$service" | tr ' ' '\n' | grep -Eq '^(sudo|admin|docker)$'; then die "Existing service $service has an administrative group; review separately."; fi
  fi
done
require_root_for_apply

operator_group="${agent_account}-operators"
viewer_group="${agent_account}-viewers"
validate_unix_name "$operator_group"
validate_unix_name "$viewer_group"

ensure_group() {
  local group=$1
  if getent group "$group" >/dev/null 2>&1; then
    log "Group exists: $group"
  else
    run groupadd --system "$group"
  fi
}

ensure_user() {
  local account=$1 kind=$2
  if id "$account" >/dev/null 2>&1; then
    log "Account exists: $account"
    return
  fi
  case "$kind" in
    agent) run useradd --create-home --shell /bin/zsh "$account" ;;
    human) run useradd --create-home --shell /bin/zsh "$account" ;;
    admin) run useradd --create-home --shell /bin/zsh --groups sudo "$account" ;;
    service) run useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$account" ;;
    *) die "Unknown account kind: $kind" ;;
  esac
  if [[ "$kind" == service ]]; then
    log "Non-interactive service identity created: $account. No password, SSH key or privileged grant was added."
  else
    warn "No password or SSH key was set for $account. Provision access separately."
  fi
}

ensure_group "$operator_group"
ensure_group "$viewer_group"
ensure_user "$agent_account" agent
for account in "${humans[@]}"; do ensure_user "$account" human; done
for account in "${admins[@]}"; do ensure_user "$account" admin; done
for account in "${services[@]}"; do ensure_user "$account" service; done
for account in "${admins[@]}"; do
  run usermod --append --groups sudo "$account"
done
for account in "${operators[@]}"; do
  if [[ "$APPLY_CHANGES" == true ]]; then
    id "$account" >/dev/null 2>&1 || die "Operator does not exist: $account"
  fi
  run usermod --append --groups "$operator_group" "$account"
done
for account in "${viewers[@]}"; do
  if [[ "$APPLY_CHANGES" == true ]]; then
    id "$account" >/dev/null 2>&1 || die "Viewer does not exist: $account"
  fi
  run usermod --append --groups "$viewer_group" "$account"
done

cat <<EOF

Review after apply:
- Provision human/admin SSH keys and approved authentication.
- Confirm $agent_account has no sudo membership.
- New accounts have no password until a human provisions one. For the shared
  desktop, set a long random local password through a secure prompt and store it
  in the approved vault; this script never creates, changes, or locks passwords.
- Configure sshd to deny direct remote login for $agent_account only after testing named-user access.
- Install agentctl for $agent_account.
- Configure NoMachine trusted users independently; trusted does not mean OS administrator.
EOF
