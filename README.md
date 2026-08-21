# Agent Development Fleet

Production-oriented guides, scripts, and an AI setup skill for dedicated Linux and macOS development machines running Codex, Claude Code, Grok Build, browsers, and long-lived agent workloads.

Current version: **0.1.0-rc.1 (pilot series)**. Version 0.x interfaces may
change as live-machine evidence is incorporated. See the
[changelog](CHANGELOG.md).

## Intended architecture

```text
Operator Mac/iPad
       |
   Tailscale
       |
       +--> NoMachine --> shared agt-* desktop
       |
       +--> SSH --> named human shell --> agentctl --> agt-* session
       |
       +--> Remote KVM --> boot/BIOS/recovery
```

Start with Ubuntu Linux agent nodes. Add macOS nodes later for Xcode and Apple-platform work. Pilot one node before cloning the configuration to additional machines.

## Start here

1. Read [Architecture and operating model](docs/00-architecture.md).
2. Complete the [Planning worksheet](docs/01-planning.md), choose a validated [profile example/reference](docs/01b-profile-field-reference.md), and follow [profile onboarding](docs/01a-onboarding-profile.md).
3. Follow either [Linux setup](docs/02-linux-setup.md) or [macOS setup](docs/03-macos-setup.md).
4. Configure [accounts and access](docs/04-accounts-and-access.md).
5. Configure [agent and source-control identities](docs/06-agent-and-source-control-identities.md).
6. Run [validation and burn-in](docs/08-validation-and-operations.md).
7. For more than one node, use [fleet rollout and change management](docs/12-fleet-rollout-and-change-management.md).

For the first physical Linux machine, use the concise
[first-node pilot runbook](docs/runbooks/first-linux-pilot.md).

Architecture decisions are recorded under [`docs/decisions`](docs/decisions),
including the separation between reproducible setup and encrypted backup.

The Python `fleetctl` validates the declarative TOML profile, renders exact phase commands, and audits live account/security state. The Bash scripts perform narrow OS-specific changes. Both default to observation/preview, contain no credentials, and do not silently authenticate external services.

Check a clean checkout before use:

```bash
make check
# Release/CI-equivalent check; requires ShellCheck.
make ci-check
./scripts/fleetctl.py --version
```

## Automation boundary

```text
Scripts                         Setup agent
-------                         -----------
Install known packages          Inspect the machine
Create declared accounts        Select the correct OS path
Write validated config          Ask optional-tool questions
Install agentctl                Explain approvals
Apply resource policy           Run scripts in order
Run deterministic checks        Diagnose and summarize results

Human approval is required for sudo, credentials, vendor accounts,
disk/security changes, remote access, and destructive operations.
```

## Repository status

This is a production-oriented implementation baseline, not a claim of live fleet certification. Repository checks are automated; one non-critical pilot, followed by recorded burn-in and staged rollout, is mandatory. Hardware drivers, corporate endpoint controls, identity policy, vendor licensing, and graphical recovery remain environment-specific gates.

See the [independent review record](docs/reviews/2026-08-20-claude-code-review.md)
for the implementation review and the
[public-readiness follow-up](docs/reviews/2026-08-21-public-readiness-review.md)
for the current release gates.

## Contributing and publication

Read [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md), and the
[public release checklist](docs/13-public-release-checklist.md) before proposing
or publishing changes. The repository does not yet include an open-source
license. Until the owner selects and approves one, the source may be reviewed
but no permission to copy, modify, or redistribute it is granted.

This project configures independent vendor products and is not sponsored or
endorsed by OpenAI, Anthropic, xAI, Tailscale, NoMachine, or other referenced
vendors.
