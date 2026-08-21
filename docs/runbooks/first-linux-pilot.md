# First Linux node pilot

Use this runbook for the first 64 GB Linux mini PC. It is a controlled pilot,
not a fleet template until the evidence below is complete.

## Before installation

- [ ] Record the exact model, serial number, RAM, storage, NICs, BIOS version,
      warranty, and return deadline.
- [ ] Have a monitor, keyboard, wired network, verified Ubuntu Desktop LTS
      installer, and a second computer available.
- [ ] Connect the remote KVM, change its default credentials, update its
      firmware, restrict its network path, and verify BIOS-level access.
- [ ] Decide the disk-encryption and unattended-reboot recovery procedure.
- [ ] Copy `personal.example.toml` or `work.example.toml` to an ignored
      `config/profiles/<machine>.local.toml`; fill in real, non-secret decisions.

## Install and establish recovery

- [ ] Update system firmware, enable virtualization/IOMMU as required, and keep
      memory or performance tuning at supported defaults for the first burn-in.
- [ ] Install Ubuntu Desktop LTS with full-disk encryption where the recovery
      design supports it. Create only the bootstrap administrator initially.
- [ ] Apply OS and firmware updates, reboot twice, and check display, wired and
      wireless networking, audio, suspend policy, storage, and thermals.
- [ ] Validate remote KVM power, BIOS, boot, disk unlock, and console input.
- [ ] Copy this repository to the node and run `make check` and
      `./scripts/preflight.sh` before any apply operation.

## Provision in reviewed phases

- [ ] Run `fleetctl.py validate`, `validate --ready`, and `plan` against the
      completed local profile.
- [ ] Preview and apply one phase at a time in the order rendered by the plan.
- [ ] Stop for every documented sudo, account, password, key, firewall,
      Tailscale, NoMachine, vendor-authentication, and recovery approval.
- [ ] Keep the KVM session open while hardening SSH. Do not close it until a
      second named-user SSH connection succeeds through Tailscale.
- [ ] Install the shared graphical agent account, `agentctl`, shells, runtimes,
      CLIs, rootless workloads, and remote desktop using the Linux guide.
- [ ] Authenticate model, source-control, and secret-store identities manually;
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
      do not drift. Record the approved Herdr and Grok Build sources and
      installed versions.
- [ ] From the agent user's managed zsh, verify Chromium, Xvfb, rootless Podman,
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
