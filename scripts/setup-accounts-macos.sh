#!/usr/bin/env bash
# Create selected local accounts with vendor password prompts, or verify MDM accounts.
set -Eeuo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"
agent_account=''
humans=()
admins=()
services=()
mode=managed
check_only=false
while (($#)); do
  case "$1" in
    --agent) agent_account=${2:?Missing agent}; shift 2 ;;
    --human) humans+=("${2:?Missing human}"); shift 2 ;;
    --admin) admins+=("${2:?Missing admin}"); shift 2 ;;
    --service) services+=("${2:?Missing service}"); shift 2 ;;
    --management) mode=${2:?Missing management selection}; shift 2 ;;
    --apply) APPLY_CHANGES=true; shift ;;
    --check) check_only=true; shift ;;
    --help|-h)
      printf '%s\n' 'Usage: setup-accounts-macos.sh --agent USER [--human USER] [--admin USER] [--service svc-purpose] [--management local|managed] [--check|--apply]'
      exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done
[[ $(uname -s) == Darwin ]] || die 'macOS only.'
[[ "$mode" == local || "$mode" == managed ]] || die 'Select local or managed account provisioning.'
[[ "$check_only" != true || "$APPLY_CHANGES" != true ]] || die '--check and --apply are exclusive.'
[[ -n "$agent_account" && ${#humans[@]} -gt 0 && ${#admins[@]} -gt 0 ]] || die 'Agent, named human and assigned administrator are required.'
all_accounts=("$agent_account" "${humans[@]}" "${admins[@]}" ${services[@]+"${services[@]}"})
for account in "${all_accounts[@]}"; do validate_unix_name "$account"; done
for ((i=0; i<${#all_accounts[@]}; i++)); do
  for ((j=i+1; j<${#all_accounts[@]}; j++)); do
    [[ "${all_accounts[i]}" != "${all_accounts[j]}" ]] || die 'Agent, human and admin roles must use distinct accounts.'
  done
done
require_root_for_apply
exists() { dscl . -read "/Users/$1" UniqueID >/dev/null 2>&1; }
is_admin() { dseditgroup -o checkmember -m "$1" admin >/dev/null 2>&1; }
missing=false
# Preflight every existing account before creating anything; never change its password/role.
for account in "${all_accounts[@]}"; do
  if exists "$account"; then
    uid=$(dscl . -read "/Users/$account" UniqueID | awk '{print $2}')
    if [[ ! "$uid" =~ ^[0-9]+$ ]] || ((uid < 501)); then die "Refusing system/directory account: $account"; fi
    log "Existing local account: $account (preserved)"
  else
    missing=true
    log "Missing account: $account"
  fi
done
for account in "$agent_account" "${humans[@]}" ${services[@]+"${services[@]}"}; do
  if exists "$account" && is_admin "$account"; then die "Expected non-admin account is an administrator: $account; review separately."; fi
done
for account in "${admins[@]}"; do
  if exists "$account" && ! is_admin "$account"; then die "Existing assigned admin lacks admin membership: $account; review separately."; fi
done
for service in ${services[@]+"${services[@]}"}; do
  [[ "$service" == svc-* ]] || die 'Service identities must use svc-purpose.'
  exists "$service" || die "Service $service requires a purpose-specific managed policy before this account phase; no local service privilege is inferred."
  service_shell=$(dscl . -read "/Users/$service" UserShell | awk '{print $2}')
  [[ "$service_shell" == */nologin || "$service_shell" == */false ]] || die "Service $service needs a reviewed non-interactive shell policy."
done
if [[ "$mode" == managed ]]; then
  [[ "$missing" != true ]] || die 'Provision the missing accounts with the approved MDM workflow, then rerun; no local override.'
  log 'PASS managed accounts and roles exist; no local changes.'
  exit 0
fi
if [[ "$check_only" == true ]]; then
  [[ "$missing" != true ]] || die 'Selected local accounts are not all present.'
  log 'PASS selected local accounts and roles exist.'
  exit 0
fi
if [[ "$APPLY_CHANGES" == true && "$missing" == true ]]; then
  [[ -t 0 && -t 1 ]] || die 'Creating accounts requires the approving human at an interactive terminal for hidden vendor password prompts.'
fi
create_user() {
  local account=$1 kind=$2
  exists "$account" && return 0
  local args=(-addUser "$account" -fullName "$account" -shell /bin/zsh -password -)
  [[ "$kind" != admin ]] || args+=(-admin)
  run /usr/sbin/sysadminctl "${args[@]}"
  if [[ "$APPLY_CHANGES" == true ]]; then
    exists "$account" || die "sysadminctl did not create $account; inspect privately before retrying."
    if [[ "$kind" == admin ]]; then is_admin "$account" || die "Admin membership missing for $account";
    elif is_admin "$account"; then die "Unexpected admin membership for $account"; fi
  fi
}
create_user "$agent_account" agent
for account in "${humans[@]}"; do create_user "$account" human; done
for account in "${admins[@]}"; do create_user "$account" admin; done
log 'FileVault/Secure Token, recovery escrow, provider authentication and the graphical session remain explicit human acceptance gates.'
