# Documentation map

[Repository home](../README.md) · [Start the first Linux machine](runbooks/day-zero-linux.md)

Use this page as the table of contents. Follow the numbered path for a new
machine; use runbooks and references only when the applicable step links to them.

## New-machine path

```text
Architecture -> Planning -> Pilot checklist
                               |
                               v
                     Day zero + profile commit
                               |
                               v
                    Configure -> Validate -> Operate
```

| Stage | Read | Outcome |
|---|---|---|
| 1. Understand | [Architecture](00-architecture.md) → [selected stack](00a-final-stack.md) | Know the trust, account, network and repository model. |
| 2. Decide | [Planning worksheet](01-planning.md) | Resolve ownership, recovery, access, tooling and policy choices. |
| 3. Prepare the pilot | [First Linux pilot](runbooks/first-linux-pilot.md) | Complete the before-power-on checklist and open the evidence record. |
| 4. Start and declare Linux | [Day-zero and agent handoff](runbooks/day-zero-linux.md), using [profile onboarding](01a-onboarding-profile.md) and the [field reference](01b-profile-field-reference.md) when linked | Install Ubuntu, create and commit one validated non-secret profile, stage reviewed inputs and hand off safely. |
| 5A. Configure Linux | [Linux setup](02-linux-setup.md) | Apply reviewed phases on Ubuntu. |
| 5B. Configure macOS | [macOS setup](03-macos-setup.md) | Apply the future Mac path and retain its manual gates. |
| 6. Establish access | [Accounts](04-accounts-and-access.md) → [tooling](05-tooling.md) → [provider identities](06-agent-and-source-control-identities.md) | Separate humans, admins, agents, tools and external credentials. |
| 7. Protect and connect | [Security/resources](07-security-and-resources.md) → [network/remote/files](09-network-remote-access-and-files.md) | Keep recovery and responsiveness while enabling remote work. |
| 8. Prove readiness | [Validation and operations](08-validation-and-operations.md) | Record repeat-apply, load, reboot, recovery and restore evidence. |
| 9. Roll out | [Fleet change management](12-fleet-rollout-and-change-management.md) | Promote through canaries and reviewed cohorts. |

## First Minisforum MS-S1 Max

Read these in order:

1. [First Linux pilot: before-power-on checklist](runbooks/first-linux-pilot.md)
2. [Day-zero Linux startup and agent handoff](runbooks/day-zero-linux.md)
3. [MS-S1 Max hardware acceptance](hardware/minisforum-ms-s1-max.md)
4. [Linux setup phases](02-linux-setup.md)
5. [Validation and operations](08-validation-and-operations.md)

```text
Physical console
      |
Ubuntu + bootstrap account
      |
start-linux-pilot.py
      |
bootstrap setup agent
      |
agent-01 operational handoff
      |
multi-day burn-in and promotion decision
```

## Operational runbooks

| Task | Runbook |
|---|---|
| Start the first Linux host | [Day zero](runbooks/day-zero-linux.md) |
| Record the first-node evidence | [First Linux pilot](runbooks/first-linux-pilot.md) |
| Create a draft PR/MR as the agent | [Draft change](runbooks/create-draft-change.md) |
| Migrate old inventory | [Pre-v2 migration](runbooks/migrate-v1-to-v2.md) or [schema 2 to 3](runbooks/migrate-v2-to-v3.md) |
| Check who performs each step | [Human/script/agent matrix](10-human-script-agent-matrix.md) |

## Decisions, evidence, and governance

- [Primary-source register](11-primary-sources.md)
- [Production and public-release checklist](13-public-release-checklist.md)
- Architecture decisions:
  [platform and nodes](decisions/0001-platform-and-nodes.md),
  [network and remote access](decisions/0002-network-and-remote-access.md),
  [shared agent account](decisions/0003-shared-agent-account.md), and
  [reproducibility and backup](decisions/0004-reproducibility-and-backup.md).
- Independent review records:
  [initial Claude review](reviews/2026-08-20-claude-code-review.md),
  [public readiness](reviews/2026-08-21-public-readiness-review.md),
  [release candidate](reviews/2026-08-22-release-candidate-review.md),
  [identity/provider review](reviews/2026-08-25-identity-and-provider-review.md),
  and [day-zero documentation review](reviews/2026-08-27-day-zero-documentation-review.md).

## Documentation conventions

- Commands are preview-only unless the example explicitly contains `--apply`.
- `PROFILE`, `FLEET_ROOT`, URLs and human-readable labels are placeholders unless
  a preceding step assigns them.
- Run commands from the toolkit root unless a guide says otherwise.
- A private hardware audit may contain serial numbers; never publish it.
- Stop at every `sudo`, credential, firmware, recovery or remote-access gate.

[Repository home](../README.md) · [Next: architecture](00-architecture.md)
