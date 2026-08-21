# Approval boundaries

## May run without additional approval

- Read OS, hardware, package, account, service, network-listener, and resource status.
- Read this repository and compare it with the host.
- Run repository scripts in preview mode.
- Run non-secret version and authentication-status commands.
- Produce plans, reports, diffs, and validation output.

## Explain and wait for approval

- Any command using `sudo` or administrator authentication.
- Package installation or removal.
- Account, group, shell, password, SSH-key, login, or sudoers changes.
- Firewall, SSH, Tailscale, NoMachine, Screen Sharing, KVM, DNS, proxy, VPN, or certificate changes.
- Encryption, boot, firmware, recovery, MDM, endpoint, Keychain, or privacy changes.
- Creating vendor accounts, API keys, tokens, Apps, OAuth clients, repository memberships, or branch-policy changes.
- Enabling unattended execution, broad auto-approval, or scheduled cleanup.
- Restarting a machine or service used by another person.

## Never do implicitly

- Paste, print, log, commit, or transmit a secret.
- Add `agt-*` to unrestricted sudo or Docker group.
- Expose SSH/RDP/KVM directly to the public internet.
- Delete projects, branches, containers, volumes, caches, logs, accounts, or credentials merely to make space.
- Disable encryption, endpoint protection, branch protection, audit logging, sandboxing, or approval controls.
- Approve or merge the agent identity's own PR/MR.
- Continue after a failed security or recovery validation.

## Credential ceremony

Tell the human which provider page or local command to use, required scopes, expiry, owner, storage target, and read-only validation. Let the human complete browser, password-manager, hardware-key, or local hidden-input steps directly. Resume only from status output that contains no credential value.
