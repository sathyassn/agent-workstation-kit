#!/usr/bin/env bash

set -Eeuo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
fixture_dir=$(mktemp -d)
trap 'rm -r "$fixture_dir"' EXIT

# The account and hardening scripts only need OS identity mocked for preview;
# no host state or privileged command is touched by these tests.
printf '%s\n' '#!/bin/sh' 'if [ "${1:-}" = "-s" ]; then echo Linux; else /usr/bin/uname "$@"; fi' >"$fixture_dir/uname"
printf '%s\n' '#!/bin/sh' 'exit 1' >"$fixture_dir/getent"
printf '%s\n' '#!/bin/sh' 'exit 1' >"$fixture_dir/id"
chmod 0755 "$fixture_dir/uname" "$fixture_dir/getent" "$fixture_dir/id"

preview=$(PATH="$fixture_dir:$PATH" "$repo_dir/scripts/setup-accounts-linux.sh" \
  --agent agt-ai-01 --human alice --admin adm-alice --operator alice)
grep -Fq 'usermod --append --groups agt-ai-01-operators alice' <<<"$preview"
grep -Fq 'this script never creates, changes, or locks passwords' <<<"$preview"

if PATH="$fixture_dir:$PATH" "$repo_dir/scripts/setup-accounts-linux.sh" \
  --agent agt-ai-01 --admin agt-ai-01 >/dev/null 2>&1; then
  printf 'Expected agent/admin overlap to be rejected.\n' >&2
  exit 1
fi

hardening_preview=$(PATH="$fixture_dir:$PATH" "$repo_dir/scripts/harden-remote-access-linux.sh" \
  --agent agt-ai-01 --ssh-user alice --ssh-user adm-alice)
grep -Fq 'AllowUsers alice adm-alice' <<<"$hardening_preview"
grep -Fq 'DenyUsers agt-ai-01' <<<"$hardening_preview"

if PATH="$fixture_dir:$PATH" "$repo_dir/scripts/harden-remote-access-linux.sh" \
  --agent agt-ai-01 --ssh-user agt-ai-01 >/dev/null 2>&1; then
  printf 'Expected direct SSH for the agent account to be rejected.\n' >&2
  exit 1
fi

if PATH="$fixture_dir:$PATH" "$repo_dir/scripts/harden-remote-access-linux.sh" \
  --agent agt-ai-01 --ssh-user alice --ssh-user alice >/dev/null 2>&1; then
  printf 'Expected duplicate SSH users to be rejected.\n' >&2
  exit 1
fi

if TMUX='' "$repo_dir/agentctl/agentctl" detach >/dev/null 2>&1; then
  printf 'Expected detach outside tmux to be rejected.\n' >&2
  exit 1
fi

# Exercise the same pure authorization predicates sourced by the installed
# privileged helper. The fake id command models one operator, one viewer, and
# one unauthorized account without requiring local account mutation.
printf '%s\n' '#!/bin/sh' \
  'case "${3:-}" in op) echo "op agt-ai-01-operators" ;; view) echo "view agt-ai-01-viewers" ;; *) echo "outsider" ;; esac' \
  >"$fixture_dir/id"
chmod 0755 "$fixture_dir/id"
AGENTCTL_OPERATOR_GROUP=agt-ai-01-operators
AGENTCTL_VIEWER_GROUP=agt-ai-01-viewers
export AGENTCTL_OPERATOR_GROUP AGENTCTL_VIEWER_GROUP
# shellcheck source=../agentctl/agentctl-policy
source "$repo_dir/agentctl/agentctl-policy"
PATH="$fixture_dir:$PATH" agentctl_is_operator op
PATH="$fixture_dir:$PATH" agentctl_is_viewer view
if PATH="$fixture_dir:$PATH" agentctl_is_viewer outsider; then
  printf 'Expected outsider to be rejected by agentctl policy.\n' >&2
  exit 1
fi

# The installer preview must expose the exact sudoers delegation for review.
printf '%s\n' '#!/bin/sh' \
  'if [ "${1:-}" = "-nG" ]; then echo "${3:-user}"; elif [ "${1:-}" = "-u" ]; then echo 1000; else exit 0; fi' \
  >"$fixture_dir/id"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$fixture_dir/getent"
chmod 0755 "$fixture_dir/id" "$fixture_dir/getent"
agentctl_preview=$(PATH="$fixture_dir:$PATH" "$repo_dir/scripts/install-agentctl-linux.sh" \
  --agent agt-ai-01 --target ai-node-01)
grep -Fq '%agt-ai-01-operators ALL=(agt-ai-01) NOPASSWD: /usr/local/libexec/agentctl-session *' <<<"$agentctl_preview"
grep -Fq "AGENTCTL_TARGET='ai-node-01'" <<<"$agentctl_preview"

# A local .deb path must be normalized before apt could consume it.
touch "$fixture_dir/example.deb"
printf '%s\n' '#!/bin/sh' \
  'case "${3:-}" in Package) echo example ;; Version) echo 1.0 ;; Architecture) echo amd64 ;; esac' \
  >"$fixture_dir/dpkg-deb"
printf '%s\n' '#!/bin/sh' \
  'printf "%064d  %s\n" 0 "${1:-}"' >"$fixture_dir/sha256sum"
chmod 0755 "$fixture_dir/dpkg-deb" "$fixture_dir/sha256sum"
deb_preview=$(cd "$fixture_dir" && PATH="$fixture_dir:$PATH" \
  "$repo_dir/scripts/install-local-deb-linux.sh" --package example.deb \
  --expected-sha256 0000000000000000000000000000000000000000000000000000000000000000)
expected_deb_path=$(realpath "$fixture_dir/example.deb")
grep -Fq "File:         $expected_deb_path" <<<"$deb_preview"

# Firewall and sudo parsers must fail closed on the cases missed by the first
# implementation, while accepting the exact tailnet-only and outbound forms.
# shellcheck source=../scripts/lib/common.sh
source "$repo_dir/scripts/lib/common.sh"
ufw_rule_is_unsafe 'ufw limit 22/tcp' 4000
ufw_rule_is_unsafe 'ufw route allow in on eth0 to any port 443 proto tcp' 4000
ufw_rule_is_unsafe 'ufw route allow in on eth0 out on tailscale0 to any port 22 proto tcp' 4000
ufw_rule_is_unsafe 'ufw allow in on tailscale0 to any port 8080 proto tcp' 4000
if ufw_rule_is_unsafe 'ufw allow in on tailscale0 to any port 22 proto tcp' 4000; then
  printf 'Expected a tailscale0-only UFW rule to be accepted.\n' >&2
  exit 1
fi
if ufw_rule_is_unsafe 'ufw allow out 443/tcp' 4000; then
  printf 'Expected an outbound UFW rule to be accepted.\n' >&2
  exit 1
fi
safe_rules=$'ufw allow out 443/tcp\nufw allow in on tailscale0 to any port 22 proto tcp'
if unsafe=$(first_unsafe_ufw_rule 4000 <<<"$safe_rules"); then
  printf 'Expected the complete safe UFW rule set to pass, got: %s\n' "$unsafe" >&2
  exit 1
fi
unsafe_rules=$'ufw allow in on tailscale0 to any port 22 proto tcp\nufw allow 8080/tcp'
[[ $(first_unsafe_ufw_rule 4000 <<<"$unsafe_rules") == 'ufw allow 8080/tcp' ]]
ufw_default_policies_are_safe $'DEFAULT_INPUT_POLICY="DROP"\nDEFAULT_FORWARD_POLICY="DROP"'
if ufw_default_policies_are_safe $'DEFAULT_INPUT_POLICY="ACCEPT"\nDEFAULT_FORWARD_POLICY="DROP"'; then
  printf 'Expected an allow-incoming UFW default to be rejected.\n' >&2
  exit 1
fi
sudo_policy_has_command_grants 'User agt may run the following commands on host:'
if sudo_policy_has_command_grants 'User agt is not allowed to run sudo on host.'; then
  printf 'Expected a no-sudo policy to be accepted.\n' >&2
  exit 1
fi
[[ $(calculate_tasks_max $((16 * 1024 * 1024))) == 4096 ]]
[[ $(calculate_tasks_max $((64 * 1024 * 1024))) == 8192 ]]
[[ $(calculate_tasks_max $((256 * 1024 * 1024))) == 16384 ]]

# The optional-tool selection is part of the desired mise configuration and
# must remain idempotent on a second apply.
printf '%s\n' '#!/bin/sh' 'exit 0' >"$fixture_dir/mise"
chmod 0755 "$fixture_dir/mise"
tooling_home="$fixture_dir/tooling-home"
mkdir -p "$tooling_home"
HOME="$tooling_home" PATH="$fixture_dir:$PATH" \
  "$repo_dir/scripts/install-user-tooling.sh" --agents --gws --apply >/dev/null
cp "$tooling_home/.config/mise/config.toml" "$fixture_dir/first-mise-config.toml"
HOME="$tooling_home" PATH="$fixture_dir:$PATH" \
  "$repo_dir/scripts/install-user-tooling.sh" --agents --gws --apply >/dev/null
cmp -s "$fixture_dir/first-mise-config.toml" "$tooling_home/.config/mise/config.toml"
grep -Fq '"npm:@googleworkspace/cli" = "latest"' "$tooling_home/.config/mise/config.toml"

printf 'Shell behavior tests passed.\n'
