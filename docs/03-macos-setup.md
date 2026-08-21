# macOS setup

Use this path for future Mac mini or Mac Studio nodes. Keep Xcode/Apple-specific work on the existing MacBook until a dedicated Mac node is added.

## Human-only bootstrap

1. Complete Setup Assistant with a temporary/bootstrap administrator.
2. Enable FileVault and escrow the recovery key through the approved process.
3. Install all macOS updates and Xcode Command Line Tools.
4. Configure power restoration, sleep behaviour, remote KVM if used, and a tested reboot/FileVault-unlock runbook.
5. Enroll corporate management or endpoint protection before development credentials are added.

## Accounts

Create separate named human, `adm-*`, and non-admin `agt-*` accounts. macOS GUI session sharing differs from Linux; validate Screen Sharing/NoMachine behaviour on the actual macOS release. Do not assume a Linux `sudo -u` graphical workflow will work on macOS.

## Scripted setup

Create a macOS [onboarding profile](01a-onboarding-profile.md). Run the `fleetctl` `base` phase first in preview mode and then with `--apply` as the intended Homebrew package owner, not root. The underlying script verifies prerequisites and installs the declared Homebrew baseline. The plan marks FileVault, account creation, remote-login, screen-sharing, privacy permissions, MDM, and resource policy as human/managed phases.

## Tooling

- Homebrew for system/user applications.
- Ghostty, VS Code, Herdr, tmux, Zsh tooling, `mise`, `gh`, and `glab`.
- OrbStack for a lightweight Docker-compatible environment; Docker Desktop is the fallback.
- Xcode and simulators selected for supported project targets.
- Google Chrome plus project-pinned Playwright browser builds.
- Codex and Claude Code through reviewed mise/npm entries; Grok Build through xAI's reviewed installer; all authenticated separately with approved identities.

macOS privacy prompts, system extensions, Xcode licenses, Keychain access, Screen Recording, Accessibility, and Developer Tools permissions require a named human at the console or approved MDM policy.
