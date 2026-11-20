# ADR 0005: Setup architecture

[Documentation home](../README.md) · [Previous decision](../decisions/0004-reproducibility-and-backup.md)

- Status: proposed (design PR D0; no code yet)
- Date: 2026-11-20
- Source: the consolidated design on issue #5 (comment 7048343188) and its
  amendments on resolving "latest" (comment 7048398540), OS support
  (comment 7048442102) and the tunnel's order (comment 7048586326), refined against the
  code at the time of writing
- Audience: maintainers and reviewers. The conductor agent and operators get
  their own pages as each part lands.

## Summary

The kit's phase scripts are careful, but the layer around them is not yet one
system:

- **Two apply paths.** There are two ways to apply changes, and one of them
  skips every safeguard.
- **Backups.** They last only for one script run.
- **Logs.** There are no log files.
- **Human steps.** They sit in the middle of automated scopes.
- **Setup is human-driven** from long runbooks.

This ADR sets one architecture for setup, upkeep and revert on Ubuntu and
macOS:

- one apply path through one executor;
- every change is a transaction with a backup and a receipt;
- a baseline and stable points the machine can return to;
- gates for the steps only a person can do;
- a conductor agent that drives the run from right after the OS install;
- one schema, one platform matrix, one capability matrix and one tool
  registry that drive validation, plans, docs and installs.

Eight pull requests (T1 to T8) build it in order, with the browser sign-in
tunnel right after T2. Each one ships with tests,
docs, an adversarial review and an interruption stress run.

```text
HUMAN (critical steps)          CONDUCTOR AGENT (setup user, no root)   KIT (one apply path)
-----------------------------   -----------------------------------     ------------------------------
OS install, disk encryption
day-zero one-liner -----------> kit release + agent CLI installed
agent CLI sign-in ------------> reads the AGENTS.md conductor recipe
interview answers <-----------> writes the machine TOML from the schema  validate: schema, platforms,
approve config ---------------> shows the plan and every non-default       capabilities, tool registry
                                loop: next --json                         S1 Stage    (baseline)
root approval on a terminal <--   root step: prepared hand-over           S2 Packages
                                  user step: preview, apply, verify       S3 Identity and accounts
G1 passwords, keys, mesh VPN,<-   gate: hand-over, poll the probe         G1
   recovery test                                                          S4 Services
                                                                          S5 Remote   (dead-man timer)
G2 GUI session, macOS privacy<-                                           G2
   prompts, FileVault                                                     S6 Agent    (settings, tools)
G3 provider sign-ins <---------                                           G3
                                                                          S7 Resources
approve repair or revert <-----  on failure: read the failure record,     S8 Audit and reboot
                                   propose repair, retry or revert           -> stable point
                                 stage summaries to the human; final report
```

## Context

What the code does today, and the problem each fact causes:

| Fact | Where | Consequence |
|---|---|---|
| `fleetctl run PROFILE PHASE --apply` runs a phase script directly | `scripts/fleetctl.py` (end of `main`) | No executor, no lock, no receipt and no staged-root check on this path. `docs/runbooks/day-zero-linux.md` documents it for root phases. |
| The executor previews, then applies, then verifies each phase and writes a receipt keyed by an input digest | `scripts/execute-setup.py` `execute()` | Good base. There is no backup, no intent record before the apply, and no log file. |
| A phase without a command stops the scope with `HUMAN GATE` and exit 3 | `execute-setup.py` `execute()` | A person's step sits inside an automated scope, with no probe to resume on. |
| Backups are temporary | `harden-remote-access-linux.sh` (the UFW backup folder is removed on exit) | No restore after the run ends. |
| `log`, `warn` and `die` print to the terminal only | `scripts/lib/common.sh` | No durable log, and no redaction. |
| A managed file that differs stops the phase ("merge it manually") | `harden-remote-access-linux.sh` | A rerun cannot converge. |
| The SSH install guard masks units before writing its record | `scripts/lib/package_setup.py` `guard_new_ssh_install` | A kill between the two leaves masks the kit cannot release. |
| sudoers and the agentctl config are installed with `install` | `install-agentctl-linux.sh` | Validated before, but not written atomically or rechecked as a whole after. |
| Supported OS versions are hard-coded | `execute-setup.py` `require_supported_linux` | Ubuntu only, and macOS versions are unchecked. |
| Scopes per OS differ | `scripts/lib/setup_scopes.py` | Linux `machine` holds identity, accounts, remote hardening, agentctl and workloads. macOS has no remote-hardening phase, and its workloads run in the agent scope. |
| Root phases on macOS run under the system Python 3.9 | `execute-setup.py` docstring | The root executor cannot use `tomllib`. |
| Tool versions live in hand-edited files | `config/mise.toml`, `config/packages.toml` | No single record of source, checksum, licence or review. |

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
   and a recorded security review. A newer version can be looked up, but only
   `registry resolve` turns it into a pin, after the compatibility checks,
   vetting and permission. Nothing floats at apply time.
8. **Documented from one source.** Each setting states what it does, why, its
   options, the safe default, the recommendation, the risk and the undo.
9. **Nothing ad hoc.** No hard-coded users, hosts or paths. Generic logic
   lives in the kit, company values in the fleet, host facts in the machine
   record. An unforeseen change needs permission, is logged and is written
   back.
10. **Verified before merge.** Tests, an adversarial review of code and docs,
    and a stress run with interruption at every boundary plus a docs-only
    walkthrough.

### 2. Stages, gates and owners

| Stage | Linux | macOS | Runs as | Reversible |
|---|---|---|---|---|
| S0 Configure | interview, machine TOML, `plan` | same | setup user (conductor) | yes (a file) |
| S1 Stage | versioned `/opt` release; baseline snapshot | same | root (human approval) | yes |
| S2 Packages | dpkg repair, apt packages, mesh VPN client, SSH install guard | Homebrew formulae and casks, mesh VPN client | root (Linux) / package owner (macOS) | yes (ledger) |
| S3 Identity | hostname, `/etc/hosts` block, accounts and groups | computer names, local accounts | root | yes (homes archived) |
| G1 Gate | passwords, SSH keys, mesh VPN up, recovery test | passwords, mesh VPN up, recovery test | human, probed | n/a |
| S4 Services | agentctl (atomic sudoers), workloads | none yet (workloads move here from the agent scope only if they need no agent session) | root | yes |
| S5 Remote | SSH and firewall hardening, alone, with the dead-man timer | none today (remote access is set in System Settings at G2) | root | yes, under the timer |
| G2 Gate | agent GUI session | agent GUI session, privacy prompts, FileVault | human, probed | n/a |
| S6 Agent | shell, tools from `mise.lock`, agent settings | same, plus workloads | agent account | yes |
| G3 Gate | provider sign-ins | same | human, probed | revoke by hand-over |
| S7 Resources | measured limits (systemd slices) | partial (see capability matrix) | root | yes |
| S8 Audit | doctor, reboot acceptance, stable point | same | root and agent | n/a |

- A scope is the unit the executor runs, with one approval. A stage is one
  scope; gates are their own one-phase scopes.
- Phase names stay as they are today, so existing receipts carry over.
- Each stage's preflight reads the receipt summary and stops with the next
  step when an earlier stage is not verified.

### 3. One apply path (T1)

- `fleetctl run --apply` exits 2 and prints the executor commands. Preview and
  the read-only `audit` stay.
- With `--apply`, a phase script refuses to start without a run token. The
  executor creates the token in a root-only (or account-only) run folder for
  each run and passes `AWK_RUN_ID`. The token guards against accidental
  bypass; it is not a boundary against root.
- The staged-root check moves into the root block of `common.sh` and into
  `path_safety.require_staged_root()`, which every Python phase calls as root.
- Fleet scripts have no apply path of their own; they call the kit's.

### 4. The agent interface (T1, extended by each PR)

- `setup-workstation.sh next --json` returns the next action, with these
  fields:
  - `schema_version`, `run_id`, `stage`, `phase` and `owner`;
  - `root` and `gate`;
  - `preview_command` and `apply_command`;
  - `handover` (section 6) and `probe`;
  - `log_path`;
  - `blocked_by`.
- `status --json` returns every phase's recorded status, run ID, receipt time
  and failure record.
- `explain PHASE --json` returns what a phase does, its owner, what it may
  touch (from `--describe`), and whether that is reversible.
- No JSON output carries a secret. Each schema has a version, golden-file
  tests and a checked example in the docs.
- Exit codes are the same for every action:

  | Code | Meaning |
  |---|---|
  | 0 | Done, or nothing is next |
  | 1 | A phase failed; read its failure record |
  | 2 | Refused, or a usage error |
  | 3 | A person must act; the hand-over says what |
  | 4 | An interrupted phase must be repaired or restored first |
  | 5 | Refused as root: not the staged copy or not the system interpreter |
  | 130 | Cancelled |

- **The conductor recipe** in `AGENTS.md` is a loop: read `next`, act or hand
  over, verify, report, repeat.
  - The conductor previews and applies the steps that do not need root.
  - For a root step, it prepares the exact staged command and waits for the
    human.
  - At a gate, it shows the hand-over as is and polls the probe.
  - Its rules: no root, no secrets, stop on the second identical failure,
    never edit receipts or journals, never approve or revert by itself.
  - Its user update says what was done, what is next, and what is needed from
    the human.
- The conductor runs as the setup user until the agent account exists. The
  agent-account stage (S6) must run in that account (section 13, question 1).

### 5. The transaction layer (T2)

```text
preflight -> describe -> backup -> intent=applying -> apply (logged) -> verify -> receipt=verified
                                         |                                  |
                    restart finds it ----+                failure ----------+--> restore -> receipt=failed
                    verify passes: converged                                     + failure record
                    otherwise: restore, then reapply
```

- **Describe.** Each phase answers `--describe` with JSON: `paths`, `state`
  providers (hostname, ufw, unit masks, sysctl, packages, accounts) and
  `irreversible` items. The executor fails closed when a phase does not
  answer.
- **Backup.** Before the apply, each item is copied with its owner, mode and
  digest, or recorded as absent. State providers write snapshots.
  `manifest.json` is written and fsynced last, so a manifest only exists for
  a complete backup.
- **Intent.** `status=applying`, with the run ID and the backup path, is
  fsynced to the journal before the apply. On the next start, a phase still
  applying blocks everything else (exit 4) until it is checked:
  - if verify passes, it is recorded as converged;
  - otherwise it is restored, then reapplied;
  - `--restore-only` stops after the restore.
- **Restore.** The manifest is replayed in reverse, then the validators run
  (`sshd -t`, `visudo -c`, `ufw status`).
- **Baseline.** Captured once, before the first change (by S1 from T6; by the
  first kit run on a hand-built host), through the same providers. It covers
  packages with their marks, accounts, the hostname and `/etc/hosts`, services
  and masks, firewall and SSH, `sudoers.d`, the declared `/etc` paths, and the
  declared user files. It is never overwritten.
- **Ownership ledger.** Each item is recorded as `created` or `changed`, taken
  from the providers' before and after views, never from what the phase
  reports about itself. Revert removes only `created` items and restores
  `changed` ones.
- **Stable points.** Each verified stage records a point: run ID, receipts,
  manifests and state digest. The last one after reboot acceptance is tagged
  `stable`. `restore-points [--json]` lists them.
- **`revert --to POINT`** (`baseline`, a point ID, or `stable`):
  - **Preview first.** In reverse stage order, plus what cannot be reverted
    (section 9).
  - **Approval.** Applies only with the human's approval, typed on a real
    terminal for root scopes.
  - **Each step is a transaction**, so an interrupted revert resumes and a
    revert can be reverted.
  - **Drift.** An item changed after the point is flagged as a conflict and
    never overwritten silently.
- **Logs.** The executor tees phase output to the terminal and to the phase
  log (0600) through one redactor. The redactor masks token shapes,
  `password=` style pairs, URL user info and query values, `Authorization:`
  headers and PEM blocks. `common.sh` also appends to `$AWK_LOG_FILE`.
  - **Phases never prompt.** Every prompt for a secret is a gate (section 6),
    which keeps the redacted pipe safe.
- **Failure record.** The last JSON line of a failed phase's log is also
  stored in the journal. Its fields are `phase`, `step`, `error_class`,
  `message`, `suggested`, `log` and `backup`.
  - `error_class` is one of `precondition`, `network`, `package-manager`,
    `validation`, `permission`, `conflict`, `interrupted` or `postcondition`.
  - `suggested` is one of `retry`, `repair`, `restore`, `revert` or
    `hand-over`.
- **Receipt summary.** A root-written summary records each scope's status and
  stable point. Other accounts keep their own, and `status` merges them.
- **Changes on permission.** An unforeseen change goes through
  `propose-change`: a change file with the paths and state, the commands, the
  risk, the undo and a record kind. It is previewed, approved, then applied as
  a transaction. Its record goes to one of three places:
  - `kit`: a generic fix, as a kit pull request;
  - `fleet`: a company value, as a fleet pull request;
  - `machine`: a host fact, in the root-only machine record.
- **Fault injection.** Stub host binaries (`apt-get`, `dpkg`, `systemctl`,
  `ufw`, `visudo`, `sshd`, `hostnamectl`, `dscl`, `launchctl`) on PATH, a fake
  root tree and a fake clock.
  - They kill the run at the Nth call or between any two executor steps.
  - Production code has no fault hooks.

### 6. Gates and hand-overs (T3)

- **A gate** is a phase with a read-only probe. On a pass it is recorded; on a
  fail it prints its hand-over and exits 3. A rerun resumes there.
- **One hand-over object** for every gate, root step, revert and proposed
  change: `machine`, `account`, `step`, `command`, `why`, `verify`, `undo`,
  `probe`. The text form extends `AGENTS.md` section 6 with `Why` and `Undo`.
- **Recovery attestation** is taken at apply time, recorded in the receipt,
  and no longer part of the exported plan.
- **The dead-man timer** guards every apply and revert step that touches SSH
  or the firewall:
  - Before it, the executor arms a restore of that step from its manifest:
    - on Linux, `systemd-run --on-active=10m` with the staged root and the
      system interpreter;
    - on macOS, a root launchd job, if a macOS phase ever changes SSH or the
      firewall (none does today).
  - `disarm --run RUN-ID` is accepted only from a fresh SSH session of a named
    SSH user, not from the session that applied.
  - A revert removes an account only after access is proven with an account
    that stays.

### 7. Lockout and convergence (T4)

- **Atomic writes for sudoers and sshd drop-ins:**
  1. a dotted temp file in the target folder (sudo ignores dotted names);
  2. validate the candidate;
  3. fsync;
  4. rename;
  5. full `visudo -c` or `sshd -t`, restoring on failure.
- **Converge, don't refuse.** Managed files use marked blocks, rewritten with
  a backup. Content outside the markers is kept.
- **dpkg repair first** when dpkg reports a half-configured package.
- **Intent before masks.** The SSH guard record is written as pending before
  `systemctl mask`.
- **Extra ports.** Profile-declared `remote.extra_ports` pass the firewall
  checks in the verifier and in `validate-host.sh`; nothing else does.
- **`/etc/hosts`** gets a marked block owned by the identity phase.
- **Account homes** are archived, root-only, before revert removes an account
  the kit created. Deleting an archive is a separate step the human approves.

### 8. One schema, interview and capabilities (T5)

- **Schema.** `config/schema.toml` declares, for every field: `what`, `why`,
  `options` (each with its effect and risk), `default`, `recommended` (with
  its condition), `stage`, `undo` and `requires`. `requires` may name a
  capability.
- **From the schema:**
  - the profile validator, replacing the hand-written tables in `fleetctl.py`;
  - `docs/reference/config.md`, with a CI staleness check;
  - the interview questions;
  - the plan's list of values set away from their default, each with its
    risk.
- **Profile schema 4:**
  - `tooling.components` names each optional component, with its options;
  - `[agent_settings]` is the agent-settings component's table;
  - `[x-org]` holds fleet keys, which the kit checks for shape only.
  - `check-profile-migration.py` migrates schema 3 to 4 by previewing and
    writing a new file, never editing in place.
- **Interview.** `fleetctl interview --json` emits the questions as data, and
  `--answers FILE` writes a draft profile, which `validate` and `plan` check.
  Secrets are never asked; the interview names the native tool where each one
  is entered.
- **Capability matrix.** `registry/capabilities.toml` gives, per capability:
  the Linux and macOS means, how to verify it, its status per OS and its gaps.
  `plan` refuses a component that is unsupported on the target OS, with the
  reason and any equivalent.
- **The root executor never parses TOML.** `plan --export-plan` resolves the
  schema, registries and profile into the JSON bundle, so root phases on the
  macOS system Python 3.9 keep working.

### 9. Platforms, tool registry and staging (T1, T6)

- **Platforms (T1).** Only versions the vendor still supports are allowed,
  with no best-effort tier.

  | Linux | macOS |
  |---|---|
  | Ubuntu LTS in Canonical's standard support: 24.04 and 26.04 | The current and previous major macOS: 26 and 15, on Apple silicon |

  - `registry/platforms.toml` records, for each version:
    - the architectures;
    - the vendor's end-of-support date;
    - the evidence each release needs: a CI job, or a recorded real-machine
      run.
  - **Preflight**, in readiness and in the executor, refuses everything else.
    That includes Ubuntu interim releases, other distributions, older macOS
    and Intel Macs. The refusal gives the reason and the nearest supported
    version. This replaces the hard-coded check in
    `execute-setup.py` `require_supported_linux`, and adds the macOS check
    that is missing today.
  - **The window follows the vendor lifecycle:**
    - a new release is added once it is validated, with real-machine evidence
      in the changelog;
    - a version is dropped at or before its end-of-support date.
  - The update watch flags upcoming end-of-support dates. The doctor warns a
    machine 90 days ahead.
- **Tool registry (T6).** `registry/tools.toml` records, per tool:
  - the pinned version per OS and its source;
  - a checksum or signature for anything outside an OS package manager;
  - licence, maintainer and release cadence;
  - the last security review and the next review date;
  - the capability it serves, how it uninstalls, and its update policy.
- **Generated from the registry:** `config/mise.toml`,
  `config/packages.toml`, `mise.lock` (installed with `--locked`), a Brewfile
  lock, apt pins and download pins.
  - CI fails on a stale generated file, or on a `latest` or range value in a
    pin.
  - An overdue review warns, then fails after the grace period the registry
    states.
- **`registry resolve TOOL` (T6).** Looking up a newer version is allowed,
  but it becomes installable only through this flow:

  ```text
  lookup -> compatibility -> vetting -> permission -> pin + record -> install
  ```

  - **Lookup.** It finds the newest version from the entry's declared source.
    The update watch or the conductor runs it.
  - **Compatibility.** All four checks must pass:
    - the version is supported on the platforms and architectures in the
      support matrix;
    - it meets the entry's constraints (other tools, runtimes, minimum OS);
    - its checksum or signature verifies;
    - a fixture smoke test passes (version output and the tool's doctor
      probe).

    On any failure the current pin stays, and the reason is reported.
  - **Vetting.** Known vulnerabilities and maintainer status are looked up
    and recorded in the entry.
  - **Permission**, set per entry:
    - automatic only for patch updates within a declared range, for tools
      marked low risk;
    - human approval for minor and major updates, for tools that run as root,
      touch the network or security boundary, or handle credentials, and for
      any update during a live setup.
  - **Pin and record.** The approved version goes into the registry and the
    generated locks through a kit pull request (generic tools) or a fleet pull
    request (company tools), with a changelog entry. The change is logged
    through the transaction layer. Only then is it installed.
  - **Never** a floating version at apply time, a silent upgrade, or a version
    on a machine that kit or fleet does not record. The T8 doctor reports an
    unrecorded version as drift.
  - The update-watch job, after T6, runs `registry resolve` on a schedule and
    opens the resulting pull requests.
- **Staging (T6).** `stage-release.sh` copies a reviewed revision to
  `/opt/agent-workstation-kit/releases/<rev>/`.
  - It verifies the tree digest and makes the tree root-owned.
  - It swaps `current` atomically, and keeps the last N releases.
  - The fleet checkout is staged the same way.
  - The staged-root check accepts `current` only when it resolves under
    `releases/` and the whole chain is root-owned.
- **Day-zero one-liner (T6).** For the setup user, without root:
  - it installs the kit at a verified release tag and one agent CLI from its
    vendor's verified source;
  - it prints the sign-in step, then hands over to the conductor.
- **Filesystem snapshots (T6).** Btrfs, ZFS, LVM-thin or APFS local snapshots
  are taken at baseline and at each stable point where available. They are an
  extra net; revert never depends on them.

What revert cannot undo, listed in every revert preview:
- the OS install, disk encryption and firmware;
- sign-ins and tokens on external services (revoked through a hand-over);
- data that agents produced (archived, never rolled back);
- OS security updates already applied;
- the day-zero kit copy and agent CLI in the setup user's home, which come
  before the baseline and are removed by the documented uninstall.

### 10. Agent settings (T7) and doctor (T8)

- **Agent settings (T7)** is the component designed earlier on issue #5:
  - presets: `keyring-clis`, `git-network`, `orchestrator`, `cloud-clis` and
    `dotnet`;
  - discovery of the user settings, every Orca profile and the Codex configs;
  - merge, never remove;
  - workspace grants and `git-net`;
  - Codex managed keys;
  - the Orca setting is read only and handed over.

  It runs as the `agent-settings` phase in S6, and its planners use the T2
  layer for backup, intent, receipts, logs and revert.
- **Doctor (T8)** is read-only. It checks:
  - every phase's live verifier;
  - the receipt summary and stable point;
  - drift against the ledger and machine record, including changes that have
    no record and tool versions the registry does not record;
  - `agent-settings check`;
  - the OS end-of-support date, with a warning from 90 days ahead;
  - the earlier doctor items.

  `--admin` runs the root-only checks from the staged root and writes a
  non-secret summary for the user-side doctor. A fleet passes `--inputs FILE`
  with expected values, which takes over the fleet's interim checks.

### 10a. Browser sign-in tunnel (right after T2)

- **What it is.** `awk-auth`, `awk-open` and managed SSH host entries finish
  a remote CLI's localhost sign-in, or show a workstation page, in the
  client's own browser, over a loopback-only SSH tunnel that closes itself.
- **Why it comes early.** It is needed day to day, it runs only in user
  space on the client, and it needs only what T2 provides.
- **What it takes from T2:** for its writes to the SSH config, a backup and an
  ownership record; redacted logs; and an undo through `revert`.
- **What it drops.** It replaces its own backup and logging with T2's. The
  security properties it already has stay the same: loopback only, a time
  limit enforced by a watchdog, no secrets in logs, and refusing readiness
  lines faked by the workstation.
- **Later.** It uses the T3 hand-over format once T3 lands.
- **Its gate** is the same as for every PR: tests, an adversarial review of
  code and docs, a stress run and a docs-only walkthrough.
- **Starting point.** An earlier standalone version passed its own review and
  stress run but was never opened. It is the starting point.

### 11. Where state lives

| What | Linux | macOS | Mode |
|---|---|---|---|
| Kit releases | `/opt/agent-workstation-kit/releases/<rev>`, `current` | same | root, 0755, not writable by others |
| Run tokens and host lock | `/run/agent-workstation-kit/` | `/var/run/agent-workstation-kit/` | root 0700 / 0755 |
| Root receipts, summary, machine record | `/var/lib/agent-workstation-kit/` | `/Library/Application Support/agent-workstation-kit/state/` | root 0700, summary 0644 |
| Root backups, baseline, archives | `/var/backups/agent-workstation-kit/` | `/Library/Application Support/agent-workstation-kit/backups/` | root 0700 |
| Root logs | `/var/log/agent-workstation-kit/<run-id>/` | `/Library/Logs/agent-workstation-kit/<run-id>/` | root 0700, files 0600 |
| Account state, backups, logs | `${XDG_STATE_HOME:-~/.local/state}/agent-workstation-kit/` | same | 0700, files 0600 |

Every root path is created by S1, checked with the existing
`secure_root_path` rule, and named in the capability matrix.

### 12. Documentation and verification

- **Pages.** Every stage and component has a page under `docs/reference/`,
  with what it does, why, the options, the safe default and recommendation,
  the risks and the undo, plus its audience. Each PR updates its own pages.
- **CI checks:**
  - the generated reference is current;
  - every field, stage, component and flag is documented;
  - every documented command exists and matches its `--help`;
  - local links resolve (external links in a scheduled job);
  - the public-safety scan passes.
- **Doc tests.** From T2 on, runbook command blocks run in preview mode
  against fixtures. Each block is marked `preview-tested` or `human-only`.
- **Each PR's gate:**
  - tests;
  - an adversarial review of code and docs;
  - a stress run that kills at every boundary;
  - a docs-only walkthrough by a fresh agent;
  - a changelog entry.

### 13. Open questions for the D0 review

1. **Who runs S6.** The conductor runs as the setup user, but S6 must run in
   the agent account, and the agent account's own CLI is only signed in at
   G3. Proposal: at G2 the human opens the agent account's session. The
   conductor there starts from `next --json` with no provider sign-in needed
   for S6, because S6 installs tools and settings only. G3 follows.
2. **macOS remote access.** No macOS phase changes SSH or the firewall today.
   Should S5 gain a macOS phase (Remote Login restricted to named users, the
   Application Firewall, pf), or stay a G2 hand-over?
3. **Workloads on macOS** run in the agent scope today. Moving them to S4
   needs them to work without an agent GUI session.
4. **Baseline for an already-managed host.** Its baseline is the as-found
   state at the first run of the new executor, not the true pre-kit state.
   The preview of `revert --to baseline` must say so.
5. **Run token for non-root scopes.** Stored under the account's runtime
   folder, it protects only against accidental bypass by that account. Is
   that enough, or should non-root phases also check that their parent is the
   executor?

## Consequences

- One path to apply, revert and audit; documented `fleetctl run --apply`
  commands go away.
- More state on disk (backups, logs, baseline). The doctor reports its size,
  and backups older than the stable point can be pruned by an approved step.
- Phase scripts gain `--describe` and lose prompts.
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
  - ends with `revert --to baseline` matching the baseline digest, in a
    fixture or a disposable machine.
- The doctor is green, and a docs-only walkthrough passes.

## Pull request order

```text
D0 (this ADR, reviewed alone)
 -> T1 one apply path, agent interface, platforms
 -> T2 transaction layer, baseline, ledger, revert, logs, failure records
 -> browser sign-in tunnel and host entries, on T2
 -> T3 gates, hand-overs, scope order, dead-man timer
 -> T4 lockout and convergence fixes, account archive
 -> T5 schema, interview, schema 4, capabilities
 -> T6 tool registry and resolve flow, staging, day-zero one-liner, snapshots
 -> T7 agent settings
 -> T8 doctor
 -> components: corporate IPsec VPN, explainer refresh, update watch
```
