# macOS workflow

1. Direct the human through `docs/03-macos-setup.md` human-only bootstrap.
2. Run `scripts/preflight.sh` and record macOS, chip, memory, FileVault, disk, Xcode tools, and management state.
3. Require approved Homebrew installation and Xcode Command Line Tools.
4. Preview `scripts/bootstrap-macos.sh`; after approval run with `--apply` as the intended package-owning user, not root.
5. Have a human create named, separate admin, and non-admin `agt-*` accounts through approved macOS/MDM procedures.
6. Configure the recommended Tailscale macOS variant, Screen Sharing/NoMachine, SSH, firewall, sleep, restart-after-power-failure, and KVM with a tested recovery path.
7. Install tooling under `agt-*`; authenticate model/source-control identities through Keychain or the chosen secrets manager.
8. Have a human approve Xcode license, signing, simulators, Screen Recording, Accessibility, Automation, Developer Tools, browser, and application privacy prompts.
9. Validate OrbStack's Docker-compatible workflow; use Docker Desktop only when needed.
10. Validate reboot/FileVault unlock, remote graphical access, SSH, builds, simulators, agents, browser tests, backup/restore, and burn-in.

Do not attempt to launch graphical applications across macOS user sessions with `sudo -u`. Operate applications inside the owning `agt-*` graphical session.
