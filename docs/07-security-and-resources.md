# Security and resource controls

[Previous: provider identities](06-agent-and-source-control-identities.md) · [Documentation home](README.md) · [Next: remote access](08-network-remote-access-and-files.md)

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

On Ubuntu, `harden-remote-access-linux.sh` is the enforceable baseline: OpenSSH is limited to the profile's complete named-user allowlist, direct SSH to `agent-NN` is denied, root/password/keyboard-interactive SSH is disabled, UFW defaults to deny inbound, and SSH/NoMachine are allowed only on `tailscale0`. Apply it only after named-user keys, Tailscale, and console recovery have been tested. Apply also requires `local-console` or a peer address captured before `sudo` under `tailscale-ssh`; the latter is verified by `tailscale whois`. Existing broader firewall rules cause the script to stop for human review.

For `local-console`, the privileged scripts also walk the process ancestry and
stop on SSH, Tailscale SSH, or Mosh ancestors. They fail closed when ancestry
cannot be inspected. This check is independent of `SSH_CONNECTION`, which
`sudo` may remove.

## Resource policy

Start with observation. Measure idle OS use and realistic peak workloads before enforcing ceilings.

Use systemd/cgroup controls in this order:

1. `CPUWeight` and `IOWeight` to preserve interactivity under contention.
2. `MemoryHigh` as a soft pressure threshold.
3. `MemoryMax` only as an emergency ceiling with measured headroom.
4. A measured process/task ceiling to catch browser or subagent explosions without constraining normal concurrency.
5. Disk free-space thresholds and bounded caches/logs.

Do not statically reserve large amounts of RAM or cores for the OS. The balanced policy should keep the desktop, SSH, monitoring, and recovery responsive while leaving most resources available to agents.

### Agent work ceiling on Linux

`apply-resource-policy-linux.sh` gives agent work one machine-wide memory
ceiling. It previews by default; `--apply` changes the machine and needs root.

```text
-.slice
`-- user.slice
    `-- user-UID.slice              agent account (not limited as a whole)
        |-- session-N.scope         the agent's login session
        `-- user@UID.service        the agent's user manager
            |-- app.slice           desktop apps (not limited)
            |-- session.slice       desktop shell (not limited)
            |-- orca_ptys_*.slice   agent-manager ptys (left by agent-work-shell)
            `-- agent.slice         implied parent (see slice naming below)
                `-- agent-work.slice  MemoryHigh, MemoryMax, MemorySwapMax, oomd
                    |-- agent services   (Slice=agent-work.slice)
                    |-- agentctl tmux sessions
                    `-- agent-manager terminals (through agent-work-shell)
```

**Where the ceiling applies, and why.** The policy is a user-level slice,
`/etc/systemd/user/agent-work.slice`, under the agent account's user manager.
Earlier versions limited the whole `user-UID.slice`. That slice also holds the
agent account's desktop, so a ceiling there throttled the desktop with the
jobs, and it missed `agentctl` sessions completely: `sudo` keeps a delegated
process in the operator's login session, outside the agent's slice. The choice
rests on systemd's documented behaviour:

- `user@.service` delegates the `memory`, `pids` and `cpu` controllers to the
  user manager, so a user-level slice can carry `MemoryHigh`, `MemoryMax`,
  `MemorySwapMax` and `TasksMax`. The script refuses when the memory controller
  is not delegated.
- Units in `/etc/systemd/user` are root-owned and load in every user manager;
  `ConditionUser=` keeps the slice to the agent account.
- systemd-oomd acts on user units (systemd 248 and newer), so the slice can
  carry `ManagedOOMMemoryPressure=kill` with its own pressure limit.
- A system-level slice would need root to place each process in it, while
  `agentctl` runs as the agent account. The user manager can place work itself.

**Limit of this design.** The agent account's own systemd configuration
(`~/.config/systemd/user`, or `systemctl --user set-property`) outranks
`/etc/systemd/user`, and work started outside the slice is not limited.
`validate-host.sh` and the phase verifier fail when an override for
`agent-work.slice` exists. Start agent services with `Slice=agent-work.slice`.

**Placement check.** `validate-host.sh` and the phase verifier fail when a
process of the agent account runs outside `agent-work.slice` and is a `claude`,
`codex` or `grok` process (by name or by its first two arguments, so a
Node-launched CLI counts), or any process in an agent manager's pty slice
(`orca_ptys_*.slice`). Desktop apps outside the slice are not flagged.

### Agent-manager terminals

An agent manager such as Orca starts each terminal in its own pty slice,
`orca_ptys_<daemon scope>.slice`, directly under the user manager and outside
`agent-work.slice`. Run its terminals through the wrapper
`scripts/agent-work-shell`; staged, that is
`/opt/agent-workstation-kit/scripts/agent-work-shell`. The wrapper runs
`systemd-run --user --scope --slice=agent-work.slice` around the account's
login shell (`$SHELL -l`), so the terminal and every agent started in it move
under the ceiling. It runs the shell directly when it is already inside the
slice or the policy is not installed.

Set it as the agent manager's terminal shell, in the agent account:

- Orca: set the default terminal shell to
  `/opt/agent-workstation-kit/scripts/agent-work-shell`. Orca stores this as
  the `terminalDefaultShell` setting; that key was read from the Orca
  application's own code, and the label of the field in Orca's settings screen
  was not confirmed. Check it on the host, then open a new terminal and confirm
  with `cat /proc/self/cgroup` that the path contains `/agent-work.slice/`.
- Another agent manager: set its terminal shell or shell command to the same
  path, then make the same check.

**Slice naming.** systemd places a slice in the tree by its name: each dash
marks a level, so `foo-bar.slice` is a child of `foo.slice`
([systemd.slice](https://www.freedesktop.org/software/systemd/man/latest/systemd.slice.html)).
That is why `agent-work.slice` sits under an implied `agent.slice`, and why a
slice named `agent-work-NAME.slice` would be a child of `agent-work.slice` and
held by its ceiling. An agent manager that lets you name its pty slices can
use that form instead of the wrapper. Orca cannot: its slice name is fixed in
code as `orca_ptys_` plus its daemon scope name, with every other character
replaced by `_`, so the wrapper is the supported route.

**What apply changes.** Each item is shown in the preview first.

| Change | Setting | Default | When it takes effect |
|---|---|---|---|
| Memory ceiling | `MemoryHigh` and `MemoryMax` as shares of total RAM read at run time | 70% and 80% | New and restarted work in the slice |
| Swap for agent work | `MemorySwapMax` as a share of RAM | 10% | Same |
| Headroom check | Refuse when the RAM outside `MemoryMax` is below the reserve share (`resources.os_memory_reserve_percent`) | 10% | Preview |
| systemd-oomd | Enabled; kills in the slice above the pressure limit (states `enabled`, `enabled-runtime`, `static` and `alias` count as enabled) | 60% | At once |
| oomd scope | Overrides each vendor oomd drop-in for `user@.service` and `-.slice` with `ManagedOOM...=auto`, so oomd acts only on `agent-work.slice` | | At once |
| Swappiness | `vm.swappiness`: 100 with zram, 10 with disk swap only, unchanged with no swap | auto | At once |
| zram | `/etc/systemd/zram-generator.conf` at a share of RAM; needs `systemd-zram-generator` | off (0) | Next boot |
| `/tmp` | Masks the packaged `tmp.mount` when it makes `/tmp` a RAM-backed tmpfs | auto | Next boot |
| Old policy | Removes the earlier `user-UID.slice` drop-in | | At once |
| Lingering | `loginctl enable-linger` so the user manager runs at boot | | At once |

Shares are flags with these defaults: `--memory-high-percent`,
`--memory-max-percent`, `--swap-max-percent`, `--memory-reserve-percent`,
`--oomd-pressure-percent`,
`--swappiness auto|keep|N`, `--zram-percent N` and `--tmp auto|disk|keep`.
The `resources` phase of a plan uses the defaults and the profile's
`os_memory_reserve_percent` (default 10). The older `os_memory_reserve_gib`
field and `--memory-reserve-gib` flag are deprecated: profiles that still carry
the field validate, but the policy no longer reads it, and the script refuses
the old flag. `CPUWeight=90` is a contention preference, not a core
reservation, `os_cpu_reserve_threads` is capacity-planning headroom, not CPU
pinning, and `TasksMax` scales from 4,096 to 16,384 with RAM.

**oomd scope.** Ubuntu's `systemd-oomd` package can ship vendor drop-ins such
as `user@.service.d/10-oomd-user-service-defaults.conf` and
`-.slice.d/10-oomd-root-slice-defaults.conf`, which let oomd kill across every
user manager, desktop included, or across the whole machine on swap use. The
preview prints the current `ManagedOOM` lines from `systemctl cat` for the
agent's user manager and `-.slice`, and the first lines of `oomctl dump`. Apply
writes a drop-in with the same name in `/etc/systemd/system/`, which replaces
the vendor file, setting `ManagedOOMMemoryPressure=auto` and
`ManagedOOMSwap=auto`. Each override is in the receipt and reverted like every
other change. This also turns those defaults off for human accounts' user
managers.

When systemd-oomd is not installed, the slice carries no `ManagedOOM` keys:
`MemoryHigh` throttles agent work under pressure, and at `MemoryMax` the
kernel OOM killer acts inside the slice only, so the desktop is spared.

The script prefers a disk-backed `/tmp` over redirecting caches: a RAM-backed
`/tmp` holds pages that can only go to swap, which is what refills swap under
load. It does not edit `/etc/fstab`; a tmpfs `/tmp` from fstab is left alone
with a warning, and `--tmp disk` refuses it.

The script refuses, with a message, when systemd is not the init system, is
older than 248, the unified cgroup v2 memory controller is missing, the user
manager does not delegate memory, `--zram-percent` is set without
`systemd-zram-generator`, or the shares break the reserve.

**Receipt and exact undo.** Apply copies each file it replaces or removes into
`/var/lib/agent-workstation-kit/resource-policy/backups/` before changing it,
and records every change (files, directories, the tmp.mount mask, the earlier
swappiness, lingering and the oomd service) in `receipt.tsv` there. A repeat
apply with the same policy changes nothing; a different policy is refused
until you revert.

```bash
# Preview, then apply after review (root, from the staged copy)
sudo /bin/bash /opt/agent-workstation-kit/scripts/apply-resource-policy-linux.sh --agent ACCOUNT
sudo /bin/bash /opt/agent-workstation-kit/scripts/apply-resource-policy-linux.sh --agent ACCOUNT --apply

# Preview the undo, then restore exactly what the receipt lists
sudo /bin/bash /opt/agent-workstation-kit/scripts/apply-resource-policy-linux.sh --agent ACCOUNT --revert
sudo /bin/bash /opt/agent-workstation-kit/scripts/apply-resource-policy-linux.sh --agent ACCOUNT --revert --apply
```

Apply records each step just before making it. If apply stops part way, it
says so and names the revert command; revert accepts a step that was recorded
but not made (a missing mask link, a file not yet written or not yet removed),
so an interrupted apply never blocks the undo. Revert refuses, and changes
nothing, when a managed file changed after apply.
`--remove` is an alias of `--revert`. Disabling lingering stops the agent's
user manager when the agent has no login session.

After the first apply, stop all `agentctl` sessions once so the tmux server
restarts inside the slice; `agentctl start` refuses while the server runs
outside it. Replace the defaults with burn-in evidence, and check placement
under load with `systemd-cgls` and `systemctl --user -M ACCOUNT@ status agent-work.slice`.

Rollback is phase-specific. The resource phase has an exact `--revert` from
its receipt; the remote-hardening script restores its prior SSH/UFW state
automatically if an apply fails. Account/package removal is intentionally not
automated because it can destroy homes, credentials, or project state. The
pilot rollback exercise must therefore use the documented recovery console,
reviewed package rollback, and backup/restore plan—not a generic destructive
uninstall command.

## Human approval boundaries

Always pause before:

- `sudo` or administrator authentication.
- Account, group, login, firewall, SSH, VPN, remote-desktop, KVM, encryption, or boot changes.
- Installing kernel modules, system extensions, MDM profiles, or endpoint software.
- Creating or granting vendor identities, tokens, OAuth scopes, or repository roles.
- Deleting data, pruning containers, removing caches outside declared paths, or rotating credentials.
- Enabling unattended execution or broad agent auto-approval.
