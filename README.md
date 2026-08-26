# Agent Workstation Kit

Production-oriented profiles, preview-first automation, guides, and an AI setup
skill for dedicated Linux and macOS workstations running Codex, Claude Code,
Grok Build, headed browsers, and long-lived agent workloads.

Current version: **0.2.0-rc.2**. This remains a pilot release until the first
physical Linux node completes the documented burn-in.

## Operating model

```text
Human Mac/iPad
      |
      +-- Tailscale -- NoMachine -- shared desktop owned by agent-01
      |                                  |
      |                                  +-- terminal / tmux / Herdr / agents
      |
      +-- Tailscale -- SSH -- personal shell -- agentctl -- agent-01 sessions
      |
      +-- Tailscale -- remote KVM -- firmware / boot / recovery (optional initially)
```

Each person signs in with an individual identity. A separate `admin-NN` account
is assigned to that person for privileged work. Shared agent processes run as a
non-admin `agent-NN` account. No credentials are copied between these identities.

## Repository model

```text
agent-workstation-kit (this public-capable repository)
  scripts + schemas + examples + OS/hardware guides + setup skill
                     |
                     +-- validates --> private workstation-fleet repository
                                          real host inventory and policy
                                          no credentials
```

Use a separate private fleet repository for both personal and organization
deployments. Start from [`templates/private-fleet`](templates/private-fleet).
An organization can fork this toolkit privately if its automation must diverge;
otherwise pin the upstream toolkit version in `kit.lock` and keep only fleet
data private.

## Start here

1. Read [architecture and operating model](docs/00-architecture.md).
2. Review the [final stack and product choices](docs/00a-final-stack.md), then
   create a draft with [profile onboarding](docs/01a-onboarding-profile.md).
3. Follow [Linux setup](docs/02-linux-setup.md) or [macOS setup](docs/03-macos-setup.md).
4. Configure [accounts and access](docs/04-accounts-and-access.md), then
   [source-control/workload identities](docs/06-agent-and-source-control-identities.md).
5. Complete [validation, burn-in, and operations](docs/08-validation-and-operations.md).

For the first Minisforum MS-S1 Max, use both the
[first Linux pilot](docs/runbooks/first-linux-pilot.md) and
[hardware acceptance runbook](docs/hardware/minisforum-ms-s1-max.md).

## Safe command flow

```bash
./scripts/fleetctl.py --fleet-root /private/fleet init machines/ac-ws-001.toml \
  --context work --namespace ac --hostname ac-ws-001 --display-name Atlas \
  --platform linux \
  --hardware-profile minisforum-ms-s1-max-64gb --human alice

./scripts/fleetctl.py validate /private/fleet/machines/ac-ws-001.toml
./scripts/fleetctl.py plan /private/fleet/machines/ac-ws-001.toml
# Resolve every "ask", review, approve, then:
./scripts/fleetctl.py validate /private/fleet/machines/ac-ws-001.toml --ready
```

`machine.hostname` remains the stable technical identifier. The optional
`--display-name` supplies a human-friendly assigned name; when omitted it
defaults to the hostname. Both names must be unique across the complete fleet.
Always use `--fleet-root` when allocating into a private fleet so its version
pin and retired-hostname ledger are enforced. Creation is serialized locally,
and `validate-fleet.py` is the required
cross-profile pre-merge check. The identity phase persists both names on the live host.

Scripts preview by default. `--apply`, `sudo`, account/access changes, external
authentication, boot/security changes, and deletion always require a human gate.
Profiles contain desired state only and never secrets.

## Quality gates

```bash
make check          # portable local checks
make ci-check       # includes mandatory ShellCheck
make public-check   # deterministic public-repository readiness
```

Repository checks do not certify a live machine. Record Ubuntu driver, firmware,
endpoint-management, NoMachine, KVM, backup/restore, and realistic workload
evidence separately before approving a node.

## License and independence

Licensed under the [Apache License 2.0](LICENSE). This project configures
independent vendor products and is not sponsored or endorsed by OpenAI,
Anthropic, xAI, Tailscale, NoMachine, Minisforum, GL.iNet, or other referenced
vendors.
