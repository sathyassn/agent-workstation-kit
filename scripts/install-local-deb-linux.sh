#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

package_file=''
expected_sha256=''

while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --package) package_file=${2:?Missing package path}; shift 2 ;;
    --expected-sha256) expected_sha256=${2:?Missing checksum}; shift 2 ;;
    --help|-h)
      cat <<'EOF'
Usage: install-local-deb-linux.sh --package FILE.deb --expected-sha256 HEX [--apply]

Download the package from the vendor using a human-reviewed path. Preview prints
metadata and the actual checksum. Apply requires the independently recorded checksum.
EOF
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die 'Linux only.'
[[ -n "$package_file" && -f "$package_file" ]] || die '--package must identify an existing file.'
command_exists dpkg-deb || die 'dpkg-deb is required.'
command_exists sha256sum || die 'sha256sum is required.'
package_file=$(realpath -- "$package_file")

actual_sha256=$(sha256sum "$package_file" | awk '{print $1}')
package_name=$(dpkg-deb -f "$package_file" Package)
package_version=$(dpkg-deb -f "$package_file" Version)
package_arch=$(dpkg-deb -f "$package_file" Architecture)

cat <<EOF
Package:      $package_name
Version:      $package_version
Architecture: $package_arch
File:         $package_file
SHA-256:      $actual_sha256
EOF

if [[ "$APPLY_CHANGES" == true ]]; then
  require_root_for_apply
  [[ "$expected_sha256" =~ ^[a-fA-F0-9]{64}$ ]] || die 'Apply requires a valid --expected-sha256.'
  [[ ${expected_sha256,,} == "$actual_sha256" ]] || die 'Checksum mismatch; package was not installed.'
  run apt-get install -y -- "$package_file"
else
  log 'Preview only. Verify metadata and checksum against an independent vendor source before apply.'
fi
