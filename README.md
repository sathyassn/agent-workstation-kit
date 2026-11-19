# Agent Workstation Kit

Production-oriented profiles, preview-first automation, concise guides and a
setup-agent skill for dedicated Linux and macOS AI development workstations.

Current version: **0.2.0-rc.7**. This remains a release candidate until the
documented Linux and macOS hardware exercises in the public-release checklist
are complete.

Run `scripts/setup-workstation.sh` for the guided setup walkthrough. The same
command provides `init`, `plan`, `run`, `resume`, `status`, `check-access`, `workloads`
and `doctor` for terminal or agent use. Preview is the default; installation
requires the displayed host/scope approval. Rich tables are optional and a plain
terminal fallback requires no extra package. See the
[walkthrough](docs/runbooks/resumable-setup.md#start-here-one-command).

## Contents

- [Purpose and boundaries](#purpose-and-boundaries)
- [How the pieces fit](#how-the-pieces-fit)
- [Start a new machine](#start-a-new-machine)
- [Normal profile workflow](#normal-profile-workflow)
- [Documentation](#documentation)
- [Repository layout](#repository-layout)
- [Safety and quality](#safety-and-quality)
- [License and independence](#license-and-independence)

## Purpose and boundaries

The kit supports Linux and macOS workstations running Codex, Claude Code,
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
operator on macOS / Windows / Linux / iPadOS / mobile
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

Here, a remote keyboard-video-mouse (KVM) device is the out-of-band recovery
console. It remains useful when the target operating system or NoMachine is
unavailable.

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

For the first Ubuntu machine or a newly introduced hardware model:

1. Open the [first Linux pilot checklist](docs/runbooks/first-linux-pilot.md)
   before power-on.
2. Use [day-zero Linux](docs/runbooks/day-zero-linux.md) as the controlling
   procedure after its **Before power-on** items are complete.
3. Hand control to the setup agent only at the readiness gate in day zero.
4. Finish the pilot evidence before treating the node or model as approved.

For a later host on already approved hardware, start directly with day-zero
Linux and record the host-specific acceptance evidence it links.

For a Mac mini or Mac Studio, start with
[Day-zero macOS startup and agent handoff](docs/runbooks/day-zero-macos.md), then
continue through the [macOS setup path](docs/03-macos-setup.md).

```text
platform + recovery preparation -> physical setup -> OS -> bootstrap account
       -> toolkit + private fleet
       -> readiness check -> supervised setup agent -> operational agent-NN
       -> burn-in -> approve or remediate
```

## Normal profile workflow

Use one entry point from the toolkit root:

```bash
./scripts/setup-workstation.sh
```

The walkthrough guides profile planning, scope review/execution, resume and
access checks. For an agent or non-interactive terminal, use the same command's
actions. For example, preview a draft profile:

```bash
./scripts/setup-workstation.sh init machines/acme-ws-001.toml \
  --fleet-root /PRIVATE/CONFIGURATION_ROOT \
  --context work --namespace acme --hostname acme-ws-001 \
  --display-name Atlas --platform linux --human alice
```

Add `--write` only to create that draft configuration. It does not create an OS
account. The underlying initializer allocates the UUID and checks uniqueness;
do not invent a UUID or replace an existing profile. Resolve every `ask`, obtain
human review, and commit the approved private profile/lock through its reviewed
workflow before staging. `plan` validates both the selected profile and the
complete configuration automatically.

Then export the execution plan and use the owner scopes in the
[walkthrough](docs/runbooks/resumable-setup.md). The normal order is packages,
machine, agent, measured resources and audit. Actual OS, credential, management,
privileged and recovery boundaries remain explicit. Helper scripts implement the
steps; operators do not need to run or remember them individually.

The operator-chosen namespace, technical hostname, assigned display name and UUID
retain the existing fleet-wide validation rules. Linux and Mac day-zero guides
remain authoritative before this entry point's prerequisites exist.

## Documentation

Use the [documentation home](docs/README.md) as the complete, ordered table of
contents. Key entry points are:

| Need | Guide |
|---|---|
| Understand the design | [Architecture and operating model](docs/00-architecture.md) |
| Start the first Linux host | [Day-zero Linux](docs/runbooks/day-zero-linux.md) |
| Configure Ubuntu | [Linux setup](docs/02-linux-setup.md) |
| Start a Mac | [Day-zero macOS](docs/runbooks/day-zero-macos.md) |
| Configure macOS | [macOS setup](docs/03-macos-setup.md) |
| Create machine input | [Profile onboarding](docs/01a-onboarding-profile.md) |
| Accept the MS-S1 Max | [Hardware runbook](docs/hardware/minisforum-ms-s1-max.md) |
| Prove the first pilot | [First Linux pilot](docs/runbooks/first-linux-pilot.md) |
| Operate or expand the fleet | [Validation](docs/09-validation-and-operations.md) and [rollout](docs/12-fleet-rollout-and-change-management.md) |

The documentation home identifies the one authoritative page for each topic.
Day-zero runbooks control new-machine sequencing; topic pages provide decisions
and reference detail rather than competing setup paths.

## Repository layout

| Path | Purpose |
|---|---|
| [`docs/`](docs/README.md) | Ordered entry points, operating guides, runbooks, decisions, and vendor sources |
| [`config/profiles/`](config/profiles) | Generic, non-secret example machine profiles |
| [`templates/private-fleet/`](templates/private-fleet) | Starting structure for a separate private inventory repository |
| [`scripts/`](scripts) | Preview-first setup, validation, identity, security, and maintenance automation |
| [`agentctl/`](agentctl) | Delegated shared-agent sessions with named-human attribution |
| [`skills/setup-agent-workstation/`](skills/setup-agent-workstation/SKILL.md) | Setup-agent instructions and approval boundaries |
| [`tests/`](tests) | Repository, documentation, profile, security, and repeatability checks |
| [`.codeflow/`](.codeflow/README.md) | Branch, commit, pull-request, secret, and pre-push test policy |
| [`.github/`](.github) | Issue forms, pull-request requirements, dependency updates, and CI |

Real profiles, fleet assignments, serial numbers, asset tags, and deployment
evidence never belong in this repository. Put them in the private fleet created
from the template.

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

Contributions follow the branch, commit, review, and test rules in
[CONTRIBUTING.md](CONTRIBUTING.md). Local hooks and hosted checks enforce the
repository's Codeflow-derived policy through the self-contained checker; no
Codeflow installation is required.

For project expectations and safe contact paths, see the
[Code of Conduct](CODE_OF_CONDUCT.md), [support policy](SUPPORT.md), and
[security policy](SECURITY.md).

## License and independence

Licensed under the [Apache License 2.0](LICENSE). This project configures
independent vendor products and is not sponsored or endorsed by OpenAI,
Anthropic, xAI, Tailscale, NoMachine, Minisforum, GL.iNet or other referenced
vendors.

For approved profiles, use the [resumable setup plan](docs/runbooks/resumable-setup.md)
and [remote Mac recipe](docs/runbooks/remote-mac.md) for checkpoint evidence.

The setup entry point `scripts/setup-workstation.sh` now exports reviewed
execution plans and runs approved owner scopes with live checks and automatic
resume receipts. See [automated setup](docs/runbooks/resumable-setup.md).
