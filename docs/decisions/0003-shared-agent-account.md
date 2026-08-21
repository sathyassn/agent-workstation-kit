# Decision: named humans plus a shared agent workspace

- **Status:** accepted
- **Decision:** Humans authenticate as themselves, administrators use separate `adm-*` accounts, and all shared agent/desktop processes run as a non-admin `agt-*` account.
- **GUI:** named NoMachine users share the physical desktop owned by `agt-*`.
- **CLI:** named SSH users enter or attach through `agentctl`.
- **Service accounts:** reserve `svc-*` for true background services, not interactive agent workspaces.
- **Tradeoff:** entry is attributable to a human, but ordinary actions after entering the shared UID are not inherently attributable per keystroke/click. Retain connection, session, agent, and source-control logs.
