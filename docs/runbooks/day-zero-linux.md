# Day-zero Linux startup and agent handoff

[Documentation home](../README.md) · [Linux setup](../02-linux-setup.md) · [First-pilot evidence](first-linux-pilot.md) · [MS-S1 Max hardware](../hardware/minisforum-ms-s1-max.md)

Use this guide from first power-on through the two setup-agent handoffs. It is
the entry point for a fresh Ubuntu workstation; it does not replace the Linux,
hardware or acceptance guides linked above.

## Contents

1. [Know the two handoffs](#1-know-the-two-handoffs)
2. [Prepare recovery and records](#2-prepare-recovery-and-records)
3. [Configure firmware](#3-configure-firmware)
4. [Install Ubuntu](#4-install-ubuntu)
5. [Install day-zero prerequisites](#5-install-day-zero-prerequisites)
6. [Obtain toolkit and fleet checkouts](#6-obtain-toolkit-and-fleet-checkouts)
7. [Install one bootstrap agent](#7-install-one-bootstrap-agent)
8. [Run the readiness command](#8-run-the-readiness-command)
9. [Perform the bootstrap handoff](#9-perform-the-bootstrap-handoff)
10. [Stage approved privileged code](#10-stage-approved-privileged-code)
11. [Move to the operational agent account](#11-move-to-the-operational-agent-account)
12. [Recover or stop](#12-recover-or-stop)

## 1. Know the two handoffs

```text
Human-only                Bootstrap setup agent          Operational agent
----------                ---------------------          -----------------
firmware                  assess and interview           long-running work
Ubuntu                    create/validate profile        browser/build agents
encryption      ----->    preview each phase    ----->   tmux/Herdr sessions
bootstrap account         pause for approvals            no sudo membership
network + first CLI       collect evidence               approved identities
```

The bootstrap agent runs as the temporary bootstrap administrator. Its login
cache is temporary and must never be copied into the final `agent-NN` account.

## 2. Prepare recovery and records

Before powering on, have:

- Monitor, keyboard and mouse.
- Wi-Fi or USB Ethernet fallback; do not depend on an untested 10GbE driver.
- Verified Ubuntu installer USB.
- Protected power and a second computer.
- Approved locations for disk-recovery material and private hardware evidence.

Open the [first-pilot checklist](first-linux-pilot.md) now. Complete its
**Before power-on** items, then use this day-zero guide as the controlling
procedure; return to that checklist for evidence at each checkpoint.

Record privately:

- Model, purchase, warranty and return deadline.
- Chassis/board serials, SSD identity, BIOS version and NIC MAC addresses.
- Organization asset tag and proposed assigned display name.

## 3. Configure firmware

Start from vendor-supported defaults and confirm:

- Secure Boot and TPM are enabled.
- SVM/virtualization and IOMMU are enabled when required.
- AC-power recovery is reviewed.
- Unsupported overclocking or memory tuning is disabled for the pilot.

For the MS-S1 Max, continue with the [hardware runbook](../hardware/minisforum-ms-s1-max.md).

## 4. Install Ubuntu

The first reviewed MS-S1 Max baseline is **Ubuntu Desktop 24.04.4 LTS amd64**.
Ubuntu 26.04 requires a separate compatibility decision before replacing this
pilot baseline.

During the interactive installer:

1. Select the normal Ubuntu Desktop installation.
2. Enable full-disk encryption when the approved recovery plan supports it.
3. Create only the temporary `bootstrap-admin` account.
4. Reboot, apply all updates, reboot again, and recheck updates.
5. Verify local login, display, fallback network, storage and time synchronization.

Do not create the final human, assigned-admin or `agent-NN` accounts manually;
the approved profile drives those later.

## 5. Install day-zero prerequisites

Ubuntu may not include Git or Make. Review, then run at the physical console:

```bash
sudo apt-get update
sudo apt-get install --yes ca-certificates curl git make python3 shellcheck unzip
```

These packages only establish the reviewed checkout and readiness path. The
profile-driven base phase installs the complete baseline later.

## 6. Obtain toolkit and fleet checkouts

Use two separate directories:

```text
~/setup/
├── agent-workstation-kit/        generic public-capable automation
└── acme-agent-workstation-fleet/ private organization inventory
```

After the repositories exist, assign their approved URLs and clone them:

```bash
mkdir -p "$HOME/setup"
cd "$HOME/setup"

KIT_REPOSITORY_URL='https://github.com/OWNER/agent-workstation-kit.git'
FLEET_REPOSITORY_URL='https://github.com/ORGANIZATION/acme-agent-workstation-fleet.git'

git clone "$KIT_REPOSITORY_URL" agent-workstation-kit
git clone "$FLEET_REPOSITORY_URL" acme-agent-workstation-fleet
```

Do not type credentials into either URL. Use the organization's approved Git
authentication flow. Until the repositories are hosted, transfer the exact
reviewed Git repository and record its approved revision in the deployment
evidence before proceeding.

## 7. Install one bootstrap agent

Only one authenticated CLI is needed for the early handoff. Codex is the
documented default; Claude may be used under an equivalent approved work
identity.

The official standalone installer is a bootstrap-only exception to the later
mise-managed tooling policy. Keep the downloaded script in a private temporary
directory, record the digest of the bytes reviewed and execute that same file:

```bash
CODEX_INSTALL_DIR="$(mktemp -d)"
chmod 0700 "$CODEX_INSTALL_DIR"
curl --fail --show-error --location \
  --output "$CODEX_INSTALL_DIR/codex-install.sh" \
  https://chatgpt.com/codex/install.sh
sha256sum "$CODEX_INSTALL_DIR/codex-install.sh"
less "$CODEX_INSTALL_DIR/codex-install.sh"
sh "$CODEX_INSTALL_DIR/codex-install.sh"

export PATH="$HOME/.local/bin:$PATH"
command -v codex
```

There is no repository-pinned checksum for this moving bootstrap installer.
Stop if organization policy requires a publisher checksum or pinned package;
use the approved internal distribution path instead.

Authenticate the temporary bootstrap account:

```bash
codex login
codex login status
```

For a remote/headless callback problem, use the vendor-documented device flow:

```bash
codex login --device-auth
```

Use an approved work identity. Do not copy a personal or bootstrap auth cache
into `agent-NN`.

## 8. Run the readiness command

From the toolkit root:

```bash
cd "$HOME/setup/agent-workstation-kit"

python3 scripts/start-linux-pilot.py \
  --fleet-root "$HOME/setup/acme-agent-workstation-fleet"
```

The command does not change host configuration. It checks:

- Ubuntu release and non-root execution.
- Clean toolkit revision and repository checks.
- Private fleet directory and `kit.lock`.
- Clean, committed private-fleet revision; an existing profile must be tracked.
- Optional profile validity.
- Codex installation/authentication.
- Host preflight.

It may create ignored test caches inside the toolkit checkout. It never invokes
`sudo`, changes an account, installs a package or reads a secret.

It does not print serial-bearing hardware data unless explicitly requested:

```bash
python3 scripts/start-linux-pilot.py \
  --fleet-root "$HOME/setup/acme-agent-workstation-fleet" \
  --profile machines/acme-ws-001.toml \
  --run-private-hardware-audit
```

Treat that terminal output as private evidence.

## 9. Perform the bootstrap handoff

When the summary contains no `FAIL`, start Codex in the toolkit root:

```bash
cd "$HOME/setup/agent-workstation-kit"
codex
```

Use the prompt printed by `start-linux-pilot.py`. The agent must:

1. Read `skills/setup-agent-workstation/SKILL.md` and approval boundaries.
2. Begin read-only and collect unresolved inputs once.
3. Create the profile through `fleetctl init`; never hand-create the UUID.
4. Resolve every `ask`, validate and show the complete plan.
5. Prepare a narrowly scoped private-fleet commit and show its exact diff.
6. Preview one phase at a time.
7. Stop before every `sudo`, credential, firmware, recovery or access change.
8. Validate and record evidence after every approved apply.

```text
profile interview -> draft -> validate -> plan -> human review
                                                  |
                           preview one phase <----+
                                  |
                           human approval
                                  |
                              apply + audit
```

The first profile-driven preview is shown only after the profile is approved.
For example:

```bash
./scripts/fleetctl.py \
  --fleet-root "$HOME/setup/acme-agent-workstation-fleet" \
  run machines/acme-ws-001.toml base
```

Do not add `sudo` or `--apply` in the `bootstrap-admin` checkout. First establish
the reviewed staging boundary below. The named-human checkout is created after
the accounts phase.

Before staging, the approved profile must be part of the private fleet's Git
history. The setup agent may prepare the commit, but a human reviews the exact
diff and authorizes the commit or merges its private PR/MR. For a supervised
local pilot with no remote workflow yet:

```bash
FLEET_ROOT="$HOME/setup/acme-agent-workstation-fleet"
PROFILE='machines/acme-ws-001.toml'

git -C "$FLEET_ROOT" diff -- "$PROFILE" kit.lock
git -C "$FLEET_ROOT" add -- "$PROFILE" kit.lock
git -C "$FLEET_ROOT" diff --cached --check
git -C "$FLEET_ROOT" diff --cached -- "$PROFILE" kit.lock
git -C "$FLEET_ROOT" commit -m "fleet: approve acme-ws-001 baseline"
git -C "$FLEET_ROOT" status --short  # expected output: nothing
```

Do not use `git add .`. If organization policy requires a PR/MR, push only after
separate approval, merge through the provider, then update this checkout to the
reviewed merge commit before continuing.

## 10. Stage approved privileged code

Run checks in the `bootstrap-admin` checkouts. Create exact committed archives
of both the generic code and its approved private input:

```bash
cd "$HOME/setup/agent-workstation-kit"
make check
git status --short                 # expected output: nothing

KIT_REVISION="$(git rev-parse HEAD)"
TOOLKIT_ARCHIVE="$HOME/setup/agent-workstation-kit-${KIT_REVISION}.tar"
git archive --format=tar --output="$TOOLKIT_ARCHIVE" "$KIT_REVISION"
KIT_SHA256="$(sha256sum "$TOOLKIT_ARCHIVE" | awk '{print $1}')"
printf 'toolkit %s  %s\n' "$KIT_SHA256" "$TOOLKIT_ARCHIVE"

FLEET_ROOT="$HOME/setup/acme-agent-workstation-fleet"
PROFILE='machines/acme-ws-001.toml'
git -C "$FLEET_ROOT" status --short  # expected output: nothing
FLEET_REVISION="$(git -C "$FLEET_ROOT" rev-parse HEAD)"
FLEET_ARCHIVE="$HOME/setup/acme-agent-workstation-fleet-${FLEET_REVISION}.tar"
git -C "$FLEET_ROOT" archive --format=tar \
  --output="$FLEET_ARCHIVE" "$FLEET_REVISION"
FLEET_SHA256="$(sha256sum "$FLEET_ARCHIVE" | awk '{print $1}')"
printf 'fleet   %s  %s\n' "$FLEET_SHA256" "$FLEET_ARCHIVE"
```

Record both revisions and digests in private deployment evidence. Review the
next command, keep console recovery open and approve this privileged change.
The fail-fast shell extracts both archives into one temporary area, publishes
the two final directories only after validation, and rolls back a normal
publication failure:

```bash
# In the separate trusted terminal, paste the two reviewed 64-character
# digests from private deployment evidence; do not recompute them here.
KIT_SHA256='REVIEWED_TOOLKIT_SHA256'
FLEET_SHA256='REVIEWED_FLEET_SHA256'

sudo bash -ceu '
  kit_archive=$1
  fleet_archive=$2
  profile=$3
  kit_sha256=$4
  fleet_sha256=$5
  kit_target=/opt/agent-workstation-kit
  fleet_target=/opt/agent-workstation-fleet

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
        mv -- "$kit_target" "$kit_stage"
      test "$fleet_published" -eq 1 && test -e "$fleet_target" && \
        mv -- "$fleet_target" "$fleet_stage"
      printf "Staging failed; preserved evidence at %s\n" "$stage" >&2
    fi
    exit "$rc"
  }
  trap rollback EXIT HUP INT TERM

  test -f "$kit_archive"
  test -f "$fleet_archive"
  test "$profile" = "machines/$(basename -- "$profile")"
  case "$profile" in *.toml) ;; *) exit 2 ;; esac
  test ! -e "$kit_target"
  test ! -L "$kit_target"
  test ! -e "$fleet_target"
  test ! -L "$fleet_target"

  install -o root -g root -m 0600 "$kit_archive" "$stage/kit.tar"
  install -o root -g root -m 0600 "$fleet_archive" "$stage/fleet.tar"
  printf "%s  %s\n" "$kit_sha256" "$stage/kit.tar" | sha256sum --check --strict -
  printf "%s  %s\n" "$fleet_sha256" "$stage/fleet.tar" | sha256sum --check --strict -

  install -d -o root -g root -m 0755 "$kit_stage"
  tar --extract --file "$stage/kit.tar" --directory "$kit_stage"
  chown -R root:root "$kit_stage"
  chmod -R go-w "$kit_stage"

  install -d -o root -g root -m 0750 "$fleet_stage"
  tar --extract --file "$stage/fleet.tar" --directory "$fleet_stage"
  chown -R root:root "$fleet_stage"
  find "$fleet_stage" -type d -exec chmod 0750 {} +
  find "$fleet_stage" -type f -exec chmod 0640 {} +

  test -x "$kit_stage/scripts/fleetctl.py"
  test -f "$fleet_stage/kit.lock"
  test -f "$fleet_stage/$profile"
  cmp --silent "$kit_stage/VERSION" "$fleet_stage/kit.lock"

  rm -- "$stage/kit.tar" "$stage/fleet.tar"

  mv -- "$kit_stage" "$kit_target"
  kit_published=1
  mv -- "$fleet_stage" "$fleet_target"
  fleet_published=1
  trap - EXIT HUP INT TERM
  rmdir -- "$stage"
' bash "$TOOLKIT_ARCHIVE" "$FLEET_ARCHIVE" "$PROFILE" \
  "$KIT_SHA256" "$FLEET_SHA256"

find /opt/agent-workstation-kit \( ! -user root -o -perm /022 \) -print
sudo find /opt/agent-workstation-fleet \
  \( ! -user root -o -perm /027 \) -print
sudo -K
```

Both final `find` commands must print nothing. If either destination exists or a
command fails, stop. The command reports the preserved temporary directory on a
normal failure. Do not reuse or delete that evidence until a human reviews it;
never overlay a new version. A power loss or `SIGKILL` can still leave a hidden
`/opt/.agent-workstation-stage.*` directory, which is not executable input and
must be reviewed before a clean retry.

The setup agent previews from the `bootstrap-admin` checkout. It then displays
the matching staged command. A human opens a separate trusted terminal, types
the apply command and immediately invalidates cached sudo authorization:

```bash
PROFILE='machines/acme-ws-001.toml'

cd "$HOME/setup/agent-workstation-kit"
./scripts/fleetctl.py \
  --fleet-root "$HOME/setup/acme-agent-workstation-fleet" \
  run "$PROFILE" base

cd /opt/agent-workstation-kit

sudo ./scripts/fleetctl.py \
  --fleet-root /opt/agent-workstation-fleet \
  run "$PROFILE" base --apply
sudo -K
```

The preview and apply must name the same profile and phase. The human must see a
password prompt; if sudo is already authorized, run `sudo -K` before the apply.
Use this preview, approval, staged-apply, `sudo -K` and audit pattern for every
privileged phase. Never let the setup agent type into the trusted apply terminal.

## 11. Move to the operational agent account

The final handoff happens only after:

- Named-human and assigned-admin accounts work independently.
- `agent-NN` exists, has no sudo membership and owns its graphical workspace.
- `agentctl`, Tailscale-only access and NoMachine reconnect pass.
- Shell, runtimes, Codex, Claude, Grok, browsers and rootless containers work
  under `agent-NN`.
- Approved work/provider identities are authenticated under `agent-NN`.
- Audit, resource observation and a recovery path pass.

```text
bootstrap-admin/Codex             agent-NN/Codex-Claude-Grok
temporary setup identity   --->   durable operational identity
logout after validation           long-running sessions live here
```

Run `codex logout` in `bootstrap-admin` after operational validation. Retain the
bootstrap OS account through burn-in; disable or remove it only through a later
reviewed change after recovery is proven.

## 12. Recover or stop

Stop rather than improvising when:

- Secure Boot, storage, network or local login fails.
- The toolkit is dirty or `kit.lock` does not match.
- A profile contains secrets or an unresolved `ask` at apply time.
- A command differs from its preview.
- The agent requests a password, token, private key or recovery material.
- Console/KVM recovery is unavailable before identity or remote hardening.

Return to the last known working console state, record the failure privately,
and use the applicable hardware, Linux or recovery guide.

[Documentation home](../README.md) · [Next: Linux setup phases](../02-linux-setup.md) · [Record pilot evidence](first-linux-pilot.md)
