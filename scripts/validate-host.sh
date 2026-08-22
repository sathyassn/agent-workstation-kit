#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

agent_account=${1:-}
nomachine_port=${2:-4000}
failures=0
warnings=0

[[ "$nomachine_port" =~ ^[0-9]+$ ]] || { printf 'NoMachine port must be numeric.\n' >&2; exit 2; }

pass() { printf 'PASS %s\n' "$*"; }
warn() { printf 'WARN %s\n' "$*"; warnings=$((warnings + 1)); }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }

target_command_path() {
  local command_name=$1 output=''
  if [[ -z "$agent_account" || $(id -un) == "$agent_account" ]]; then
    output=$(/bin/zsh -ic "command -v $command_name" 2>/dev/null || true)
  elif [[ ${EUID:-$(id -u)} -eq 0 ]]; then
    output=$(sudo -H -u "$agent_account" -- /bin/zsh -ic "command -v $command_name" 2>/dev/null || true)
  else
    fail "cannot inspect $agent_account user tooling without root; rerun with sudo"
    return
  fi
  if [[ -n "$output" ]]; then
    pass "target command $command_name: ${output%%$'\n'*}"
  else
    fail "target command $command_name is missing"
  fi
}

service_active() {
  local service=$1
  if systemctl is-active --quiet "$service"; then
    pass "service $service is active"
  else
    fail "service $service is inactive"
  fi
}

effective_equals() {
  local policy=$1 expected=$2 effective=$3
  if grep -Fxq "$policy $expected" <<<"$effective"; then
    pass "sshd $policy=$expected"
  else
    fail "sshd $policy is not $expected"
  fi
}

if [[ -n "$agent_account" ]]; then
  if id "$agent_account" >/dev/null 2>&1; then
    pass "account $agent_account exists"
  else
    fail "account $agent_account is missing"
  fi
fi

for command_name in git zsh tmux mise node python3 bun pnpm gh glab herdr codex claude grok; do
  target_command_path "$command_name"
done

if [[ $(uname -s) == Linux ]]; then
  for command_name in docker podman chromium-browser Xvfb; do
    target_command_path "$command_name"
  done
  if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
    fail 'Linux security validation requires root; rerun with sudo'
  else
    if systemctl is-active --quiet ssh.service || systemctl is-active --quiet ssh.socket; then
      pass 'OpenSSH service or socket is active'
    else
      fail 'OpenSSH service and socket are inactive'
    fi
    for service in fail2ban tailscaled; do service_active "$service"; done
    if command -v mokutil >/dev/null 2>&1 && mokutil --sb-state 2>/dev/null | grep -Fq 'SecureBoot enabled'; then
      pass 'Secure Boot is enabled'
    else
      fail 'Secure Boot is not proven enabled'
    fi

    if ufw_status=$(ufw status 2>/dev/null); then
      if grep -Fxq 'Status: active' <<<"$ufw_status"; then
        pass 'UFW is active'
      else
        fail 'UFW is not active'
      fi
      ufw_added=''
      ufw_inspected=true
      if ! ufw_added=$(LC_ALL=C ufw show added 2>/dev/null); then
        fail 'cannot inspect persisted UFW rules'
        ufw_inspected=false
      fi
      unsafe_ufw_rule=''
      if unsafe_ufw_rule=$(sed -n '/^ufw /p' <<<"$ufw_added" | first_unsafe_ufw_rule "$nomachine_port"); then
        fail "UFW has an unapproved inbound/forward accept rule: $unsafe_ufw_rule"
      elif [[ "$ufw_inspected" == true ]]; then
        pass 'UFW persisted inbound/forward accept rules are restricted to tailscale0'
      fi
      if [[ -r /etc/default/ufw ]] && ufw_default_policies_are_safe "$(< /etc/default/ufw)"; then
        pass 'UFW default incoming and routed policies are DROP'
      else
        fail 'UFW default incoming/routed policies are not proven to be DROP'
      fi
      for port_proto in 22/tcp "$nomachine_port/tcp" "$nomachine_port/udp"; do
        if grep -E "^${port_proto//\//\\/} on tailscale0[[:space:]]+ALLOW IN" <<<"$ufw_status" >/dev/null; then
          pass "UFW allows $port_proto on tailscale0"
        else
          fail "UFW lacks $port_proto allow on tailscale0"
        fi
      done
    else
      fail 'cannot read UFW status'
    fi

    if sshd -t; then
      pass 'sshd configuration syntax is valid'
      sshd_effective=$(sshd -T)
      effective_equals permitrootlogin no "$sshd_effective"
      effective_equals passwordauthentication no "$sshd_effective"
      effective_equals kbdinteractiveauthentication no "$sshd_effective"
      denyusers=$(awk '$1 == "denyusers" {$1=""; sub(/^ /, ""); print}' <<<"$sshd_effective")
      if tr ' ' '\n' <<<"$denyusers" | grep -Fxq "$agent_account"; then
        pass "sshd denies direct login for $agent_account"
      else
        fail "sshd does not deny direct login for $agent_account"
      fi
      allowusers=$(awk '$1 == "allowusers" {$1=""; sub(/^ /, ""); print}' <<<"$sshd_effective")
      if [[ -n "$allowusers" ]] && ! tr ' ' '\n' <<<"$allowusers" | grep -Fxq "$agent_account"; then
        pass 'sshd has an explicit AllowUsers list that excludes the agent account'
      else
        fail 'sshd AllowUsers is missing or includes the agent account'
      fi
    else
      fail 'sshd configuration syntax is invalid'
    fi

    if [[ -n "$agent_account" ]] && id "$agent_account" >/dev/null 2>&1; then
      memberships=$(id -nG "$agent_account" | tr ' ' '\n')
      for forbidden_group in sudo docker; do
        if grep -Fxq "$forbidden_group" <<<"$memberships"; then
          fail "$agent_account belongs to privileged group $forbidden_group"
        else
          pass "$agent_account is not in $forbidden_group"
        fi
      done

      sudo_policy=$(LC_ALL=C sudo -l -U "$agent_account" 2>&1 || true)
      if sudo_policy_has_command_grants "$sudo_policy"; then
        fail "$agent_account has a sudoers command grant outside group-membership checks"
      elif grep -Fq 'is not allowed to run sudo' <<<"$sudo_policy"; then
        pass "$agent_account has no sudoers command grants"
      else
        fail "could not prove that $agent_account has no sudoers command grants"
      fi

      password_state=$(passwd -S "$agent_account" 2>/dev/null | awk '{print $2}')
      if [[ "$password_state" == P ]]; then
        pass "$agent_account has a local desktop/unlock password"
      else
        fail "$agent_account has no usable local password for graphical unlock"
      fi

      uid=$(id -u "$agent_account")
      resource_dropin="/etc/systemd/system/user-$uid.slice.d/50-agent-workstation.conf"
      if [[ -r "$resource_dropin" ]] && \
         grep -Eq '^MemoryHigh=' "$resource_dropin" && \
         grep -Eq '^MemoryMax=' "$resource_dropin"; then
        pass "resource policy exists for user-$uid.slice"
      else
        fail "resource policy is missing for user-$uid.slice"
      fi
      tasks_max=$(awk -F= '$1 == "TasksMax" {print $2}' "$resource_dropin" 2>/dev/null || true)
      if [[ "$tasks_max" =~ ^[0-9]+$ ]] && ((tasks_max >= 4096 && tasks_max <= 16384)); then
        pass "resource TasksMax=$tasks_max is within the reviewed emergency range"
      else
        fail 'resource TasksMax is missing or outside the reviewed 4096-16384 range'
      fi

      outside_slice=0
      process_count=0
      while IFS= read -r pid; do
        [[ -r "/proc/$pid/cgroup" ]] || continue
        process_count=$((process_count + 1))
        if ! grep -Fq "user-$uid.slice" "/proc/$pid/cgroup"; then
          outside_slice=$((outside_slice + 1))
        fi
      done < <(pgrep -u "$uid" 2>/dev/null || true)
      if ((process_count == 0)); then
        warn "no live $agent_account processes; validate cgroup placement during load test"
      elif ((outside_slice == 0)); then
        pass "all observed $agent_account processes are under user-$uid.slice"
      else
        fail "$outside_slice of $process_count $agent_account processes are outside user-$uid.slice"
      fi
    fi

    if systemctl is-enabled --quiet apt-daily-upgrade.timer; then
      pass 'automatic security-update timer is enabled'
    else
      warn 'automatic security-update timer is not enabled'
    fi

    root_source=$(findmnt -n -o SOURCE / 2>/dev/null || true)
    if [[ -n "$root_source" ]] && lsblk -s -n -o TYPE "$root_source" 2>/dev/null | grep -Fxq crypt; then
      pass 'root filesystem is backed by a Linux encrypted block device'
    else
      fail 'root filesystem encryption is not detected'
    fi
  fi
elif [[ $(uname -s) == Darwin ]]; then
  target_command_path docker
  if [[ -d '/Applications/Google Chrome.app' ]]; then
    pass 'Google Chrome application is installed'
  else
    fail 'Google Chrome application is missing'
  fi
  if fdesetup status 2>/dev/null | grep -Fq 'FileVault is On'; then
    pass 'FileVault is on'
  else
    fail 'FileVault is not confirmed on'
  fi
  warn 'macOS Screen Sharing/NoMachine, firewall, privacy grants, and FileVault recovery require manual evidence'
fi

printf '\nValidation failures: %d; warnings: %d\n' "$failures" "$warnings"
((failures == 0)) && exit 0
exit 1
