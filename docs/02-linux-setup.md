# Linux setup

Use Ubuntu LTS on the initial production nodes. Perform the first pilot interactively before reusing the profile elsewhere.

## Phase 0: human-only installation

1. Update firmware and record the device serial number.
2. Install supported **Ubuntu Desktop LTS** from verified installation media. Ubuntu Server is outside this baseline unless a separately reviewed graphical desktop and display-manager build has been added.
3. Enable full-disk encryption when compatible with the unattended-boot plan.
4. Create the first temporary/bootstrap administrator.
5. Apply firmware and OS updates, reboot, and verify network/display stability.
6. Connect and secure the remote KVM. Test BIOS and disk-unlock access.

These steps cannot be safely delegated to an agent that depends on the unfinished machine.

## Phase 1: read-only assessment

Copy this repository to the machine, create and validate the [onboarding profile](01a-onboarding-profile.md), then run:

```text
./scripts/preflight.sh
```

Review hardware, OS, storage, memory, virtualization, desktop, and existing configuration. Resolve unsupported OS or insufficient disk space before proceeding.

Use `fleetctl.py plan` to render profile-specific commands. The direct script commands below remain useful for inspection and troubleshooting; normal onboarding should run them through `fleetctl.py` one phase at a time.

## Phase 2: scripted base installation

Preview first:

```text
./scripts/bootstrap-linux.sh
```

Apply only after reviewing the plan:

```text
sudo ./scripts/bootstrap-linux.sh --apply
```

The base script installs stable OS packages and security/diagnostic prerequisites. It deliberately does not start SSH, authenticate external services, or install a licensed NoMachine package.

## Phase 3: accounts and access

Run the account script with explicit names, first without and then with `--apply`. Set passwords or SSH keys through the organization-approved process; do not put them on the command line or in a profile. The script never changes an existing password and leaves a newly created account without one.

Provision public keys for every named SSH user. Install and test Tailscale from the console/KVM, then preview `scripts/harden-remote-access-linux.sh`. Apply it only with an open, tested recovery path and its explicit `--confirm-recovery-tested` flag. It enforces key-only SSH, denies direct SSH for `agt-*`, restricts SSH and NoMachine to `tailscale0`, and starts SSH only after the firewall is active. Keep recovery open until a second named-user SSH session succeeds.

Set a long random local password for `agt-*` using an interactive password prompt and store it in the approved shared vault; it is for local graphical login/unlock and recovery, not SSH. Install NoMachine Enterprise Desktop from the verified vendor package, activate the license, create the `agt-*` graphical session, and mark only approved named users as trusted for that desktop. Choose and document one shared-desktop lock mode from [accounts and access](04-accounts-and-access.md), then test reconnect and lock behavior.

For vendor `.deb` files such as NoMachine, Chrome, VS Code, the ChatGPT Linux preview, or an approved Ghostty build, use `scripts/install-local-deb-linux.sh`. Preview the package metadata/checksum first and require an independently obtained expected SHA-256 for apply. Do not pipe community installers directly into a shell on work nodes.

## Phase 4: user-space tooling and agents

Log in as the agent account through the graphical workspace or controlled local setup session. Install version-managed runtimes, `gh`, `glab`, Codex, Claude Code, and Grok Build. Authentication is a separate human-approved phase.

## Phase 5: controls and validation

Run the profile `workloads` phase as a named administrator. It installs rootless Podman with Docker CLI compatibility, Chromium, Xvfb, and supporting packages without adding `agt-*` to the Docker group. Pin Playwright in each project and install that project's matching browser build. Install Grok Build from xAI's reviewed installer, then record its version.

Apply the balanced resource policy only after measuring the node. Run browser, container, build, multi-agent, reconnect, reboot, backup, and restore tests described in [validation and operations](08-validation-and-operations.md).
