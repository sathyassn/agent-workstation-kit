#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

readonly driver_commit='0b82eab2c29596aa5479690362544d8ce4d61d55'
readonly driver_sha256='6f0baecb54ff88ddfd225423ce2f5a365f0755336288810e67a3b6b88dff261c'
readonly driver_url="https://github.com/minisforum-repo/r8127-dkms/archive/$driver_commit.tar.gz"
# Provenance record (2026-08-22): the immutable GitHub commit archive above was
# downloaded over HTTPS and hashed locally. The release tag resolved to this
# commit at verification time. Re-verify URL, commit and digest when changing
# any pin; this record is not a substitute for vendor trust assessment.

while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --help|-h)
      printf 'Usage: install-ms-s1-r8127-dkms.sh [--apply]\n'
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die 'Linux only.'
require_root_for_apply

cat <<EOF
MS-S1 Max RTL8127 DKMS plan
  Source:       $driver_url
  Commit/tag:   $driver_commit (release 11.015.00-1)
  SHA-256:      $driver_sha256
  Secure Boot:  must remain enabled; Ubuntu DKMS signs with a machine MOK

This phase installs a third-party kernel module. Keep Wi-Fi, a USB NIC, or local
console access until both 10GbE ports survive a reboot and a kernel update.
It does not disable Secure Boot or blacklist an in-kernel driver.
EOF

if [[ "$APPLY_CHANGES" != true ]]; then
  log 'Preview only. Re-run with --apply after source, hash, recovery, and maintenance window are approved.'
  exit 0
fi

command -v curl >/dev/null 2>&1 || die 'curl is required.'
command -v mokutil >/dev/null 2>&1 || die 'mokutil is required; install it before this phase.'
mokutil --sb-state | grep -Fq 'SecureBoot enabled' || die 'Secure Boot must be enabled before installing the driver.'
build_user=${SUDO_USER:-}
[[ -n "$build_user" && "$build_user" != root ]] || \
  die 'Apply through sudo from a named non-root administrator so third-party source is built unprivileged.'
id "$build_user" >/dev/null 2>&1 || die "Cannot resolve invoking build user: $build_user"

work_dir=$(mktemp -d /var/tmp/agent-workstation-r8127.XXXXXX)
archive="$work_dir/source.tar.gz"
chown "$build_user" "$work_dir"
runuser -u "$build_user" -- curl --fail --location --proto '=https' --tlsv1.2 --output "$archive" "$driver_url"
printf '%s  %s\n' "$driver_sha256" "$archive" | sha256sum --check --status || die 'Driver source SHA-256 mismatch.'

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install --yes --no-install-recommends build-essential curl debhelper dh-dkms dkms linux-headers-generic linux-headers-"$(uname -r)" mokutil
runuser -u "$build_user" -- tar --extract --gzip --file "$archive" --directory "$work_dir"
source_dir="$work_dir/r8127-dkms-$driver_commit"
[[ -d "$source_dir/debian" ]] || die 'Verified archive has an unexpected layout.'
# $1 is intentionally expanded by the unprivileged child shell.
# shellcheck disable=SC2016
runuser -u "$build_user" -- sh -c 'cd "$1" && exec dpkg-buildpackage --build=binary --no-sign' sh "$source_dir"
package=$(find "$work_dir" -maxdepth 1 -type f -name 'r8127-dkms_*.deb' -print -quit)
[[ -n "$package" ]] || die 'DKMS package was not produced.'
apt-get install --yes "$package"

dkms status | grep -F r8127 || die 'r8127 is not registered with DKMS.'
signer=$(modinfo -F signer r8127 2>/dev/null || true)
[[ -n "$signer" ]] || die 'r8127 was built but has no module signer; stop before reboot.'
mok_der=/var/lib/shim-signed/mok/MOK.der
[[ -r "$mok_der" ]] || die "Ubuntu DKMS signing certificate is missing: $mok_der"
if ! mokutil --test-key "$mok_der" >/dev/null 2>&1; then
  cat >&2 <<EOF
[agent-workstation] HUMAN ACTION REQUIRED: the DKMS signing key is not enrolled.
Run this interactively, choose a one-time password, then reboot and enroll the
displayed key at the physical console/KVM:
  sudo mokutil --import $mok_der
After the enrollment reboot, rerun this script. It will verify enrollment before
reporting the driver phase complete. Never record the one-time password.
Build evidence remains in: $work_dir
EOF
  exit 3
fi

cat <<EOF

Driver package installed; its signing key is enrolled. Signer: $signer

Human gate before this phase is complete:
1. Reboot; verify Secure Boot, r8127, both ports, link speed, and network.
2. Install one normal kernel update and repeat the verification.
3. Remove $work_dir after evidence is captured and a human approves cleanup.
EOF
