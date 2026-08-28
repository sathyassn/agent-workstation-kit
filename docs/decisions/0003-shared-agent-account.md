# Decision: named humans plus a shared agent workspace

[Previous decision](0002-network-and-remote-access.md) · [Documentation home](../README.md) · [Next decision](0004-reproducibility-and-backup.md)

- **Status:** accepted
- **Decision:** Humans authenticate as themselves, assigned `admin-NN` accounts are privileged only for administration, and shared agent/desktop processes run as non-admin `agent-NN`.
- **GUI:** named NoMachine users share the physical desktop owned by `agent-NN` without sharing its password; use named desktops plus `agentctl` if product validation cannot preserve individual authentication.
- **CLI:** named SSH users enter or attach through `agentctl`.
- **Service accounts:** reserve `svc-*` for true background services, not interactive agent workspaces.
- **Tradeoff:** entry is attributable to a human, but ordinary actions after entering the shared UID are not inherently attributable per keystroke/click. Retain connection, session, agent, and source-control logs.
