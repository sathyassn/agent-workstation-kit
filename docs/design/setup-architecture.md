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
| Stage | A step in the setup order, S0 to S8 (S8 has two parts, S8a and S8b). Each stage is exactly one executor scope; `--scope` takes the stage's name, such as `packages` or `remote`. Some scopes share a name with a phase inside them: scope `identity` holds the phases `identity` and `accounts`, and scope `audit` holds the phase `audit`. |
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
| `fleetctl run PROFILE PHASE --apply` runs a phase script directly | `scripts/fleetctl.py` (end of `main`) | No executor, lock, receipt or staged-root check. It is documented in `docs/runbooks/day-zero-linux.md`, `docs/02-linux-setup.md` (twice), `docs/03-macos-setup.md` and `docs/01a-onboarding-profile.md`. |
| Some root scripts are in no scope | `install-local-deb-linux.sh`, `install-ms-s1-r8127-dkms.sh`; the allowlist is in `scripts/lib/setup_scopes.py` | Vendor packages (NoMachine, browsers, editors) and a kernel driver apply outside every safeguard. |
| The host lock checks `/var/run` with `secure_root_path` | `execute-setup.py` `host_apply_lock` | On macOS, `/var/run` is `root:daemon 0775`, so the check fails and **every macOS root scope exits 2 today**. T1 fixes this. |
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

### 2. Stages, gates and owners

| Stage (`--scope`) | Linux | macOS | Runs as | Reversible |
|---|---|---|---|---|
| S0 Configure | interview, machine TOML, `plan` | same | setup user (conductor) | yes (a file in the fleet) |
| G0 `recovery` | console recovery tested | same, with the recovery key | human; probe: attestation typed at the gate | n/a |
| S1 `stage` | versioned `/opt` release (T6), state folders, baseline (its own `baseline` scope from T2 until T6) | same | root | no; removed by the documented uninstall |
| S2 `packages` | dpkg repair, apt packages, vendor packages, hardware drivers, mesh VPN client; install guard on SSH and remote desktop | Homebrew formulae and casks (package owner); mesh VPN client from the vendor `.pkg` (root sub-phase) | root, or package owner for Homebrew | yes (ledger) |
| S3 `identity` | hostname, `/etc/hosts` block, accounts and groups | computer names; accounts created with a disabled password | root | yes (homes archived, from T4) |
| G1 `access` | passwords, SSH keys, mesh VPN up, a second tailnet device holding a named user's key | passwords, secure tokens, mesh VPN up (system extension approved), Remote Login and Screen Sharing enabled for named users only, a second tailnet device | human; conductor polls the non-root probes, root check once at the end | n/a |
| S4 `services` | the `agentctl` helper and its sudoers rule (atomic), workloads | none | root | yes |
| S5 `remote` | SSH and firewall hardening, alone, under the dead-man timer; releases the SSH and remote-desktop install guards | SSH limited to named users, Application Firewall checked, under a launchd dead-man | root | yes, under the timer |
| G2 `session` | agent desktop or console session; the conductor's CLI signed in there | same, plus privacy prompts; FileVault verified | human; agent-account probe | n/a |
| S6 `agent` | shell, mise tools, agent settings | same, plus workloads (OrbStack needs the agent's GUI session) | agent account | yes |
| G3 `sign-ins` | provider sign-ins | same | human; agent-account probe | revoke by hand-over |
| S7 `resources` | reserve, one ceiling for agent work (user slice), early OOM handling, `/tmp` policy (section 7a); deferrable | a gate until a macOS method is qualified; deferrable | root | yes |
| S8a `audit` | root checks, reboot acceptance | same | root | n/a |
| S8b `audit-agent` | agent checks | same | agent account | n/a |

- **Order.** Each scope's preflight reads the public export (section 11) and
  stops with the next step when an earlier stage is not verified.
- **Deferring S7.** S7 is the only stage that can be deferred, because it
  needs a measured load. The deferral is recorded in the receipts.
- **The stable point** is tagged by root after S8a passes and root has
  checked S8b's record live.
- **Gate owners and records.**
  - **Root gates.** G0 and the root half of G1 run through the staged root
    executor, so their records are root's. The approval is low risk, because
    a probe is read-only.
  - **Polling G1.** The conductor polls G1's non-root probes as often as it
    likes: mesh VPN up, Remote Login reachable over the tailnet, and on macOS
    `com.apple.access_ssh` listing only named users. When they pass, the human
    runs the root check once.
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
- **Declared once.** Each unit declares:
  - its inputs (schema fields, from T1);
  - its dependencies: other units, capabilities and OS support;
  - its privilege: user or root scope;
  - the paths and state it owns (`--describe`).

  `plan` resolves the dependency order and refuses a unit that is
  unsupported on the target or conflicts with another.
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
  release carries checksums, and signatures later, pinned in the registry.
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
  wskit setup [--all | --stage S4 | --unit vpn] [--apply]     preview by default
  wskit <unit> <verb> [flags]                                  each unit standalone
  wskit status | doctor | explain <unit|field>
  wskit next --json                                            conductor interface
  wskit validate | approve | migrate-profile
  wskit restore-points | revert --to <point|baseline> | resume [--restore-only]
  wskit disarm --run RUN-ID | propose-change FILE
  wskit stage | registry check|bump | docs gen
  wskit tunnel auth|open|host|hook                             the browser-tunnel unit
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

- **Units,** about 15. Stages compose them in the order of the stage table
  (section 2):

  | Stage | Units |
  |---|---|
  | S1 | baseline |
  | S2 | packages, runtimes, agent CLIs; the mesh VPN client is installed here |
  | S3 | accounts, git identity, secrets hand-overs |
  | S4 | containers and workloads (Linux; macOS workloads run in S6) |
  | S5 | SSH, firewall, remote desktop, mesh VPN settings |
  | S6 | shell, agent settings and sandbox, MCP, browser tunnel hook; macOS workloads |
  | S7 | resource limits, snapshots |
  | S8 | doctor and the stable point |
  | Any stage, optional | corporate VPN, workspace and repositories |

- **Data is embedded.** The schema, the registries, dotfile templates and
  docs templates are compiled into the binary with `go:embed`, so the
  one-liner fetches only the binary. A fleet adds to them, never replaces
  them: with `--fleet-root`, `wskit plan` reads the fleet's `[x-org]`
  schema fragment, its registry entries and its templates. Fleet additions
  may add fields, tools and values; they cannot loosen a kit rule (a pin
  policy, a refusal or a platform). The plan records the digest of every
  fleet file it read.
- **Go details for T1:**
  - **Module and layout.** One module,
    `github.com/sathyassn/agent-workstation-kit`, with the Go toolchain
    pinned in `go.mod`:

    ```text
    cmd/wskit/          main only
    cmd/agentctl/       the narrow sudoers helper (section 3)
    internal/cli  engine  planner  schema  registry  conductor  docsgen  redact  ui
    internal/platform/{linux,darwin}
    internal/units/<unit>/
    schema/  registry/  dotfiles/  templates/      data, embedded
    scripts/bootstrap.sh                           the only shell
    testdata/  e2e/                                fixtures, container and VM runs
    ```

    `docs/architecture` documents the layout. There is no `pkg/`, because
    nothing here is a public Go API.
  - **The unit interface.** Each unit implements:

    ```go
    type Unit interface {
        ID() UnitID
        Describe(ctx context.Context) (Description, error)
        Plan(ctx context.Context, in Inputs) (Plan, error)
        Apply(ctx context.Context, tx engine.Tx, p Plan) error
        Verify(ctx context.Context, p Plan) (Result, error)
        Revert(ctx context.Context, tx engine.Tx, r Receipt) error
        Status(ctx context.Context) (Status, error)
    }
    ```

    `engine.Tx` is the only way a unit changes a file or runs a tool, so the
    engine sees every write and backs it up first. `UnitID`, verbs, stages,
    scopes, statuses and exit codes are typed constants, never loose
    strings.
  - **JSON shapes.** `Describe` returns `{schema_version, unit, scope,
    depends_on[], capabilities[], paths[], state[], irreversible[]}`.
    `Plan` returns `{schema_version, unit, plan_id, digest, changes[{path or
    state, action, before_digest, after_digest, risk}], non_defaults[],
    blocked_by}`. Each shape has a version, golden-file tests and a checked
    example in the docs.
  - **Generated versus checked at run time.** `go generate` turns
    `schema/*.toml` into Go structs and constants for every field; CI fails
    when the generated code is stale. Rules that depend on the host or on
    other values (platform support, conflicts, ranges of allowed values)
    are checked at run time by `schema` and `planner`.
  - **TOML.** One TOML library, pinned to an exact version in `go.mod` and
    recorded in the registry like every other dependency (principle 7).
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
  - **Agents,** the preferred way to run setup: `--json`, no terminal, or
    `WSKIT_PLAIN=1` gives stable, versioned JSON and plain text, with no
    animation, no colour codes and no prompts. Every interactive question
    has a flag or config equivalent, so nothing needs a terminal. `next
    --json` drives the conductor.
  - **Accessibility.** `NO_COLOR`, `TERM=dumb`, narrow terminals and screen
    readers (`--plain`) are respected.
  - **Secrets** never appear on screen; secret inputs are masked.
  - **Tests:** snapshot tests for plain and JSON output, and golden-file
    tests for the terminal views (`teatest`).
  - Every Charm dependency is pinned in `go.mod` and listed in the registry.
- **Fleet logic.** Logic that a fleet holds today moves into generic kit
  units driven by fleet values. The fleet keeps config, machine records and
  values only its organisation uses.
- **How the earlier sections read in Go.** Phase scripts become units.
  `--describe` is the unit's `Describe`, and a phase's apply is the unit's
  `Apply` as the engine runs it. Commands named in this document are `wskit`
  commands: for example `wskit next --json`, `wskit approve`, `wskit resume`
  and `wskit interview`.

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
  whole.
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
- **Staging from T1 to T5.** A documented root step stages a release
  binary: it copies the binary into a root-owned temporary folder, checks it
  there with the system checksum tool (`sha256sum` on Linux, `shasum -a 256`
  on macOS) against the checksum published with the release, and only then
  installs it at the fixed staged path. The binary never checks itself.
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
- **Remote desktop.** The packages unit masks NoMachine's service before
  the package is installed, with its record written first. It stays masked
  until the firewall is in place and the remote-desktop unit releases it in
  S5. This closes the exposure that the SSH install guard already closes for
  SSH (`package_setup.py` `guard_new_ssh_install`).
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
  - `schema_version`, `run_id`, `stage`, `phase` and `owner`;
  - `root` and `gate`;
  - `preview_command` and `apply_command`;
  - `handover` (null until T3) and `probe`;
  - `log_path` (null until T2);
  - `blocked_by`.

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
  | 4 | An interrupted phase must be finished, restored or reapplied first; or a dead-man timer is armed and waiting for disarm |
  | 5 | Refused as root: not the staged binary (section 3) |
  | 130 | Cancelled at a safe point |

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
- **Where the conductor runs.** It runs as the setup user until G2. At G2 the
  human signs the conductor's own CLI in inside the agent account, which is
  the one sign-in that moves earlier than G3. The conductor runs S6, G3 and
  S8b from there. On Linux the agent account is denied SSH, so this is a
  desktop or console session.
- **Reverts past S3** remove the agent account. They are therefore driven
  from the setup user, and the executor refuses to remove the account its
  caller is running in.

### 5. The transaction layer (T2)

```text
preflight -> describe -> backup -> intent=applying -> apply (logged) -> verify -> receipt=verified
                                         |                                  |
                    restart finds it ----+                failure ----------+--> intent=restoring -> restore
                    restore, then reapply                                         -> receipt=failed + failure record
                    (no verify shortcut)
```

- **Describe.** Each phase answers `--describe` with JSON:
  - `paths`;
  - `state` providers: hostname, ufw, unit masks, sysctl, packages, accounts,
    launchd jobs, Application Firewall;
  - `irreversible` items.

  The executor fails closed when a phase does not answer.
- **Backup.** Before the apply, each item is copied with its owner, mode and
  digest, or recorded as absent. State providers write snapshots.
  `manifest.json` is written and fsynced last, so a manifest exists only for a
  complete backup. A kill during the backup leaves no manifest, and the next
  start backs up again.
- **Intent.** `status=applying`, with the run ID and the backup path, is
  fsynced to the journal before the apply. On the next start, a phase that is
  still `applying` blocks everything else (exit 4) until it is handled:
  - it is restored from its manifest, then reapplied. There is no
    verify-only shortcut, because a half-applied state can pass verify;
  - `wskit resume` does this, and is the command that clears
    exit 4; `--restore-only` stops after the restore.
- **Restore** writes `status=restoring` first. The manifest is replayed in
  reverse, then the validators run (`sshd -t`, `visudo -c`, `ufw status`).
  Restore is idempotent, and a `restoring` phase is always finished before
  anything else runs.
- **Signals and the terminal.**
  - The engine ignores SIGHUP, and each tool it starts runs in its own
    session. Output goes to the log first; a lost terminal never stops a run.
  - On SIGINT or SIGTERM, the executor stops at the next safe point: it lets
    the running phase finish, or after the grace period stops it, leaving the
    intent record for the next start. It then exits 130.
- **Baseline.** A root step captures it once, before any other scope:
  - from T2 on, it is its own root scope, `baseline`, which runs before
    every other scope on both OSes; from T6, it becomes part of S1;
  - on macOS, the first change is made by `packages`, run by the package
    owner, so the executor refuses `packages` until the root `baseline`
    record exists in the public export; `next` hands the `baseline` root
    step over first.
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
  - an upgrade is listed as irreversible: downgrades are not offered.
- **Stable points.**
  - Each verified stage records a point: ID, run ID, receipts, manifests and
    state digest.
  - A cross-account point is the pair of the root point (written by root) and
    the agent point (written by the agent account), joined in the public
    export.
  - The point after S8a and S8b is tagged `stable`.
  - `restore-points [--json]` lists them.
- **`revert --to POINT`:**
  - **Preview first:** the steps in reverse stage order, plus what cannot be
    reverted (section 9).
  - **Approval** comes from the human, typed on a real terminal for root
    scopes. The conductor sequences the steps across the agent and root
    accounts, as hand-overs.
  - **Each step is a transaction**, so an interrupted revert resumes and a
    revert can be reverted.
  - **Drift.** An item whose digest changed after the point is a conflict. The
    revert stops there until the human chooses to keep or overwrite it.
  - **Not before T3 and T4.** Revert refuses with exit 2, naming the PR that
    adds the safeguard:
    - steps that touch SSH or the firewall, until T3 adds the timer;
    - steps that remove an account, until T4 adds the home archive (T4 also
      uses T3's access proof).
- **Logs.**
  - The executor tees phase output to the terminal and to the phase log
    (0600), through one redactor. The redactor masks token shapes,
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

### 6. Gates, hand-overs and the dead-man timer (T3)

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
- **The dead-man timer** guards every apply and revert step that touches SSH
  or the firewall.
  - **Linux.** A persistent unit pair is written to `/etc/systemd/system`: a
    service that restores the step from its manifest with the staged
    binary, and a timer with `OnActiveSec=10min` and
    `OnBootSec=2min`. A power loss before disarm therefore restores at the
    next boot.
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
  - **macOS.** A root LaunchDaemon with `RunAtLoad` and a deadline file does
    the same.
  - **Order.** Arm, then apply, then verify. The scope then ends with the
    timer still armed, and releases the host lock.
  - **Lock.** While a timer is armed, the executor refuses new applies with
    exit 4. The timer's restore takes the host lock and the root journal
    lock, so it races nothing.
  - **The restore** writes `restored-by-timer` to the journal.
  - **Disarm.** `disarm --run RUN-ID` removes the units. It is accepted only
    from a session that has `sshd` as a process ancestor, never `tailscaled`,
    with a login session ID different from the one that armed it.
    - T3 moves the ancestor check from bash (`remote_login_ancestor_present`)
      into Go for both OSes.
    - Disarm needs the second tailnet device from G1.
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
  the kit created. Deleting an archive is a separate step the human approves.
- **Shell baseline.** `install-shell-baseline.sh` installs `~/.zshrc` and
  `~/.zsh_plugins.txt` only when they are absent. When a CLI installer has
  written a `.zshrc` first, apply stops and asks for a manual merge, and a
  host where the phase never ran has no baseline at all. From T4, in the
  shell unit:
  - an existing `.zshrc` gets a delimited managed block, with other tools'
    blocks kept, a backup first and a syntax check (`zsh -n`) before the
    swap; revert removes only the block;
  - interactive extras (prompt themes, autosuggestions, highlighting,
    framework aliases) load only in interactive human shells, and are
    skipped when an agent harness marks its tool shells (Claude Code sets
    `CLAUDECODE`), so agent commands see a predictable environment. The
    shell unit's page documents the guard.

### 7a. Resources (S7, T4)

A supervised pilot showed the failure this stage must prevent: one test
suite grew to about a third of RAM, several sessions ran full gates in
parallel, swap filled, and every agent session and the desktop stalled for
hours, while a guard that watched load and available memory stayed green.

- **A reserve, set at setup.** A share of memory and CPU is kept for the OS,
  the desktop and other users, larger while a desktop session is active.
- **One ceiling for all agent work.** On Linux, a user slice with
  `MemoryHigh` and `MemoryMax`. On macOS, priority and process limits,
  recorded in the capability matrix as a gap.
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
  run time. Config and docs hold no fixed GB or load numbers.
- **The session side** (a runner that caps each heavy job, and a scheduled
  sweep) belongs to the agent tooling, not the kit. The kit installs the
  machine-wide part and checks for it.

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
- **Interview.** `wskit interview --json` emits the questions as data, and
  `--answers FILE` writes a draft profile, which `validate` and `plan` check.
  Secrets are never asked; the interview names the native tool where each one
  is entered.
- **Capability matrix.** `registry/capabilities.toml` gives, per capability:
  the Linux and macOS means, how to verify it, its status per OS and its gaps.
  `plan` refuses a component that is unsupported on the target OS, with the
  reason and any equivalent.
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
    it counts as practically outdated.
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
- **`wskit registry bump TOOL` (T6),** the resolve flow from the amendment
  on resolving "latest". Looking up a newer version is allowed. It
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
  - **Permission:**
    - automatic only for low-risk patch updates that the entry's approval
      policy allows;
    - human approval for minor and major updates, for tools that run as root,
      touch the network or the security boundary, or handle credentials, and
      for any update during a live setup.
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
- **Staging (T6).**
  - **Each release:** `wskit stage` copies a released binary and its data
    files to `/opt/agent-workstation-kit/releases/<version>/`, verifies
    their checksums against the registry, makes the tree root-owned, swaps
    `current` atomically and keeps the last N releases.
  - **The fleet checkout** is staged the same way.
  - **The first staging** has no staged copy to run from. The root step
    copies the downloaded binary into a root-owned temporary folder, checks
    its checksum and signature there with system tools (the checksum tool
    and `ssh-keygen -Y verify`) against the values embedded in the one-liner
    and the docs, and only then runs `wskit stage` from that copy. A file
    the setup user could still change is never run as root.
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
    in the one-liner text and in the docs. From T6 every release is signed,
    and the one-liner verifies both the checksum and the signature
    (`ssh-keygen -Y verify`, which ships with both OSes). From P0 to T5,
    releases carry checksums only, and staging is the documented root step
    in section 3.
  - It prints the sign-in step, then hands over to the conductor.
- **Filesystem snapshots (T6).** Btrfs, ZFS or LVM-thin snapshots are taken at
  baseline and at each stable point where available.
  - On macOS, APFS local snapshots are purged after about a day, so they are
    only a short-term net around a stage.
  - Revert never depends on any snapshot.

What revert cannot undo, listed in every revert preview:
- the OS install, disk encryption and firmware;
- sign-ins and tokens on external services (revoked through a hand-over);
- data that agents produced (archived, never rolled back);
- package upgrades, whether by the kit or by OS security updates;
- the day-zero kit copy and agent CLI in the setup user's home, which come
  before the baseline and are removed by the documented uninstall;
- S1's staged releases and state folders (also the uninstall).

### 10. Agent settings (T7) and doctor (T8)

- **Agent settings (T7)** is the component designed earlier on issue #5:
  - presets: `keyring-clis`, `git-network`, `orchestrator`, `cloud-clis` and
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
- **MCP preset (T7).** MCP servers are declared once, independent of chat
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

- **What it is.** The `browser-tunnel` unit: `wskit tunnel` commands on the
  client, a browser hook on the workstation, and managed SSH host entries.
  Together they finish a remote CLI's localhost sign-in, or show a
  workstation page, in the client's own browser, over a loopback-only SSH
  tunnel that closes itself. It replaces the earlier `awk-auth` and
  `awk-open` scripts.

  ```text
  workstation (Linux)                          client (macOS or Linux)
    CLI waits on 127.0.0.1:P                     wskit tunnel auth --host WS 'URL'
    BROWSER hook hands the URL over  ------->      | reads P from a loopback redirect_uri
                                                   | ssh -L 127.0.0.1:P:127.0.0.1:P WS
    127.0.0.1:P <---- SSH, loopback only -----     | opens URL in the system browser
    callback served                                | closes: callback done or time limit
  ```

- **Workstation side (Linux).** `BROWSER` points at the hook, which receives
  the URL a CLI tries to open and hands it to the client as a hand-over line
  with the exact client command. It never opens a listener of its own. On a
  macOS workstation, many CLIs open URLs through Launch Services and ignore
  `BROWSER`, so the workstation side is Linux only, and macOS is recorded as
  a gap in the capability matrix.
  - **Where `BROWSER` is set.** A marked block in `~/.zshenv`, which every
    zsh reads, interactive or not, so agent tool shells get it too. The
    block sets `BROWSER` only when `SSH_CONNECTION` is set, so a local
    desktop session keeps opening its own browser. Revert removes only the
    block.
- **Client side (macOS or Linux).** The client opens the URL in the system
  browser. When the URL carries a loopback redirect URI (`localhost` or
  `127.0.0.1` with a port), it first opens an SSH `-L` forward for that one
  port, bound to the client's loopback, and keeps it only until the callback
  completes or the time limit passes. `wskit tunnel open` shows a plain
  local page, such as a dev server on a workstation port, on request, under
  the same rules.
  - **Host entries** live in a kit-owned file,
    `~/.ssh/config.d/agent-workstations`, included by one marked `Include`
    line at the top of `~/.ssh/config`. Entries use the tailnet name, never
    a raw address.
  - **Time limit:** 600 seconds by default, settable from 30 to 3600. A
    watchdog closes the forward at the limit even if the client process is
    stuck.
  - **The "tunnel ready" handshake.** Readiness and callback progress are
    read only from the local `ssh` client's own debug log (`ssh -v -E
    FILE`), which the workstation cannot write. Text that arrives over the
    session's output is never trusted, so a workstation that prints a
    "forwarded" line changes nothing. A test proves this.
- **Scope.** User scope only, on both sides. No root, no system settings.
- **Coexistence.** It works alongside the mesh VPN (host entries use the
  tailnet name), NoMachine and other remote desktops. It binds only client
  loopback ports, and refuses a port that is already in use.
- **A unit (section 2a).** `plan`, `apply`, `verify` and `revert` cover its
  SSH host entries and the hook setting, with a preview, a dated backup,
  atomic writes inside a marked block, and an undo that removes only what it
  wrote. The binary is pinned like every release. Its client rows follow the
  platform rule (section 9).
- **Order.** It is needed day to day and runs only in user space. It comes
  right after T1, whose CLI skeleton, unit interface and platform table it
  uses, and before T2, with its own backup and undo. When T2 lands, it moves
  onto T2's client mode (section 5) for its writes, logs and undo. T3
  composes its workstation hook into S6; until then it runs standalone only.
- **Security properties** carried over from the earlier scripts: loopback
  only, a time limit enforced by a watchdog, no secrets in logs, and
  refusing "tunnel ready" lines faked by the workstation.
- **Tests:** standalone apply and revert of the host entries and the hook;
  the watchdog time limit; a port already in use; a faked "tunnel ready"
  line; a redirect URI that is not loopback (no forward); and an
  interruption at each write.
- **Later.** It uses the T3 hand-over format once T3 lands.
- **Gate.** The full gate.
- **Starting point.** The earlier Python scripts and their tests, on the
  maintainer's unpublished branch `feat/remote-browser-signin`
  (`scripts/lib/remote_signin.py`, `tests/test_remote_signin.py`). The
  unit's epic carries their behaviours and test cases as the parity list.

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
| Company tools | Registry entries in the fleet, in the kit's registry format, with the same resolve flow. | T6 |
| Machine records | `records/HOST.toml` in the fleet: the OS version and its vendor end-of-support date, where the baseline and stable points are kept, and every hand change. Structured, so the T8 drift check can read it. `propose-change` with kind `machine` drafts these entries. The host keeps a root-only copy for the doctor. | T2 (format), T8 (drift check) |

### 10c. Corporate IPsec VPN client (component, after T8)

A unit for a split-tunnel corporate VPN (requirement R-NET-19). A pilot
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
- **Each PR's gate:**
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
     baseline, a macOS-only run, a user-scope-only run, a failed root
     approval, a fleet override, and agent output against terminal output.
  3. **Consistency:** units, stages, T-items and epics map one to one, and
     the dependencies have no cycles.
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
versions, so on macOS an `os-managed` pin usually moves through `registry
resolve` before a new install can succeed.

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
| Principle 7 | `latest` not allowed | `latest` and ranges refused for every source; enforcement per source kind is proposed amendment A1 | Some sources keep no old versions |

## Consequences

- One path to apply, revert and audit; documented `fleetctl run --apply`
  commands go away.
- The kit moves to Go: one `wskit` binary replaces `agentctl`, `fleetctl`
  and `setup-workstation.sh`. The Python and shell are frozen and deleted
  piece by piece at parity.
- macOS root scopes work again (T1).
- More state on disk (backups, logs, baseline). The doctor reports its size,
  and backups older than the stable point can be pruned by an approved step.
- Units have `Describe` and never prompt. macOS account passwords
  move to G1.
- Existing receipts are invalidated once, by T1; the next run re-records
  them.
- Profiles move to schema 4 with a migration.
- Tool updates become registry pull requests with a security note.
- The kit stays a release candidate (`0.2.0-rc.N`) until the acceptance
  below.

## Acceptance for 0.2.0

- T1 to T8 merged, each with its evidence.
- An agent conducts one clean setup on a supported Linux machine and one on a
  supported macOS machine. Each run:
  - goes from the interview to the stable point;
  - includes one deliberate interrupt that the agent diagnoses and resumes;
  - ends with `revert --to baseline` passing the match digest (section 5),
    in a fixture or a disposable machine.
- The browser tunnel runs on T2's client mode, standalone and composed.
- The doctor is green, and a docs-only walkthrough passes.

## Pull request order

```text
D0 (this ADR, reviewed alone)
 -> T1 Go CLI skeleton (wskit), unit interface, engine locks and receipts, schema, platforms,
       plan, next --json, conductor recipe and review rules in AGENTS.md, CI builds on Linux
       and macOS; frozen-code fixes: fleetctl run --apply refused, macOS host lock,
       remote-desktop guard
 -> browser tunnel unit and host entries, on T1, with its own backup and undo
 -> T2 engine: transaction layer, baseline scope, ledger, match digest, revert (no accounts,
       SSH or firewall yet), logs, failure records, client mode; the tunnel moves onto it
 -> T3 gates (G0 replaces the recovery flags), hand-overs with su -P, stages composing
       units, dead-man timer (revert of SSH and firewall enabled), mesh VPN, human-steps policy
 -> T4 lockout and convergence units, shell managed block, resources (S7), account archive
       (revert of accounts enabled)
 -> T5 interview, shell prompt field, schema 4 migration, capabilities, fleet schema fragment,
       generated docs
 -> T6 tool registry and bump flow, generated pins, staging, day-zero one-liner, snapshots
 -> update watch (follows T6)
 -> T7 agent settings, every config home, MCP preset
 -> T8 doctor, fleet machine-record drift
 -> remaining units: corporate IPsec VPN, workspace and repositories, explainer refresh;
       each frozen script is deleted when its unit reaches parity

Public-safety scan hardening: its own security PR, at any point.
```
