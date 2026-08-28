# Accounts and access

[Previous: macOS setup](03-macos-setup.md) · [Documentation home](README.md) · [Next: tooling](05-tooling.md)

## Naming and privilege

| Identity | Example | Normal purpose | Privileged |
|---|---|---|---:|
| Named human | `alice` | Personal desktop, SSH, files, audit trail | No |
| Assigned administrator | `admin-01` → `alice` | OS changes and recovery only | Yes |
| Shared agent runtime | `agent-01` | Agents, browser tests, builds, shared desktop | No |
| Purpose service | `svc-backup` | One non-interactive service | Only narrowly scoped |

Human names use the organization's stable username/IdP handle. Resolve a real
collision centrally with a stable suffix such as `jsmith-02`; do not invent a
different algorithm per host. `admin-NN` is independent of a person's name, but
it is never a shared credential: the private fleet assignment maps each one to
exactly one named human.

Hostnames provide scope. `acme-ws-001/agent-01` and `acme-ws-002/agent-01` are
different principals, so local names remain short without fleet collisions.

## Shared work: two valid entry paths

```text
Named-user shell
alice@acme-ws-001
  |
  +-- agentctl shell acme-ws-001
        identity: agent-01 for this child shell
        return:   exit or Ctrl-D

  +-- agentctl attach acme-ws-001 project-a
        identity: agent-01 for this tmux client
        detach:   agentctl detach OR Ctrl-b d
        result:   session/processes continue; alice returns to own shell
```

`agentctl` is not a universal or permanent login switch. Every invocation is a
scoped delegated command. Authorization comes from `agent-01-operators` or
`agent-01-viewers`. Operators can intentionally request an interactive shell and
therefore arbitrary commands as `agent-01`; they do not receive root. Viewers
can invoke only a separate, root-owned read-only entry point. Actions are logged
with the initiating human.

```text
Shared graphical desktop
alice Mac                              bob Mac
   \                                    /
    +-- individually authenticated ----+
                    |
              NoMachine server
                    |
            desktop owned by agent-01
                    |
        Terminal / VS Code / browser / agent apps
        already execute as agent-01
```

This is the preferred GUI flow. No `agentctl shell` is needed inside that
desktop. Configure NoMachine so each person authenticates individually and is
authorized for the existing agent-owned physical desktop; never distribute the
agent password. Test the exact Enterprise Desktop release, concurrent control,
observer behavior, lock/reconnect, clipboard, and file-transfer policy before
production. If product behavior cannot meet that identity requirement, use
named-user desktops plus `agentctl` instead of sharing credentials.

Only one person should actively type in a shared graphical session at a time.
Use tmux/Herdr sessions and project ownership to avoid conflicting actions.

## `agentctl` commands

```text
agentctl list
agentctl status acme-ws-001
agentctl shell acme-ws-001
agentctl start acme-ws-001 project-a claude
agentctl attach acme-ws-001 project-a
agentctl observe acme-ws-001 project-a
agentctl detach
agentctl stop acme-ws-001 project-a
```

`detach` must run inside tmux; `Ctrl-b d` is equivalent. `stop` terminates the
session and requires confirmation. Observers attach read-only. Operators can
start, attach, and stop.

## macOS difference

Mac nodes retain the identity names, but GUI applications must run in the
owning `agent-01` graphical login. Do not use `sudo -u` to launch GUI apps across
macOS sessions. Validate Apple Screen Sharing and/or NoMachine on the actual OS,
including FileVault boot recovery and privacy permissions.
