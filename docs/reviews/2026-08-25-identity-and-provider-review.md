# Identity and provider review — 2026-08-25

[Previous review](2026-08-22-release-candidate-review.md) · [Documentation home](../README.md)

Scope: `0.2.0-rc.2` machine identity, fleet-wide uniqueness, schema migration,
provider identities, and privileged connection-safety changes before the local
commit. This is static review evidence, not live-machine evidence.

## Reviewers and automated evidence

- Claude Code 2.1.246, Claude Opus 5, high reasoning, interactive plan mode.
- Grok Build 1.0.5, Grok 4.6, high reasoning, interactive mode.
- `git diff --check`: pass.
- `make ci-check`: pass after all review fixes, including ShellCheck and 93
  Python tests.

Both reviewers inspected the repository directly and independently. Their final
focused passes reported no unresolved P0, P1, or P2 findings.

## Material findings closed

- Fleet allocation and validation reject duplicate technical hostnames,
  case-insensitive assigned display names, UUIDs, and asset tags. Allocation is
  serialized with a no-follow, owner-checked lock and refuses malformed or
  nested inventory that would make uniqueness unprovable.
- Retired hostnames cannot be reallocated. `fleetctl init` infers the fleet root
  for the standard `machines/` layout, while documented commands pass
  `--fleet-root` explicitly so toolkit version and retirement checks remain in
  force.
- Schema-2 migration is a whole-fleet, staged schema-3 replacement. Mixed-schema
  inventory is rejected instead of silently escaping collision checks.
- The root-owned local identity record is written atomically without secrets;
  runtime technical and friendly names plus Linux local NSS resolution are
  audited against the private profile.
- Privileged identity and remote-hardening applies require tested recovery and
  explicit connection context. Tailscale SSH peers are checked with
  `tailscale whois`. A local-console claim checks process ancestry for SSH,
  Tailscale SSH, and Mosh and fails closed if ancestry is unavailable, so a
  normal `sudo` environment reset cannot hide an SSH session.
- Shared provider identities are purpose-scoped rather than coupled to a mutable
  machine name. GitHub App, GitLab service-account, and Atlassian service-account
  flows separate provider administration from target-host authentication; a
  shared agent may create branches and draft PRs/MRs but cannot approve or merge
  its own work.
- Multi-operator profiles reject a named-human Atlassian identity in the shared
  agent home. Profiles reject recognizable credential prefixes and never store
  provider secrets.

Provider conclusions were checked against the vendor links in
[`docs/11-primary-sources.md`](../11-primary-sources.md) on 2026-08-25.

## Gate decisions

| Gate | Decision | Conditions |
|---|---|---|
| Local RC commit | **GO** | Commit the exact checked tree and rerun `make ci-check` from the clean commit. |
| First supervised Linux pilot | **GO** | Start at the attached physical console, keep recovery open, and capture every runbook item. |
| Private GitHub publication | **NO ACTION** | The owner requires a separate explicit approval before remote creation or push. |
| Public visibility | **NO-GO** | Complete Linux/macOS live evidence, conduct contact and legal approval, full-history scan, hosted CI/security settings, and the public-release checklist. |

## Live evidence still required

- MS-S1 Max firmware, networking, Secure Boot/MOK, RTL8127, reboot/kernel
  update, repeat-apply, rollback, recovery, power-loss, and backup/restore tests.
- NoMachine identity/reconnect behavior and a realistic four-session,
  multi-subagent, browser/Playwright load with measured resource and thermal
  headroom.
- The false-local-console rejection test from a real SSH session whose `sudo`
  policy clears `SSH_CONNECTION`.
- macOS account, naming, FileVault, remote-desktop, privacy, Xcode, and
  workload validation on the intended Mac mini/Studio release.

[Next review: day-zero documentation](2026-08-27-day-zero-documentation-review.md) · [Documentation home](../README.md)
