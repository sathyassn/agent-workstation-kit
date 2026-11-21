# Roadmap: the Go kit to 1.0.0

[Documentation home](../README.md)

This is the plan for building the kit as one Go binary, `wskit`, from the
design in ADR 0005 (`docs/design/setup-architecture.md`, PR #13). It is
checked in before any Go code, and it changes only through reviewed PRs.
Each epic below becomes a CodeFlow epic record under `project-management/`
with its tasks; this page is the map across them.

Status: proposed (P0 plan PR). Each epic is a CodeFlow record in
`project-management/epics/`.

## Phases

```text
P0 Foundation --> P1 Core --> P2 Units (waves A, B, C in parallel) --> P3 Composition
                                                                         |
                                       P5 Cutover <-- P4 Acceptance <----+
```

| Phase | Outcome | Epics |
|---|---|---|
| P0 Foundation | The repository is ready for Go: CodeFlow full tier, layout, module, CI with lint, tests, vulnerability scan and a signed release build, code generation from the schema, and the day-zero `bootstrap.sh` | EPC-001, EPC-002 |
| P1 Core | `wskit` runs: the CLI skeleton and contracts, the unit catalog and domains, schema-generated types, platforms, `plan`, `next --json`, the UI layer, and the transaction engine | EPC-003, EPC-004, EPC-005 |
| P2 Units | Each part of the setup is a standalone unit with its own epic and PR series. Wave A starts with the tunnel (EPC-006), then resources (EPC-010), both on T1 before T2 | Wave A: EPC-006, EPC-010, EPC-007 to EPC-009, EPC-011, EPC-012, EPC-033; wave B: EPC-013 to EPC-018; wave C: EPC-019 to EPC-022 |
| P3 Composition | Stages and gates compose the units; lockout guarantees, one config and the interview, the registry and staging, the update watch and the conductor recipe | EPC-023 to EPC-028 |
| P4 Acceptance | 1.0.0: a clean agent-driven run on Linux and macOS, with an interrupt and resume, and revert to baseline passing the match digest | EPC-030 |
| P5 Cutover | `wskit` is the only tool for the kit and every fleet; the frozen Python and shell are deleted at parity, and the tree matches the documented layout | EPC-031, EPC-032 |
| After 1.0.0, alongside P5 | Organisation units: declarative units built from typed operations; command steps and plugins later | EPC-029 |

## Epics

Every epic's acceptance includes standalone apply and revert tests for each
unit it owns, and an interrupted revert that resumes (ADR 0005 section 2c).

| Epic | Phase | Outcome: after it merges, ... | Depends on | Units |
|---|---|---|---|---|
| EPC-001 Foundation for the Go kit | P0 | contributors build, lint, test and release a signed `wskit` for Linux and macOS from a CodeFlow full-tier repository | none | none |
| EPC-002 Public-safety scan hardening | P0 | the public scan matches private names by keyed HMAC and fails closed, without printing what it found | none | none |
| EPC-003 CLI skeleton and contracts (T1a to T1c) | P1 | an agent runs `wskit plan` and `wskit next --json`, units register through the generated catalog, and root steps run only from the staged binary | EPC-001 | none |
| EPC-004 Terminal UI and plain output (T1d) | P1 | people get the guided terminal UI and agents get stable JSON and plain output from the same events | EPC-003 | none |
| EPC-005 Transaction engine (T2) | P1 | every unit change is previewed, backed up, applied under intent, verified, receipted and revertible, and the baseline is captured | EPC-003 | baseline |
| EPC-006 Browser tunnel unit | P2 A | a person finishes a remote CLI's localhost sign-in in their own browser over a loopback-only tunnel that closes itself | EPC-003 (EPC-005 to move onto the engine) | browser-tunnel |
| EPC-007 VPN unit | P2 A | every fleet reaches office-only services through one generic `wskit vpn` unit, with provider profiles and typed destination sources | EPC-005, EPC-015 | vpn |
| EPC-008 Shell unit | P2 A | the shell baseline is a managed block that coexists with other tools, with a prompt option and an agent-shell guard | EPC-005 | shell |
| EPC-009 Agent settings unit (T7) | P2 A | every agent config home gets the declared settings and narrow sandbox exceptions | EPC-005, EPC-011 | agent-settings |
| EPC-010 Resources unit | P2 A, first after the tunnel | agent work runs under a machine-wide ceiling (shares of RAM), with early OOM handling, swap and zram sizing, a `/tmp` policy and free-space floors, all on by default | EPC-003 (EPC-005 to move onto the engine) | resource-limits |
| EPC-011 Registry-driven tools unit | P2 A | a CLI is added or removed with `wskit tools add|remove` from a reviewed registry entry, with no code | EPC-005 | tools |
| EPC-033 macOS indexing unit | P2 A | development volumes on macOS are excluded from Spotlight and the FSEvents on-disk log by default, with an exact revert | EPC-003 (EPC-005 to move onto the engine) | indexing |
| EPC-012 MCP catalog unit | P2 A | an MCP server is added to every agent config home with `wskit mcp add`, with sign-in as a hand-over | EPC-009 | mcp |
| EPC-013 Packages and runtimes units | P2 B | OS packages and runtimes install at exact pins, with the SSH and remote-desktop install guards | EPC-005 | packages-user, packages, runtimes |
| EPC-014 Identity and accounts units | P2 B | hostnames, accounts, groups and git identity are set and revertible for both account topologies, with homes archived before removal | EPC-005 | machine-identity, accounts, git-identity |
| EPC-015 SSH and firewall units | P2 B | SSH and firewall changes apply and revert under the dead-man timer, which ships in this epic, on both OSes | EPC-005, EPC-014 | ssh, firewall |
| EPC-016 Mesh VPN unit | P2 B | the mesh VPN is configured and verified, with its sign-in as a hand-over | EPC-013 | mesh-vpn |
| EPC-017 Remote desktop unit | P2 B | remote desktop is installed masked and released only behind the firewall | EPC-013, EPC-015 | remote-desktop |
| EPC-018 Fleet commands | P2 B | a fleet maintainer manages a private fleet of config and records with `wskit fleet` | EPC-003, EPC-005 | none (command group) |
| EPC-019 Workloads and agentctl units | P2 C | workloads and the narrow `agentctl` helper are installed with an atomic sudoers rule | EPC-005, EPC-014 | agentctl, workloads |
| EPC-020 Snapshots unit | P2 C | filesystem snapshots are taken at baseline and stable points where supported | EPC-005 | snapshots |
| EPC-021 Workspace unit | P2 C | the agent workspace and its repositories are set up from config | EPC-011, EPC-014 | workspace |
| EPC-022 Doctor (T8) | P2 C | a person or agent runs one read-only check of every unit, drift and end-of-support date | EPC-005 | doctor |
| EPC-023 Stages and gates (T3) | P3 | the full setup runs as stages composing units, with gates, hand-overs and stable points | the P2 units it composes | all (composition) |
| EPC-024 Lockout and convergence guarantees (T4) | P3 | no interruption leaves a locked-out or half-converged machine, proven across units | EPC-023 | all (checks) |
| EPC-025 One config and the interview (T5) | P3 | a person or agent writes a machine config through the interview, with layered values, `[tools]` and `[mcp]` choices, and generated docs | EPC-003, EPC-011, EPC-012 | none |
| EPC-026 Registry and staging (T6) | P3 | tools move only through `registry bump`, releases are staged, and the one-liner verifies them | EPC-001, EPC-005 | none |
| EPC-027 Update watch | P3 | upcoming tool updates and end-of-support dates are flagged before they matter | EPC-026 | none |
| EPC-028 Conductor recipe | P3 | an agent conducts setup from `AGENTS.md`, in the kit and in a private fleet | EPC-023 | none |
| EPC-029 Organisation units | after 1.0.0 | a fleet adds declarative units built from typed operations, through the same engine; command steps and plugins come later | EPC-005, EPC-018 | declarative units |
| EPC-030 Acceptance for 1.0.0 | P4 | the release criteria hold on one Linux and one macOS machine | EPC-023 to EPC-028 | all |
| EPC-031 Cutover | P5 | the frozen Python and shell are gone | EPC-030 | all |
| EPC-032 Fleet adoption | P5 | existing hosts are converged, fleets use `wskit fleet`, and fleet scripts are retired | EPC-029, EPC-030 | all |

The dependency graph has no cycles. Within a phase, edges run EPC-011 to
EPC-009 to EPC-012 in wave A, and EPC-014 to EPC-015 to EPC-017 in wave B;
EPC-007 in wave A depends on EPC-015 in wave B, so the VPN unit lands after
the SSH and firewall group. Every other edge points to an earlier phase.

## Versions and milestones

The Go `wskit` is a new major version, 1.0.0. The Python `0.2.0-rc` line is
frozen for fixes and is never released as a final 0.2.0.

| Milestone | Release tags | Phases |
|---|---|---|
| alpha | `1.0.0-alpha.N` | P1 and P2 (waves A, B and C, in parallel) |
| beta | `1.0.0-beta.N` | P3 |
| rc | `1.0.0-rc.N` | P4 |
| 1.0.0 | `1.0.0` | after P4's criteria hold |

Each unit's milestone is in the catalog below.

## Unit catalog

Generated from `registry/units.toml` by `tests/catalog_tables.py`; edit the
TOML file, never this table. The same table appears in SPC-001 and in ADR
0005 section 15.

<!-- catalog:units:begin -->
| Unit | Domain | Linux stage | Linux runs as | macOS stage | macOS runs as | Depends on (Linux; macOS) | Epic | Milestone |
|---|---|---|---|---|---|---|---|---|
| `baseline` | system | baseline | root | baseline | root | none; none | EPC-005 | alpha |
| `packages-user` | system | none | none | packages-user | package-owner | none; baseline | EPC-013 | alpha |
| `packages` | system | packages | root | packages | root | baseline; packages-user | EPC-013 | alpha |
| `machine-identity` | identity | identity | root | identity | root | packages; packages | EPC-014 | alpha |
| `accounts` | identity | identity | root | identity | root | machine-identity; machine-identity | EPC-014 | alpha |
| `agentctl` | system | services | root | none | none | accounts; none | EPC-019 | alpha |
| `workloads` | system | services | root | agent | agent | agentctl; accounts | EPC-019 | alpha |
| `firewall` | network | remote | root | remote | root | packages; packages | EPC-015 | alpha |
| `ssh` | remote | remote | root | remote | root | accounts, firewall; accounts, firewall | EPC-015 | alpha |
| `mesh-vpn` | network | remote | root | remote | root | packages, ssh; packages, ssh | EPC-016 | alpha |
| `remote-desktop` | remote | remote | root | none | none | packages, ssh; none | EPC-017 | alpha |
| `vpn` | network | remote | root | remote | root | packages, ssh; packages, ssh | EPC-007 | alpha |
| `shell` | agent | agent | agent | agent | agent | accounts; accounts | EPC-008 | alpha |
| `git-identity` | identity | agent | agent | agent | agent | accounts; accounts | EPC-014 | alpha |
| `runtimes` | agent | agent | agent | agent | agent | shell; shell | EPC-013 | alpha |
| `tools` | agent | agent | agent | agent | agent | runtimes; runtimes | EPC-011 | alpha |
| `agent-settings` | agent | agent | agent | agent | agent | tools; tools | EPC-009 | alpha |
| `mcp` | agent | agent | agent | agent | agent | agent-settings; agent-settings | EPC-012 | alpha |
| `browser-tunnel` | remote | agent | user | agent | user | none; none | EPC-006 | alpha |
| `workspace` | workspace | agent | agent | agent | agent | tools, git-identity; tools, git-identity | EPC-021 | alpha |
| `resource-limits` | ops | resources | root | resources | root | none; none | EPC-010 | alpha |
| `indexing` | system | none | none | resources | root | none; baseline | EPC-033 | alpha |
| `snapshots` | data | resources | root | none | none | baseline; none | EPC-020 | alpha |
| `doctor` | ops | audit | root | audit | root | none; none | EPC-022 | alpha |
<!-- catalog:units:end -->

## Mappings

**ADR items to epics.** T1 is EPC-003 and EPC-004 (milestones T1a to T1d), T2 is
EPC-005, T3 is EPC-023, T4 is EPC-024, T5 is EPC-025, T6 is EPC-026, T7 is EPC-009 and T8 is EPC-022.
The tunnel of ADR section 10a is EPC-006; the fleet commands are EPC-018; the
extensibility of section 2d is EPC-003 (catalog and domains) and EPC-029.

**Units to epics.** The unit catalog above maps each unit to exactly one
epic and one stage per OS.

Traceability from each ADR decision to its epic, tasks and tests is in
[traceability](traceability.md).

**Plan items from issue #5 to epics,** each tracked once:

| Item | Epic |
|---|---|
| Shell baseline gaps (managed block, prompt option, agent-shell guard) | EPC-008; the drift check in EPC-022 |
| MCP servers declared once, every config home | EPC-012 |
| Agent sandbox exceptions | EPC-009 |
| Resources from the pilot (reserve, ceiling, OOM, pressure, `/tmp`) | EPC-010 |
| VPN destinations, status file and prompts | EPC-007 |
| Browser tunnel | EPC-006 |
| Admin hand-overs with `su -P` on Ubuntu 26.04 | EPC-023 (hand-over generator), tested in EPC-014 |
| Public-safety scan design | EPC-002 |
| Review and merge rules for agents | EPC-003 (`AGENTS.md`) |

## Gates

- **Every epic's PRs:** tests, an adversarial review of code and docs, an
  interruption stress run, a docs-only walkthrough by a fresh agent, and a
  changelog entry (ADR 0005 section 12).
- **Plan and design PRs** (this one included): an adversarial review and a
  four-part stress test by other model families before merge, as ADR 0005
  section 12 states.
- **Parallel work.** Once EPC-001 and EPC-003 merge, each unit epic gets its own
  worktree and worker. Workers never edit `internal/engine`; engine changes
  go through one owner.

## Standards

Code standards are enforced in CI from EPC-001: `gofmt` and `goimports`,
`go vet`, `golangci-lint` with the checked-in `.golangci.yml`, and
`govulncheck`. The rest of the standards (wrapped errors, contexts, typed
constants, generated schema types, table-driven tests) are in the
`## Stack standards` section of `AGENTS.md`.
