# Documentation

[Repository home](../README.md) · [Start Linux](runbooks/day-zero-linux.md) · [Start macOS](runbooks/day-zero-macos.md)

This is the complete documentation index. A day-zero runbook is the controlling
procedure for a new host; the topic guides it links explain decisions or provide
reference detail. Do not combine fragments from several pages into a new setup
sequence.

## Choose a path

| Goal | Start here |
|---|---|
| Set up the first Linux pilot | [First Linux pilot](runbooks/first-linux-pilot.md), then [day-zero Linux](runbooks/day-zero-linux.md) |
| Set up another Linux host | [Day-zero Linux](runbooks/day-zero-linux.md) |
| Set up a Mac | [Day-zero macOS](runbooks/day-zero-macos.md) |
| Configure an already prepared host | [Linux setup](02-linux-setup.md) or [macOS setup](03-macos-setup.md) |
| Create or change a machine profile | [Profile onboarding](01a-onboarding-profile.md) |
| Operate, validate, or troubleshoot a host | [Validation and operations](09-validation-and-operations.md) |
| Contribute to the toolkit | [Contributing guide](../CONTRIBUTING.md) |

```text
choose platform and recovery
          |
          +--> first Linux/new model: pilot before-power-on checklist
          |
          v
day-zero OS + bootstrap account
          |
          v
validated private profile + reviewed snapshots
          |
          v
supervised setup agent, one phase at a time
          |
          v
remote access + acceptance tests + burn-in
          |
          v
approve one host, then canary later changes
```

## New-machine sequence

Read the row for the current stage. The linked runbook tells you when a human,
script, or setup agent acts and where approval is required.

| Stage | Controlling page | Result |
|---|---|---|
| 1. Understand | [Architecture](00-architecture.md) and [selected stack](00a-final-stack.md) | Trust boundaries, identities, repositories, and default products are understood. |
| 2. Decide | [Planning worksheet](01-planning.md) | Ownership, naming, recovery, access, policy, and optional tools are decided. |
| 3. Prepare a Linux pilot | [First Linux pilot](runbooks/first-linux-pilot.md) | Before-power-on decisions and evidence are ready for the first node or a new hardware model; skip this stage for an already approved model. |
| 4A. Start Linux | [Day-zero Linux](runbooks/day-zero-linux.md) | Ubuntu, the bootstrap account, committed profile, and safe setup-agent handoff are complete. |
| 4B. Start macOS | [Day-zero macOS](runbooks/day-zero-macos.md) | Setup Assistant, recovery, committed profile, and safe setup-agent handoff are complete. |
| 5A. Configure Linux | [Linux setup](02-linux-setup.md) | Approved Linux phases are applied and checked. |
| 5B. Configure macOS | [macOS setup](03-macos-setup.md) | Approved macOS phases and manual gates are complete. |
| 6. Connect safely | [Security and resources](07-security-and-resources.md), then [remote access and files](08-network-remote-access-and-files.md) | The host remains recoverable and responsive while remote work is enabled. |
| 7. Prove readiness | [Validation and operations](09-validation-and-operations.md) | Reboot, access, load, browser, restore, and recovery evidence is recorded. |
| 8. Roll out | [Fleet rollout](12-fleet-rollout-and-change-management.md) | A proven change advances through canaries and reviewed cohorts. |

## One authoritative page per topic

These pages own their subject. Other pages should link here instead of
restating changing product choices, field definitions, or operating rules.

| Topic | Authoritative page |
|---|---|
| System design and identity flows | [Architecture and operating model](00-architecture.md) |
| Default product choices | [Selected workstation stack](00a-final-stack.md) |
| Required decisions and optional-tool interview | [Planning worksheet](01-planning.md) |
| Profile workflow and private-fleet boundary | [Profile onboarding](01a-onboarding-profile.md) |
| Every TOML field and allowed value | [Profile field reference](01b-profile-field-reference.md) |
| Linux configuration phases | [Linux setup](02-linux-setup.md) |
| macOS configuration phases | [macOS setup](03-macos-setup.md) |
| Local users, privileges, and `agentctl` | [Accounts and access](04-accounts-and-access.md) |
| Installed developer tools | [Development tooling](05-tooling.md) |
| GitHub, GitLab, Atlassian, and model identities | [Agent and provider identities](06-agent-and-source-control-identities.md) |
| Host security and resource controls | [Security and resource controls](07-security-and-resources.md) |
| Network, remote desktop, KVM, and file movement | [Network, remote access, and files](08-network-remote-access-and-files.md) |
| Acceptance, burn-in, and maintenance | [Validation and operations](09-validation-and-operations.md) |
| Human/script/setup-agent boundaries | [Responsibility matrix](10-human-script-agent-matrix.md) |
| Vendor claims and research dates | [Primary-source register](11-primary-sources.md) |
| Inventory, migrations, canaries, and cohorts | [Fleet rollout and change management](12-fleet-rollout-and-change-management.md) |
| Publication and production gates | [Production and public-release checklist](13-public-release-checklist.md) |

## Task runbooks

| Task | Runbook |
|---|---|
| Start and hand off a Linux host | [Day-zero Linux](runbooks/day-zero-linux.md) |
| Start and hand off a Mac | [Day-zero macOS](runbooks/day-zero-macos.md) |
| Stage reviewed macOS inputs | [Root-owned macOS snapshots](runbooks/stage-approved-macos-snapshots.md) |
| Record first-node evidence | [First Linux pilot](runbooks/first-linux-pilot.md) |
| Accept Minisforum MS-S1 Max hardware | [MS-S1 Max acceptance](hardware/minisforum-ms-s1-max.md) |
| Create a draft PR or MR as the agent identity | [Draft change](runbooks/create-draft-change.md) |
| Migrate legacy profiles | [Pre-v2 migration](runbooks/migrate-v1-to-v2.md) or [schema 2 to 3](runbooks/migrate-v2-to-v3.md) |

## Design decisions and evidence

Architecture decision records explain why the current design was selected:

- [Ubuntu first and multiple nodes](decisions/0001-platform-and-nodes.md)
- [Tailscale, NoMachine, and staged remote KVM](decisions/0002-network-and-remote-access.md)
- [Named humans plus a shared agent workspace](decisions/0003-shared-agent-account.md)
- [mise first; reproducibility and backup remain separate](decisions/0004-reproducibility-and-backup.md)

Current automated checks and outstanding live-host proof are maintained in the
[production and public-release checklist](13-public-release-checklist.md).
Historical reviewer transcripts belong in pull requests or private assurance
records; they are not operating instructions and are not a second source of
product truth.

## Reading conventions

- Commands are preview-only unless the example explicitly includes `--apply`.
- `PROFILE`, `FLEET_ROOT`, URLs, namespaces, and display names are placeholders
  until a preceding step assigns them.
- Run commands from the toolkit root unless the page says otherwise.
- Never publish private profiles, serial numbers, asset tags, identifiers, or
  evidence containing organization details.
- Stop at every `sudo`, credential, firmware, recovery, or remote-access gate.
- For a changing vendor fact, follow the citation in the
  [primary-source register](11-primary-sources.md) and recheck it before use.

[Repository home](../README.md) · [Start with architecture](00-architecture.md)
