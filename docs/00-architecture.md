# Architecture and operating model

[Documentation home](README.md) · [Next: selected stack](00a-final-stack.md)

## Goals

- Move sustained agents, browsers, builds, and containers off operator laptops.
- Let named people supervise a shared non-admin agent workspace.
- Keep human, administrator, OS runtime, model-provider, and source-control
  identities separate and auditable.
- Support a mixed Linux/macOS fleet and add higher-memory nodes by measured need.
- Preserve a recovery route outside the normal OS session.

## Identity layers

```text
Person / IdP identity                  alice
       |
       +-- daily Linux account         alice             (no sudo)
       +-- assigned admin account      admin-01         (sudo; admin work only)
       +-- agentctl authorization      operator/viewer  (group membership)
                    |
Shared OS runtime   agent-01                            (no sudo; no direct SSH)
       |
       +-- model workload identity     OpenAI / Anthropic / xAI
       +-- source-control identity     GitLab service account / GitHub App
```

The complete principal is `hostname/account`, for example
`acme-ws-001/agent-01`. Account names can therefore repeat safely on other hosts.
Never copy a human's cached credentials into the shared agent home.

## Access paths

```text
Flow A — named-user shell and terminal delegation

macOS/Windows/Linux operator -- Tailscale/SSH -- alice
                                                   |
                                                   +-- agentctl shell acme-ws-001
                                                       child zsh as agent-01
                                                       `exit` returns to alice

Flow B — shared graphical agent desktop

macOS/Windows/Linux/mobile -- Tailscale -- NoMachine
                                              |
                                              +-- desktop owned by agent-01
                                              +-- Terminal is already agent-01
                                              +-- Codex/Claude/Grok GUI or CLI

Flow C — out-of-band recovery

any approved browser -- Tailscale -- remote KVM
                                      |
                                      +-- display/keyboard/boot/recovery
```

In Flow A, `agentctl` changes identity only for that child shell or tmux client;
it is not a machine-wide switch. `exit` leaves `agentctl shell`. Inside an
attached tmux session, `agentctl detach` or `Ctrl-b d` detaches the current
client while the processes continue.

Flow B is the preferred daily shared-GUI path after NoMachine behavior is tested
on the actual release. Each person authenticates individually and is authorized
for the agent-owned desktop; the agent account password is not shared. Directly
launch GUI apps as another macOS user is not supported, so Mac nodes use
the owning `agent-01` graphical session rather than Linux-style delegation.

## Fleet and recovery

Profiles use `<namespace>-<class>-<NNN>` hostnames: `ws` Linux workstation,
`mac` Apple workstation, `hv` hypervisor, `vws` virtual workstation, `nas`,
`mgmt`, and `srv` only as a fallback. Keep real inventory in a private fleet
repository and never reuse names in its retirement ledger.

Remote KVM is not needed for headed Playwright: the real/virtual graphical
session provides a display that NoMachine can show. KVM covers failures below
that layer. It may be deferred during a physically supervised pilot, then added
before office placement or unattended operation.
