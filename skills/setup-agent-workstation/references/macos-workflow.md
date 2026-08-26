# macOS workflow

1. Direct the human through `docs/03-macos-setup.md` human-only bootstrap.
2. Run `scripts/preflight.sh` and record macOS, chip, memory, FileVault, disk, Xcode tools, and management state.
3. Require approved Homebrew installation and Xcode Command Line Tools.
4. Preview `scripts/bootstrap-macos.sh`; after approval run with `--apply` as the intended package-owning user, not root.
5. Preview and apply the profile's privileged `identity` phase with console/KVM recovery open, `--confirm-recovery-tested`, and an explicit `local-console` or `tailscale-ssh` connection context. Verify HostName, LocalHostName, ComputerName, and the root-owned local identity record.
6. Have a human create named, assigned `admin-NN`, and non-admin `agent-NN` accounts through approved macOS/MDM procedures.
7. Configure the recommended Tailscale macOS variant, Screen Sharing/NoMachine, SSH, firewall, sleep, restart-after-power-failure, and KVM with a tested recovery path.
8. Install tooling under `agent-NN`; authenticate model, GitHub, GitLab, optional Atlassian, and vault identities through Keychain or the approved secret broker. Provider administrators create identities from trusted administrative machines, not the agent host.
9. Have a human approve Xcode license, signing, simulators, Screen Recording, Accessibility, Automation, Developer Tools, browser, and application privacy prompts.
10. Validate OrbStack's Docker-compatible workflow; use Docker Desktop only when needed.
11. Validate identity drift, reboot/FileVault unlock, remote graphical access, SSH, builds, simulators, agents, browser tests, backup/restore, and burn-in.

Do not attempt to launch graphical applications across macOS user sessions with `sudo -u`. Operate applications inside the owning `agent-NN` graphical session.
