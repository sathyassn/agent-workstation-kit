# Ubuntu workflow

1. Direct the human through `docs/02-linux-setup.md` Phase 0.
2. Run `scripts/preflight.sh` and resolve unsupported conditions.
3. Preview `scripts/bootstrap-linux.sh` without sudo; after approval run it with `sudo ... --apply`. It installs but does not enable SSH.
4. Preview `scripts/setup-accounts-linux.sh` with explicit repeated account flags; after approval apply it.
5. Provision named-user SSH keys. Install Tailscale from the vendor-supported package, authenticate/tag the node, and apply least-privilege grants from the console/KVM.
6. Preview `scripts/harden-remote-access-linux.sh`. After explicit approval and only with tested recovery open, apply it with `--confirm-recovery-tested` and an explicit `local-console` or `tailscale-ssh` context. For SSH, capture and pass the peer address before `sudo`; prove a second named-user SSH connection before closing recovery.
7. Provision the `agent-NN` local graphical password through a human-only prompt and vault it. Preview vendor `.deb` metadata/checksums with `scripts/install-local-deb-linux.sh`; install only with an independently verified SHA-256. Install licensed NoMachine, create the `agent-NN` physical desktop, authorize named trusted users, select the documented lock policy, and test identity, observer/controller, file transfer, and reconnect.
8. Preview and apply `scripts/install-agentctl-linux.sh`.
9. Install mise using its signed official installer or supported package. As `agent-NN`, preview and apply `scripts/install-user-tooling.sh --agents`; add `--gws` only after explicit approval.
10. Preview and apply `scripts/install-shell-baseline.sh` with a reviewed Antidote release tag.
11. Preview/apply the `workloads` phase to install rootless Podman/Docker compatibility, Chromium, Xvfb, and browser libraries. Pin Playwright and its browser per project.
12. Install VS Code, optional ChatGPT Linux preview, and Ghostty through reviewed packages supported by the Ubuntu release. Computer Use is not currently available in the Linux ChatGPT preview; validate UI work through Playwright/browser harnesses. Do not silently substitute community packages.
13. Perform local credential ceremonies for model providers, GitLab/GitHub, and the chosen secrets manager.
14. Run observation first. Preview and apply `scripts/apply-resource-policy-linux.sh` only after reviewing calculated headroom.
15. Run privileged host validation, reboot/KVM recovery, headed Playwright, four-session load, backup/restore, and burn-in.

Keep the bootstrap administrator until remote access and recovery pass. Then disable or retain it according to the approved break-glass policy; do not delete it automatically.
