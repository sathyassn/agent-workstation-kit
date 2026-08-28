# First Linux node pilot

[Documentation home](../README.md) · [Day-zero startup](day-zero-linux.md) · [MS-S1 Max hardware](../hardware/minisforum-ms-s1-max.md) · [Linux setup](../02-linux-setup.md)

Use this checklist alongside the [day-zero guide](day-zero-linux.md) for the
first 64 GB Linux mini PC. Read and complete **Before power-on** first; day zero
then controls execution, and this checklist records evidence at each checkpoint.
It is not a fleet template until the evidence below is complete.

## Contents

1. [Before power-on](#before-power-on)
2. [Install and establish recovery](#install-and-establish-recovery)
3. [Provision in reviewed phases](#provision-in-reviewed-phases)
4. [Acceptance and capacity baseline](#acceptance-and-capacity-baseline)
5. [Promotion decision](#promotion-decision)

## Before power-on

- [ ] Record the exact model, serial number, RAM, storage, NICs, BIOS version,
      warranty, and return deadline.
- [ ] Have a monitor, keyboard, fallback network, verified Ubuntu Desktop 24.04.4 LTS
      installer, and a second computer available.
- [ ] Keep a monitor and keyboard attached. Record KVM as `deferred` while the
      node is physically supervised; install/test it before unattended placement.
- [ ] Decide the disk-encryption and unattended-reboot recovery procedure.
- [ ] Decide the proposed technical hostname, fleet-unique assigned display
      name, asset tag and non-secret policy inputs. The day-zero setup-agent
      interview later creates the UUID/profile with `fleetctl init`, validates
      the whole fleet and commits the approved profile before staging.

## Install and establish recovery

- [ ] Update system firmware, enable virtualization/IOMMU as required, and keep
      memory or performance tuning at supported defaults for the first burn-in.
- [ ] Install Ubuntu Desktop 24.04.4 LTS with full-disk encryption where the recovery
      design supports it. Create only the bootstrap administrator initially.
- [ ] Apply OS and firmware updates, reboot twice, and check display, wired and
      wireless networking, audio, suspend policy, storage, and thermals.
- [ ] Validate the approved console path. If KVM is installed, also validate
      power, BIOS, boot, disk unlock, and input through Tailscale.
- [ ] Validate clean toolkit and private-fleet checkouts, then follow the
      [day-zero staging procedure](day-zero-linux.md). Privileged applies must
      use the root-owned toolkit and fleet snapshots under `/opt`; never apply
      user-writable code or profile input. Run `make check` and
      `./scripts/preflight.sh` before staging.
- [ ] On an MS-S1 Max, complete `docs/hardware/minisforum-ms-s1-max.md`, retain
      Secure Boot, and prove RTL8127 DKMS survives a kernel update.

## Provision in reviewed phases

- [ ] Run `fleetctl.py validate`, `validate --ready`, and `plan` against the
      completed local profile.
- [ ] Preview and apply one phase at a time in the order rendered by the plan.
- [ ] After `identity`, verify technical hostname, pretty/display name, UUID and
      asset tag against both the private profile and root-owned local manifest.
      On Linux, verify `hostnamectl --static`, `hostnamectl --transient`,
      `hostnamectl --pretty`, `hostname`, and `getent hosts <hostname>`. Resolve
      any reviewed `/etc/hosts` alias before proceeding, then reboot and repeat.
- [ ] Stop for every documented sudo, account, password, key, firewall,
      Tailscale, NoMachine, vendor-authentication, and recovery approval.
- [ ] Keep the KVM session open while hardening SSH. Do not close it until a
      second named-user SSH connection succeeds through Tailscale.
- [ ] Declare `local-console` when applying locally. If applying over
      Tailscale SSH, capture the peer before `sudo`, pass `tailscale-ssh` plus
      `--ssh-source-ip`, and confirm the command rejects a non-tailnet peer.
- [ ] From an SSH shell whose `sudo` policy clears `SSH_CONNECTION`, prove that
      identity and remote-hardening both reject a false `local-console` claim
      before mutation. Then use the attached console/KVM or verified
      `tailscale-ssh` path.
- [ ] Install the shared graphical agent account, `agentctl`, shells, runtimes,
      CLIs, rootless workloads, and remote desktop using the Linux guide.
- [ ] Have provider admins create GitHub/GitLab/Atlassian identities on trusted
      admin machines, then authenticate model, provider, and secret-store
      identities manually under `agent-NN`;
      never place their credentials in the profile or repository.

## Acceptance and capacity baseline

- [ ] Run the complete acceptance list in `docs/08-validation-and-operations.md`.
- [ ] Apply remote hardening twice and confirm the second run is a no-op. Keep
      KVM access until a second named-user SSH session succeeds through
      Tailscale.
- [ ] Verify Ubuntu's actual `ufw show added` output, deny-by-default input and
      routed-forward policy, the active `ssh.socket`/`ssh.service`, effective
      `sshd -T` settings, and drop-in precedence.
- [ ] Apply user tooling twice and confirm the mise configuration and lock data
      do not drift. Verify pinned Herdr `0.8.2`; record the separately approved
      Grok Build source and installed version.
- [ ] From the agent user's managed zsh, verify Ubuntu's snap-packaged Chromium,
      snap confinement/desktop access, Xvfb, project-owned Playwright browsers,
      and cgroup placement under both headed and headless load; verify rootless Podman,
      the Docker-compatible command path, and the agent user's subuid/subgid
      ranges.
- [ ] Verify read-only tmux observation and storage-encryption detection on the
      actual target filesystem.
- [ ] Confirm four concurrent representative agent sessions, their subagents,
      headed Playwright, builds, and containers while remote access remains
      responsive.
- [ ] Record idle and peak memory, swap, CPU load, pressure stall information,
      process/task counts, browser processes, disk growth, temperatures,
      throttling, and reconnect behavior.
- [ ] Observe for 24 hours, then continue a multi-day real-work burn-in.
- [ ] Do not apply final memory/task ceilings from guesswork. Use the captured
      peaks and retain enough headroom for the desktop, SSH, recovery, and short
      workload bursts.
- [ ] Reboot, reconnect, back up a disposable project, and restore it to a
      separate path.
- [ ] Exercise one canary-to-cohort rollout and rollback, then rerun the audit.

## Promotion decision

- [ ] Record all deviations, manual steps, exact package versions, failures,
      fixes, and unresolved hardware or driver issues.
- [ ] Decide whether 64 GB supports the measured workload without sustained
      pressure. If not, reduce per-node concurrency or select 128 GB for the
      affected workload class.
- [ ] Open a reviewed repository change for reusable discoveries; do not edit
      later nodes manually from memory.
- [ ] Promote only after the audit passes and the pilot evidence has independent
      review.
