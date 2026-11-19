#!/usr/bin/env bash
# The public entry point; OS-specific scripts remain implementation helpers.
set -Eeuo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
if [[ ${EUID:-$(id -u)} -eq 0 ]]; then
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
