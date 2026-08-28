# Agent Workstation Kit

Production-oriented profiles, preview-first automation, concise guides and a
setup-agent skill for dedicated Linux and macOS AI development workstations.

Current version: **0.2.0-rc.3**. This is a release candidate until the first
physical Linux node completes the documented burn-in.

## Contents

- [Purpose and boundaries](#purpose-and-boundaries)
- [How the pieces fit](#how-the-pieces-fit)
- [Start a new machine](#start-a-new-machine)
- [Normal profile workflow](#normal-profile-workflow)
- [Documentation](#documentation)
- [Safety and quality](#safety-and-quality)
- [License and independence](#license-and-independence)

## Purpose and boundaries

The kit supports Linux and future macOS workstations running Codex, Claude Code,
Grok Build, headed browser tests and long-lived agent workloads. It provides:

- A public-capable toolkit containing generic code, tests and guidance.
- A separate private fleet repository containing real machine inventory.
- Individual human and administrator identities plus a shared non-admin agent
  runtime account.
- Preview-first changes, explicit approval gates and live-host evidence.

It does not store passwords, tokens, private keys or recovery material. It also
does not turn a repository check into proof that a physical machine is ready.

## How the pieces fit

```text
operator Mac/iPad
      |
      +-- Tailscale + NoMachine --> agent-NN graphical desktop
      |                                  |
      |                                  +-- Codex / Claude / Grok / browsers
      |
      +-- Tailscale + SSH --> named-human shell
      |                            |
      |                            +-- agentctl --> agent-NN tmux sessions
      |
      +-- Tailscale + KVM --> firmware / boot / recovery (when deployed)
```

```text
agent-workstation-kit                 private workstation-fleet
generic code and documentation       real hosts and approved desired state
              |                                   |
              +------ kit.lock version pin -------+
              |
              +------ validates profiles -------->+
```

Start a personal or organization fleet from
[`templates/private-fleet`](templates/private-fleet). Keep that fleet private
even when this toolkit is public. Fork this toolkit only when local automation
must diverge; otherwise pin and review upstream releases.

## Start a new machine

For a fresh Ubuntu machine, follow one entry point:

1. Open [Day-zero Linux startup and agent handoff](docs/runbooks/day-zero-linux.md).
2. Complete the linked hardware and first-pilot evidence for the machine model.
3. Hand control to the setup agent only at the readiness gate in that guide.
4. Finish live-host validation before treating the node as operational.

For future Apple nodes, use the [macOS setup path](docs/03-macos-setup.md).

```text
physical setup -> Ubuntu -> bootstrap account -> toolkit + private fleet
       -> readiness check -> supervised setup agent -> operational agent-NN
       -> burn-in -> approve or remediate
```

## Normal profile workflow

Run these examples from the toolkit root. Replace the sample namespace, host,
display name and human handle with approved values:

```bash
FLEET_ROOT='/private/path/workstation-fleet'
PROFILE='machines/ac-ws-001.toml'

./scripts/fleetctl.py --fleet-root "$FLEET_ROOT" init "$PROFILE" \
  --context work --namespace ac --hostname ac-ws-001 \
  --display-name 'Atlas' --platform linux \
  --hardware-profile minisforum-ms-s1-max-64gb --human alice

./scripts/fleetctl.py --fleet-root "$FLEET_ROOT" validate "$PROFILE"
./scripts/fleetctl.py --fleet-root "$FLEET_ROOT" plan "$PROFILE"
# Resolve every "ask", obtain human review, then validate readiness.
./scripts/fleetctl.py --fleet-root "$FLEET_ROOT" validate "$PROFILE" --ready
./scripts/validate-fleet.py "$FLEET_ROOT"
# Human-review the exact profile/lock diff, then commit it before staging.
git -C "$FLEET_ROOT" status --short
```

`machine.hostname`, `machine.display_name` and `machine.uuid` must be unique in
the complete fleet. `fleetctl init` generates the UUID; do not invent one.
The [day-zero guide](docs/runbooks/day-zero-linux.md) gives the narrow `git add`,
review and commit commands; a root-owned archive must never contain uncommitted
or untracked fleet input.

## Documentation

Use the [documentation map](docs/README.md) as the complete, ordered table of
contents. Key entry points are:

| Need | Guide |
|---|---|
| Understand the design | [Architecture and operating model](docs/00-architecture.md) |
| Start the first Linux host | [Day-zero Linux](docs/runbooks/day-zero-linux.md) |
| Configure Ubuntu | [Linux setup](docs/02-linux-setup.md) |
| Configure a future Mac | [macOS setup](docs/03-macos-setup.md) |
| Create machine input | [Profile onboarding](docs/01a-onboarding-profile.md) |
| Accept the MS-S1 Max | [Hardware runbook](docs/hardware/minisforum-ms-s1-max.md) |
| Prove the first pilot | [First Linux pilot](docs/runbooks/first-linux-pilot.md) |
| Operate or expand the fleet | [Validation](docs/08-validation-and-operations.md) and [rollout](docs/12-fleet-rollout-and-change-management.md) |

## Safety and quality

Scripts preview by default. `--apply`, `sudo`, account or access changes,
external authentication, firmware, recovery and deletion require a human gate.

```bash
make check          # portable repository and unit checks
make ci-check       # adds mandatory ShellCheck
make public-check   # deterministic public-readiness checks
```

Repository checks do not certify a live host. Record firmware, drivers,
endpoint management, remote access, backup/restore and realistic workload
evidence separately before approving a node.

## License and independence

Licensed under the [Apache License 2.0](LICENSE). This project configures
independent vendor products and is not sponsored or endorsed by OpenAI,
Anthropic, xAI, Tailscale, NoMachine, Minisforum, GL.iNet or other referenced
vendors.
