# ADR 0005: Setup architecture

[Previous decision](../decisions/0004-reproducibility-and-backup.md) · [Documentation home](../README.md)

- Status: proposed (design PR D0; no code yet)
- Date: 2026-11-21
- Source: the consolidated design on issue #5 (comment 7048343188) and its
  amendments on resolving "latest" (7048398540) and OS support (7048442102,
  with the correction 7054972288); principle 11 (comment 7059892027 on #13);
  the implementation decision (comment 7060018625 on #13); the user decision
  of 2026-11-21 to build the browser tunnel as the first unit, which replaces
  the tunnel-order amendment (7048586326); and the later plan additions on #5: admin
  hand-overs on Ubuntu 26.04 (7048920107), shell baseline gaps (7055221363),
  MCP servers (7055619722), pilot learnings (7056549334) and VPN destinations
  (7056636078). It is refined against the code.
- Audience: maintainers and reviewers. The conductor agent and operators get
  their own pages as each part lands.
- Numbering: this is decision 0005. It lives in `docs/design/` because the
  source design asked for that path. The decisions index links to it.

## Summary

The kit's phase scripts are careful, but the layer around them is not yet one
system:

- **Two apply paths.** One of them skips every safeguard.
- **Backups.** They last only for one script run.
- **Logs.** There are no log files.
- **Human steps.** They sit in the middle of automated scopes.
- **macOS root scopes.** They cannot run today: see the context table.

This ADR sets one architecture for setup, upkeep and revert on Ubuntu and
macOS:

- one apply path through one executor;
- every change is a transaction with a backup and a receipt;
- a baseline and stable points the machine can return to;
- gates for the steps only a person can do;
- a conductor agent that drives the run from right after the OS install;
- one schema, one platform matrix, one capability matrix and one tool
  registry that drive validation, plans, docs and installs;
- every part is a unit that works on its own, and stages only compose units;
- one Go binary, `wskit`, with units as subcommands. The Python and shell are
  frozen and replaced unit by unit.

Eight pull requests (T1 to T8) build it in order, with the browser sign-in
tunnel as the first unit right after T1. Each one ships with tests, docs, an
adversarial review and an interruption stress run.

```text
HUMAN (critical steps)          CONDUCTOR AGENT (setup user, no root)   KIT (one apply path)
-----------------------------   -----------------------------------     ------------------------------
OS install, disk encryption
day-zero one-liner -----------> kit release + agent CLI installed
agent CLI sign-in ------------> reads the AGENTS.md conductor recipe
interview answers <-----------> writes the machine TOML from the schema  validate: schema, platforms,
approve config --------------> shows the plan and every non-default       capabilities, tool registry
G0 recovery test <-------------   gate: hand-over, poll the probe         G0
root approval on a terminal <--   root step: prepared hand-over           S1 Stage and baseline
                                  user step: preview, apply, verify       S2 Packages
                                                                          S3 Identity and accounts
G1 passwords, keys, secure     <- gate: hand-over, poll the probe         G1
   tokens, mesh VPN, Remote Login                                         S4 Services (Linux)
                                                                          S5 Remote   (dead-man timer)
G2 agent session (+ conductor  <-                                         G2
   sign-in there), privacy prompts                                        S6 Agent    (settings, tools)
G3 provider sign-ins <---------                                           G3
                                                                          S7 Resources (deferrable)
approve repair or revert <-----  on failure: read the failure record,     S8a Audit (root), S8b Audit (agent)
                                   propose repair, retry or revert           -> stable point
                                 stage summaries to the human; final report
```

## Terms

| Term | Meaning |
|---|---|
| Setup user | The admin account used at day zero (for example `bootstrap-admin`). It is a sudoer, but the conductor running in it never runs `sudo`. |
| Conductor | The agent CLI that drives the run by following the AGENTS.md recipe. It never holds root and never sees a secret. |
| Stage | A step in the setup order, S0 to S8. Each scope has one privilege; a stage that needs two privileges on an OS has two scopes (S2 on macOS: `packages-user`, then `packages`; S8: `audit`, then `audit-agent`). Otherwise a stage is exactly one executor scope. The one stage flag is `--stage`, which takes the scope ID, such as `packages` or `remote`; there is no `--scope` flag. Some scopes share a name with a phase inside them: scope `identity` holds the phases `identity` and `accounts`, and scope `audit` holds the phase `audit`. |
| Gate | A one-phase scope with a read-only probe and a hand-over, G0 to G3. |
| Phase | One step inside a scope. Today it is a kit script; in `wskit` it is a unit's `Apply` as the engine runs it (section 2b). Phase names stay as they are today. |
| Run | One executor invocation. Its run ID is 32 lower-case hex characters. |
| Point | A recorded restore point. Its ID is `baseline` or `sp-<stage>-<first 8 characters of the run ID>`; `stable` is an alias for the newest tagged point. |
| Public export | Non-secret status that root writes for the conductor to read (section 11). |
| Unit | A part of the setup that works on its own and is composed into stages, such as the shell baseline or the browser sign-in tunnel (principle 11, section 2a). |
| Readiness | The read-only check (`fleetctl setup`, the pilot scripts) that a person runs on the target before the first scope. It checks the platform, the profile and the host facts, and ends `READY` or `NOT READY`. |
| Package owner | On macOS, the named admin account that owns the Homebrew prefix. It runs the Homebrew part of S2, never as root. |
| Grace period | After SIGINT or SIGTERM, how long the executor waits for the running phase to finish before it stops it: 120 seconds by default, set by `--grace SECONDS`. |
| Machine record | The structured record of one host: `records/HOST.toml` in the private fleet, with a root-only copy on the host for the doctor (section 10b). This ADR uses no other name for it. |

## Context

What the code does today, and the problem each fact causes:

| Fact | Where | Consequence |
|---|---|---|
| `fleetctl run PROFILE PHASE --apply` exits 2 and prints the `setup-workstation.sh` executor handoff (preview and read-only `audit` stay) | `scripts/fleetctl.py` (`print_executor_handoff`, `main`) | Previously ran phase scripts directly with no executor, lock, receipt or staged-root check; fixed in the frozen code and guarded by `tests/check_repository.py`. `wskit` replaces both entry points. |
| Some root scripts are in no scope | `install-local-deb-linux.sh`, `install-ms-s1-r8127-dkms.sh`; the allowlist is in `scripts/lib/setup_scopes.py` | Vendor packages (NoMachine, browsers, editors) and a kernel driver apply outside every safeguard. |
| The host lock lives in a root-owned 0700 run folder: `/Library/Application Support/agent-workstation-kit/run` on macOS, `/run/agent-workstation-kit` on Linux | `execute-setup.py` `host_lock_directory`, `host_apply_lock` | Previously checked `/var/run`, which is `root:daemon 0775` on macOS, so every macOS root scope exited 2; fixed in the frozen code and verified by the `macos-host-lock` CI job. A Linux folder left at 0755 by an earlier executor is tightened to 0700 in place; any other loose mode is refused. |
| The executor previews, applies, verifies and writes a receipt keyed by an input digest | `execute-setup.py` `execute()`, `phase_input()` | Good base. There is no backup, no intent record and no log file. The digest includes the executor's own source, so T1 invalidates existing receipts. Phases are idempotent, so the next run reapplies and re-records them. |
| A phase without a command stops the scope with `HUMAN GATE` and exit 3 | `execute-setup.py` `execute()`; the macOS `resources` phase has no command (`fleetctl.py` `phase_for`) | A person's step sits inside an automated scope, with no probe to resume on. macOS `resources` can never pass. |
| Every root apply requires `--confirm-recovery-tested` | `execute-setup.py` `main`, `install-machine-identity.py`, `fleetctl.py` | The recovery attestation is demanded before the steps that need it, and repeated on every scope. |
| Root scripts re-run themselves with `env -i`, keeping only `SSH_CONNECTION` and `SUDO_USER` | `scripts/lib/common.sh` | Any variable the executor passes, such as a run ID, is dropped. |
| macOS account creation needs a terminal for a hidden password prompt | `setup-accounts-macos.sh` (`sysadminctl -password -`) | It cannot run behind a logging pipe. |
| Backups are temporary | `harden-remote-access-linux.sh` (the UFW backup folder is removed on exit) | No restore after the run ends. |
| `log`, `warn` and `die` print to the terminal only | `common.sh` | No durable log, and no redaction. |
| A managed file that differs stops the phase ("merge it manually") | `harden-remote-access-linux.sh` | A rerun cannot converge. |
| The SSH install guard masks units before writing its record | `package_setup.py` `guard_new_ssh_install` | A kill between the two leaves masks the kit cannot release. |
| sudoers and the agentctl config are installed with `install` | `install-agentctl-linux.sh` | Validated first, but not written atomically or rechecked as a whole afterwards. |
| OS versions are checked in four places, differently | `execute-setup.py` `require_supported_linux`, `start-linux-pilot.py`, `bootstrap-linux.sh` (warns only), `start-macos-pilot.py` (accepts any macOS) | No single policy. |
| Root phases on macOS run under the system Python 3.9 | `execute-setup.py` docstring | The root executor cannot use `tomllib`. |
| CI runs only on `ubuntu-24.04` | `.github/workflows/ci.yml` | There is no evidence for macOS or for Python 3.9. |
| `path_safety.require_staged_root()` exists and is called by the package steps | `path_safety.py`, `package_setup.py` | T1 extends it to every root phase. It does not need to be created. |
| Tool versions live in hand-edited files; apt and Homebrew are "validated rather than assumed to be bit-identical" | `config/mise.toml`, `config/packages.toml`, ADR 0004 | No single record of source, checksum, licence or review. |

## Decision

### 1. Principles

1. **Configure once.** One machine file, validated against one schema,
   declares everything, optional components included.
2. **Scripts do the work, an agent conducts, the human decides.** The human
   keeps the OS, disk encryption, every secret and sign-in, root approvals,
   config approval, reverts and merges.
3. **Every change is a transaction:** preflight, preview, backup, intent,
   apply, verify, receipt. On failure, restore.
4. **Any point is reachable:** the baseline or any verified stable point. The
   kit removes only what it created.
5. **Interrupt-safe.** A crash, Ctrl-C, power loss or closed terminal never
   leaves a half-applied or locked-out machine; a rerun repairs or resumes.
6. **Both OSes get equal care.** Every capability maps to the right tool on
   each OS, and the supported versions are explicit.
7. **Pinned and vetted.** Every tool has an exact version, a verified source
   and a recorded security review. `latest` and version ranges are refused,
   for every tool and every source. A newer version becomes a pin only through
   `wskit registry bump`. Nothing floats at apply time. How this applies to
   sources that keep no old versions is proposed amendment A1 (section 13).
8. **Documented from one source.** Each setting states what it does, why, its
   options, the safe default, the recommendation, the risk and the undo. The
   docs are generated from the schema and tested.
9. **Nothing ad hoc.** No hard-coded users, hosts or paths. Generic logic
   lives in the kit, company values in the fleet, host facts in the machine
   record. An unforeseen change needs permission, is logged and is written
   back.
10. **Verified before merge.** Tests, an adversarial review of code and docs,
    and a stress run with interruption at every boundary plus a docs-only
    walkthrough.
11. **Modular units, composed.** Every part of the setup is a unit that can
    be used on its own and is also composed into the full setup. Stages only
    compose units (section 2a).
12. **No lock-in.** The kit sets things up faster and correctly; people and
    their agents stay free to change anything by hand (section 2f).
13. **Best practices on by default.** Every configuration area applies the
    learned best practice automatically, shown in `plan` with its reason,
    and each default is configurable or can be opted out. People are never
    required to know to ask for it (section 2h).

### 2. Stages, gates and owners

| Stage (`--stage`) | Linux | macOS | Runs as | Reversible |
|---|---|---|---|---|
| S0 Configure | interview, machine TOML, `plan` | same | setup user (conductor) | yes (a file in the fleet) |
| G0 `recovery` | console recovery tested | same, with the recovery key | human; probe: attestation typed at the gate | n/a |
| S1 `stage` | versioned `/opt` release (T6), state folders, baseline (its own `baseline` scope from T2 until T6) | same | root | no; removed by the documented uninstall |
| S2 `packages` | dpkg repair, apt packages, vendor packages, hardware drivers, mesh VPN client; install guard on SSH and remote desktop | two scopes in order: `packages-user` (Homebrew formulae and casks, as the package owner), then `packages` (vendor `.pkg` files such as the mesh VPN client, as root) | root; on macOS the package owner, then root | yes (ledger) |
| S3 `identity` | hostname, `/etc/hosts` block, accounts and groups | computer names; accounts created with a disabled password | root | yes (homes archived, from T4) |
| G1 `access` | credential readiness: passwords, SSH keys, mesh VPN up, a second tailnet device holding a named user's key. No SSH reachability is required here | credential readiness: passwords, secure tokens, mesh VPN up (system extension approved), Remote Login and Screen Sharing enabled for named users only, a second tailnet device | human; conductor polls the non-root probes, root check once at the end | n/a |
| S4 `services` | the `agentctl` helper and its sudoers rule (atomic), workloads | none | root | yes |
| S5 `remote` | `firewall` and `ssh` are one engine transaction under one dead-man arming (no exit 4 between its steps): firewall rules, then the SSH change and the release of the SSH install guard, then the proof. Proof depends on the change: with remote access selected, a new SSH session over the mesh VPN from the second device; otherwise a local console confirmation, with no timer. On a fresh install the mesh-VPN client is installed in S2 and enrolled at G1, and `mesh-vpn` runs before the group, so the proof path exists. `remote-desktop` and `vpn` follow, each waiting for the group's disarm; `next` returns `disarm` before any later root unit. Any other firewall-touching apply is refused while no proof is possible | `firewall` only checks the Application Firewall (pf is left alone); `ssh` limits SSH to named users under a launchd dead-man; `mesh-vpn` settings; `remote-desktop` is `skipped`, because Screen Sharing is enabled by hand-over at G1; the same connectivity proof | root | yes, under the timer |
| G2 `session` | agent desktop or console session; the conductor's CLI signed in there | same, plus privacy prompts; FileVault verified | human; agent-account probe | n/a |
| S6 `agent` | shell, git identity, runtimes (mise tools), agent CLIs, agent settings, MCP, tunnel hook | same, plus workloads (OrbStack needs the agent's GUI session) | agent account | yes |
| G3 `sign-ins` | provider sign-ins | same | human; agent-account probe | revoke by hand-over |
| S7 `resources` | resource limits (section 7a): reserve, one ceiling for agent work (user slice), early OOM handling, `/tmp` policy; snapshots where the filesystem supports them; deferrable | resource limits by priority and process limits (a recorded capability gap for memory ceilings); snapshots are `skipped`; deferrable | root | yes |
| S8a `audit` | root checks, reboot acceptance | same | root | n/a |
| S8b `audit-agent` | the `audit-agent` unit runs the agent-account checks; `stable` is tagged only after its receipt is in the public export | same | agent account | n/a |

- **Order.** Each scope's preflight reads the public export (section 11) and
  stops with the next step when an earlier stage is not verified.
- **Outcomes that count as verified.** A unit's outcome is `verified`,
  `skipped` (the capability matrix says it does not apply on this
  platform), or `deferred` (allowed only for S7, with the reason recorded).
  All three count toward the stable point, and `next` returns them, so a
  macOS machine reaches `stable` without a Linux-only unit.
- **Deferring S7.** S7 is the only stage that can be deferred, because its
  limits are best set after a measured load. The deferral is recorded in
  the receipts, and the doctor reminds until it is applied.
- **The stable point** is tagged by root after S8a passes and root has
  checked S8b's record live.
- **Gate owners and records.**
  - **Root gates.** G0 and the root half of G1 run through the staged root
    executor, so their records are root's. The approval is low risk, because
    a probe is read-only.
  - **Polling G1.** The conductor polls G1's non-root probes as often as it
    likes: credentials in place, mesh VPN up, the second device enrolled, and
    on macOS Remote Login enabled with `com.apple.access_ssh` listing only
    named users. When they pass, the human runs the root check once. G1 does
    not ask for SSH reachability: on a fresh Linux host SSH stays masked by
    the install guard until S5, and S5's verify is the connectivity proof.
  - **Agent records.** G2, G3, S6 and S8b records are kept in the agent
    account's store. Root reads them to show status, never as the authority
    for a root step.
  - **Root's records.** Root writes its own records, and the `stable` tag, to
    the public export.
- **Recovery attestation** is given once, at G0, through the root executor,
  before any change. S3 `identity` and S5 `remote` check the root G0 record.
  Until T3 introduces G0, the existing `--confirm-recovery-tested` flags stay;
  T3 removes them in the same PR.
- **Owners until T3.** The scopes today are `packages`, `machine`, `agent`,
  `resources` and `audit`. Until T3 splits them into the stages above:
  - the remote-desktop install guard (section 3) is set in `packages` and
    released by the `remote-hardening` phase in the `machine` scope, which
    already holds the firewall; from T3 the remote-desktop unit releases it
    in S5;
  - the browser tunnel (section 10a) runs standalone only; T3 composes its
    workstation hook into S6.

### 2a. Units (principle 11)

Every part of the setup is a unit: for example the shell baseline, the agent
settings and MCP preset, the browser sign-in tunnel, the corporate VPN client,
resource limits, the mesh VPN client and remote desktop. Today's phases become
units and keep their names.

```text
            conductor recipe         stage (scope)            standalone use
                   |                      |                         |
                   +----------+-----------+-------------------------+
                              v
              wskit <unit> plan | apply | verify | revert | describe  --json
                              |
                              v
              shared transaction layer (T2): preflight, preview, backup,
              intent, apply, verify, receipt, ledger, undo
```

- **Standalone.** Each unit has its own entry point,
  `wskit <unit> plan|apply|verify|revert --json`, plus `describe`. Each has its
  own preflight, preview, backup, receipt and undo through the shared
  transaction layer. A unit can be installed, upgraded or reverted on a
  machine without running any stage.
- **Composed.** Stages and the conductor recipe call the same entry points,
  in order. No stage holds unit logic of its own, and no unit relies on
  running inside a stage.
- **Order rules.** `setup --all` enforces stage order. `wskit UNIT` and
  `setup --unit` enforce only that unit's dependencies. A stale dependant
  waits until its dependencies verify. A unit whose dependency is
  unsupported or left out is `skipped` with the reason, never refused.
- **Declared once.** Each unit declares:
  - its inputs (schema fields, from T1);
  - its dependencies: other units, capabilities and OS support;
  - its privilege: user or root scope;
  - the paths and state it owns (`--describe`).

  `plan` resolves the dependency order. A unit that is unsupported on the
  target is `skipped` with the reason; a unit that conflicts with another
  is refused, naming both.
- **Documented the same way.** Each unit has a page with what it does, why,
  the options, the safe default, the risks and the undo, plus how to run it
  standalone and how it composes.
- **Tested both ways.** Each unit's tests cover standalone apply and revert.
  The composed run covers the order, interruption and revert to baseline.
- **How T1 to T8 map onto it:**
  - T1 builds the CLI skeleton, the unit interface and the JSON shapes, the
    schema, platforms, `plan` and `next --json` (section 2b);
  - T2 is the shared engine that every unit's backup, intent, receipt and
    undo go through;
  - T3 makes stages and gates compose units;
  - units declare their inputs in the schema from T1; T6 records the tools
    each unit installs;
  - T7 and later components are units; T8 runs every unit's `verify`.
- **Before T2.** A unit that ships between T1 and T2, which is only the
  browser tunnel, uses T1's CLI, unit interface and platform table, and
  carries its own backup and undo behind the same verbs. When T2 lands, it
  moves onto the shared engine without changing its verbs or its records'
  meaning.

### 2b. Implementation: Go, one CLI, units as subcommands

This is a user decision (2026-11-21).

- **Language.** All logic is written in Go and built in CI without cgo
  (`CGO_ENABLED=0`), one binary per OS and CPU: Linux on amd64 and arm64,
  macOS on arm64. `darwin/amd64` is left out on purpose, because only Apple
  silicon Macs are supported. Each
  release carries checksums and a signature, pinned in the registry.
  - Only two things are not Go: data (the TOML schema, the registries and
    dotfile templates), and the day-zero one-liner, a few lines of POSIX sh
    that download the pinned binary, verify its checksum and run it.
  - **Why not Python:** macOS has Python 3 only once the Command Line Tools
    are installed, and root tooling should not depend on an interpreter
    path.
  - **Why not Rust:** both are safe, but Go builds fast on both OSes and is
    quicker to write and review for orchestration and system calls.
- **The existing Python and shell are frozen:** fixes only. Each piece is
  replaced by a Go unit and deleted once the unit reaches parity, shown by
  its tests. Until then, the frozen executor keeps its current checks.
- **The CLI.** Its working name is `wskit`. It replaces `agentctl`,
  `fleetctl` and `setup-workstation.sh`.

  ```text
  wskit init | interview
  wskit plan [--unit a,b]
  wskit setup [--all | --stage services | --unit vpn] [--apply]  preview by default
  wskit <unit> <verb> [flags]                                  each unit standalone
  wskit status | doctor | explain <unit|field>
  wskit next --json                                            conductor interface
  wskit validate | approve | migrate-profile
  wskit restore-points | revert --to <point|baseline> | resume [--restore-only]
  wskit disarm --run RUN-ID | propose-change FILE
  wskit stage | registry check|bump | docs gen
  wskit tunnel auth|open|host|hook                             the browser-tunnel unit
  wskit config get|set|explain|validate                        layered config
  wskit fleet init | validate | check [--host] | privacy-lint
  wskit fleet host add|show|record|retire | lock show|bump | values get|set|explain
  wskit version | completion bash|zsh|fish
  ```

  - **Common flags:** `--json`; preview by default, with `--apply` to make
    changes; `--config`; and the approval phrase for root scopes.
  - **Privileges.** User-scope units never need root. A root-scope verb
    runs as `sudo /opt/agent-workstation-kit/current/bin/wskit ...` for that
    step only.
- **Packages:**

  | Package | What it holds |
  |---|---|
  | `engine` | The transaction layer (section 5) |
  | `planner` | Dependencies, owned paths and conflicts |
  | `schema` | Validation, the interview and generated docs |
  | `registry` | Pins and checksums |
  | `platform` | The platform and capability matrices, and the Linux and macOS adapters |
  | `units/*` | One package per unit. Each implements one interface (`Describe`, `Plan`, `Apply`, `Verify`, `Revert`, `Status`) and holds no engine logic |
  | `conductor` | The JSON interface (section 4) |
  | `docs` | Generation from the schema |

- **Units,** about 20, each in exactly one stage. The one mapping of unit
  to domain, stage, scope, dependencies and epic is the unit catalog; it is
  generated from `registry/units.toml`.

- **CLI conventions, defined once** and shared by every command and unit:
  - preview by default, with `--apply` to make changes;
  - `--json`, `--verbose` and `--quiet`;
  - `status` and `explain` for every unit;
  - documented, typed exit codes (section 4);
  - shell completion for bash, zsh and fish; `version`; generated help and
    man pages;
  - the same verbs and flags across units, declared in one place in
    `internal/cli`, so a unit cannot invent its own spelling.

  **Framework:** cobra with Charm's fang, both pinned in `go.mod` and listed
  in the registry. Cobra gives the command tree, completions and man pages;
  fang styles help and errors with the same Lip Gloss theme as the terminal
  UI. Kong was the alternative; cobra is chosen because its completion and
  man-page generation are built in and widely used.
- **Fleet management in the same CLI.** The private fleet repository holds
  config and records only, with no fleet code. `wskit fleet`:
  - `init` creates a private fleet repository from the kit's template;
  - `validate` checks the schema and that company and machine files agree;
  - `host add|show|record|retire` manages machine records;
  - `check [--host]` is the doctor with fleet inputs;
  - `privacy-lint` is the keyed identifier scan (section 12), generic;
  - `lock show|bump` shows and moves the kit pin, verified against the
    release checksum and signature;
  - `values get|set|explain` reads and writes company values with their
    schema docs.
- **Everything configurable, from one schema.** Values come in layers, each
  overriding the one before: kit defaults, then fleet or organisation, then
  machine, then user, then environment variables, then flags. `wskit config
  get|set|explain|validate` shows each effective value and the layer it came
  from. The safety floor is not configurable, and the docs say so: backups,
  undo, the dead-man timer, secret handling, and the approval phrase for
  root.
- **Data is embedded.** The schema, the registries, dotfile templates and
  docs templates are compiled into the binary with `go:embed`, so the
  one-liner fetches only the binary. `go:embed` cannot reach parent
  folders, so one small package at the module root, `assets.go`, embeds
  `schema/`, `registry/`, `dotfiles/` and `templates/`, and other packages
  read the data through it. A fleet adds to the data, never replaces it:
  with `--fleet-root`, `wskit plan` reads the fleet's `[x-org]` schema
  fragment, its registry entries, its templates and its declarative units.
  - **Merge rules.** A fleet entry whose ID duplicates a kit ID is refused.
    Only new keys under `[x-org]` are accepted; a fleet value for a kit
    field is a value in the fleet layer (section 2b, config layers), never a
    change to the field's definition. A fleet entry cannot loosen a kit rule
    (a pin policy, a refusal or a platform).
  - **Approval binds to exact inputs.** Approval records the fleet's
    reviewed commit and the digest of the resolved plan. Every fleet file
    the plan read is staged with the plan, so a root step reads exactly
    what was approved.
- **Go structure** (signatures and layout details in the P1 core spec). The
  Charm libraries are pure Go, so `CGO_ENABLED=0` builds hold for every
  target; Huh runs inside the one Bubble Tea program so it never takes the
  signals; darwin binaries are signed on macOS (a Linux cross-compile is
  unsigned); `systemd`, `launchctl` and other OS tools are called through
  `os/exec`.
  - one module, whose path is the repository's own path, so `go install`
    and vulnerability tooling resolve it;
  - `cmd/wskit` and `cmd/agentctl` hold `main` only; everything else is
    under `internal/`; a public `pkg/sdk` comes only with plugins, after
    1.0.0 (section 2d);
  - data folders are embedded through one package at the module root;
  - one `Unit` interface (`Describe`, `Plan`, `Apply`, `Verify`, `Revert`,
    `Status`); units change the machine only through `engine.Tx` and report
    only through non-blocking events;
  - Go types and constants are generated from the schema and the unit
    catalog; rules that depend on the host or on other values are checked
    at run time;
  - libraries are pinned to exact versions with registry entries: one TOML
    library, one canonical-JSON library (RFC 8785), cobra and fang, the
    Charm terminal libraries, and `golang.org/x/sys`.
- **Terminal UI for people, plain output for agents** (user requirement,
  2026-11-21; P1, next to the CLI skeleton).
  - **One rendering layer,** `internal/ui`, with two front ends over the
    same typed events and results: a terminal UI and plain or JSON output.
    Units emit typed events through the engine and never print.
  - **People, on an interactive terminal:** a guided UI built on the Charm
    toolkit: Bubble Tea for screens, Huh for the interview forms and
    confirmations, Bubbles for spinners, progress and lists, Lip Gloss for
    styling, and Glamour for `explain` and the docs.
    - The interview is grouped forms with defaults, inline validation from
      the schema, and a "why, risk, undo" panel per field from the same
      schema.
    - A plan view shows units by stage, the changes, the risks, and values
      set away from their defaults.
    - Apply shows live progress per step, clear confirmations for root
      steps (the approval phrase), each hand-over as a card (Machine,
      Account, Command, Why, Verify, Undo), and a final report with the
      stable point.
    - Status and doctor are a compact dashboard, problems first; `--verbose`
      adds detail.
  - **Agents,** the preferred way to run setup, get stable, versioned JSON
    or plain text, with no animation, no colour codes and no prompts.
  - **Which front end runs,** in order of precedence:
    1. `--json`: the result as JSON on standard output only; events as JSON
       lines on standard error; tool output goes only to the log;
    2. `--plain` or `WSKIT_PLAIN=1`: plain text, no prompts;
    3. the terminal UI only when both standard output and standard error
       are terminals, `TERM` is not `dumb`, and neither of the above is set;
    4. otherwise plain.

    The conductor recipe sets `WSKIT_PLAIN=1`, so an agent running in a
    pseudo-terminal never gets the terminal UI. Bubble Tea's own signal
    handler is turned off, so signals always reach the engine's one cancel
    path. Every interactive question
    has a flag or config equivalent, so nothing needs a terminal. `next
    --json` drives the conductor.
  - **Accessibility.** `NO_COLOR`, `TERM=dumb`, narrow terminals and screen
    readers (`--plain`) are respected.
  - **Secrets** never appear on screen; secret inputs are masked.
  - **Tests:** snapshot tests for plain and JSON output, and golden-file
    tests for the terminal views (`teatest`).
  - Every Charm dependency is pinned in `go.mod` and listed in the registry.
- **One tool going forward.** From the P5 cutover, `wskit` is the only tool
  for the kit and for every private fleet. Logic that a fleet holds today
  moves into generic kit units driven by fleet values, or into the fleet's
  declarative units (section 2d). The fleet keeps company
  values, machine records and organisation-only extensions. A fleet
  adoption epic covers the convergence run for existing hosts, migrating
  the fleet repository to `wskit fleet`, and retiring the fleet's scripts.
- **How the earlier sections read in Go.** Phase scripts become units.
  `--describe` is the unit's `Describe`, and a phase's apply is the unit's
  `Apply` as the engine runs it. Commands named in this document are `wskit`
  commands: for example `wskit next --json`, `wskit approve`, `wskit resume`
  and `wskit interview`.

### 2c. Revert everywhere

The kit's headline guarantee: every unit and every stage can return to the
previous stable point, or to the baseline taken before the first change.

- **Per unit.** Each unit's `Revert`, with the engine's restore (section
  5), returns its owned paths and state to the unit's previous receipt.
  The unit catalog lists every unit; none is exempt, and a unit that cannot
  revert a part (a package upgrade, say) lists it in the cannot-undo list.
- **Per stage and point.** `revert --to POINT` walks the units back in
  reverse dependency order, each step a transaction.
- **The cannot-undo list** (section 9) appears in every preview: apply,
  revert and `next`.
- **Hand-built hosts.** At their first convergence run they get an
  `as-found` baseline. Hand changes made before that run are recorded in
  the machine record, not reverted.
- **Proof.** Every unit epic's acceptance includes standalone apply and
  revert tests, and an interrupted revert that resumes. The composed run
  ends with `revert --to baseline` passing the match digest (section 5).

### 2d. Extensibility

- **Functional domains** are unit metadata: `system`, `identity`,
  `network`, `remote`, `agent`, `workspace`, `data` and `ops`. Commands stay
  flat (`wskit vpn up`); `wskit list [--domain D]` groups units by domain.
  Stages and profiles compose units by domain and dependency.
- **A unit catalog.** A new kit unit implements the `Unit` interface and is
  added to `registry/units.toml`; `go generate` writes the catalog list
  (`internal/units/catalog_gen.go`), so no core file is edited by hand and
  no package-level registration state exists. `wskit dev new-unit NAME
  --domain D` creates the package from a template, with tests and a docs
  page, and the docs explain how to add a unit.
- **The rule for fleets: config first, extension second, never a separate
  organisation tool.**
  1. **Generic, configurable units.** A fleet uses kit units with its own
     values. For example, `wskit vpn` is one unit for every fleet, with
     provider profiles (`ipsec-ikev1-xauth-psk`, `ipsec-ikev2`, and
     WireGuard later) and the gateway and destinations from fleet values.
     The route-based tunnel, safety rules, dead-man, published status and
     destination kinds come into the kit generically, from a fleet's
     reference implementation (its design and tests, no organisation
     values). Fleets run `wskit vpn up`, never a fleet-named variant.
  2. **Typed extension points inside units.** For example, VPN destination
     sources: built-in `gke`, `srv`, `gitops-allowlist` (a reverse-proxy IP
     allowlist reader), `dns` and `prefix`. Whatever a source returns still
     goes through the unit's safety rules, caps and undo.
  3. **Declarative units** in the fleet: TOML recipes validated by the
     schema, built only from typed engine operations (below).
- **Declarative units use typed operations only (P3).** The operations are:
  packages, files from templates, managed blocks, services, users and
  groups, and checks. Each maps to an `engine.Tx` call with declared owned
  paths, so backup, undo and the owned-path rule apply. There are no
  arbitrary command steps and no `command` destination source until an
  enforceable isolation boundary exists.
- **After 1.0.0, not in this plan:**
  - **Command steps and `command` sources,** with plugins, under the same
    isolation contract.
  - **Plugin executables** (`wskit-NAME`, shown as `wskit fleet NAME`),
    built against a small public Go SDK (`pkg/sdk`) and pinned by checksum
    in the fleet lock, with a versioned JSON protocol. They need their own
    isolation, trust and recovery contract first, because a separate
    process can escape `engine.Tx`.
  - **Custom fleet-built `wskit` binaries:** a possible later option through
    the same SDK. It would freeze the internal API and need per-fleet
    signing.
- **Fleet data stays add-only** (section 2b).
- **Tests:** a sample declarative unit under `testdata/`, taken through
  apply, revert, an interrupt and a refusal (an operation outside the
  declared paths).
- **When:** the catalog, domains and the `Unit` interface in P1 (T1); the
  declarative units with typed operations after 1.0.0 (EPC-029), so 1.0.0
  ships with kit units only.

### 2e. Desired state, converged

- **The machine config declares what is wanted:** `[tools].enable`,
  `[mcp].servers` and each unit's options under `[units]`. `wskit setup`
  converges the machine to it.
- **Adding later is one more unit.** The plan shows only the delta; removing
  something is that unit's revert. First setup and later changes use the
  same code path and transactions.
- **Direct verbs record themselves in the config.** `wskit tools add|remove
  TOOL`, `wskit mcp add|remove SERVER` and `wskit UNIT apply|revert` propose
  the config change and the machine change together, and preview both by
  default. `wskit config set ...` followed by `wskit setup` does the same
  thing.
  - **User-scope verbs** may apply locally with `--apply`: the config is
    updated and the same transaction runs, so the config matches the
    machine.
  - **Root-scope verbs, and any change to a config that lives in a fleet,**
    draft the config change (a fleet PR, or `wskit approve` for a kit-only
    machine) and stop. The machine change runs only after that config
    commit is approved.
  - **Removing a component** (`remove`) takes it out of the config and runs
    its unit's revert to absence, in reverse dependency order, refusing
    while other enabled units depend on it. **Rolling back** (`revert`)
    returns a unit to its previous receipt and leaves the config's
    component list unchanged.
- **A generic, registry-driven `tools` unit.** Most CLIs (Google Workspace,
  GitHub and GitLab CLIs, cloud CLIs, agent CLIs) need no code. A registry
  entry gives:
  - the pinned version per OS;
  - the source and its checksum or signature;
  - the install and uninstall method;
  - the verify command;
  - an optional sign-in hand-over;
  - capability notes, such as "no headless sign-in on macOS".

  A new CLI is a reviewed registry PR. An entry whose method needs root (an
  apt or `.pkg` install) is installed by `packages` in S2 from the same
  entry.
- **An MCP catalog,** `registry/mcp.toml`. Each entry gives the name, the
  endpoint or pinned command, the harnesses that get it, and the auth type
  (OAuth, or a client ID and callback port where dynamic registration is not
  supported). `wskit mcp add` applies it to every agent config home through
  the `mcp` unit (section 10), and the sign-in is a hand-over. It replaces a
  fleet's interim MCP scripts.
- **Support limits show in `plan`,** from the capability matrix, never as
  surprises during apply.
- **The interview** offers `[tools]` and `[mcp]` choices from the registry
  and the catalog (T5).

### 2f. No lock-in (principle 12)

- **Drift is reported, never silently reverted.** `status` and the doctor
  show manual or agent changes per unit. For each, the person chooses:
  `wskit adopt UNIT` (write the current state into the config and record a
  new receipt), re-apply the config, or ignore it (recorded as accepted
  drift). `setup` shows drift in its plan and never overwrites a manual
  change without one of those choices. The dead-man restore of the kit's
  own in-flight SSH or firewall step is the one automatic undo. Drift
  reconciliation is separate from in-flight recovery, which follows the
  intent record (section 5). For a root unit or a fleet config, `adopt`
  uses the draft-and-stop path (section 2e).
- **Shared files are changed only inside the kit's marked blocks or its own
  drop-in files** (`~/.zshrc`, `~/.zshenv`, `~/.ssh/config`, `sudoers.d`,
  firewall rules, `environment.d`). Content outside them is never touched,
  and tests prove it.
- **Outputs are standard and readable without `wskit`:** plain systemd units
  and launchd plists, `sshd_config.d` drop-ins, dotfiles and plain TOML. No
  daemon runs, and no kit state is needed for the machine to keep working.
- **Walking away is always possible.** `wskit unmanage UNIT` removes the
  unit's active membership and leaves the machine as it is; its history and
  point references stay as a tombstone, with an accepted match exception, so
  a later revert to baseline still passes. `wskit adopt UNIT` is the
  reverse. Removing `wskit` entirely leaves the machine
  working. Only `revert` undoes, and only on request.
- **Every unit page documents its manual equivalent:** what the unit
  changed, and how to change it by hand.

### 2g. Presets, form factor and platforms

- **Presets have two axes,** the same on macOS and Linux, laptop or desktop:
  - **purpose:** `developer` (a person's own daily machine) or `agent-host`,
    which adds remote access, the mesh VPN, the dead-man timer for remote
    changes, enforced resource limits and always-on services;
  - **tier:** `minimal` (shell, git identity, core runtimes), `standard`
    (the default: minimal plus registry CLIs, agent CLIs, agent settings and
    MCP, resources, and the tunnel where relevant) or `full` (every unit
    feasible for this OS, version, form factor and purpose).

  A machine's units are the preset, plus the person's additions, minus
  their removals (section 2e). Units the capability matrix marks
  unsupported, or that the preset leaves out, are `skipped` with a stated
  reason, never failed. The defaults are `developer` and `standard`; the
  pilot box is `agent-host` and `full`. `wskit plan --purpose X --tier Y`
  previews any combination. Presets live in `registry/presets.toml`.
- **Form factor** (`portable` or `fixed`) is detected (the Linux chassis
  type; the macOS model and battery) and can be overridden. It adjusts only
  these defaults, each shown in `plan` with its reason:
  - power and sleep: no always-on services or timers that assume the
    machine is awake on a battery machine, and battery- and thermal-aware
    resource budgets;
  - network changes: firewall defaults for untrusted networks, and VPN and
    tunnel roaming;
  - incoming remote access is off by default on portable machines unless
    chosen;
  - disk encryption is required on portable machines and warned on fixed
    ones;
  - backup and snapshot timing that is aware of sleep.
- **Presets narrow the gates.** `plan` keeps G1, G2, S4 and S5 only for the
  units selected; a dropped gate or stage is `skipped` and counts toward the
  stable point. A default `developer` and `standard` run on a portable
  machine reaches S6 with no remote-access gate.
- **Which axis wins.** Purpose decides which units are selected. Form factor
  adjusts only the five defaults of the selected units. An explicit setting
  overrides both, and an `agent-host` on a portable machine gets a warning
  in `plan`. Disk encryption on a portable machine is a safety requirement,
  not a default: a hand-over that refuses to continue until its probe
  passes.
- **Account topology** is chosen in the config (asked after the tier, with
  the default from the purpose), and both are supported equally. Roles are
  modelled separately from the number of accounts:

  | | Separate (default for agent-host) | Single (default for developer) |
  |---|---|---|
  | Accounts | admin, one per named person, agent | one account |
  | Roles held | admin: root steps; person: sign-ins, SSH; agent: agent work | all three |
  | Who may SSH | named people only, never the agent account | that account, when SSH is selected |
  | Where the conductor runs | the setup user until G2, then the agent account (section 4) | that account throughout |
  | Accounts revert may remove | those the kit created, never the one the person signs in with | none |
  | Resource boundary (section 7a) | the agent account's user slice | a separate agent-work slice for processes the agent manager starts, or an opt-in with the limit stated in `plan` |
  | Browser tunnel (section 10a) | `skipped` with its reason in 1.0, recorded as a capability gap: the hook store is private to the agent account, but the fetch runs as the person over SSH; a forced-command design is planned after 1.0 | supported: one account owns the hook store and the fetch, over SSH as that account (this also covers a person who runs agents in their own account) |

  Acceptance covers both topologies end to end.
- **Baselines.** The root baseline is required before any machine change. A
  user-scope unit run on its own needs only an account-local baseline, kept
  in the account state (section 11).
- **Selectors and overrides.** `--stage` takes a scope ID. `--platform` and
  `--form-factor` are preview selectors. `--set`, `--add`, `--remove` and
  `--config` are persistent overrides written to the config.
- **Platform detection is automatic.** The platform layer detects the OS,
  version and architecture and admits only what `registry/platforms.toml`
  lists. People never pass the OS. `--platform OS-VERSION` and
  `--form-factor` exist only for `plan` and preview (CI fixtures and
  reviews); `apply` refuses them. OS adapters sit behind interfaces, and
  capability gaps are reported at plan time.
- **Windows is out of scope.** Adding it later means new adapters and
  capability rows, not a redesign.

### 2h. Best practices on by default (principle 13)

- Defaults across every configuration area apply the learned best practice
  automatically, shown in `plan` with their reason; each is configurable or
  can be opted out.
- **Resources,** proven on the pilot: the agent-work memory and CPU ceiling
  as shares of RAM with early OOM handling (systemd-oomd; launchd and
  taskpolicy on macOS); swap and zram sized as a share of RAM; `/tmp` kept
  disk-backed or caches redirected off tmpfs; disk free-space floors with
  cleanup guidance; the resource runner and sweep wired into the agent
  settings bundle.
- **macOS:** development volumes detected and confirmed; Spotlight and the
  FSEvents on-disk log off for them; Time Machine excluded where a
  destination exists (section 7b).
- **Elsewhere,** the same approach: security hardening, pinned versions,
  update policy and log rotation.
- **Rules:** defaults come from one table and may differ by purpose, tier
  and form factor; each has a reason and a source in
  `registry/sources.toml`; `wskit explain SETTING` shows the default, the
  reason and how to change it; tests show each default is applied when the
  setting is unset, and that every opt-out works.

### 3. One apply path (T1)

- **Through the engine only.** A unit's `Apply` and `Revert` are reached
  only through the engine: `wskit <unit> apply`, `wskit setup --apply` and
  `wskit revert` all call it. Units cannot call each other's `Apply`. A
  repository test fails if any package other than `engine` calls them. In
  T1, before T2's full layer, the engine already holds the host lock and the
  journal lock, checks the platform and the approved plan, and writes
  receipts.
- **Nothing leaks to tools.** The engine starts external tools with Go's
  `os/exec`, which passes only standard input, output and error unless a
  descriptor is listed. It passes none, so no lock or failure descriptor
  reaches a tool, and a daemon a package starts can never hold the host
  lock. Each tool's environment is built from an allowlist, never inherited
  whole: `PATH` (set to the system default, not the caller's), `HOME`,
  `USER`, `LOGNAME`, `LANG`, `LC_ALL`, `TERM`, `TMPDIR`, and
  `DEBIAN_FRONTEND=noninteractive` on Linux. On macOS, the package owner's
  tools also get the Homebrew prefix (`/opt/homebrew/bin`) at the front of
  `PATH`. A unit adds a named variable only by declaring it in `Describe`.
- **Tools never outlive the lock unnoticed.** Every apply runs its tools
  inside OS containment that outlives the engine and can be emptied:
  - **Linux:** a transient systemd scope per apply, named after the run ID
    (in the system manager for root, in the user manager for an account
    where one runs; otherwise a process group, recorded as a gap);
  - **macOS:** a launchd job per apply (in the system domain for root, in
    the user's domain for an account), labelled with the run ID.

  The containment's name is written to the intent before launch, and
  process identities are recorded only after the supervisor acknowledges
  the launch. An identity is the boot ID, the process ID and its start
  time, so a reused ID or a process from an earlier boot is never touched.
  If the engine dies, the lock is released but the intent remains. The next
  start reads the intent first and empties the containment within a bounded
  time (wait for the grace period, then terminate, then kill); if it cannot,
  it refuses with exit 4 instead of taking the lock. Only then does it
  restore. This minimal intent and recovery is part of T1. The protocol is
  a state machine in the P1 core spec.
- **Package managers are never killed.** A package-manager transaction (dpkg,
  apt, Homebrew, `installer`) is never terminated by cancel, the grace
  period or the dead-man timer: they wait for it to finish, and a cancel
  while one runs is refused with a message. Recovery never restores
  package-database files; packages are reverted only through the package
  manager.
- **Durable writes.** Every record (intent, receipt, journal entry, export
  file) is written to a temporary file in the same folder, fsynced,
  renamed into place, and then the folder is fsynced. The order is: intent
  before apply; receipt after verify; the public export after the receipt.
  At start, receipts are the authority, and the export is regenerated from
  them when the two disagree. A temporary file left by a crash is ignored
  and removed.
- **One root-approval check, before any lock.** A root step checks the
  approval phrase and the approved plan digest once, before it takes any
  lock or writes any intent.
- **A refused root approval changes nothing.** When the person declines the
  approval phrase, or `sudo` fails, no mutation starts. The engine records a
  `blocked` hand-over for that step, keeps every earlier receipt, and exits
  3. The conductor reports it, does not retry by itself, and asks the person
  again only after they say so.
- **Two binaries.** `wskit`, and the minimal sudo helper `agentctl`
  (below). Nothing else ships as an executable, apart from the day-zero
  `bootstrap.sh`.
- **Staged binary check.** As root, `wskit` refuses to run (exit 5) unless
  its own resolved path is one of these, with the whole parent chain
  root-owned and not writable by group or others:
  - from T1 to T5, the fixed staged path
    `/opt/agent-workstation-kit/bin/wskit`;
  - from T6, `/opt/agent-workstation-kit/current/bin/wskit`, where
    `current` resolves under `releases/`;
  - a dead-man restore copy in a root-only backup folder (section 6), run
    only by the timer unit after the unit itself has checked its digest.

  This replaces `path_safety.require_staged_root()` and the root block of
  `common.sh` for Go code.
- **First install and staging use system tools only.** No `wskit` runs as
  root before it is staged. The documented root step, the same from T1 on:
  1. download the binary, the checksum file and its signature into a
     root-owned temporary folder (`curl`);
  2. verify the signature of the checksum file with `ssh-keygen -Y verify`
     against the release public key embedded in the docs and the
     one-liner;
  3. verify the binary against that checksum with the system tool
     (`sha256sum` on Linux, `shasum -a 256` on macOS);
  4. install it at the staged path with `install -o root -m 0755`.

  The owner signs the checksum file with the release key when publishing,
  from the first Go release. The binary never checks itself. From T6,
  `wskit stage` performs later upgrades the same way, starting from an
  already staged and trusted binary.
- **macOS Gatekeeper.** `curl` sets no quarantine attribute, so Gatekeeper
  does not block the downloaded binary; Go signs `darwin/arm64` binaries
  ad hoc, which Apple silicon requires. Notarization is not needed for this
  path. When a person downloads with a browser instead, the docs give the
  verify steps above, then `xattr -d com.apple.quarantine`.
  CI tests this on Linux and macOS runners: it stages a built binary with
  `sudo` into the fixed path and checks that the root check passes there
  and refuses a user-writable copy, a symlinked parent and a group-writable
  folder.
- **Standalone rules per scope:**

  | Scope | Plan when run standalone | Locks | Run folder |
  |---|---|---|---|
  | User (an account's own files) | With no profile, `wskit <unit> plan` builds a plan for that unit alone, from its flags, config and schema defaults. The person approves it by running `--apply` on that plan. | the account lock `account-setup.lock` in the account run folder, and the journal lock next to the account journal | the account run folder (section 11) |
  | Root | Always from an approved, exported plan (section 8), even for one unit | the host lock `privileged-setup.lock` in the root run folder, and the journal lock next to the root journal | the root run folder (section 11) |

  The host lock applies only to root scopes. The frozen executor uses the
  same lock files (its macOS lock moves with the T1 fix).
- **Receipts and backups in T1.** Before T2's full layer, T1's engine
  already gives units a minimal file transaction through `engine.Tx`: each
  file a unit writes is backed up first (owner, mode, digest, or recorded
  as absent), written atomically, and listed in a receipt. `revert` restores
  from that receipt. The tunnel unit uses only this, so it holds no engine
  logic, and T2 extends the same `engine.Tx` without changing the unit.
- **Two apply paths during the move.** Until a unit reaches parity, its
  phase is applied by the frozen executor only. The PR that brings a unit to
  parity makes the frozen executor refuse that phase, so each phase has one
  owner at a time. `wskit` takes the same host lock and journal lock files
  as the frozen executor, so the two can never apply at once.
- **The sudoers helper stays narrow.** Today the agent account's sudoers
  rule names the narrow `agentctl` helper. `wskit` takes over agentctl's
  setup and management, but the root actions granted to the agent account
  stay in a separate minimal binary, `cmd/agentctl`, with exact subcommands
  and arguments. The sudoers rule never names `wskit`.
- **macOS host lock.** The run folder moves under a root-owned 0755 parent
  (section 11). The engine creates that parent,
  `/Library/Application Support/agent-workstation-kit/`, and its `run/`
  folder itself, as the first step of any root scope, and refuses to go on
  if an existing one is not root-owned or is writable by others. T2 later
  adds the other state folders. A test runs on real macOS in CI.
- **Remote desktop (Linux).** The packages unit masks NoMachine's service before
  the package is installed, with its record written first. It stays masked
  until the firewall is in place and the remote-desktop unit releases it in
  S5. This closes the exposure that the SSH install guard already closes for
  SSH (`package_setup.py` `guard_new_ssh_install`). On macOS there is no
  NoMachine masking, and `remote-desktop` is `skipped` (section 2).
- **Fixes to the frozen code in T1.** These close live problems in the
  context table while the Python and shell remain in use:
  - `fleetctl run --apply` exits 2 and prints the executor commands; preview
    and the read-only `audit` stay. T1 updates every document that shows it
    and adds a repository check that fails if one returns;
  - the macOS host lock fix above, in `execute-setup.py`, so macOS root
    scopes run again;
  - the same remote-desktop guard for `install-local-deb-linux.sh`: it
    refuses to install NoMachine unless the firewall is active.

  The vendor-package and driver scripts become parts of the packages unit,
  and are deleted at parity.
- **One OS policy.** The platform check (section 9) replaces all four version
  checks in the context table.
- **CI** builds and tests the binary on Linux and macOS for every target
  above. While frozen Python remains, it keeps its jobs, plus a Python 3.9
  job for the root executor. Where hosted runners lack a supported macOS
  version, a recorded run on a real machine is the evidence.
- **Fleet scripts** have no apply path of their own; they call `wskit`.

### 4. The agent interface (T1, extended by each PR)

- **`wskit next --json`** returns the next action. It finds the
  plan from the path recorded at export, or from `--plan-file`. Its fields:
  - `schema_version`, `run_id`, `stage`, `unit` and `owner`;
  - `root` and `gate`;
  - `preview_argv` and `apply_argv`: argument arrays, never shell strings;
  - `handover` and `probe`;
  - `log_path`;
  - `blocked_by` and `outcome` (`verified`, `skipped`, `deferred`,
    `failed`, `stale` or null).

  This is the one `next` schema; its JSON schema and fixtures are in
  `schema/json/` (section 15).

  For a root step it exits 3, with the hand-over carrying the staged command.
- **`status --json`** returns every phase's recorded status, run ID, receipt
  time and failure record (from T2). It reads the public export plus the
  caller's own records.
- **`explain UNIT --json`** returns what a unit does, its owner, what it
  may touch (from `Describe`, T2), and whether that is reversible.
  `explain FIELD --json` does the same for a schema field.
- No JSON output carries a secret. Each schema has a version, golden-file
  tests and a checked example in the docs.
- **Exit codes**, the same for every action:

  | Code | Meaning |
  |---|---|
  | 0 | Done, or nothing is next |
  | 1 | A phase failed; read its failure record |
  | 2 | Refused, or a usage error |
  | 3 | A person must act; the hand-over says what |
  | 4 | Blocked by recovery: an interrupted step in this journal must be restored and reapplied first (`resume`); or the step is guarded by an armed dead-man timer, which only `disarm` or the timer may change (the message points to `disarm`) |
  | 5 | Refused as root: not the staged binary (section 3) |
  | 6 | Drift found: `verify`, `status` or the doctor saw live state that differs from the receipts |
  | 130 | Cancelled at a safe point |

  Snapshot results are informational and never change the exit code.

- **The conductor recipe** in `AGENTS.md` is a loop: read `next`, act or hand
  over, verify, report, repeat.
  - It previews and applies the non-root steps.
  - For a root step, it shows the prepared command and waits for the human.
  - At a gate, it shows the hand-over as is and polls the probe.
  - **Rules:** no root and no secrets; stop on the second identical failure;
    never edit receipts or journals; never approve or revert by itself.
  - **Its user update** says what was done, what is next, and what the human
    must do.
  - A private fleet's `AGENTS.md` gets the same recipe from the template
    (section 10b).
- **Approvals.** Root approvals are enforced by `sudo` and the typed command.
  A non-root plan approval (the approval phrase, today `--approve-plan`) is
  procedural. The conductor could pass it
  itself, so the recipe forbids that, and the receipts record who ran the
  step. Config approval depends on where the profile lives:
  - **With a private fleet,** it is enforced by the fleet's branch
    protection: the plan uses only the committed profile.
  - **Kit only, without a fleet,** the profile lives in a private folder
    outside every kit checkout. The human runs `wskit approve PROFILE` on
    a real terminal. It shows the values set away from their defaults, asks
    for the profile's short digest to be typed back, and records the digest
    in the setup user's state. `plan` refuses a profile whose digest has no
    approval. This is procedural, like `--approve-plan`, and the recipe
    forbids the conductor from running it.
- **Where the conductor runs.** With a separate working account, it runs as
  the setup user until G2. At G2 the human signs the conductor's own CLI in
  inside the agent account, which is the one sign-in that moves earlier than
  G3. The conductor runs S6, G3 and S8b from there. On Linux the agent
  account is denied SSH, so this is a desktop or console session. With a
  single account (section 2g), the conductor runs in that account
  throughout, and G2 only checks the session.
- **Reverts past S3** remove the agent account. They are therefore driven
  from the setup user, and the executor refuses to remove the account its
  caller is running in.

### 5. The transaction layer (T2)

```text
apply:  preflight -> describe -> backup -> record(apply) -> apply (logged) -> verify -> receipt
                                                 |                                 |
                              restart finds it --+               failure ----------+--> restore
                              restore, then reapply                                     -> failed receipt
revert and restore-only have their own records and recovery (below, and the P1 core spec)
```

- **Describe.** Each phase answers `--describe` with JSON:
  - `paths`;
  - `state` providers: hostname, ufw, unit masks, sysctl, packages, accounts,
    launchd jobs, Application Firewall;
  - `irreversible` items.

  The executor fails closed when a phase does not answer.
- **Backup.** Before the apply, each item is copied with its owner, mode,
  ACLs, extended attributes and digest, or recorded as absent. Where a
  filesystem does not support ACLs or extended attributes, the manifest
  records that they were not captured, and the revert preview says so.
  State providers write snapshots of their state.
  `manifest.json` is written and fsynced last, so a manifest exists only for a
  complete backup. A kill during the backup leaves no manifest, and the next
  start backs up again.
- **Intent, per operation.** Each operation writes its own record, naming
  it, before it changes anything: `apply`, `revert` or `restore-only`. An
  unfinished record blocks its own journal (the root journal, or that
  account's journal) with exit 4 until it is handled; other journals are
  not blocked. While the step's dead-man timer is armed, section 6 decides
  instead: only `disarm` or the timer may change it. Otherwise, at the next
  start (`wskit resume`):
  - **an interrupted apply** is restored from its manifest, then reapplied,
    wherever the crash happened, including after a passed verify and before
    the receipt. There is no verify-only shortcut, because a half-applied
    state can pass verify;
  - **an interrupted revert** resumes at the first undo step it had not
    completed, and never reapplies;
  - **`resume --restore-only`** restores an interrupted apply and stops.

  The state machines and the torn-state table for each operation are in the
  P1 core spec, with tests that interrupt every undo step.
- **Restore** writes `status=restoring` first. The manifest is replayed in
  reverse, then the validators run (`sshd -t`, `visudo -c`, `ufw status`).
  Restore is idempotent, and a `restoring` phase is always finished before
  anything else runs.
- **Signals, cancellation and the terminal.** The engine is the one owner
  of cancellation.
  - The engine ignores SIGHUP. Each tool runs in a process group the engine
    owns (section 3). Output goes to the log first; a lost terminal never
    stops a run.
  - SIGINT, SIGTERM, or a cancel from the terminal UI, all become one
    cancel request to the engine. The UI never stops anything itself: it
    sends the request, stays up, and shows "stopping at the next safe
    point".
  - The engine lets the running step finish. When the grace period ends,
    it cancels the step's context and stops its process group, leaving the
    intent record for the next start. It then exits 130. A second cancel
    request changes nothing; the UI says so.
- **Baseline.** A root step captures it once, before any other scope:
  - from T2 on, it is its own root scope, `baseline`, which runs before
    every other scope on both OSes; from T6, it becomes part of S1;
  - on macOS, the first change is made by `packages-user`, run by the
    package owner, so the executor refuses both S2 scopes (`packages-user`
    and `packages`) until the root `baseline` receipt exists in the public
    export; `next` hands the `baseline` root step over first.
  - It is taken through the same providers and covers:
    - packages with their manual or auto marks;
    - accounts and groups;
    - the hostname and `/etc/hosts`;
    - services, masks and launchd jobs;
    - firewall and SSH;
    - `sudoers.d`;
    - the declared `/etc` paths and user files.
  - **The state digest** is a SHA-256 over these providers' normalised output.
    It covers exactly that list and nothing else; the revert preview says so.
  - **The revert match.** Some changes cannot be reverted and are not the
    kit's: security updates and dpkg repair change the versions of packages
    that were already there. So `revert --to baseline` is checked with a
    second digest, the match digest, over the same providers with these
    differences left out:
    - the version of a package that was in the baseline, when the package is
      still present with the same manual or auto mark and the change came
      from outside the kit (an OS security update or dpkg repair);
    - new auto-marked dependencies that an OS security update pulled in for
      a baseline package;
    - a `created-auto` package kept because something outside the ledger
      depends on it (see the ownership ledger);
    - an upgrade the kit made to a baseline package, which every revert
      preview already lists as irreversible;
    - package manager caches and lists;
    - the kit's own records: state, public export, backups, archives and
      logs;
    - the uninstall-only items in section 9 (staged releases, state folders,
      the day-zero copy);
    - timestamps.

    Origin is proved from the package managers' own records, never assumed:
    apt and dpkg history (`/var/log/apt/history.log`, whose requester is
    `unattended-upgrades` for security updates) and the kit's ledger, which
    records every package change the kit makes, including upgrades of
    baseline packages. On macOS, Homebrew keeps no record of who asked for
    a change, so a formula change that the ledger does not record has an
    unknown origin. A difference whose origin cannot be proved fails the
    match; the human may accept it in the revert report, which records that
    acceptance. Every left-out difference is still listed in the revert report
    with its origin. Any other difference fails the match. The full state
    digest is reported too, for information.
  - It is never overwritten.
  - On an already-managed host it is labelled `as-found`, and every revert
    preview says that it is not the pre-kit state.
- **Ownership ledger.** Each item is recorded as one of:
  - `created`: absent before;
  - `created-auto`: a package pulled in as a dependency;
  - `changed`: existed, now different;
  - `removed`: existed, deleted by the kit, such as a legacy drop-in or an
    unmasked unit.

  It is taken from the providers' views before and after, never from what the
  phase reports about itself. Revert deletes `created` items, restores
  `changed` ones and recreates `removed` ones. A `created-auto` package is
  removed with the last `created` package that pulled it in, and only when
  the removal simulation shows that nothing outside the ledger depends on
  it; otherwise it stays, and the report lists it.
- **Unit revert and engine restore.** The engine restores what it backed
  up: it replays the manifest and the ledger. A unit's `Revert` covers only
  state that needs its own undo, such as releasing a masked service or
  removing a package through the package manager.
  - `wskit <unit> revert` targets the unit's last verified receipt: it
    undoes that unit's changes since its previous receipt, or since the
    baseline when there is none.
  - It refuses to revert a unit that other applied units depend on, and
    names them; reverting the set is `revert --to POINT`.
- **Packages:**
  - before removing any package, revert simulates the removal (`apt-get -s
    remove`, `brew uninstall --dry-run`), and any package beyond the ledger's
    list is a conflict that stops the step;
  - packages are installed and removed only through the package manager,
    and uninstalls run in reverse dependency order;
  - **downgrades where the package manager allows them.** On Ubuntu, the
    kit keeps the `.deb` files of earlier versions that an advertised point
    references, so revert can reinstall them. Homebrew keeps no earlier
    formula versions, so a kit-initiated upgrade there is listed in the
    cannot-undo list of every preview.
- **Stable points.**
  - Each verified stage records a point: ID, run ID, receipts, manifests and
    state digest.
  - A cross-account point references every participating account's receipts
    and manifests: root, the agent account and, on macOS, the package owner.
    It is joined in the public export, and retention and cross-account
    revert sequencing apply to that complete set.
  - The point after S8a and S8b is tagged `stable`.
  - `restore-points [--json]` lists them.
  - **Retention.** Nothing that an advertised point references (manifests,
    backups, receipts, and the release binary that wrote them) is ever
    pruned. Pruning happens only by retiring a point, which is an approved
    step that removes the point from the list first. Release binaries that
    points reference stay staged, so a point made by an older kit release
    is restored by that release's code.
- **Dependants go stale on every undo.** When a unit is restored after a
  failure, reverted, or rolled back by `revert --to`, every applied unit
  that depends on it, directly or transitively (the unit catalog), is
  marked `stale`. A stale unit is
  re-verified before any apply or resume continues, and reapplied when its
  verify fails.
- **`revert --to POINT`:**
  - **Preview first:** the steps in reverse stage order and, within a stage,
    reverse dependency order, plus what cannot be reverted (section 9).
    Every unit downstream of a reverted one is marked stale.
  - **Protected access.** A revert keeps the remote access path that the
    last connectivity proof established (the mesh-VPN SSH path) and its full
    dependency closure: the packages, the account, its keys and the firewall
    rule it needs. This holds even when going back to a baseline that had
    none. The baseline match digest exempts exactly that closure, and the
    revert report prints it. Removing it is a separate, console-only
    operation the person asks for.
  - **Approval** comes from the human, typed on a real terminal for root
    scopes. The conductor sequences the steps across the agent and root
    accounts, as hand-overs.
  - **Each step is a transaction**, so an interrupted revert resumes and a
    revert can be reverted.
  - **Drift.** An item whose digest changed after the point is a conflict. The
    revert stops there until the human chooses to keep or overwrite it.
  - **Not before T3 and T4.** Revert refuses with exit 2, naming the PR that
    adds the safeguard:
    - steps that touch SSH or the firewall, until the dead-man timer exists;
    - steps that remove an account, until the home archive exists.
  - **Safeguards ship with or before what they protect.** The dead-man
    timer ships in the same epic as the `ssh` and `firewall` units; their
    apply and revert verbs are refused until it exists. Account removal is
    refused until the home archive ships with the `accounts` unit (PR
    order).
- **Logs.**
  - Tool output goes only to the step's log (0600), through one redactor;
    it is never copied to the terminal in JSON or plain mode. The only
    exception is the browser hook's two non-secret lines, printed only when it
    runs as the `BROWSER` program (section 10a). The
    terminal UI shows progress events, and can open the log. The redactor masks token shapes,
    `password=` style pairs, URL user info and query values, `Authorization:`
    headers and PEM blocks.
  - No script writes a log file of its own, so no variable can steer root's
    writes.
  - **Phases never prompt.** Every secret is entered at a gate, in the native
    tool, or in a unit's own prompt tool that the gate's hand-over runs on
    a real terminal (as for the corporate VPN gateway, section 10c).
- **Failure record.**
  - A unit reports a failure to the engine as a typed error in the same
    process, never on its output, so output from the tools it starts cannot
    forge a record. Until a frozen script is deleted, it reports on a
    private descriptor (`AWK_FAILURE_FD`) that its tools do not inherit.
  - The executor writes the record itself, and makes one when a phase dies
    without reporting.
  - The record's fields: `phase`, `step`, `error_class`, `message`
    (redacted), `suggested`, `log` and `backup`.
  - `error_class` is one of `precondition`, `network`, `package-manager`,
    `validation`, `permission`, `conflict`, `interrupted` or `postcondition`.
  - `suggested` is one of `retry`, `repair`, `restore`, `revert` or
    `hand-over`.
- **Journals.** From T2, journals live at the fixed paths in section 11, one
  per account. `--journal` remains for one release as an override, then goes.
  T2 creates and checks its own state folders. It does not wait for S1 in T6.
- **Changes on permission.** An unforeseen change goes through
  `propose-change`.
  - **The change file** holds the paths and state, the commands, the risk, the
    undo and a record kind.
  - **Approval.** It is previewed, approved, then applied as a transaction.
  - **The record kind** says where it is written down:
    - `kit`: a generic fix, drafted as a kit PR;
    - `fleet`: a company value, drafted as a fleet PR;
    - `machine`: a host fact, written to the host's copy of the machine
      record and drafted into the fleet's copy (section 10b).
- **Fault injection.**
  - **The fakes:** the platform adapters sit behind interfaces, so tests
    give the engine fake host tools (`apt-get`, `dpkg`, `systemctl`, `ufw`,
    `visudo`, `sshd`, `hostnamectl`, `dscl`, `sysadminctl`, `launchctl`,
    `socketfilterfw`), a test-root setting that moves every path under a
    temporary folder, and a fake clock.
  - **The kills:** at the Nth call, or between any two executor steps,
    including during the backup, the intent write, restore, revert, and
    arming and disarming the timer.
  - Kills come from a test binary built with a build tag (`faultinject`);
    the release binary has no fault hooks.
- **Client mode** (for the browser sign-in tunnel, section 10a). This is an
  account-level transaction mode with no profile, plan or host binding.
  - It provides backup, ledger, logs and revert for the account's own files,
    under the account state folder.
  - Clients follow the client rows of `registry/platforms.toml`.

### 6. Gates and hand-overs (T3), and the dead-man timer (with the `ssh` and `firewall` units)

- **A gate** is a one-phase scope with a read-only probe. On a pass it is
  recorded; on a fail it prints its hand-over and exits 3. A rerun resumes
  there.
- **One hand-over object** for every gate, root step, revert and proposed
  change: `machine`, `account`, `step`, `command`, `why`, `verify`, `undo`,
  `probe`. The text form extends `AGENTS.md` section 6 with `Why` and `Undo`.
- **Switching to an admin account on Linux.** Ubuntu 26.04 ships `sudo-rs`,
  which needs a terminal to authenticate, and util-linux `su -c` runs its
  command without a controlling terminal. So `su - ADMIN -c "sudo ..."` fails
  with "A terminal is required to authenticate". On Linux, a hand-over that
  switches to an admin account emits `su -P - ADMIN -c "..."`, which gives
  the command a new pseudo-terminal, or an interactive `su - ADMIN` followed
  by the command. The generator, its docs and a test cover the `-P` form.
  This applies to Linux only.
- **macOS accounts.**
  - S3 creates them with a disabled password (`dscl` `Password` set to `*`,
    or a `sysadminctl` route that T3 proves with a stub test). No password is
    passed on a command line or a prompt.
  - At G1, the human sets each password with the native tool, and grants
    secure tokens so the accounts can unlock FileVault.
  - The kit never sees either value.
- **The dead-man timer** guards every apply and revert step that changes
  SSH, the firewall or VPN routes while remote access is selected. If the
  person cannot show they can still get in, the step is restored
  automatically. With remote access off, there is no timer.
  - **One authority:** the dead-man state file (P1 core spec). Arm, re-arm,
    proof, disarm and restore are recorded there, and every reader, the
    restore included, reads only that file.
  - **Before arming,** the hand-over asks the person to open a second
    session now (SSH over the mesh VPN from the second device, the console,
    or Screen Sharing on macOS), so they never depend on the session that
    runs the change.
  - **Order.** Arm, apply, re-arm, verify:
    1. arm with the full deadline before apply, so a crash during apply is
       covered;
    2. apply;
    3. re-arm, which resets the deadline so it counts from the end of
       apply (10 minutes by default);
    4. verify, by change kind (below).

    The scope then ends with the timer still armed and releases the host
    lock. While a timer is armed, only `disarm` or the timer may change the
    guarded step; `resume`, `revert` and new root applies exit 4 with a
    pointer to `disarm`.
  - **Verify and disarm by change kind:**

    | Change | Timer | Verify (the proof that allows disarm) |
    |---|---|---|
    | SSH or firewall, remote access selected | yes | a new SSH session over the mesh VPN from the second device, through OpenSSH; a Tailscale SSH session (`tailscaled` as its server) does not count, because it bypasses the OpenSSH config being changed |
    | SSH or firewall, remote access off | no | a confirmation typed at the local console |
    | VPN routes | yes, when remote access is selected | the route checks, a confirmation typed locally, and the mesh connectivity preserved if it existed; never an incoming-SSH proof |

    Disarm is accepted only after that proof is recorded in the dead-man
    file, from a login session other than the one that armed it: SSH, the
    console, or Screen Sharing.
  - **Conditional edges.** `firewall` and `ssh` depend on `mesh-vpn` only
    when remote access is selected. `vpn` waits for the protected group's
    disarm only when that group ran in the same setup, and never requires
    `ssh` itself. A test covers a portable `developer` with the VPN
    selected and incoming access left out.
  - **Torn disarm.** Disarm writes `disarming` to the dead-man file and
    fsyncs it before it removes the units, then `none`. A restore that finds
    `disarming` or `disarmed` exits; the next start removes leftover units.
  - **A restore closes the run.** Under the journal lock, the timer or boot
    recovery writes a failed receipt for the run it restored. That apply is
    never reapplied automatically; the person decides what happens next.
  - T3 moves the ancestor check from bash (`remote_login_ancestor_present`)
    into Go for both OSes.
  - **Deadline and boot recovery are separate.**
    - **Linux:** a timer unit with `OnActiveSec` set at each arm runs the
      restore service at the deadline. Boot recovery is a separate oneshot
      service, enabled at arm, that runs once at boot and restores only if
      the dead-man file shows the step armed and not disarmed. `OnBootSec` is not
      used, because combined with `OnActiveSec` on a running host it fires
      at once.
    - **macOS:** a root LaunchDaemon with `RunAtLoad=true` and
      `StartInterval=30` runs `deadman check --run RUN-ID` from the restore
      copy. Each run reads the dead-man file: it restores
      at once when the armed step belongs to an earlier boot (its recorded
      boot ID differs from the current one), whatever its deadline, as boot
      recovery does on Linux; it restores when the deadline has passed and
      the step is not disarmed; otherwise it exits.
  - **Atomic transitions.** Arm, re-arm, disarm and restore are transitions
    of one state machine, kept in its own state file (separate from the
    journal) and guarded by its own dead-man lock. Arm, re-arm and disarm
    take only that lock, never the host or journal lock, so an executor
    holding the host lock can wait for the arm acknowledgement without
    deadlock. Each transition writes the state (with the boot ID and the
    deadline) and fsyncs it before it changes the scheduler.
  - **The restore claim.** A restore takes the dead-man lock, writes a claim
    with a defined lifetime, stops the executor, releases the dead-man lock,
    then takes the host lock, then the journal lock. Only one restore runs
    at a time (this matters on macOS, where `StartInterval` can start
    overlapping runs); a dead claimant's claim is taken over atomically, and
    executors respect a live claim. The restore re-reads the state
    immediately before it changes anything. The exact transitions are in the
    P1 core spec.
  - **Deadlines.** The first arm, before apply, sets a 30-minute deadline
    to cover the apply. The re-arm after apply sets 10 minutes from that
    moment. Both are settable within fixed bounds.
  - **The restore never blocks on a live executor.** If the executor of the
    same run is still alive at the deadline, the restore stops it within a
    bounded time (terminate, wait, kill, matched by its recorded boot ID,
    process ID and start time), then empties that apply's containment
    (section 3), then takes the host lock and the root journal lock. A
    killed executor releases its locks. Steps guarded by the dead-man timer
    run no package-manager transaction, so the package-manager exception
    never delays a dead-man restore.
  - **The restore copy runs only after the system checksum tool has verified
    it** (see below).
  - **Restores keep a proven path.** Every restore keeps the mesh-VPN SSH
    path that an earlier connectivity proof established: firewall rules for
    the mesh interface and the SSH listener on it are never removed by a
    restore.
  - **The restore** records `restored-by-timer` in the dead-man file and a failed receipt in the journal.
  - **What the restore runs from.** The restore code must not change while
    the timer is armed.
    - **T3 to T5,** before versioned releases exist: arming copies the
      staged `wskit` binary into the step's root-only backup folder, with
      its digest in the manifest. The timer unit checks that digest with the
      system checksum tool before it runs the copy (on Linux an
      `ExecStartPre` step; on macOS the LaunchDaemon's command checks, then
      runs it). A restage while armed cannot change it.
    - **From T6,** the restore names the exact `releases/<version>`, not
      `current`, and the unit checks the binary's digest the same way.
      `wskit stage` refuses to prune a release that an armed timer names.
  - **Accounts.** A revert removes an account only after access is proven
    with an account that stays.

### 7. Lockout and convergence (T4)

- **Atomic writes for sudoers and sshd drop-ins:**
  1. write a dotted temp file in the target folder (sudo ignores dotted
     names);
  2. validate the candidate;
  3. fsync;
  4. rename;
  5. run the full `visudo -c` or `sshd -t`, restoring on failure.
- **Managed blocks instead of refusal.** Managed files use marked blocks,
  rewritten with a backup. Content outside the markers is kept.
- **dpkg repair first** when dpkg reports a half-configured package.
- **Intent before masks.** The SSH guard record is written as pending before
  `systemctl mask`.
- **Extra ports.** Profile-declared `remote.extra_ports` pass the firewall
  checks in the verifier and in `validate-host.sh`; nothing else does. The
  NoMachine port is opened by S5, which also releases its install guard.
- **`/etc/hosts`** gets a marked block owned by the identity phase.
- **Account homes** are archived, root-only, before revert removes an account
  the kit created. The archive ships with the `accounts` unit. Deleting an archive is a separate step the human approves.
- **Shell baseline.** `install-shell-baseline.sh` installs `~/.zshrc` and
  `~/.zsh_plugins.txt` only when they are absent. When a CLI installer has
  written a `.zshrc` first, apply stops and asks for a manual merge, and a
  host where the phase never ran has no baseline at all. The `shell` unit's
  epic ships these, in the shell unit's first PR:
  - an existing `.zshrc` gets a delimited managed block, with other tools'
    blocks kept, a backup first and a syntax check (`zsh -n`) before the
    swap; revert removes only the block;
  - interactive extras (prompt themes, autosuggestions, highlighting,
    framework aliases) load only in interactive human shells, and are
    skipped when an agent harness marks its tool shells (Claude Code sets
    `CLAUDECODE`), so agent commands see a predictable environment. The
    shell unit's page documents the guard.

### 7a. Resources (S7; the first unit right after T2)

The machine-wide part is the first unit built right after T2, which gives
it the root baseline it needs; a pilot host kept thrashing, and it is
covered meanwhile by the frozen script fix. A supervised pilot showed the
failure this stage must prevent: one test
suite grew to about a third of RAM, several sessions ran full gates in
parallel, swap filled, and every agent session and the desktop stalled for
hours, while a guard that watched load and available memory stayed green.

- **A reserve, set at setup.** A share of memory and CPU is kept for the OS,
  the desktop and other users, larger while a desktop session is active.
- **One ceiling for all agent work, never on a person's desktop.** On Linux,
  the ceiling (`MemoryHigh` and `MemoryMax` as shares of RAM) goes on a
  dedicated agent-work slice, per the topology table in section 2g: under
  the agent account's user manager with a separate account, or a separate
  agent-work slice for the processes the agent manager starts with a single
  account. It never goes on an interactive person's whole user slice
  without an explicit opt-in, shown in `plan`; the resources unit's verify
  checks that boundary. On macOS, priority and process limits, recorded in
  the capability matrix as a gap.
- **Preconditions, not early dependencies.** The machine-wide reserve needs
  only the baseline. The agent-work slice needs the account it is for, and
  enabling early OOM handling or zram needs their packages; when those are
  missing, the unit refuses with a pointer to the unit that provides them,
  so it can run early on a host that already has them.
- **Early out-of-memory handling:** `systemd-oomd` or an equivalent.
- **Admission by pressure.** Admission reads
  pressure-stall data on Linux (PSI) and the memory-pressure level on macOS.
  Swap use blocks only together with pressure, because cold pages in swap
  are harmless.
- **`/tmp` policy.** Recent Ubuntu mounts `/tmp` as tmpfs, so agent scratch
  and package caches placed there use memory. The stage sets the policy: a
  disk-backed `/tmp`, or caches pointed at disk. The doctor reports tmpfs
  use.
- **Budgets are relative.** Every limit is a share of the machine, read at
  run time. Config and docs hold no fixed GB or load numbers. The schema
  generates resource fields as shares, and `wskit migrate-profile` converts
  existing absolute values to shares of the host they were written for,
  with a preview.
- **The session side** (a runner that caps each heavy job, and a scheduled
  sweep) belongs to the agent tooling, not the kit. The kit installs the
  machine-wide part and checks for it.

### 7b. Indexing on macOS development volumes

A macOS unit, `indexing`, on by default for `developer` and `agent-host`
at every tier. Heavy file churn on development volumes makes `fseventsd`
and Spotlight use CPU and memory for days.

- Development volumes (separate volumes under `/Volumes` holding
  repositories, worktrees or `node_modules`) are detected and confirmed in
  the interview. The system volume is never accepted; folders on it are
  left to System Settings.
- Apply turns Spotlight indexing off, removes the volume's index store,
  writes `.fseventsd/no_log` (live file watching keeps working), adds a
  Time Machine exclusion when a destination exists, and restarts
  `fseventsd` once. A keep-Spotlight option turns off only the FSEvents
  on-disk log.
- The previous state is recorded and revert restores it exactly. The
  removed index store is in the cannot-undo list: macOS rebuilds it.

### 8. One schema (T1), interview and capabilities (T5)

- **Schema (T1).** `schema/*.toml` declares, for every field: `what`, `why`,
  `options` (each with its effect and risk), `default`, `recommended` (with
  its condition), `stage`, `undo` and `requires`. `requires` may name a
  capability.
- **What the schema covers when:**
  - **T1:** the schema format; every field that profiles use today (schema
    3), with the full metadata above; the generated Go types and
    constants; the validator, replacing the hand-written tables in
    `fleetctl.py`; and the plan's list of values set away from their
    default, each with its risk.
  - **T5:** profile schema 4 (below), the interview, the generated
    `docs/reference/config.md` with a CI staleness check, and the fleet
    schema fragment.
- **Profile schema 4:**
  - `tooling.components` names each optional component, with its options;
  - `[agent_settings]` is the agent-settings component's table;
  - `[x-org]` holds fleet keys. The kit checks their shape, and a fleet may
    supply a schema fragment for them (section 10b).
  - `wskit migrate-profile PROFILE` previews and writes a schema 4 file and never
    edits in place. The frozen `check-profile-migration.py` stays a checker
    until it is deleted.
- **Shell prompt.** A schema field for the prompt, with what, why, options,
  default and undo. Options: the kit default prompt; a prompt from a pinned
  plugin or theme loaded through Antidote (for example an Oh My Zsh theme
  with its `lib` and `git` plugins, pinned by commit); or none. Third-party
  themes are pinned by commit and checksum in the registry (T6), and
  automatic updates stay off.
- **Interview.** It asks purpose, then tier (section 2g), then shows the
  plan. Two modes:
  - **quick:** accept the preset's defaults, then show the plan;
  - **custom:** walk each selected unit and its schema fields with what,
    why, options, default, risk and undo, and add or remove units.

  `wskit interview --json` emits the questions as data, and `--answers FILE`
  writes a draft profile, which `validate` and `plan` check. Secrets are
  never asked; the interview names the native tool where each one is
  entered.
- **Every input has a default** or a derived default, settable in the
  config and shown and editable in the interview with its why and risk: the
  machine name from a validated naming pattern (purpose and form factor, or
  the fleet's pattern), account names, mesh-VPN hostname, tags and auth
  method (interactive sign-in by default; an auth key only as a secret
  hand-over), exit-node and route acceptance (off by default), the
  remote-desktop choice, and timezone and locale from the OS. Git identity
  is asked with no guessed value. Secrets have no default and are never
  stored in config; they are entered at hand-overs.
- **Flags instead of questions,** for `plan`, `setup` and `config`:
  `--set key=value` (repeatable, schema-validated), `--add UNIT`,
  `--add tools:NAME`, `--add mcp:NAME`, `--remove UNIT` and `--config FILE`.
  The same answers given through the interview or through flags produce
  the same config and the same plan digest. Every value set away from its
  default shows in `plan` with its risk. The safety floor cannot be
  overridden, and a refusal names the rule.
- **Capability matrix.** `registry/capabilities.toml` gives, per capability:
  the Linux and macOS means, how to verify it, its status per OS and its gaps.
  A component that is unsupported on the target OS is `skipped` by `plan`,
  with the reason and any equivalent, never refused or failed.
- **Root acts only on the approved plan.** `wskit plan` resolves the
  profile, the platform table, the schema and the registries into one JSON
  plan with a digest. A root step reads only that plan, staged root-owned,
  and refuses one whose digest was not approved. It never re-reads the
  user's TOML.

### 9. Platforms, tool registry and staging (T1, T6)

- **Platforms (T1).** Only versions the vendor still supports are allowed,
  with no best-effort tier. The rule is fixed; the list lives in
  `registry/platforms.toml`, so it moves with the vendors.
  - **The rule:**
    - Ubuntu LTS releases in Canonical's standard support;
    - the current and previous major macOS, on Apple silicon.
  - **The list today:**

    | Linux | macOS |
    |---|---|
    | Ubuntu 24.04 LTS and 26.04 LTS | macOS 27 and 26, on Apple silicon |

    Ubuntu 22.04 is not listed: its standard support ends in April 2027, so
    it counts as practically outdated. The pinned list in
    `registry/platforms.toml` is what preflight enforces; the vendor rule
    above is how the list is maintained.
  - **What it records** for each version: the architectures, the vendor's
    end-of-support date, and the evidence each release needs (a CI job, or a
    recorded real-machine run).
  - **Client rows** for the browser sign-in tunnel follow the same rule, so a
    client laptop on an older macOS is refused with the nearest supported
    version.
  - **Evidence.** Every listed version needs a CI job or a recorded
    real-machine run for each release. Where hosted runners lack a listed
    macOS version, the recorded run is the evidence.
  - **Preflight**, in readiness and in the executor, refuses everything else,
    with the reason and the nearest supported version. The table travels in
    the exported plan.
  - **The window follows the vendors.** A release is added once validated,
    and dropped at or before its end-of-support date. The update watch flags
    dates that are coming up, and the doctor warns a machine 90 days ahead.
- **Tool registry (T6).** `registry/tools.toml` records, per tool:
  - its exact version pin, and its source kind, which sets how the pin is
    enforced (proposed amendment A1, section 13);
  - its source;
  - a checksum or signature for anything outside an OS package manager;
  - licence, maintainer and release cadence;
  - the last security review and the next review date;
  - the capability it serves, how it uninstalls, and its update policy;
  - the compatibility-check inputs: the other tools and runtimes it works
    with, and the minimum OS version;
  - the command that prints its installed version, and the doctor probe
    that checks it.
- **References registry.** `registry/sources.toml` lists curated,
  authoritative links per unit, capability and topic (vendor docs, release
  notes, security advisories, upgrade guides), each with its purpose and
  last-verified date, under the same review rules as the tool registry. Each
  unit's page renders them, and `wskit explain UNIT --sources` shows them:
  a reliable starting point, not the final word. The update watch checks
  their health and staleness and opens PRs to refresh them.
- **Generated from the registry:**
  - `config/mise.toml` and `mise.lock` (installed with `--locked`);
  - `config/packages.toml`;
  - the Brewfile;
  - the apt pins (`/etc/apt/preferences.d`) and the `apt-mark hold` list,
    for packages from repositories that keep old versions;
  - the download pins.

  Checks on the generated files:
  - CI fails on a stale generated file, and on a `latest` or range value in
    any pin.
  - An overdue review warns, then fails after the grace period the registry
    states.
- **`wskit registry bump TOOL` (T6),** the bump flow (from the amendment
  on "latest"). Looking up a newer version is allowed. It
  becomes installable only through this flow:

  ```text
  lookup -> compatibility -> vetting -> permission -> pin + record -> install
  ```

  - **Lookup:** the newest version from the entry's declared source, run by
    the update watch or the conductor.
  - **Compatibility.** All four must pass, or the current pin stays and the
    reason is reported:
    - the version is supported on the platforms and architectures in the
      matrix;
    - it meets the entry's constraints;
    - its checksum or signature verifies;
    - a fixture smoke test passes.
  - **Vetting:** known vulnerabilities and maintainer status, recorded in the
    entry.
  - **Permission.** Every pin change lands through a pull request that a
    person merges. The update watch may open low-risk patch updates on its
    own, as the entry's approval policy allows; it never merges them. Tools
    that run as root, touch the network or the security boundary, or handle
    credentials, and any update during a live setup, also need the person's
    explicit approval in the PR.
  - **Pin and record:** a kit PR (generic) or a fleet PR (company tools), with
    a changelog entry, logged through the transaction layer. Only then is it
    installed.
  - **Never** a floating version at apply time, a silent upgrade, or an
    unrecorded version. The T8 doctor reports one as drift.
- **Mesh VPN client in S2 (T3).**
  - **Linux:** Tailscale's apt repository joins the approved sources in
    `config/packages.toml`, pinned by its signing-key fingerprint. It moves
    into the registry in T6.
  - **macOS:** a root S2 sub-phase installs the vendor `.pkg`, verified by
    checksum (`installer -pkg`). The app updates itself, so its source kind
    is `self-updating` (A1). The Homebrew cask is not used,
    because it runs the same `.pkg` with `sudo` and prompts. Approving the
    system extension is a G1 hand-over.
  - **Before the S5 group.** The client is installed in S2 and enrolled at
    G1, and `mesh-vpn` runs before `firewall` and `ssh`, so the proof path
    exists when the group runs. `mesh-vpn` never waits for the group's
    disarm. A fresh first install is tested end to end.
- **Staging (T6).**
  - **Each release:** `wskit stage` copies a released binary and its data
    files to `/opt/agent-workstation-kit/releases/<version>/`, verifies
    their checksums against the registry, makes the tree root-owned, swaps
    `current` atomically, and prunes only releases that no advertised point
    and no armed timer names.
  - **The fleet checkout** is staged the same way.
  - **The first staging** is the system-tools root step in section 3. A
    file the setup user could still change is never run as root.
  - **The command path** becomes
    `sudo /opt/agent-workstation-kit/current/bin/wskit ...`. The binary's
    root check (section 3) accepts `current` only when it resolves under
    `releases/` and the whole chain is root-owned.
- **Day-zero one-liner (T6).**
  - A few lines of POSIX sh. It downloads, for the setup user and without
    root, the pinned `wskit` binary for the OS and CPU, verifies its
    SHA-256, and runs it to install one agent CLI from its vendor's verified
    source.
  - Its trust anchor is the release public key and the checksums, embedded
    in the one-liner text and in the docs. Every Go release is signed, and
    the one-liner verifies both the signature and the checksum
    (`ssh-keygen -Y verify`, which ships with both OSes).
  - It prints the sign-in step, then hands over to the conductor.
- **Filesystem snapshots** (the `snapshots` unit in S7).
  Btrfs, ZFS or LVM-thin snapshots are taken at baseline and at each stable
  point where available. Results are informational.
  - On macOS, APFS local snapshots are purged after about a day, so they are
    only a short-term net around a stage.
  - Revert never depends on any snapshot.

What revert cannot undo, listed in every revert preview:
- the OS install, disk encryption and firmware;
- sign-ins and tokens on external services (revoked through a hand-over);
- data that agents produced (archived, never rolled back);
- package upgrades by OS security updates, and kit-initiated upgrades where
  the package manager keeps no earlier version (Homebrew);
- the proven remote access path, which revert keeps (section 5);
- the day-zero kit copy and agent CLI in the setup user's home, which come
  before the baseline and are removed by the documented uninstall;
- S1's staged releases and state folders (also the uninstall).

### 10. Agent settings (T7) and doctor (T8)

- **Agent settings (T7)** is the component designed earlier on issue #5:
  - settings bundles: `keyring-clis`, `git-network`, `orchestrator`, `cloud-clis` and
    `dotnet`;
  - discovery of the user settings, every agent-manager profile and the Codex configs;
  - merge, never remove;
  - workspace grants and `git-net`;
  - Codex managed keys;
  - the agent manager's own setting is read only and handed over.

  It runs as the `agent-settings` unit, composed into S6. Its planners use
  the T2 layer. From the pilots, T7 also covers:
  - **Every config home.** Agent managers create a config home per profile
    or account, such as per-profile Claude Code homes and per-account Codex
    homes. Editing `~/.claude` or `~/.codex` alone misses most of them. The
    applier finds every home, and the doctor compares each with the
    declaration.
  - **Sandbox exceptions, explicit and narrow.** CLIs that use a keyring
    over D-Bus, or a local control socket (such as an agent-manager CLI),
    are excluded by exact command. Read-only helpers that must see the whole
    machine are excluded by exact command, never by a pattern that would
    let them run other commands. Shared state folders go in `allowWrite`.
    Each exception states its reason and its undo.
- **MCP servers (the `mcp` unit and catalog, section 2e).** MCP servers are declared once, independent of chat
  accounts. The pilots found the same tracker integration set up three ways
  (a chat-account connector, a user-scope server in some profiles only, and
  a plugin with its own copy), and configs still pointing at a deprecated
  endpoint.
  - **Declared in the machine TOML** as `[agents.mcp.NAME]`: transport, URL
    or pinned command, scope, which harnesses get it, and the auth type.
    Which servers and endpoints a company uses live in the fleet's
    `[x-org]` table.
  - **Applied to every config home** (user settings, every agent-manager
    profile home, every coding-agent account home) through the transaction
    layer, with a backup, a preview and an undo.
  - **Chat-account connectors are off** in coding-agent sessions by default
    (Claude Code `disableClaudeAiConnectors: true`), so tools never depend
    on which chat account is signed in. Opting in is a schema option with
    its risk stated.
  - **One server per integration.** Plugin-bundled duplicates of a declared
    server are disabled.
  - **Pinned.** stdio servers have exact versions, never `@latest`. Remote
    endpoint versions are tracked in the registry with their deprecation
    dates.
  - **Sign-in is a hand-over,** one per config home: OAuth through
    `mcp login --no-browser`, pasting the redirect back, which works over
    SSH. Tokens stay in each harness's own store and never reach tracked
    files. Sign-ins are not shared across homes, because refresh-token
    rotation breaks shared tokens.
  - **OAuth quirks are recorded per harness in the registry.** Some vendors
    do not support dynamic client registration and need a pre-registered
    client ID and a fixed callback port (in Codex,
    `[mcp_servers.NAME.oauth]` with `client_id` and `callback_port`). A
    harness's callback path may still differ from the one the vendor app
    registered.
  - **Doctor:** every config home whose servers differ from the
    declaration, unauthenticated declared servers, duplicates and unpinned
    stdio servers.
- **Doctor (T8)** is read-only. It checks:
  - every phase's live verifier;
  - the public export and the stable point;
  - drift against the ledger and the machine record, including changes with
    no record and unrecorded tool versions;
  - `agent-settings check`;
  - the OS end-of-support date, from 90 days ahead;
  - a missing or unmanaged shell baseline, and the interactive-only guard
    (section 7);
  - tmpfs use and the resource reserve (section 7a);
  - the earlier doctor items.

  `--admin` runs the root-only checks from the staged root and writes their
  non-secret result to the public export. A fleet passes `--inputs FILE` with
  expected values, which takes over the fleet's interim checks.

### 10a. Browser sign-in tunnel (first unit, after T1)

- **What it is.** The `browser-tunnel` unit. It finishes a remote CLI's
  localhost sign-in, or shows a workstation page, in the client's own
  browser, over a loopback-only SSH tunnel that closes itself. It replaces
  the earlier `awk-auth` and `awk-open` scripts.

  ```text
  workstation (Linux)                          client (macOS or Linux)
    CLI calls $BROWSER URL                       person runs the printed command:
    wskit-browser-hook stores URL (0600),        wskit tunnel auth --host WS --handle H
      prints handle H and the client command       | fetches URL over SSH, deletes it there
                                                   | checks redirect host and port P
                                                   | ssh -L 127.0.0.1:P:127.0.0.1:P WS
    127.0.0.1:P <---- SSH, loopback only -----     | waits for "forward ready", opens browser
    callback served                                | "callback delivered", or time limit
  ```

- **Commands.** `wskit tunnel auth|open|host|hook|fetch|close|status`.
  `host set|update|remove` manages host entries; `close PORT` ends a
  tunnel; `status` lists live ones. The unit's `plan`, `apply`, `verify`
  and `revert` cover the host entries and the hook setting.
- **Workstation side (Linux).** `BROWSER` points at `wskit-browser-hook`, a
  symlink to the `wskit` binary that runs `tunnel hook` when called by that
  name, because many tools run `BROWSER` as a single program path with no
  arguments.
  - **Where `BROWSER` is set.** A marked block in `~/.zshenv`, which every
    zsh reads, interactive or not, so agent tool shells get it too. The
    block sets `BROWSER` only when `SSH_CONNECTION` is set, so a local
    desktop session keeps opening its own browser. Revert removes only the
    block.
  - **The hook's output.** It never prints the URL, because sign-in URLs
    can carry codes. It stores the URL in a 0600 file in the account run
    folder, under a random handle, and prints two lines: one plain
    hand-over line for people, and one machine line, `WSKIT-TUNNEL v1
    host=ALIAS handle=HANDLE port=P`. The client fetches the URL over SSH
    (`wskit tunnel fetch HANDLE`), which deletes the file. Handles expire
    with the time limit.
  - **macOS workstations** are a recorded gap: many macOS CLIs open URLs
    through Launch Services and ignore `BROWSER`. T1's capability table
    records it.
- **Client side (macOS or Linux).**
  - **Redirect hosts.** A forward opens only when the redirect URI's host is
    exactly `localhost`, `127.0.0.1` or `::1`, with a port from 1024 to
    65535. Any other host opens the browser with no forward. The forward
    binds the same address family on the client's loopback only.
  - **One durable owner for each tunnel's lifetime.** Each tunnel runs as
    a supervisor job: a transient systemd user unit on Linux, or a launchd
    agent job on macOS, created through `engine.Tx` and recorded. The job
    acknowledges that it owns the deadline before `ssh` starts, then runs
    `ssh -N -v -E LOGFILE -o ExitOnForwardFailure=yes -o ControlMaster=no
    -o ControlPersist=no` inside itself, and stops it at the time limit.
    Because the OS service manager owns the job, expiry happens even if the
    client process dies and nobody reruns anything. A record (port,
    deadline, job name) is written before the job starts; a crash before
    the acknowledgement leaves a record with no job, which the next run
    removes.
  - **"Forward ready"** needs two things: the OpenSSH debug line
    `Local connections to HOST:P forwarded to remote address ...`, with HOST
    exactly `127.0.0.1`, `localhost` or `[::1]`, in
    `ssh`'s own `-E` log, which the workstation cannot write, and a check
    that a listener owned by that `ssh` is bound on the requested address
    family and port. The browser opens only after both. Tests cover an
    occupied port and both address families. Text arriving over the session is never trusted, so a
    workstation that prints such a line changes nothing.
  - **"Callback delivered"** is separate: the log shows a connection on the
    forwarded port and its close, followed by three quiet seconds. The unit
    reports "callback delivered", never "signed in", because only the CLI
    knows whether the sign-in worked.
  - **Plain output** never shows a URL's query values.
- **Host entries** live in a kit-owned file,
  `~/.ssh/config.d/agent-workstations`, included by one marked `Include`
  line at the top of `~/.ssh/config`. Entries use the tailnet name, never a
  raw address.
- **Time limit:** 600 seconds by default, settable from 30 to 3600.
- **Threats it handles,** each with a test:
  - IPv6: `::1` is handled as its own family, and a forward never binds a
    non-loopback address;
  - injection: aliases, handles and ports are validated against strict
    patterns, and no value reaches a shell;
  - orphans and process-ID reuse: process records carry start times;
  - concurrent tunnels: one lock file per port, and a port in use is
    refused;
  - a faked "forward ready" line from the workstation;
  - a redirect URI that is not loopback.
- **Composition.** `plan` skips the unit from the topology (a separate agent
  account), and on macOS applies the client side only. The supervisor job
  is started with the session environment it needs (`XDG_RUNTIME_DIR` and
  the session bus on Linux); if it does not acknowledge, the client fails
  closed before `ssh` runs.
- **Scope and coexistence.** User scope only, on both sides; no root and no
  system settings. It works alongside the mesh VPN, NoMachine and other
  remote desktops, and binds only client loopback ports.
- **Order.** It comes right after T1, whose CLI skeleton, unit interface,
  minimal `engine.Tx` and platform table it uses, and before T2. When T2
  lands, it moves onto T2's client mode (section 5) without changing its
  commands or records. T3 composes its workstation hook into S6; until
  then it runs standalone only. It uses T1's hand-over object (the P1 core spec).
- **Parity.** The behaviours the earlier Python scripts tested become the
  parity list. Their Go tests and fixtures are written fresh under
  `testdata/tunnel/` and pass the public-safety scan; nothing is copied
  from private branches.
- **Gate.** The gate for every PR (section 12).

### 10b. Fleet side

The private fleet follows the same principles. The kit ships what a fleet
needs in `templates/private-fleet/`.

| Item | What | PR |
|---|---|---|
| No fleet apply path | Fleet scripts call the kit's executor. | T1 (template and docs) |
| Conductor recipe | The fleet `AGENTS.md` template gets the conductor loop, pointing at the kit's. | T1 |
| Human-only steps | `policy/human-steps.toml` in the template lists the steps no agent takes, matching the gates and root steps. | T3 |
| One host file | From T5, the machine TOML with `[x-org]` is the only per-host file. Until then, a fleet that keeps company values in a separate file (some use a `company.toml`) checks the two agree with its own script. The kit defines no format for that file. | T5 |
| Company values | A fleet schema fragment for `[x-org]`, in the kit's schema format, so company values get the same generated docs. | T5 |
| Company tools | Registry entries in the fleet, in the kit's registry format, with the same bump flow. | T6 |
| Machine records | `records/HOST.toml` in the fleet: the OS version and its vendor end-of-support date, where the baseline and stable points are kept, and every hand change. Structured, so the T8 drift check can read it. `propose-change` with kind `machine` drafts these entries. The host keeps a root-only copy for the doctor. | T2 (format), T8 (drift check) |

### 10c. VPN unit (`wskit vpn`, wave A)

One generic unit for a split-tunnel VPN (requirement R-NET-19), used by
every fleet with its own values and provider profile (section 2d). A pilot
showed that a fixed list of cluster names misses services that allow only
the office address: clusters created later, a managed database with an IP
access list, and dashboards behind a proxy allowlist. A full-tunnel vendor
client hid this, because it routes everything.

- **Destinations are typed configuration, not code.** Entries in the machine
  or `[x-org]` config, with names only in tracked files:
  - `kind = "cloud-cluster"`: every cluster in a project, with include and
    exclude, so new clusters are covered;
  - `kind = "srv"`: a DNS SRV record (as database connection strings use),
    resolved to hosts, then addresses;
  - `kind = "dns"`: host names.

  Each entry has a check port for status. Adding a destination is a
  reviewed config change and a reinstall, with no code edit.
- **No addresses in tracked files.** The gateway is typed once at install
  into a root-only site file. Destinations are resolved as root at refresh
  time (the cloud CLI through a named user's login; DNS through the system
  resolver), and stored root-only, written atomically. DNS-based entries are
  re-resolved at `up`, and stale resolution is flagged.
- **Only global unicast addresses.** Private, loopback, link-local, CGNAT,
  multicast and reserved ranges are refused, as is anything overlapping
  local routes, the mesh VPN or the default gateway. The route count is
  capped, and a large change needs confirmation.
- **Nothing identifying on screen.** Agents can read terminal scrollback.
  Secrets and addresses are read without echo. Output shows names, counts
  and change marks only. Error and log text carries no addresses, and tests
  enforce it.
- **Published status for every local user.** Root writes a sanitised status
  file atomically (`O_EXCL|O_NOFOLLOW`, fsync, rename, mode 0644, in a
  root-owned folder): state, times, tunnel counts and per-name route
  presence. Writer and reader enforce one schema: extra fields are refused,
  numbers must be finite, and malformed input exits cleanly. Readers refuse
  symlinks and user-owned files, and a missing or stale file reads as
  `unknown`. Sandboxed agents and other users get status without `sudo`;
  the live view stays root-only.
- **Under the dead-man timer.** Route and firewall changes arm the timer of
  section 6 before apply. They are verified by route checks, a local
  confirmation and the preserved mesh connectivity if present, never by an
  incoming-SSH proof. The unit depends on the S5 group's disarm.
- **Status** is concise by default (state, time left, N of M routed,
  problems only); `--verbose` lists each destination by kind and name.
- **Discovery aid.** A read-only audit lists cloud resources with
  office-only allowlists, so the destination list can be checked for gaps.
- **Prompts** read from the controlling terminal, never from standard
  input, and are tested under a pseudo-terminal.
- **Uninstall** also removes the runtime folder and timers. Its host-touching
  steps are guarded, so a test-root run never acts on the real host.

### 11. Where state lives

| What | Linux | macOS | Mode |
|---|---|---|---|
| Kit releases | `/opt/agent-workstation-kit/bin/wskit` (T1 to T5); `/opt/agent-workstation-kit/releases/<version>`, `current` (from T6) | same | root, 0755, not writable by others |
| Kit state top folder | `/var/lib/agent-workstation-kit/` | `/Library/Application Support/agent-workstation-kit/` | root 0755, holding only the folders below |
| Root run folder and host lock | `/run/agent-workstation-kit/` | `/Library/Application Support/agent-workstation-kit/run/` | root 0700 |
| Root journals, receipts, the host's copy of the machine record | `/var/lib/agent-workstation-kit/state/` | `/Library/Application Support/agent-workstation-kit/state/` | root 0700 |
| Public export (summary, per-phase status, redacted failure records, root gate records, the `stable` tag, doctor `--admin` result) | `/var/lib/agent-workstation-kit/public/` | `/Library/Application Support/agent-workstation-kit/public/` | root 0755, files 0644, written atomically, no secrets |
| Root backups, baseline, archives | `/var/backups/agent-workstation-kit/` | `/Library/Application Support/agent-workstation-kit/backups/` | root 0700 |
| Root logs | `/var/log/agent-workstation-kit/<run-id>/` | `/Library/Logs/agent-workstation-kit/<run-id>/` | root 0700, files 0600 |
| Account state, journal, backups, logs | `${XDG_STATE_HOME:-~/.local/state}/agent-workstation-kit/` | same | 0700, files 0600 |
| Account run folder | `$XDG_RUNTIME_DIR/agent-workstation-kit/`, or the account state folder's `run/` when that variable is unset | `${XDG_STATE_HOME:-~/.local/state}/agent-workstation-kit/run/` | 0700 |

- Each root folder's parent chain is root-owned, with no symlinks, and no
  folder in it is writable by group or others.
  The kit's own folders are created by the first root scope, not only by S1:
  the macOS top folder and run folder from T1 (section 3), the rest from T2.
- `/var/run` on macOS is never used, because it is group-writable.

### 12. Documentation and verification

- **Pages.** Every stage and component has a page under `docs/reference/`,
  with what it does, why, the options, the safe default and recommendation,
  the risks and the undo, plus its audience. Each PR updates its own pages.
- **CI checks:**
  - the generated reference is current;
  - every field, stage, component and flag is documented;
  - every documented command exists and matches its `--help`;
  - local links resolve (external links in a scheduled job);
  - the public-safety scan passes (hardened below);
  - the Go build and test jobs pass on Linux and macOS, and the frozen
    Python's jobs pass while it remains (from T1).
- **Public-safety scan hardening** (its own security PR, with a
  cross-lineage review; it does not depend on T1):
  - personal names and private network names are matched by keyed HMAC, so
    the rule file neither contains them nor lets them be guessed; the key
    lives in an untracked local file and a CI secret;
  - CI fails closed when the key is missing, and when the file listing fails
    or is empty;
  - findings print only file, line and rule, never the matched text, so CI
    logs cannot leak what the scan exists to catch;
  - the rule file and the scanner's own tests are scanned too, allowing only
    listed synthetic values;
  - obfuscations are caught: zero-padded octets, glued prefixes, and partial
    private-range prefixes.
- **Doc tests.** From T2 on, runbook command blocks run in preview mode
  against fixtures. Each block is marked `preview-tested` or `human-only`.
- **Each PR's gate:** the engineering standard (`docs/standards/engineering.md`,
  added with the P0 plan) as acceptance criteria, and:
  - tests;
  - an adversarial review of code and docs;
  - a stress run that kills at every boundary;
  - a docs-only walkthrough by a fresh agent;
  - a changelog entry.
- **The gate for plan and design PRs** (this ADR, the P0 plan with the
  roadmap and epics, and later plan changes; user requirement,
  2026-11-21). Before merge, other model families review the PR
  adversarially and stress-test the plan: Codex and Grok, with Fable as the
  fallback when one has no quota. They work from a read-only clone, and
  their findings are posted on the PR. The stress test has four parts:
  1. **Walkthrough.** A fresh agent builds the task breakdown for T1 and
     the tunnel unit from the docs alone, and lists every point where it
     had to guess.
  2. **Scenario probes:** an interrupt at each boundary, revert to
     baseline, a macOS-only run, a `developer` run on a portable machine, a
     user-scope-only run, a failed root approval, a fleet override, and
     agent output against terminal output. Every review prompt includes the
     macOS-only and the portable `developer` scenarios.
  3. **Consistency:** every unit belongs to exactly one stage and one epic,
     every T-item maps to named epics (a stage may hold several units, and
     a T-item may span several milestone PRs), and the dependencies have no
     cycles.
  4. **Feasibility** of the Go and Charm design.
- **`AGENTS.md`** drops the old "planned: issue #5, step N" numbering in T1
  and names T-PRs instead. T1 also adds the review and merge rules learned
  in the pilots:
  - before deleting a branch at merge, check whether an open PR uses it as
    its base, and retarget that PR first, because deleting the base closes
    it;
  - reviewer agents create scratch under one fixed, unique path and delete
    only that path, never with wildcards in shared temp folders;
  - a PR whose early commits carried text that must not reach `main` is
    squash-merged; history is rewritten only on the owner's explicit
    approval;
  - agents message each other through the agent manager's messaging, never
    by typing into another agent's input.

### 13. Settled questions, a proposed amendment and an operator question

Settled:

1. **Who runs S6:** the conductor in the agent account, signed in at G2
   (section 4). The fallback is that S6 becomes a hand-over the human pastes.
2. **macOS remote access:**
   - enabling Remote Login stays a G1 hand-over, because it needs Full Disk
     Access;
   - S5 on macOS limits SSH to named users (`com.apple.access_ssh` and an
     `sshd_config.d` drop-in) and checks the Application Firewall, under the
     launchd dead-man;
   - pf is left alone.
3. **macOS workloads** stay in S6. OrbStack is a per-user GUI app.
4. **Already-managed hosts** get an `as-found` baseline, captured by the root
   `baseline` scope before any other scope (section 5).
5. **Admission to apply:** in `wskit`, a unit's `Apply` is reached only
   through the engine (section 3). The frozen executor keeps its current
   checks until each phase moves.
6. **The platform list** (section 9): Ubuntu 24.04 and 26.04 LTS, and
   macOS 27 and 26 on Apple silicon, with no best-effort tier.

**Proposed amendment A1: how exact pins apply to each kind of source**
(principle 7). Every tool has an exact pin, and `latest` and ranges are
refused for every source. Some sources keep no old versions, so an exact pin
cannot always be installed there. This amendment proposes how each source
kind enforces the pin. It is proposed, not settled; the operator decides.

| Source kind | Examples | How the exact pin is enforced |
|---|---|---|
| `exact` | downloads, vendor `.pkg` files (except apps that update themselves), mise tools, third-party apt repositories that keep old versions (such as Tailscale's) | installed at exactly the pin; apt pins and `apt-mark hold` where they apply |
| `os-managed` | the Ubuntu archive, Homebrew formulae | the registry pin is installed exactly (`apt-get install PKG=VERSION`) while the source still serves it. When it no longer does, the install stops, and the version the source serves becomes the new pin only through `wskit registry bump`. A later change on the host, including an OS security update, is flagged as drift with its origin and recorded in the machine record |
| `self-updating` | casks marked `auto_updates`, vendor `.deb` files that add their own apt repository | the registry pin is installed while the source still serves it; when it does not, the install stops until `wskit registry bump` moves the pin. The app's own later updates are flagged as drift with their origin, recorded in the machine record, and re-pinned through `wskit registry bump` |

For `os-managed` tools, the OS security channel stays on
(`unattended-upgrades`), as in ADR 0004. Its updates are then flagged and
re-pinned, never silently accepted. Homebrew does not keep old formula
versions: when it no longer serves a pin, the install stops until a
`registry bump` PR is merged. No pin moves at apply time.

**Question for the operator.** `unattended-upgrades` covers only Ubuntu's
security origins by default. Should the kit add the origins of the
third-party repositories it installs, so their packages also get automatic
security updates (which A1 would then flag and re-pin)?

### 14. Deviations from the consolidated design

Where this ADR differs from the consolidated design on issue #5, and why:

| Item | Earlier design (issue #5, or the implementation comment where named) | This ADR | Why |
|---|---|---|---|
| Recovery gate | Recovery attested at apply time (T4) | Gate G0, once, before any change (T3) | One attestation, given before the steps that need it, instead of one per scope |
| Audit stage | One S8 | S8a (root) and S8b (agent account) | Root and agent checks run as different accounts and keep separate records; root tags `stable` after checking both |
| Baseline | Part of S1 | Its own root scope `baseline` from T2, part of S1 from T6 | macOS changes start with `packages`, run by the package owner, so the baseline needs a root step before it |
| Browser sign-in tunnel | After T8, then right after T2 | The first unit, right after T1 and before T2; onto T2's layer when T2 lands | Principle 11 makes it a unit that needs no stage, it is needed day to day, and it needs T1's CLI skeleton (user decision, 2026-11-21) |
| Implementation | Python and shell | Go, one `wskit` binary; Python and shell frozen and deleted at parity; the schema moves to T1 | User decision, 2026-11-21 (section 2b) |
| Units per stage | The implementation decision lists the firewall in S4, the mesh VPN in S5, and resources in S6 and S7 | Firewall with SSH in S5, mesh VPN client installed in S2 and set up in S5, resource limits in S7 only | The dead-man timer guards SSH and firewall changes together; the client is a package; one owner for resources |
| Dead-man order | Arm after apply, before verify | Arm before apply, re-arm after apply so the deadline counts from there, then verify | A crash during apply stays covered |
| Module path | The cross-family review asked for a neutral path | The repository's own path | `go install` and vulnerability tooling resolve it; it moves with the repository |
| Git identity | The implementation decision listed git identity in S3 | `git-identity` is in S6, as the agent account's own setting | It is per-account user configuration, applied by the account that uses it |
| Secret hand-overs as a unit | The implementation decision listed "secrets hand-overs" in S3 | Secrets are entered only at gates G1 and G3; there is no secrets unit | A unit changes machine state; a gate only checks it |
| Principle 7 | `latest` not allowed | `latest` and ranges refused for every source; enforcement per source kind is proposed amendment A1 | Some sources keep no old versions |

### 15. Where the contracts live

This ADR records decisions, principles and safety rules. The contracts that
code is built against live in specs and generated files that tests check:

| Contract | Home | Lands in |
|---|---|---|
| Unit catalog: unit, domain, stage, privilege, dependencies per OS, epic and PR | `registry/units.toml`, with generated docs and Go constants | P1 spec, T1a |
| The `Unit` interface, `engine.Tx`, the event type and sink | the P1 core spec (`project-management/specs/`) | P1 spec, T1a |
| JSON schemas: `next`, `status`, `explain`, `Describe`, `Plan`, hand-over, failure record | `schema/json/`, with golden fixtures | P1 spec, T1a and T1b |
| State machines: intent and recovery, dead-man arm, re-arm and disarm, process supervision | the P1 core spec, with fault-injection tests | P1 spec, T1c and T3 |
| Plan digest, approval records, lock order | the P1 core spec | P1 spec, T1b and T1c |
| Schema 3 parity fixtures | `testdata/profiles/` | T1b |
| Tunnel protocol: hook line, handle store, readiness and callback signals | the tunnel unit's spec | E06 |
| Dependencies and their versions | `go.mod` and registry entries | P0 |

The unit catalog, generated from `registry/units.toml` (edit the TOML file,
never this table; a repository check keeps this copy, the roadmap's and
SPC-001's identical):

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
| `firewall` | network | remote | root | remote | root | packages, mesh-vpn (if remote-access); packages, mesh-vpn (if remote-access) | EPC-015 | alpha |
| `ssh` | remote | remote | root | remote | root | accounts, firewall, mesh-vpn (if remote-access); accounts, firewall, mesh-vpn (if remote-access) | EPC-015 | alpha |
| `mesh-vpn` | network | remote | root | remote | root | packages; packages | EPC-016 | alpha |
| `remote-desktop` | remote | remote | root | none | none | packages, firewall; none; waits for s5-disarm | EPC-017 | alpha |
| `vpn` | network | remote | root | remote | root | packages; packages; waits for s5-disarm | EPC-007 | alpha |
| `shell` | agent | agent | agent | agent | agent | accounts; accounts | EPC-008 | alpha |
| `git-identity` | identity | agent | agent | agent | agent | accounts; accounts | EPC-014 | alpha |
| `runtimes` | agent | agent | agent | agent | agent | shell; shell | EPC-013 | alpha |
| `tools` | agent | agent | agent | agent | agent | runtimes; runtimes | EPC-011 | alpha |
| `agent-settings` | agent | agent | agent | agent | agent | tools; tools | EPC-009 | alpha |
| `mcp` | agent | agent | agent | agent | agent | agent-settings; agent-settings | EPC-012 | alpha |
| `browser-tunnel` | remote | agent | user | agent | user | none; none | EPC-006 | alpha |
| `workspace` | workspace | agent | agent | agent | agent | tools, git-identity; tools, git-identity | EPC-021 | alpha |
| `resource-limits` | ops | resources | root | resources | root | baseline; baseline | EPC-010 | alpha |
| `indexing` | system | none | none | resources | root | none; baseline | EPC-033 | alpha |
| `snapshots` | data | resources | root | none | none | baseline; none | EPC-020 | alpha |
| `doctor` | ops | audit | root | audit | root | none; none | EPC-022 | alpha |
| `audit-agent` | ops | audit-agent | agent | audit-agent | agent | shell, tools, agent-settings, mcp; shell, tools, agent-settings, mcp | EPC-022 | alpha |
<!-- catalog:units:end -->

A dependency on `packages` is satisfied by the OS's own package units, so
macOS edges never point at a Linux-only unit. Each stage is exactly one
executor scope with one privilege; where an OS needs two privileges in one
stage, the stage has two scopes, as S2 on macOS and S8 do.

## Consequences

- One path to apply, revert and audit; documented `fleetctl run --apply`
  commands go away.
- The kit moves to Go: one `wskit` binary replaces `agentctl`, `fleetctl`
  and `setup-workstation.sh`. The Python and shell are frozen and deleted
  piece by piece at parity.
- macOS root scopes work again (T1).
- More state on disk (backups, logs, baseline). The doctor reports its size,
  and a point can be retired by an approved step, which frees what only it
  referenced.
- Units have `Describe` and never prompt. macOS account passwords
  move to G1.
- Existing receipts are invalidated once, by T1; the next run re-records
  them.
- Profiles move to schema 4 with a migration.
- Tool updates become registry pull requests with a security note.
- **Versions.** The Go `wskit` is a new major version, 1.0.0. The Python
  `0.2.0-rc` line is frozen for fixes and is never released as a final
  0.2.0. Pre-releases are `1.0.0-alpha.N` (P1 and P2), `1.0.0-beta.N` (P3)
  and `1.0.0-rc.N` (P4). From 1.0.0, releases follow Semantic Versioning:
  breaking config or CLI changes only in a major version, after one minor
  release of deprecation warnings; the machine config has a schema version,
  and `wskit migrate` previews and reverses config migrations; JSON output
  and the `next` schema are versioned; catalog, registry and preset changes
  are noted in the changelog. Releases are signed.
- **The final tree** matches the documented Go layout exactly: P5 deletes
  all frozen Python and shell, and a CI check compares the layout table in
  `docs/architecture.md` with the tree.

## Acceptance for 1.0.0

- T1 to T8 and the P2 unit epics merged, each with its evidence, including
  standalone apply and revert tests for every unit (section 2c).
- An agent conducts one clean setup on a supported Linux machine and one on a
  supported macOS machine. Each run:
  - goes from the interview to the stable point;
  - includes one deliberate interrupt that the agent diagnoses and resumes;
  - ends with `revert --to baseline` passing the match digest (section 5),
    in a fixture or a disposable machine.
- Both account topologies, and a default `developer` run on a portable Mac,
  are covered.
- The browser tunnel runs on T2's client mode, standalone and composed.
- The doctor is green, and a docs-only walkthrough passes.

## Pull request order

```text
D0 (this ADR, reviewed alone)
 -> P0 foundation: CodeFlow full tier, Go module and layout, CI (lint, test, govulncheck,
       signed release build), code generation, bootstrap.sh; public-safety scan hardening
 -> T1 in four milestones, each its own PR:
       T1a CLI skeleton and contracts: cobra and fang, typed constants, catalog and domains,
           events and the UI sink interface, exit codes, frozen-code fixes
       T1b schema 3 parity, generated types, config layers, platforms, planner, next --json
       T1c engine.Tx minimum: locks, durable intent and recovery, process supervision,
           receipts, staged binary check
       T1d terminal UI and plain or JSON front ends
 -> browser tunnel unit (user scope, no root baseline needed), the only unit before T2
 -> T2 engine: transaction layer, baseline, ledger, match digest, revert, restore points,
       logs, failure records, client mode; the tunnel moves onto it
 -> P2 unit waves, in parallel:
       A: resources and macOS indexing first, right after T2 (pilot hosts are covered
          meanwhile by the frozen script fix and the reference script), then VPN, shell,
          agent settings, tools (registry-driven), MCP catalog
       B: packages and runtimes; identity and accounts, with the home archive (account
          removal refused until it ships); SSH and firewall, with the dead-man timer in
          the same epic (their apply and revert refused until it ships); mesh VPN;
          remote desktop; fleet commands
       C: workloads and agentctl, snapshots, workspace, doctor
 -> P3: T3 stages and gates, T4 lockout guarantees, T5 one config and interview,
       T6 registry and staging, update watch, conductor recipe
 -> P4: 1.0.0 acceptance (1.0.0-rc.N)
 -> P5: cutover (frozen Python and shell deleted at parity), fleet adoption
 -> after 1.0.0: organisation units (declarative units with typed operations); plugins later

Plan and design PRs pass the gate in section 12 before merge.
```
