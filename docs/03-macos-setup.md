# macOS setup

[Previous: Linux setup](02-linux-setup.md) · [Documentation home](README.md) · [Day-zero entry](runbooks/day-zero-macos.md) · [Next: accounts](04-accounts-and-access.md)

Use this path for Mac mini and Mac Studio workstations. Start a new Mac with the
[day-zero guide](runbooks/day-zero-macos.md); this page continues from its
readiness and setup-agent handoff.

## Contents

1. [Phase 0: human bootstrap](#phase-0-human-bootstrap)
2. [Phase 1: assess and declare](#phase-1-assess-and-declare)
3. [Phase 2: base applications](#phase-2-base-applications)
4. [Phase 3: machine identity](#phase-3-machine-identity)
5. [Phase 4: accounts](#phase-4-accounts)
6. [Phase 5: network and remote access](#phase-5-network-and-remote-access)
7. [Phase 6: operational agent environment](#phase-6-operational-agent-environment)
8. [Phase 7: Apple and browser workloads](#phase-7-apple-and-browser-workloads)
9. [Phase 8: validation and burn-in](#phase-8-validation-and-burn-in)

```text
day zero -> profile -> base -> identity -> accounts -> remote access
                                                   |
                                                   v
                    validation <- workloads <- agent desktop
```

## Phase 0: human bootstrap

Complete [day-zero macOS](runbooks/day-zero-macos.md) first. The named human
owns these gates:

- Setup Assistant, Apple ownership/activation and MDM enrollment.
- FileVault enablement, recovery escrow and tested cold-boot unlock.
- macOS updates and Xcode Command Line Tools.
- Temporary `bootstrap-admin`, local console and recovery access.
- One temporary authenticated setup-agent CLI.

## Phase 1: assess and declare

Run the read-only readiness check and preflight:

```bash
PYTHON_BIN="$(brew --prefix python@3.13)/bin/python3.13"
"$PYTHON_BIN" scripts/start-macos-pilot.py \
  --fleet-root "$HOME/setup/acme-agent-workstation-fleet"
./scripts/preflight.sh
```

Create a `platform = "macos"` profile through `fleetctl init`. Resolve every
`ask`, validate the complete fleet, review the exact diff and commit it before
any apply:

```bash
PROFILE='machines/acme-mac-001.toml'
FLEET_ROOT="$HOME/setup/acme-agent-workstation-fleet"
PYTHON_BIN="$(brew --prefix python@3.13)/bin/python3.13"

"$PYTHON_BIN" ./scripts/fleetctl.py \
  --fleet-root "$FLEET_ROOT" validate "$PROFILE" --ready
"$PYTHON_BIN" ./scripts/validate-fleet.py "$FLEET_ROOT"
"$PYTHON_BIN" ./scripts/fleetctl.py \
  --fleet-root "$FLEET_ROOT" plan "$PROFILE"
git -C "$FLEET_ROOT" status --short
```

The profile's `deployment.namespace` is a short operator-chosen prefix for the
private fleet, such as `acme`, `lab` or `home`; it is not fixed by this kit.
Technical hostname, assigned display name and UUID must remain unique.

## Phase 2: base applications

Day zero installs the human-approved Homebrew and Python prerequisite. Confirm
the same package-owning user and runtime; do not reinstall or change ownership:

```bash
brew --version
PYTHON_BIN="$(brew --prefix python@3.13)/bin/python3.13"
"$PYTHON_BIN" --version
```

Then preview the base phase:

```bash
"$PYTHON_BIN" ./scripts/fleetctl.py \
  --fleet-root "$FLEET_ROOT" run "$PROFILE" base
```

After reviewing every package and cask, apply as that same package owner:

```bash
"$PYTHON_BIN" ./scripts/fleetctl.py \
  --fleet-root "$FLEET_ROOT" run "$PROFILE" base --apply
```

The baseline installs Git/LFS, `gh`, `glab`, `jq`, `mise`, Python, ShellCheck,
tmux, Ghostty, Chrome, VS Code and OrbStack. It does not create accounts,
authenticate services, grant privacy permissions or enable remote access.

Run `brew doctor` and record package versions. Do not accept automatic changes
to an organization-managed shell, endpoint agent or system extension.

## Phase 3: machine identity

The identity phase sets:

- `HostName`: stable technical hostname, for example `acme-mac-001`.
- `LocalHostName`: the same DNS-safe technical identity.
- `ComputerName`: fleet-unique assigned name, for example `Orchard`.
- `/Library/Application Support/Agent Workstation Kit/identity.toml`: root-owned
  non-secret local record, mode `0644`.

Keep local console or KVM recovery open. Preview from the clean ordinary
checkout, then complete the
[root-owned macOS staging procedure](runbooks/stage-approved-macos-snapshots.md).
A named human runs the exact staged apply with an explicit connection context:

```bash
"$PYTHON_BIN" ./scripts/fleetctl.py \
  --fleet-root "$FLEET_ROOT" run "$PROFILE" identity

cd /opt/agent-workstation-kit
sudo /usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME=/var/root \
  /usr/bin/python3 \
  /opt/agent-workstation-kit/scripts/install-machine-identity.py \
  --hostname acme-mac-001 --display-name Orchard \
  --uuid REVIEWED_UUIDV4 --asset-tag REVIEWED_ASSET_TAG \
  --namespace acme --platform macos --role workstation --apply \
  --confirm-recovery-tested --connection-context local-console
sudo -K
```

For Tailscale SSH, use `--connection-context tailscale-ssh` and the verified
source address described by `fleetctl plan`. The installer checks process
ancestry and fails closed when it cannot prove the declared context.

Verify:

```bash
scutil --get HostName
scutil --get LocalHostName
scutil --get ComputerName
cat '/Library/Application Support/Agent Workstation Kit/identity.toml'
```

## Phase 4: accounts

Create accounts through MDM where available; otherwise a named human uses
**System Settings → Users & Groups**. The approved profile remains authoritative.

| Account | Purpose | Admin | Graphical session |
|---|---|---:|---:|
| named human, e.g. `alice` | attributable daily/recovery access | No | Optional |
| assigned `admin-NN` | privileged changes for one named owner | Yes | Recovery/setup only |
| `agent-NN` | shared agent runtimes and headed tests | No | Yes |
| `bootstrap-admin` | day-zero only | Temporary | Setup only |

Requirements:

- Never share a named-human or assigned-admin credential.
- Store the `agent-NN` local recovery password in the approved vault; humans
  normally reach its existing desktop through authorized remote-session policy.
- Decide which human/admin accounts can unlock FileVault. Do not grant Secure
  Token merely for convenience.
- Do not run GUI applications as `agent-NN` with `sudo -u`. Log into the actual
  `agent-NN` graphical session.

## Phase 5: network and remote access

Follow the [cross-platform access matrix](09-network-remote-access-and-files.md):

1. Install Tailscale using the approved macOS distribution path.
2. Enroll the target as a tagged non-human node; each operator uses an individual
   Tailscale identity on macOS, Windows, Linux, iPadOS, iOS or Android.
3. Install NoMachine Enterprise Desktop for a uniform cross-platform physical
   desktop. A Mac operator may additionally use Apple's Screen Sharing.
4. Enable Remote Login only for named allowlisted users; do not allow all users
   or direct `agent-NN` SSH.
5. Keep ports private to Tailscale. Test local-console recovery before tightening
   the firewall or access policy.

macOS privacy prompts may require Screen Recording, Accessibility, Automation,
Input Monitoring or Full Disk Access. Grant only the minimum required app and
record the approving human and reason. Use MDM privacy-policy payloads when the
organization manages them.

## Phase 6: operational agent environment

Log into the `agent-NN` graphical desktop and install user-space tooling there:

- Managed Zsh configuration with Antidote plugins, completions and suggestions.
- tmux and Herdr; validate detach, reattach and restart recovery.
- `mise`-managed Node.js, Python and Bun versions.
- Codex CLI, Claude Code, Grok Build, `gh` and `glab`.
- Optional `gws` and secret-vault CLIs only when selected in the profile.

Provider administrators create GitHub, GitLab and Atlassian workload identities
from a separate trusted administrative machine. Authenticate their scoped
credentials under `agent-NN` only after following the
[provider identity ceremony](06-agent-and-source-control-identities.md).

Do not copy `bootstrap-admin` caches, browser profiles, keychains, `.ssh` or
model credentials into `agent-NN`.

## Phase 7: Apple and browser workloads

As a named human, complete only the project-required items:

- Install Xcode from the approved Apple or managed distribution path.
- Accept the Xcode license and install required SDKs/simulator runtimes.
- Configure signing identities through the approved Keychain/CI process.
- Grant Developer Tools, Screen Recording, Accessibility and Automation access
  only to reviewed tools that need them.
- Validate OrbStack's Docker-compatible path; use Docker Desktop only for a
  documented compatibility requirement.
- Install project-pinned Playwright packages and their matching browsers.

A KVM is not required for headed Playwright. Start headed tests inside the
active `agent-NN` desktop and observe them through NoMachine or Screen Sharing.
The KVM remains the out-of-band boot/recovery path.

## Phase 8: validation and burn-in

Before production use, capture evidence for:

- Profile, local identity record and live macOS names agree.
- FileVault cold-boot unlock and approved recovery work.
- MDM/EDR, firewall, Tailscale, desktop and SSH policies survive reboot.
- Operators from each required client OS can connect with individual identities.
- Codex, Claude, Grok, GitHub, GitLab and optional Atlassian access use the
  intended workload identity and least privilege.
- Xcode build/simulator, headed and headless browser tests, containers, backup
  and restore work.
- Realistic multi-session load retains measured OS headroom without crippling
  agent throughput.

The repository path is implemented and testable, but live Mac evidence remains
pending until these checks are exercised on the selected Mac mini or Mac Studio.
Record results in the private fleet evidence, not this public repository.

[Previous: Linux setup](02-linux-setup.md) · [Documentation home](README.md) · [Next: accounts and access](04-accounts-and-access.md)
