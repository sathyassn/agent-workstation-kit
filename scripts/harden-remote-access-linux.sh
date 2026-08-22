#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

agent_account=''
nomachine_port=4000
recovery_confirmed=false
connection_context=''
ssh_source_ip=''
ssh_users=()

while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --agent) agent_account=${2:?Missing agent account}; shift 2 ;;
    --ssh-user) ssh_users+=("${2:?Missing SSH user}"); shift 2 ;;
    --nomachine-port) nomachine_port=${2:?Missing port}; shift 2 ;;
    --confirm-recovery-tested) recovery_confirmed=true; shift ;;
    --connection-context) connection_context=${2:?Missing connection context}; shift 2 ;;
    --ssh-source-ip) ssh_source_ip=${2:?Missing SSH source IP}; shift 2 ;;
    --help|-h)
      cat <<'EOF'
Usage: harden-remote-access-linux.sh --agent agent-01 --ssh-user USER
       [--ssh-user USER ...] [--nomachine-port 4000]
       [--confirm-recovery-tested]
       [--connection-context local-console]
       [--connection-context tailscale-ssh --ssh-source-ip ADDRESS]
       [--apply]

Preview is the default. Apply requires a tested console/KVM recovery path,
active Tailscale, an explicit connection context, and a usable SSH public key
for every named SSH user. For a remote apply, capture the SSH peer address in
the named-user shell before invoking sudo and pass it with --ssh-source-ip.
EOF
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -n "$agent_account" ]] || die '--agent is required.'
((${#ssh_users[@]} > 0)) || die 'at least one --ssh-user is required.'
validate_unix_name "$agent_account"
[[ "$nomachine_port" =~ ^[0-9]+$ ]] || die 'NoMachine port must be numeric.'
((nomachine_port >= 1 && nomachine_port <= 65535)) || die 'NoMachine port is out of range.'
declare -A seen_ssh_users=()
for account in "${ssh_users[@]}"; do
  validate_unix_name "$account"
  [[ "$account" != "$agent_account" ]] || \
    die 'the agent account must never be included in the direct SSH allowlist.'
  [[ -z "${seen_ssh_users[$account]:-}" ]] || die "Duplicate SSH user: $account"
  seen_ssh_users[$account]=1
done
require_root_for_apply

dropin=/etc/ssh/sshd_config.d/00-agent-workstation.conf
legacy_dropin=/etc/ssh/sshd_config.d/00-agent-fleet.conf
older_legacy_dropin=/etc/ssh/sshd_config.d/60-agent-fleet.conf
dropin_created=false
tmp=''
ufw_backup_dir=''
ufw_touched=false
ufw_was_active=false

cleanup() {
  local exit_status=$? restoration_failed=false
  trap - EXIT
  set +e
  if ((exit_status != 0)) && [[ "$ufw_touched" == true && -n "$ufw_backup_dir" ]]; then
    for path in /etc/default/ufw /etc/ufw/user.rules /etc/ufw/user6.rules; do
      backup="$ufw_backup_dir${path}"
      if [[ -r "$backup" ]] && ! cp -p -- "$backup" "$path"; then
        restoration_failed=true
      fi
    done
    if [[ "$ufw_was_active" == true ]]; then
      ufw --force reload >/dev/null 2>&1 || true
    else
      ufw --force disable >/dev/null 2>&1 || true
    fi
    warn 'Restored the pre-change UFW files after a failed apply; verify connectivity from the recovery console.'
    [[ "$restoration_failed" == false ]] || \
      warn 'At least one UFW file could not be restored; use the recovery console before closing this session.'
  fi
  if [[ "$dropin_created" == true ]]; then
    rm -f "$dropin"
  fi
  [[ -z "$tmp" ]] || rm -f "$tmp"
  [[ -z "$ufw_backup_dir" ]] || rm -rf -- "$ufw_backup_dir"
  exit "$exit_status"
}
trap cleanup EXIT

cat <<EOF
Remote-access hardening preview
  Direct SSH users:  ${ssh_users[*]}
  Denied SSH user:   $agent_account
  Allowed interface: tailscale0 only
  Allowed ports:     22/tcp, $nomachine_port/tcp, $nomachine_port/udp

sshd policy:
  PermitRootLogin no
  PasswordAuthentication no
  KbdInteractiveAuthentication no
  PubkeyAuthentication yes
  AllowUsers ${ssh_users[*]}
  DenyUsers $agent_account
EOF

if [[ "$APPLY_CHANGES" != true ]]; then
  log "Would write $dropin, validate sshd, enable UFW, then enable SSH."
  exit 0
fi

[[ "$recovery_confirmed" == true ]] || \
  die 'apply requires --confirm-recovery-tested after a live console/KVM recovery test.'
case "$connection_context" in
  local-console)
    [[ -z "$ssh_source_ip" ]] || die '--ssh-source-ip is valid only with --connection-context tailscale-ssh.'
    [[ -z ${SSH_CONNECTION:-} ]] || \
      die 'The current shell reports an SSH connection; use tailscale-ssh and pass its source IP.'
    ;;
  tailscale-ssh)
    [[ -n "$ssh_source_ip" ]] || die 'tailscale-ssh requires --ssh-source-ip captured before sudo.'
    [[ "$ssh_source_ip" != -* && "$ssh_source_ip" != *[[:space:]]* ]] || die 'Invalid SSH source IP.'
    if [[ -n ${SSH_CONNECTION:-} && ${SSH_CONNECTION%% *} != "$ssh_source_ip" ]]; then
      die 'The supplied SSH source does not match SSH_CONNECTION.'
    fi
    ;;
  *)
    die 'apply requires --connection-context local-console or tailscale-ssh.'
    ;;
esac
for command_name in sshd ufw tailscale systemctl; do
  command_exists "$command_name" || die "Missing required command: $command_name"
done
systemctl is-active --quiet tailscaled || die 'Tailscale is not active.'
ip link show tailscale0 >/dev/null 2>&1 || die 'tailscale0 is not available.'
if [[ "$connection_context" == tailscale-ssh ]]; then
  tailscale whois "$ssh_source_ip" >/dev/null 2>&1 || \
    die "Supplied SSH source ($ssh_source_ip) is not authenticated by this tailnet. Reconnect through Tailscale or apply at the tested local console."
fi
id "$agent_account" >/dev/null 2>&1 || die "Unknown agent account: $agent_account"
[[ ! -e "$legacy_dropin" && ! -e "$older_legacy_dropin" ]] || \
  die 'A legacy agent-fleet SSH policy exists. Review and migrate it before applying schema v2.'

for account in "${ssh_users[@]}"; do
  id "$account" >/dev/null 2>&1 || die "Unknown SSH user: $account"
  account_home=$(getent passwd "$account" | cut -d: -f6)
  authorized_keys="$account_home/.ssh/authorized_keys"
  [[ -r "$authorized_keys" ]] || die "No readable authorized_keys for $account"
  grep -Eq '^[[:space:]]*(ssh-|ecdsa-|sk-|cert-authority)' "$authorized_keys" || \
    die "No usable public key found for $account"
done

if [[ -r "$dropin" ]]; then
  if ! grep -Fxq "AllowUsers ${ssh_users[*]}" "$dropin" || \
     ! grep -Fxq "DenyUsers $agent_account" "$dropin"; then
    die "$dropin already exists with different policy; merge it manually."
  fi
else
  tmp=$(mktemp)
  cat >"$tmp" <<EOF
# Managed by agent-workstation-kit. Direct remote login to the shared account is denied.
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AllowUsers ${ssh_users[*]}
DenyUsers $agent_account
EOF
  install -o root -g root -m 0644 "$tmp" "$dropin"
  dropin_created=true
fi

sshd -t || die 'sshd validation failed; a newly created drop-in will be removed.'

effective=$(sshd -T)
grep -Fxq 'permitrootlogin no' <<<"$effective" || die 'effective SSH policy still permits root login.'
grep -Fxq 'passwordauthentication no' <<<"$effective" || die 'effective SSH policy still permits passwords.'
grep -Fxq 'kbdinteractiveauthentication no' <<<"$effective" || die 'effective SSH policy still permits keyboard-interactive authentication.'
effective_denyusers=$(awk '$1 == "denyusers" {$1=""; sub(/^ /, ""); print}' <<<"$effective")
tr ' ' '\n' <<<"$effective_denyusers" | grep -Fxq "$agent_account" || \
  die "effective SSH policy does not deny $agent_account."
effective_allowusers=$(awk '$1 == "allowusers" {$1=""; sub(/^ /, ""); print}' <<<"$effective")
for account in "${ssh_users[@]}"; do
  tr ' ' '\n' <<<"$effective_allowusers" | grep -Fxq "$account" || \
    die "effective SSH policy does not allow declared user $account."
done
effective_allow_count=$(wc -w <<<"$effective_allowusers" | tr -d ' ')
[[ "$effective_allow_count" -eq "${#ssh_users[@]}" ]] || \
  die 'effective SSH AllowUsers contains undeclared or duplicate entries.'

ufw_added=$(LC_ALL=C ufw show added 2>/dev/null) || die 'Cannot inspect persisted UFW rules.'
if unsafe_ufw_rule=$(sed -n '/^ufw /p' <<<"$ufw_added" | first_unsafe_ufw_rule "$nomachine_port"); then
  die "UFW has an unapproved inbound/forward accept rule: $unsafe_ufw_rule"
fi
[[ -r /etc/default/ufw ]] || die 'Cannot inspect /etc/default/ufw.'
ufw_defaults=$(< /etc/default/ufw)
ufw_default_policies_are_safe "$ufw_defaults" || \
  die 'UFW incoming/routed defaults are not DROP. Review the existing policy before applying.'

ufw_backup_dir=$(mktemp -d)
for path in /etc/default/ufw /etc/ufw/user.rules /etc/ufw/user6.rules; do
  [[ -r "$path" ]] || continue
  install -d "$ufw_backup_dir$(dirname "$path")"
  cp -p -- "$path" "$ufw_backup_dir$path"
done
ufw status | grep -Fxq 'Status: active' && ufw_was_active=true
ufw_touched=true

ufw default deny incoming
ufw default allow outgoing
ufw default deny routed
ufw allow in on tailscale0 to any port 22 proto tcp comment 'agent-workstation ssh'
ufw allow in on tailscale0 to any port "$nomachine_port" proto tcp comment 'agent-workstation nomachine tcp'
ufw allow in on tailscale0 to any port "$nomachine_port" proto udp comment 'agent-workstation nomachine udp'
ufw --force enable
systemctl enable --now ssh.socket 2>/dev/null || systemctl enable --now ssh
if systemctl is-active --quiet ssh.service; then
  systemctl reload ssh.service
elif systemctl is-active --quiet ssh.socket; then
  log 'OpenSSH socket is active; newly spawned service instances will read the validated policy.'
else
  die 'OpenSSH is not active after enablement.'
fi
dropin_created=false
ufw_touched=false

log 'Remote-access hardening applied. Keep the recovery console open.'
log 'Open a second named-user SSH session over the Tailscale address now; do not disconnect recovery until it succeeds.'
