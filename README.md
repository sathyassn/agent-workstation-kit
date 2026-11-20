# Agent Workstation Kit

Set up dedicated Ubuntu and macOS machines where AI coding agents (Codex,
Claude Code, Grok Build) run long-lived work, with every change previewed
before it is applied.

Upcoming version: **0.2.0-rc.7**, not yet tagged. It stays a release candidate
until the Linux and macOS hardware exercises in the
[public-release checklist](docs/13-public-release-checklist.md) are complete.
Changes are listed in the [changelog](CHANGELOG.md).

## What it is

- **Scripts** that install and verify a workstation. Each previews by default
  and changes the machine only with `--apply`.
- **One entry point,** `scripts/setup-workstation.sh`, that plans, runs and
  resumes the setup and checks the result.
- **Machine profiles:** one TOML file per machine describes its platform,
  accounts and tools.
- **A private-fleet template** for the repository where an organisation keeps
  its real machines. This public kit holds no real machines, people or secrets.
- **Guides and a setup skill** for the person and the AI agent doing the setup.
  Agents start with [AGENTS.md](AGENTS.md).

## How it fits together

```text
  agent-workstation-kit (public)   your private fleet repository
  scripts, defaults, guides        machines/<host>.toml, policy, assignments
              |                           |
              +---------------------------+  kit.lock pins a kit version
              |                           |
              v                           v
   reviewed kit copy (staged   <---  reviewed profile
   to /opt for root scopes)          for one machine
              |
              v
      setup-workstation.sh: plan -> preview -> person approves -> apply -> verify
              |
              v
      workstation: named people + admin account + shared non-admin agent account
```

Each workstation is reached privately:

```text
person on any device
   |
   +-- Tailscale + NoMachine --> agent desktop (Codex, Claude, Grok, browsers)
   +-- Tailscale + SSH -------> named person's shell --> agentctl --> agent sessions
   +-- Tailscale + KVM -------> firmware, boot and recovery console (if deployed)
```

A remote KVM (keyboard, video, mouse) device is the out-of-band console for when
the operating system or remote desktop is unavailable.

Keep the fleet repository private even though this kit is public. Pin and review
kit releases rather than forking; fork only when local automation must diverge.

## Quick start

You need a freshly installed machine, a person at its console, and a private
fleet repository made from [`templates/private-fleet`](templates/private-fleet).

**Ubuntu:**

1. Before power-on, open the [first Linux pilot checklist](docs/runbooks/first-linux-pilot.md)
   for a new hardware model.
2. Follow [day-zero Linux](docs/runbooks/day-zero-linux.md) to the readiness check.
   Hand control to the setup agent only at that readiness gate.
3. Plan, preview and apply each scope with [automated and resumable setup](docs/runbooks/resumable-setup.md).
4. For a new hardware model, finish the pilot evidence before treating the node
   or model as approved.

**macOS:**

1. Follow [day-zero macOS](docs/runbooks/day-zero-macos.md) to the readiness check.
2. Continue with the [macOS setup path](docs/03-macos-setup.md) and
   [automated and resumable setup](docs/runbooks/resumable-setup.md).

**For a machine in a private fleet,** draft its handoff and check readiness with
[guided fleet setup](docs/runbooks/guided-fleet-setup.md) first.

**Machine profiles:** create a draft with `setup-workstation.sh init` (preview,
then `--write`). The initializer allocates the UUID and checks uniqueness, so
never invent one or replace an existing profile. Resolve every `ask`, get the
profile reviewed, and commit it with the fleet's `kit.lock` before staging.

The entry point in short:

```bash
./scripts/setup-workstation.sh            # guided walkthrough in a terminal
./scripts/setup-workstation.sh --help     # actions: init, plan, run, resume, status,
                                          # check-access, workloads, doctor

# Export the plan for one machine to a private folder outside the kit:
./scripts/setup-workstation.sh plan --fleet-root FLEET \
  --profile machines/HOST.toml --export-plan PRIVATE/execution-plan.json

# Preview one scope; nothing changes until a person approves and you add --apply:
./scripts/setup-workstation.sh run --plan-file PRIVATE/execution-plan.json --scope agent
```

## Learn more

| Need | Guide |
|---|---|
| The full, ordered documentation | [Documentation home](docs/README.md) |
| How the design works | [Architecture and operating model](docs/00-architecture.md) |
| Create a machine profile | [Profile onboarding](docs/01a-onboarding-profile.md) and [field reference](docs/01b-profile-field-reference.md) |
| Configure Ubuntu or macOS | [Linux setup](docs/02-linux-setup.md), [macOS setup](docs/03-macos-setup.md) |
| Reach a Mac remotely | [Remote Mac](docs/runbooks/remote-mac.md) |
| Accept the MS-S1 Max | [Hardware runbook](docs/hardware/minisforum-ms-s1-max.md) |
| Operate or grow the fleet | [Validation](docs/09-validation-and-operations.md), [rollout](docs/12-fleet-rollout-and-change-management.md) |
| Release a new version | [Releasing](docs/maintenance/releasing.md) |

## Repository layout

| Path | Purpose |
|---|---|
| [`scripts/`](scripts) | The entry point and preview-first setup, validation and security scripts |
| [`config/`](config) | Kit defaults and example profiles in `config/profiles/` |
| [`templates/private-fleet/`](templates/private-fleet) | Starting structure for a private fleet repository |
| [`docs/`](docs/README.md) | Guides, runbooks, decisions and vendor sources |
| [`agentctl/`](agentctl) | Shared agent sessions that record which named person started them |
| [`skills/setup-agent-workstation/`](skills/setup-agent-workstation/SKILL.md) | Setup-agent instructions and approval boundaries |
| [`tests/`](tests) | Repository, documentation, profile, security and repeatability checks |
| [`.codeflow/`](.codeflow/README.md), [`.github/`](.github) | Commit, branch and pull-request policy, and CI |

Real profiles, assignments, serial numbers, asset tags and deployment evidence
never belong here; keep them in the private fleet.

## Safety and checks

- Scripts preview by default. `--apply`, `sudo`, account and access changes,
  sign-ins, firmware, recovery and deletion need a person's approval.
- Repository checks do not certify a live machine. Record firmware, drivers,
  endpoint management, remote access, backup and restore, and workload evidence
  separately before approving one.

```bash
make check          # portable repository and unit checks
make ci-check       # adds mandatory ShellCheck
make public-check   # deterministic public-readiness checks
```

The shell checks need Bash 4 or newer. On macOS, install it with Homebrew; see
[CONTRIBUTING.md](CONTRIBUTING.md).

## Contributing and support

- [CONTRIBUTING.md](CONTRIBUTING.md): branches, commits, changelog entries,
  reviews and releases. Local hooks and hosted checks enforce the policy; no
  Codeflow installation is needed.
- [Code of Conduct](CODE_OF_CONDUCT.md), [support](SUPPORT.md) and
  [security policy](SECURITY.md).

## License

Licensed under the [Apache License 2.0](LICENSE). The kit configures independent
vendor products and is not sponsored or endorsed by OpenAI, Anthropic, xAI,
Tailscale, NoMachine, Minisforum, GL.iNet or other referenced vendors.
