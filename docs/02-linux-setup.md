# Linux setup

[Previous: profile fields](01b-profile-field-reference.md) · [Documentation home](README.md) · [Day-zero entry](runbooks/day-zero-linux.md) · [Next: macOS setup](03-macos-setup.md)

Use Ubuntu LTS on the initial production nodes. Follow the [day-zero
guide](runbooks/day-zero-linux.md) for a fresh machine and perform the first
pilot interactively before reusing its profile elsewhere.

## Contents

1. [Human-only installation](#phase-0-human-only-installation)
2. [Read-only assessment](#phase-1-read-only-assessment)
3. [Base installation](#phase-2-scripted-base-installation)
4. [Machine identity](#phase-3-machine-identity)
5. [Accounts and remote access](#phase-4-accounts-and-access)
6. [User tooling and agents](#phase-5-user-space-tooling-and-agents)
7. [Controls and validation](#phase-6-controls-and-validation)

## Phase 0: human-only installation

1. Update firmware and record the device serial number.
2. Install supported **Ubuntu Desktop LTS** from verified installation media.
   The first MS-S1 Max baseline is 24.04.4 LTS. Ubuntu Server is outside this
   baseline unless a separately reviewed graphical desktop and display-manager
   build has been added.
3. Enable full-disk encryption when compatible with the unattended-boot plan.
4. Create the first temporary/bootstrap administrator.
5. Apply firmware and OS updates, reboot, and verify network/display stability.
6. Keep local monitor/keyboard recovery. A remote KVM may be deferred while the pilot remains physically supervised; install and test it before office placement or unattended operation.

These steps cannot be safely delegated to an agent that depends on the unfinished machine.

## Phase 1: read-only assessment

Copy this repository to the machine, create and validate the [onboarding profile](01a-onboarding-profile.md), then run:

```bash
cd "$HOME/setup/agent-workstation-kit"
./scripts/preflight.sh
```

Review hardware, OS, storage, memory, virtualization, desktop, and existing configuration. Resolve unsupported OS or insufficient disk space before proceeding.

After validation, follow the [day-zero staging procedure](runbooks/day-zero-linux.md)
to copy exact reviewed toolkit and private-fleet revisions to root-owned paths
under `/opt`. Run privileged phases only from the staged toolkit and only
against the staged fleet input. Keep ordinary development and proposed updates
in separate user-owned checkouts; promote replacements only after their checks
and review pass.

Use `fleetctl.py plan` to render profile-specific commands. The direct script commands below remain useful for inspection and troubleshooting; normal onboarding should run them through `fleetctl.py` one phase at a time.

For a node created with an earlier profile/schema, follow the
[v1-to-v2 migration runbook](runbooks/migrate-v1-to-v2.md); do not delete legacy
SSH or systemd files automatically.

## Phase 2: scripted base installation

Preview first from the reviewed bootstrap checkout:

```text
./scripts/bootstrap-linux.sh
```

Apply only after reviewing the plan and moving to the matching root-owned
toolkit snapshot:

```text
cd /opt/agent-workstation-kit
sudo ./scripts/bootstrap-linux.sh --apply
sudo -K
```

The base script installs stable OS packages and security/diagnostic prerequisites. It deliberately does not start SSH, authenticate external services, or install NoMachine. On MS-S1 Max hardware, complete the separate [RTL8127/Secure Boot runbook](hardware/minisforum-ms-s1-max.md).

## Phase 3: machine identity

Preview and then apply the profile's `identity` phase from the root-owned toolkit
snapshot. It sets the stable technical hostname, the human-friendly pretty name,
and `/etc/agent-workstation-kit/identity.toml`. Confirm the audit can read the
record and that it exactly matches the approved private profile.

Before apply, inspect `/etc/hosts`. If it contains the current short hostname,
replace only that existing alias with the approved new technical hostname in a
separately reviewed privileged change. The identity script fails closed while an
old mapping remains; it does not rewrite an organization-managed hosts file.
Keep console or KVM access open, then verify `hostnamectl`, local hostname
resolution, `sudo`, and the identity audit before continuing.

Apply only after recovery is open and tested:

```bash
cd /opt/agent-workstation-kit
PROFILE='machines/ac-ws-001.toml'
sudo ./scripts/fleetctl.py --fleet-root /opt/agent-workstation-fleet \
  run "$PROFILE" identity \
  --apply --confirm-recovery-tested --connection-context local-console
sudo -K
```

When applying through a named-user Tailscale SSH session, use
`--connection-context tailscale-ssh --ssh-source-ip "$ssh_peer"` as shown for
remote hardening below. The explicit context prevents a privileged identity
change from being applied without recording a tested recovery path. The
local-console form checks both the SSH environment and the full process ancestry
for SSH, Tailscale SSH, and Mosh; it fails closed if ancestry cannot be inspected,
including after `sudo` strips the SSH environment.

## Phase 4: accounts and access

Run the account script with explicit names, first without and then with `--apply`. Set passwords or SSH keys through the organization-approved process; do not put them on the command line or in a profile. The script never changes an existing password and leaves a newly created account without one.

Provision public keys for every named SSH user. Install and test Tailscale from
the console, then preview the profile's `remote-hardening` phase. Apply only
with open, tested recovery and an explicit connection context:

```bash
cd /opt/agent-workstation-kit
PROFILE='machines/ac-ws-001.toml'

# At a physical console or independently tested KVM:
sudo ./scripts/fleetctl.py --fleet-root /opt/agent-workstation-fleet \
  run "$PROFILE" remote-hardening --apply \
  --confirm-recovery-tested --connection-context local-console
sudo -K

# In a named-user SSH shell reached through Tailscale, capture before sudo:
ssh_peer=${SSH_CONNECTION%% *}
sudo ./scripts/fleetctl.py --fleet-root /opt/agent-workstation-fleet \
  run "$PROFILE" remote-hardening --apply \
  --confirm-recovery-tested --connection-context tailscale-ssh \
  --ssh-source-ip "$ssh_peer"
sudo -K
```

The remote form verifies the supplied peer with `tailscale whois`; a missing
context fails closed even when `sudo` strips SSH environment variables. The
phase enforces key-only SSH, denies direct SSH for `agent-NN`, restricts SSH
and NoMachine to `tailscale0`, and starts SSH only after the firewall is
active. The local-console form independently checks process ancestry and fails
closed if it cannot prove the absence of a remote-login ancestor. Keep recovery
open until a second named-user SSH session succeeds.

Set a long random local password for `agent-NN` using an interactive prompt and store it in the approved vault; it is for graphical login/unlock and recovery, not SSH. Install NoMachine Enterprise Desktop from a verified package, create the agent-owned physical desktop, and authorize named users without sharing that password. Choose a lock mode and test identity, reconnect, observer/controller, clipboard, and file-transfer behavior.

Ubuntu normally allocates subordinate UID/GID ranges when `agent-NN` is created.
If the workload phase reports they are missing, stop and have an administrator
allocate a unique, non-overlapping range in `/etc/subuid` and `/etc/subgid`
through the organization's account-management process; never copy a range from
another account or node blindly.

For vendor `.deb` files such as NoMachine, Chrome, VS Code, the ChatGPT Linux preview, or an approved Ghostty build, use `scripts/install-local-deb-linux.sh`. Preview the package metadata/checksum first and require an independently obtained expected SHA-256 for apply. Do not pipe community installers directly into a shell on work nodes.

## Phase 5: user-space tooling and agents

Log in as the agent account through the graphical workspace or controlled local setup session. Install version-managed runtimes, `gh`, `glab`, Codex, Claude Code, and Grok Build. Authentication is a separate human-approved phase.

Authenticate GitLab, GitHub, and Atlassian only after following the separate
[provider identity ceremony](06-agent-and-source-control-identities.md). Provider
admins create identities from their own trusted machines; only the scoped
runtime credential is brokered to `agent-NN`.

## Phase 6: controls and validation

Run the profile `workloads` phase as a named administrator. It installs rootless Podman with Docker CLI compatibility, Chromium, Xvfb, and supporting packages without adding `agent-NN` to the Docker group. Pin Playwright in each project and install that project's matching browser build. Install Grok Build from xAI's reviewed installer, then record its version.

Apply the balanced resource policy only after measuring the node. Run browser, container, build, multi-agent, reconnect, reboot, backup, and restore tests described in [validation and operations](08-validation-and-operations.md).
