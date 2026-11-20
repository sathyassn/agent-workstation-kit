#!/usr/bin/env bash
# The public entry point; OS-specific scripts remain implementation helpers.
set -Eeuo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Succeed only when PATH and every parent directory are root-owned, not
# symlinks, and not group/world writable. Used before any root execution.
path_is_root_controlled() {
  local path=$1 owner mode
  [[ "$path" == /* ]] || return 1
  while :; do
    [[ -e "$path" && ! -L "$path" ]] || return 1
    if [[ $(uname -s) == Darwin ]]; then
      read -r owner mode < <(stat -f '%u %Lp' "$path") || return 1
    else
      read -r owner mode < <(stat -c '%u %a' "$path") || return 1
    fi
    [[ "$owner" == 0 ]] || return 1
    (( (8#$mode & 8#022) == 0 )) || return 1
    [[ "$path" != / ]] || return 0
    path=$(dirname "$path")
  done
}

if [[ ${EUID:-$(id -u)} -eq 0 ]]; then
  # Root runs only the reviewed root-owned snapshot, for preview and status too.
  # Nothing from the checkout is sourced or executed before this check.
  staged_root=/opt/agent-workstation-kit
  if [[ "$script_dir" != "$staged_root/scripts" ]] ||
    ! path_is_root_controlled "$script_dir/lib/setup_cli.py" ||
    ! path_is_root_controlled "$script_dir/lib/common.sh" ||
    [[ -n $(find "$staged_root" \( ! -user 0 -o -perm -g+w -o -perm -o+w -o -type l \) -print -quit 2>&1) ]]; then
    printf '[agent-workstation] ERROR: %s\n' "Root may run only the root-owned staged copy at $staged_root. Stage the reviewed source there (docs/runbooks/resumable-setup.md); root refuses a user-writable checkout." >&2
    exit 5
  fi
  root_home=/root
  [[ $(uname -s) != Darwin ]] || root_home=/var/root
  exec /usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$root_home" \
    /usr/bin/python3 "$script_dir/lib/setup_cli.py" "$@"
fi
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"
if [[ $(uname -s) == Darwin ]]; then
  if [[ -x /opt/homebrew/bin/brew ]]; then export PATH="/opt/homebrew/bin:$PATH";
  elif [[ -x /usr/local/bin/brew ]]; then export PATH="/usr/local/bin:$PATH"; fi
fi
if [[ -n ${AWK_PYTHON:-} ]]; then
  exec "$AWK_PYTHON" "$script_dir/lib/setup_cli.py" "$@"
fi
if python_bin=$(find_toolkit_python 2>/dev/null); then
  exec "$python_bin" "$script_dir/lib/setup_cli.py" "$@"
fi
# Basic help/doctor and portable staged execution can use the OS Python 3.9+.
exec /usr/bin/python3 "$script_dir/lib/setup_cli.py" "$@"
