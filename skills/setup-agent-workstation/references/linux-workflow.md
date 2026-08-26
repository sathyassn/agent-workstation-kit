# Ubuntu workflow

1. Direct the human through `docs/02-linux-setup.md` Phase 0.
2. Run `scripts/preflight.sh` and resolve unsupported conditions.
3. Preview `scripts/bootstrap-linux.sh` without sudo; after approval run it with `sudo ... --apply`. It installs but does not enable SSH.
4. Preview the profile's `identity` phase. If `/etc/hosts` maps the current hostname, have an administrator replace only that existing alias with the approved new hostname. Apply identity with console/KVM recovery open, `--confirm-recovery-tested`, and an explicit `local-console` or `tailscale-ssh` connection context, then verify the technical hostname, display name, local identity record, and local/loopback name resolution.
5. Preview `scripts/setup-accounts-linux.sh` with explicit repeated account flags; after approval apply it.
6. Provision named-user SSH keys. Install Tailscale from the vendor-supported package, authenticate/tag the node, and apply least-privilege grants from the console/KVM.
7. Preview `scripts/harden-remote-access-linux.sh`. After explicit approval and only with tested recovery open, apply it with `--confirm-recovery-tested` and an explicit `local-console` or `tailscale-ssh` context. For SSH, capture and pass the peer address before `sudo`; prove a second named-user SSH connection before closing recovery.
8. Provision the `agent-NN` local graphical password through a human-only prompt and vault it. Preview vendor `.deb` metadata/checksums with `scripts/install-local-deb-linux.sh`; install only with an independently verified SHA-256. Install licensed NoMachine, create the `agent-NN` physical desktop, authorize named trusted users, select the documented lock policy, and test identity, observer/controller, file transfer, and reconnect.
9. Preview and apply `scripts/install-agentctl-linux.sh`.
10. Install mise using its signed official installer or supported package. As `agent-NN`, preview and apply `scripts/install-user-tooling.sh --agents`; add `--gws` only after explicit approval.
11. Preview and apply `scripts/install-shell-baseline.sh` with a reviewed Antidote release tag.
12. Preview/apply the `workloads` phase to install rootless Podman/Docker compatibility, Chromium, Xvfb, and browser libraries. Pin Playwright and its browser per project.
13. Install VS Code, optional ChatGPT Linux preview, and Ghostty through reviewed packages supported by the Ubuntu release. Computer Use is not currently available in the Linux ChatGPT preview; validate UI work through Playwright/browser harnesses. Do not silently substitute community packages.
14. Perform approved credential ceremonies for model providers, GitHub, GitLab, optional Atlassian, and the chosen secrets manager. Provider administrators work from trusted administrative machines; never sign an administrator into the agent host.
15. Run observation first. Preview and apply `scripts/apply-resource-policy-linux.sh` only after reviewing calculated headroom.
16. Run privileged host validation, identity drift audit, reboot/KVM recovery, headed Playwright, four-session load, backup/restore, and burn-in.

Keep the bootstrap administrator until remote access and recovery pass. Then disable or retain it according to the approved break-glass policy; do not delete it automatically.
