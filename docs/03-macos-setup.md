# macOS setup

Use this path for future Mac mini or Mac Studio nodes. Keep Xcode/Apple-specific work on the existing MacBook until a dedicated Mac node is added.

## Human-only bootstrap

1. Complete Setup Assistant with a temporary/bootstrap administrator.
2. Enable FileVault and escrow the recovery key through the approved process.
3. Install all macOS updates and Xcode Command Line Tools.
4. Configure power restoration, sleep behaviour, remote KVM if used, and a tested reboot/FileVault-unlock runbook.
5. Enroll corporate management or endpoint protection before development credentials are added.

## Accounts

Create separate named human, assigned `admin-NN`, and non-admin `agent-NN` accounts. macOS GUI session sharing differs from Linux; validate Screen Sharing/NoMachine behaviour on the actual macOS release. Do not assume a Linux `sudo -u` graphical workflow will work on macOS.

## Scripted setup

Create a macOS [onboarding profile](01a-onboarding-profile.md). Run the `fleetctl` `base` phase first in preview mode and then with `--apply` as the intended Homebrew package owner, not root. The underlying script verifies prerequisites and installs the declared Homebrew baseline. Next, preview the privileged `identity` phase and apply it with `--confirm-recovery-tested --connection-context local-console` (or the documented `tailscale-ssh` context and peer address); it sets HostName/LocalHostName, the friendly Computer Name, and a root-owned identity record under `/Library/Application Support/Agent Workstation Kit`. The audit checks all three macOS names. The plan marks FileVault, account creation, remote-login, screen-sharing, privacy permissions, MDM, and resource policy as human/managed phases.

The identity installer checks process ancestry as well as SSH environment
variables before accepting `local-console`, and fails closed when it cannot
inspect that ancestry. This prevents a normal `sudo` environment reset from
disguising an SSH session. Treat the macOS path as unverified until it is
exercised on the future Mac mini/Studio pilot in the release checklist.

## Tooling

- Homebrew for system/user applications.
- Ghostty, VS Code, Herdr, tmux, Zsh tooling, `mise`, `gh`, and `glab`.
- OrbStack for a lightweight Docker-compatible environment; Docker Desktop is the fallback.
- Xcode and simulators selected for supported project targets.
- Google Chrome plus project-pinned Playwright browser builds.
- Codex and Claude Code through reviewed mise/npm entries; Grok Build through xAI's reviewed installer; all authenticated separately with approved identities.

macOS privacy prompts, system extensions, Xcode licenses, Keychain access, Screen Recording, Accessibility, and Developer Tools permissions require a named human at the console or approved MDM policy.

Provider identities are created by their organization administrators on trusted
administrative machines. Follow the [provider identity ceremony](06-agent-and-source-control-identities.md)
before authenticating anything under the shared `agent-NN` account.
