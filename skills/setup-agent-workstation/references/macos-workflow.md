# macOS workflow

1. Direct the human through `docs/runbooks/day-zero-macos.md`; do not accept
   handoff before `scripts/start-macos-pilot.py` reports no `FAIL`.
2. Continue with `docs/03-macos-setup.md`. Run `scripts/preflight.sh` and record
   macOS, chip, memory, FileVault, disk, Xcode tools, and management state.
3. Require approved Homebrew installation and Xcode Command Line Tools.
   Use the documented Homebrew Python 3.13 binary for toolkit controllers;
   Apple's Command Line Tools Python may be too old for `tomllib`.
4. Preview and apply the profile's `base` phase through `fleetctl` as documented;
   `scripts/bootstrap-macos.sh` is its implementation. Run it as the intended
   package-owning user, not root.
5. Before the privileged `identity` phase, follow
   `docs/runbooks/stage-approved-macos-snapshots.md`. Preview from the ordinary
   checkout, then have the human apply only the matching root-owned snapshots
   with console/KVM recovery open, `--confirm-recovery-tested`, and an explicit
   `local-console` or `tailscale-ssh` connection context. Verify HostName,
   LocalHostName, ComputerName, and the root-owned local identity record.
6. Have a human create named, assigned `admin-NN`, and non-admin `agent-NN` accounts through approved macOS/MDM procedures.
7. Configure the recommended Tailscale macOS variant, Screen Sharing/NoMachine, SSH, firewall, sleep, restart-after-power-failure, and KVM with a tested recovery path.
8. Install tooling under `agent-NN`; authenticate model, GitHub, GitLab, optional Atlassian, and vault identities through Keychain or the approved secret broker. Provider administrators create identities from trusted administrative machines, not the agent host.
9. Have a human approve Xcode license, signing, simulators, Screen Recording, Accessibility, Automation, Developer Tools, browser, and application privacy prompts.
10. Validate OrbStack's Docker-compatible workflow; use Docker Desktop only when needed.
11. Validate identity drift, reboot/FileVault unlock, remote graphical access, SSH, builds, simulators, agents, browser tests, backup/restore, and burn-in.

Do not attempt to launch graphical applications across macOS user sessions with `sudo -u`. Operate applications inside the owning `agent-NN` graphical session.

For every staged privileged phase, invoke the compatible staged installer
through a clean `sudo /usr/bin/env -i` environment with the fixed system `PATH`
shown in the staging runbook. Never execute an env-based shebang with a
human-writable `PATH`.
