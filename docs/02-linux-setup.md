# Linux setup

Use Ubuntu LTS on the initial production nodes. Perform the first pilot interactively before reusing the profile elsewhere.

## Phase 0: human-only installation

1. Update firmware and record the device serial number.
2. Install supported **Ubuntu Desktop LTS** from verified installation media. Ubuntu Server is outside this baseline unless a separately reviewed graphical desktop and display-manager build has been added.
3. Enable full-disk encryption when compatible with the unattended-boot plan.
4. Create the first temporary/bootstrap administrator.
5. Apply firmware and OS updates, reboot, and verify network/display stability.
6. Keep local monitor/keyboard recovery. A remote KVM may be deferred while the pilot remains physically supervised; install and test it before office placement or unattended operation.

These steps cannot be safely delegated to an agent that depends on the unfinished machine.

## Phase 1: read-only assessment

Copy this repository to the machine, create and validate the [onboarding profile](01a-onboarding-profile.md), then run:

```text
./scripts/preflight.sh
```

Review hardware, OS, storage, memory, virtualization, desktop, and existing configuration. Resolve unsupported OS or insufficient disk space before proceeding.

After validation, copy the exact reviewed snapshot to
`/opt/agent-workstation-kit`, owned by root and not group/world writable. Run
privileged phases from that immutable staging copy. Keep ordinary development
and proposed updates in a separate named-human checkout; promote a new snapshot
only after its checks and review pass.

Use `fleetctl.py plan` to render profile-specific commands. The direct script commands below remain useful for inspection and troubleshooting; normal onboarding should run them through `fleetctl.py` one phase at a time.

For a node created with an earlier profile/schema, follow the
[v1-to-v2 migration runbook](runbooks/migrate-v1-to-v2.md); do not delete legacy
SSH or systemd files automatically.

## Phase 2: scripted base installation

Preview first:

```text
./scripts/bootstrap-linux.sh
```

Apply only after reviewing the plan:

```text
sudo ./scripts/bootstrap-linux.sh --apply
```

The base script installs stable OS packages and security/diagnostic prerequisites. It deliberately does not start SSH, authenticate external services, or install NoMachine. On MS-S1 Max hardware, complete the separate [RTL8127/Secure Boot runbook](hardware/minisforum-ms-s1-max.md).

## Phase 3: accounts and access

Run the account script with explicit names, first without and then with `--apply`. Set passwords or SSH keys through the organization-approved process; do not put them on the command line or in a profile. The script never changes an existing password and leaves a newly created account without one.

Provision public keys for every named SSH user. Install and test Tailscale from
the console, then preview the profile's `remote-hardening` phase. Apply only
with open, tested recovery and an explicit connection context:

```text
# At a physical console or independently tested KVM:
sudo ./scripts/fleetctl.py run PROFILE remote-hardening --apply \
  --confirm-recovery-tested --connection-context local-console

# In a named-user SSH shell reached through Tailscale, capture before sudo:
ssh_peer=${SSH_CONNECTION%% *}
sudo ./scripts/fleetctl.py run PROFILE remote-hardening --apply \
  --confirm-recovery-tested --connection-context tailscale-ssh \
  --ssh-source-ip "$ssh_peer"
```

For an external fleet, add `--fleet-root PRIVATE_FLEET` before `run`. The
remote form verifies the supplied peer with `tailscale whois`; a missing
context fails closed even when `sudo` strips SSH environment variables. The
phase enforces key-only SSH, denies direct SSH for `agent-NN`, restricts SSH
and NoMachine to `tailscale0`, and starts SSH only after the firewall is
active. Keep recovery open until a second named-user SSH session succeeds.

Set a long random local password for `agent-NN` using an interactive prompt and store it in the approved vault; it is for graphical login/unlock and recovery, not SSH. Install NoMachine Enterprise Desktop from a verified package, create the agent-owned physical desktop, and authorize named users without sharing that password. Choose a lock mode and test identity, reconnect, observer/controller, clipboard, and file-transfer behavior.

Ubuntu normally allocates subordinate UID/GID ranges when `agent-NN` is created.
If the workload phase reports they are missing, stop and have an administrator
allocate a unique, non-overlapping range in `/etc/subuid` and `/etc/subgid`
through the organization's account-management process; never copy a range from
another account or node blindly.

For vendor `.deb` files such as NoMachine, Chrome, VS Code, the ChatGPT Linux preview, or an approved Ghostty build, use `scripts/install-local-deb-linux.sh`. Preview the package metadata/checksum first and require an independently obtained expected SHA-256 for apply. Do not pipe community installers directly into a shell on work nodes.

## Phase 4: user-space tooling and agents

Log in as the agent account through the graphical workspace or controlled local setup session. Install version-managed runtimes, `gh`, `glab`, Codex, Claude Code, and Grok Build. Authentication is a separate human-approved phase.

## Phase 5: controls and validation

Run the profile `workloads` phase as a named administrator. It installs rootless Podman with Docker CLI compatibility, Chromium, Xvfb, and supporting packages without adding `agent-NN` to the Docker group. Pin Playwright in each project and install that project's matching browser build. Install Grok Build from xAI's reviewed installer, then record its version.

Apply the balanced resource policy only after measuring the node. Run browser, container, build, multi-agent, reconnect, reboot, backup, and restore tests described in [validation and operations](08-validation-and-operations.md).
