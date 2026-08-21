# Security and resource controls

## Security baseline

- Full-disk encryption and tested recovery.
- Firmware updates and secure boot where supported.
- Named users, separate administrators, non-admin agent account.
- SSH keys, Tailscale ACLs/device approval, host firewall, and no public remote-desktop port.
- No direct root login and no password-based SSH after key access is validated.
- Scoped workload identities with rotation and revocation owners.
- Automatic security updates with scheduled reboot coordination.
- Backups with periodic restore tests.
- Central or retained logs for access, `agentctl`, agents, and resource events.
- Browser and project separation for work and personal profiles.

Do not add the shared agent account to unrestricted `sudo` or the Docker group. Docker group membership is effectively root access. Prefer rootless containers for the shared account or a narrowly managed container service.

On Ubuntu, `harden-remote-access-linux.sh` is the enforceable baseline: OpenSSH is limited to the profile's complete named-user allowlist, direct SSH to `agt-*` is denied, root/password/keyboard-interactive SSH is disabled, UFW defaults to deny inbound, and SSH/NoMachine are allowed only on `tailscale0`. Apply it only after named-user keys, Tailscale, and console/KVM recovery have been tested. Existing broader firewall rules cause the script to stop for human review.

## Resource policy

Start with observation. Measure idle OS use and realistic peak workloads before enforcing ceilings.

Use systemd/cgroup controls in this order:

1. `CPUWeight` and `IOWeight` to preserve interactivity under contention.
2. `MemoryHigh` as a soft pressure threshold.
3. `MemoryMax` only as an emergency ceiling with measured headroom.
4. A measured process/task ceiling to catch browser or subagent explosions without constraining normal concurrency.
5. Disk free-space thresholds and bounded caches/logs.

Do not statically reserve large amounts of RAM or cores for the OS. The balanced policy should keep the desktop, SSH, monitoring, and recovery responsive while leaving most resources available to agents.

The supplied systemd policy governs the agent user's `user-UID.slice`; validate after reboot that the desktop, terminals, browsers, and tests descend from that slice. `CPUWeight=90` is a contention preference, not a core reservation. `TasksMax` scales from 4,096 to 16,384 with detected RAM (roughly 8,000 on a 64 GB-class node), so it is an emergency process ceiling rather than a normal workload throttle. Replace calculated values with burn-in evidence. Preview rollback with `apply-resource-policy-linux.sh --agent ACCOUNT --remove`; apply it only with human approval, then end all target-user sessions or reboot.

## Human approval boundaries

Always pause before:

- `sudo` or administrator authentication.
- Account, group, login, firewall, SSH, VPN, RDP, KVM, encryption, or boot changes.
- Installing kernel modules, system extensions, MDM profiles, or endpoint software.
- Creating or granting vendor identities, tokens, OAuth scopes, or repository roles.
- Deleting data, pruning containers, removing caches outside declared paths, or rotating credentials.
- Enabling unattended execution or broad agent auto-approval.
