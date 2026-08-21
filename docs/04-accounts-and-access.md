# Accounts and access

## Account convention

| Type | Pattern | Daily use | Sudo |
|---|---|---:|---:|
| Human | organization username, such as `alice` | Yes | No |
| Administrator | `adm-<human>` | No | Yes |
| Shared agent | `agt-ai-NN` | Through desktop or `agentctl` | No |
| System service | `svc-<purpose>` | No | No |

Do not reuse email addresses as Unix usernames. Map corporate identities to short stable names in deployment records.

## Desktop path

The graphical session must already be owned by `agt-ai-NN`. Named humans authenticate to NoMachine as themselves and are authorized as trusted users for that desktop. A terminal opened there is already an agent-account shell.

Test GNOME locking, NoMachine disconnect locking, screen blanking, unattended reconnect, and KVM recovery together. Do not distribute the agent password simply to bypass a lock screen.

The shared graphical account needs a usable local password even though direct SSH is denied. Generate it through an interactive OS prompt, store it in 1Password/Bitwarden with audited access, and never pass it to a setup script or agent chat.

Choose one explicit desktop policy during the pilot:

1. **Dedicated shared desktop (recommended for this design):** after encrypted-disk unlock, start the `agt-*` desktop using the approved GDM/console procedure; disable automatic screen locking only for this dedicated account; rely on physical security, full-disk encryption, Tailscale policy, NoMachine named-user authorization, and KVM recovery. The password remains escrowed for exceptional unlock/recovery.
2. **Locked desktop:** retain automatic locking. Authorized operators must retrieve the shared desktop password from the vault to unlock GNOME, so secret-access logs become part of the audit trail.

Do not enable automatic login or disable locking for a general human account, portable machine, publicly reachable host, or node that does not meet the surrounding controls.

## Terminal path

`agentctl` is a repository-provided broker. It logs the human entry point and switches only the selected terminal process tree.

```text
agentctl status ai-node-01
agentctl shell ai-node-01
agentctl start ai-node-01 project-a claude
agentctl attach ai-node-01 project-a
agentctl detach
```

`agentctl detach` is run from an attached tmux session and detaches the current client. The normal tmux shortcut, `Ctrl-b d`, remains available. Detach does not stop agents.

`agentctl stop` asks the operator to type the session name before terminating it. `--yes` is reserved for separately approved non-interactive maintenance.

## Audit limitations

The OS records the named person entering through SSH or NoMachine. Once inside the shared desktop or shell, ordinary processes use the shared agent UID. `AGENT_OPERATOR`, shared shell history, and local syslog improve traceability but are not tamper-proof identity controls. Use retained/central logs, agent-native logs, branch attribution, vault-access logs, and a one-controller-at-a-time policy. Fine-grained attribution of every GUI click is not guaranteed; use separate human desktops instead if that is a compliance requirement.
