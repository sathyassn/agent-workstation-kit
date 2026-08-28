# Day-zero macOS startup and agent handoff

[Documentation home](../README.md) · [macOS setup phases](../03-macos-setup.md) · [Profile onboarding](../01a-onboarding-profile.md) · [Access from every operator OS](../09-network-remote-access-and-files.md)

Use this guide for a fresh Mac mini or Mac Studio, from first power-on until a
setup agent can safely continue. The human completes security and recovery
ceremonies; the agent assesses, plans and previews.

## Contents

1. [Know the handoff boundary](#1-know-the-handoff-boundary)
2. [Prepare ownership and recovery](#2-prepare-ownership-and-recovery)
3. [Complete Setup Assistant](#3-complete-setup-assistant)
4. [Update and verify the Mac](#4-update-and-verify-the-mac)
5. [Install day-zero prerequisites](#5-install-day-zero-prerequisites)
6. [Obtain toolkit and fleet checkouts](#6-obtain-toolkit-and-fleet-checkouts)
7. [Install one bootstrap agent](#7-install-one-bootstrap-agent)
8. [Run the readiness command](#8-run-the-readiness-command)
9. [Perform the setup-agent handoff](#9-perform-the-setup-agent-handoff)
10. [Continue through macOS setup](#10-continue-through-macos-setup)
11. [Move to the operational agent account](#11-move-to-the-operational-agent-account)
12. [Stop conditions](#12-stop-conditions)

## 1. Know the handoff boundary

```text
Human at the Mac                 Supervised setup agent
----------------                 ----------------------
Setup Assistant                  read-only assessment
ownership / MDM                  profile interview
FileVault recovery     ------>   validate + plan
updates + CLT                    preview one phase
network + first CLI              stop at every approval gate
```

The setup agent runs under the temporary `bootstrap-admin` account. That
account's model login is temporary and must not be copied into `agent-NN`.

## 2. Prepare ownership and recovery

Before power-on, have:

- Monitor, keyboard and pointing device; a local console is enough for a
  supervised pilot.
- Wired Ethernet plus a tested fallback network.
- A second computer and the approved Apple support/recovery procedure.
- The private fleet repository location and an approved secret vault.
- For organization-owned Macs: asset record, Automated Device Enrollment/MDM
  decision, FileVault escrow policy and endpoint-security owner.

Do not use a personal Apple Account on an organization-owned shared Mac unless
the organization explicitly requires and approves it. A remote KVM may be
deferred while the Mac is supervised, but it is required before unattended
placement when firmware, boot and FileVault recovery must be reachable.

## 3. Complete Setup Assistant

At the physical console:

1. Confirm the device belongs to the expected owner and is not unexpectedly
   activation-locked or enrolled elsewhere.
2. Complete organization enrollment when Automated Device Enrollment appears.
3. Create only the temporary local administrator `bootstrap-admin`, unless MDM
   supplies a different approved bootstrap account.
4. Leave personal cloud sync, consumer sharing and unrelated telemetry at the
   organization-approved settings.
5. Confirm date, time zone, keyboard, display and Ethernet operation.

Do not create final human, assigned-admin or `agent-NN` accounts ad hoc. Their
names and roles come from the reviewed private TOML profile or MDM.

## 4. Update and verify the Mac

Open **System Settings → General → Software Update**, install all supported
updates and reboot until no approved update remains.

Then verify:

- **Privacy & Security → FileVault** matches policy. For an organization Mac,
  confirm recovery is escrowed to the approved management system before relying
  on unattended operation. For a personal fleet, store the recovery key outside
  the Mac in the approved vault.
- At least one authorized human can unlock FileVault after a cold restart.
  `agent-NN` does not need preboot unlock merely to run workloads after a human
  has started the Mac.
- Sleep and restart-after-power-loss choices match the site's recovery plan.
- MDM/EDR enrollment is healthy where required.

Do not enable broad Screen Sharing, Remote Login or public port forwarding yet.
Remote access is configured later over Tailscale for named users only.

## 5. Install day-zero prerequisites

Install Xcode Command Line Tools from the bootstrap account:

```bash
xcode-select --install
```

Complete the Apple dialog, then verify without `sudo`:

```bash
xcode-select -p
git --version
make --version
python3 --version
```

The readiness suite requires Python 3.11 or newer; Apple's Command Line Tools
Python may be older. Install Homebrew as the package-owning bootstrap user,
never root, by downloading, inspecting and executing the same installer bytes:

```bash
BREW_INSTALL_DIR="$(mktemp -d)"
chmod 0700 "$BREW_INSTALL_DIR"
curl --fail --show-error --location \
  --output "$BREW_INSTALL_DIR/homebrew-install.sh" \
  https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh
shasum -a 256 "$BREW_INSTALL_DIR/homebrew-install.sh"
less "$BREW_INSTALL_DIR/homebrew-install.sh"
/bin/bash "$BREW_INSTALL_DIR/homebrew-install.sh"

brew install python@3.13
PYTHON_BIN="$(brew --prefix python@3.13)/bin/python3.13"
"$PYTHON_BIN" --version
```

The installer URL moves over time and this repository does not claim a pinned
publisher checksum. If policy requires an internally distributed package, use
that approved path. The normal base phase reuses this Homebrew installation.

## 6. Obtain toolkit and fleet checkouts

Use separate directories for generic code and private desired state:

```text
~/setup/
├── agent-workstation-kit/          public-capable toolkit
└── acme-agent-workstation-fleet/   private organization inventory
```

Assign the approved URLs and clone without embedding credentials:

```bash
mkdir -p "$HOME/setup"
cd "$HOME/setup"

KIT_REPOSITORY_URL='https://github.com/OWNER/agent-workstation-kit.git'
FLEET_REPOSITORY_URL='https://github.com/ORGANIZATION/acme-agent-workstation-fleet.git'

git clone "$KIT_REPOSITORY_URL" agent-workstation-kit
git clone "$FLEET_REPOSITORY_URL" acme-agent-workstation-fleet
```

If no remote exists, transfer the exact reviewed Git repositories from a
trusted machine and record both revisions. Do not use an unversioned folder or
a ZIP with no provenance as privileged setup input.

## 7. Install one bootstrap agent

One authenticated CLI is enough for handoff. Codex is the documented default.
Download the official installer to a private temporary directory, inspect it
and execute those same bytes:

```bash
CODEX_INSTALL_DIR="$(mktemp -d)"
chmod 0700 "$CODEX_INSTALL_DIR"
curl --fail --show-error --location \
  --output "$CODEX_INSTALL_DIR/codex-install.sh" \
  https://chatgpt.com/codex/install.sh
shasum -a 256 "$CODEX_INSTALL_DIR/codex-install.sh"
less "$CODEX_INSTALL_DIR/codex-install.sh"
sh "$CODEX_INSTALL_DIR/codex-install.sh"

export PATH="$HOME/.local/bin:$PATH"
command -v codex
codex login
codex login status
```

The installer URL moves over time and this repository does not claim a pinned
publisher checksum. If policy requires one, stop and use the organization's
approved software-distribution path. Use an approved setup identity and never
copy this auth cache into the operational agent home.

## 8. Run the readiness command

From the toolkit root:

```bash
cd "$HOME/setup/agent-workstation-kit"

PYTHON_BIN="$(brew --prefix python@3.13)/bin/python3.13"
"$PYTHON_BIN" scripts/start-macos-pilot.py \
  --fleet-root "$HOME/setup/acme-agent-workstation-fleet"
```

This read-only command checks:

- macOS, non-root execution and Xcode Command Line Tools.
- A clean toolkit revision and the repository test suite.
- A real, clean private fleet checkout with a matching `kit.lock`.
- When `--profile machines/<host>.toml` is supplied, that profile's platform,
  location, commit state and validation.
- Codex installation/authentication and host preflight.

It invokes no `sudo`, changes no System Settings and reads no secret value. The
repository test suite may create ignored Python or test caches inside the
toolkit checkout; it does not change host configuration.

## 9. Perform the setup-agent handoff

When no check reports `FAIL`, start Codex from the toolkit root:

```bash
cd "$HOME/setup/agent-workstation-kit"
codex
```

Use the prompt printed by `start-macos-pilot.py`. The setup agent must:

1. Read `skills/setup-agent-workstation/SKILL.md` and its macOS workflow.
2. Begin read-only and ask for unresolved non-secret inputs once.
3. Create the macOS profile with `fleetctl init`; the command generates UUIDv4.
4. Explain that `deployment.namespace` is the operator-chosen fleet prefix.
5. Validate uniqueness across the private fleet and show the complete plan.
6. Prepare a narrow private-fleet diff; a human reviews and commits it.
7. Preview one phase at a time and stop at every privileged or credential gate.

Example only—choose the namespace, hostname and assigned display name in the
private fleet:

```bash
PYTHON_BIN="$(brew --prefix python@3.13)/bin/python3.13"
"$PYTHON_BIN" ./scripts/fleetctl.py \
  --fleet-root "$HOME/setup/acme-agent-workstation-fleet" \
  init machines/acme-mac-001.toml \
  --context work --namespace acme --hostname acme-mac-001 \
  --display-name Orchard --platform macos \
  --hardware-profile mac-mini-or-studio --human alice
```

Never place an organization name, asset serial, token or recovery material in
the public toolkit. Real values live only in the private fleet or asset system.

## 10. Continue through macOS setup

After the profile is reviewed, committed and validation passes, follow
[macOS setup phases](../03-macos-setup.md) in order:

```text
profile -> base tools -> machine identity -> accounts -> private access
            -> agent desktop -> Xcode/browser/containers -> validation
```

The agent may run assessments and previews. A named human must act at `sudo`,
MDM, FileVault, privacy, system-extension, Xcode/signing and credential prompts.
Before the first privileged phase, use the
[macOS staging runbook](stage-approved-macos-snapshots.md) so `sudo` never
executes user-writable toolkit code or private TOML input.

## 11. Move to the operational agent account

Handoff is complete only when:

- Named-human and separately assigned administrator accounts work.
- `agent-NN` is non-admin and owns its graphical session and workspace.
- Tailscale plus NoMachine/Screen Sharing and SSH work for authorized humans.
- Codex, Claude Code, Grok Build, browsers and project tooling work inside the
  `agent-NN` session.
- Provider credentials were authorized under the approved workload identity.
- Reboot/FileVault unlock, backup/restore and resource checks have evidence.

Run `codex logout` under `bootstrap-admin`. Retain that OS account through
burn-in, then disable or remove it only through an approved recovery-tested
change.

## 12. Stop conditions

Stop rather than improvise when:

- Activation, MDM ownership, FileVault escrow or local recovery is unclear.
- The toolkit is dirty, the fleet is uncommitted or `kit.lock` differs.
- The agent requests a password, token, private key or recovery key.
- A privacy or system-extension prompt has no approved owner.
- Remote access is about to be exposed outside Tailscale.
- A command differs from its reviewed preview.

[Documentation home](../README.md) · [Next: macOS setup phases](../03-macos-setup.md) · [Operator access matrix](../09-network-remote-access-and-files.md)
