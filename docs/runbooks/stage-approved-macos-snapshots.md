# Stage approved macOS setup snapshots

[Documentation home](../README.md) · [macOS day zero](day-zero-macos.md) · [macOS setup](../03-macos-setup.md)

Use this initial-deployment procedure after the profile and `kit.lock` are
reviewed, committed and clean. It creates root-owned, immutable inputs for
privileged macOS phases; it does not replace an existing stage.

## 1. Create exact committed archives

Run as `bootstrap-admin` from clean checkouts:

```bash
cd "$HOME/setup/agent-workstation-kit"
make check
git status --short                    # expected output: nothing

KIT_REVISION="$(git rev-parse HEAD)"
TOOLKIT_ARCHIVE="$HOME/setup/agent-workstation-kit-${KIT_REVISION}.tar"
git archive --format=tar --output="$TOOLKIT_ARCHIVE" "$KIT_REVISION"
KIT_SHA256="$(shasum -a 256 "$TOOLKIT_ARCHIVE" | awk '{print $1}')"
printf 'toolkit %s  %s\n' "$KIT_SHA256" "$TOOLKIT_ARCHIVE"

FLEET_ROOT="$HOME/setup/acme-agent-workstation-fleet"
PROFILE='machines/acme-mac-001.toml'
git -C "$FLEET_ROOT" status --short  # expected output: nothing
FLEET_REVISION="$(git -C "$FLEET_ROOT" rev-parse HEAD)"
FLEET_ARCHIVE="$HOME/setup/acme-agent-workstation-fleet-${FLEET_REVISION}.tar"
git -C "$FLEET_ROOT" archive --format=tar \
  --output="$FLEET_ARCHIVE" "$FLEET_REVISION"
FLEET_SHA256="$(shasum -a 256 "$FLEET_ARCHIVE" | awk '{print $1}')"
printf 'fleet   %s  %s\n' "$FLEET_SHA256" "$FLEET_ARCHIVE"
```

Record both Git revisions and SHA-256 values in private deployment evidence.
The operator must compare them with the reviewed commits before continuing.

## 2. Publish root-owned inputs

Keep local console or KVM recovery open. In a separate trusted Terminal window,
paste the reviewed archive paths, profile and digests, then run the exact command
below. The setup agent must not type in this window.

```bash
TOOLKIT_ARCHIVE="$HOME/setup/agent-workstation-kit-REVIEWED_TOOLKIT_REVISION.tar"
FLEET_ARCHIVE="$HOME/setup/acme-agent-workstation-fleet-REVIEWED_FLEET_REVISION.tar"
PROFILE='machines/acme-mac-001.toml'
KIT_SHA256='REVIEWED_TOOLKIT_SHA256'
FLEET_SHA256='REVIEWED_FLEET_SHA256'

sudo /usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME=/var/root \
  /bin/bash -ceu '
  kit_archive=$1
  fleet_archive=$2
  profile=$3
  kit_sha256=$4
  fleet_sha256=$5
  kit_target=/opt/agent-workstation-kit
  fleet_target=/opt/agent-workstation-fleet

  test ! -L /opt
  if test ! -e /opt; then
    install -d -o root -g wheel -m 0755 /opt
  fi
  test -d /opt
  test "$(stat -f %Su:%Sg:%Lp /opt)" = "root:wheel:755"

  stage=$(mktemp -d /opt/.agent-workstation-stage.XXXXXX)
  kit_stage=$stage/kit
  fleet_stage=$stage/fleet
  kit_published=0
  fleet_published=0

  rollback() {
    rc=$?
    trap - EXIT HUP INT TERM
    if test "$rc" -ne 0; then
      test "$kit_published" -eq 1 && test -e "$kit_target" && \
        mv "$kit_target" "$kit_stage"
      test "$fleet_published" -eq 1 && test -e "$fleet_target" && \
        mv "$fleet_target" "$fleet_stage"
      printf "Staging failed; preserved evidence at %s\n" "$stage" >&2
    fi
    exit "$rc"
  }
  trap rollback EXIT
  trap "exit 129" HUP
  trap "exit 130" INT
  trap "exit 143" TERM

  test -f "$kit_archive"
  test -f "$fleet_archive"
  test "$profile" = "machines/$(basename "$profile")"
  case "$profile" in *.toml) ;; *) exit 2 ;; esac
  test ! -e "$kit_target"
  test ! -L "$kit_target"
  test ! -e "$fleet_target"
  test ! -L "$fleet_target"

  install -o root -g wheel -m 0600 "$kit_archive" "$stage/kit.tar"
  install -o root -g wheel -m 0600 "$fleet_archive" "$stage/fleet.tar"
  printf "%s  %s\n" "$kit_sha256" "$stage/kit.tar" | \
    shasum -a 256 --check --strict -
  printf "%s  %s\n" "$fleet_sha256" "$stage/fleet.tar" | \
    shasum -a 256 --check --strict -

  install -d -o root -g wheel -m 0755 "$kit_stage"
  tar -xf "$stage/kit.tar" -C "$kit_stage"
  if find "$kit_stage" ! -type d ! -type f -print -quit | grep -q .; then
    printf "Toolkit archive contains a symlink or special entry\n" >&2
    exit 1
  fi
  chown -R root:wheel "$kit_stage"
  chmod -R go-w "$kit_stage"

  install -d -o root -g wheel -m 0750 "$fleet_stage"
  tar -xf "$stage/fleet.tar" -C "$fleet_stage"
  if find "$fleet_stage" ! -type d ! -type f -print -quit | grep -q .; then
    printf "Fleet archive contains a symlink or special entry\n" >&2
    exit 1
  fi
  chown -R root:wheel "$fleet_stage"
  find "$fleet_stage" -type d -exec chmod 0750 {} +
  find "$fleet_stage" -type f -exec chmod 0640 {} +

  test -x "$kit_stage/scripts/fleetctl.py"
  test -f "$fleet_stage/kit.lock"
  test -f "$fleet_stage/$profile"
  kit_version=$(tr -d "\r\n" < "$kit_stage/VERSION")
  fleet_version=$(tr -d "\r\n" < "$fleet_stage/kit.lock")
  if test -z "$kit_version" || test "$kit_version" != "$fleet_version"; then
    printf "Toolkit VERSION and fleet kit.lock do not match\n" >&2
    exit 1
  fi

  rm "$stage/kit.tar" "$stage/fleet.tar"
  mv "$kit_stage" "$kit_target"
  kit_published=1
  mv "$fleet_stage" "$fleet_target"
  fleet_published=1
  trap - EXIT HUP INT TERM
  rmdir "$stage"
' bash "$TOOLKIT_ARCHIVE" "$FLEET_ARCHIVE" "$PROFILE" \
  "$KIT_SHA256" "$FLEET_SHA256"

test "$(stat -f %Su:%Sg:%Lp /opt)" = "root:wheel:755"
find /opt/agent-workstation-kit \
  \( ! -user root -o ! -group wheel \) -print
find /opt/agent-workstation-kit \
  \( -perm -020 -o -perm -002 \) -print
sudo find /opt/agent-workstation-fleet \
  \( ! -user root -o ! -group wheel -o -perm -020 -o -perm -004 \
     -o -perm -002 -o -perm -001 \) -print
sudo -K
```

The `/opt` ownership test must pass and all three `find` commands must print
nothing. An existing `/opt/homebrew` is expected on Apple silicon and is left
untouched; this procedure manages only the two named `agent-workstation-*`
children. If a target already exists or any command fails, stop and review the
preserved staging directory. Do not overlay, delete or reuse it until the failed
deployment has been investigated.

## 3. Use only the stage for privileged phases

The setup agent shows the preview from the ordinary checkout. A named human
compares it with the staged command, replaces every `REVIEWED_*` value and every
sample argument with the exact reviewed values, runs the apply in the trusted
Terminal and immediately clears cached authorization:

```bash
cd /opt/agent-workstation-kit
sudo /usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME=/var/root \
  /usr/bin/python3 \
  /opt/agent-workstation-kit/scripts/install-machine-identity.py \
  --hostname acme-mac-001 --display-name Orchard \
  --uuid REVIEWED_UUIDV4 --asset-tag REVIEWED_ASSET_TAG \
  --namespace acme --platform macos --role REVIEWED_ROLE --apply \
  --confirm-recovery-tested --connection-context local-console
sudo -K
```

Every argument, the phase and Git revisions must match the `fleetctl` preview
and private evidence. `/usr/bin/python3` is used only for this staged identity
installer, which is tested against Apple's Command Line Tools Python and does
not import the toolkit's TOML controller.

[Documentation home](../README.md) · [Return to macOS setup](../03-macos-setup.md)
