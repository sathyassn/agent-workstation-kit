# ADR 0005: Setup architecture

[Documentation home](../README.md) · [Previous decision](../decisions/0004-reproducibility-and-backup.md)

- Status: proposed (design PR D0; no code yet)
- Date: 2026-11-20
- Source: the consolidated design on issue #5 (comment 7048343188) and its
  amendments on resolving "latest" (7048398540), OS support (7048442102) and
  the tunnel's order (7048586326). It is refined against the code, and revised
  after rounds 1 to 5 of the D0 adversarial review.
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
  registry that drive validation, plans, docs and installs.

Eight pull requests (T1 to T8) build it in order, with the browser sign-in
tunnel right after T2. Each one ships with tests, docs, an adversarial review
and an interruption stress run.

```text
HUMAN (critical steps)          CONDUCTOR AGENT (setup user, no root)   KIT (one apply path)
-----------------------------   -----------------------------------     ------------------------------
OS install, disk encryption
day-zero one-liner -----------> kit release + agent CLI installed
agent CLI sign-in ------------> reads the AGENTS.md conductor recipe
interview answers <-----------> writes the machine TOML from the schema  validate: schema, platforms,
approve config (fleet PR) ----> shows the plan and every non-default       capabilities, tool registry
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
| Stage | A step in the setup order, S0 to S8. Each stage is exactly one executor scope; `--scope` takes the stage's name, such as `packages` or `remote`. Some scopes share a name with a phase inside them: scope `identity` holds the phases `identity` and `accounts`, and scope `audit` holds the phase `audit`. |
| Gate | A one-phase scope with a read-only probe and a hand-over, G0 to G3. |
| Phase | One kit script inside a scope. Phase names stay as they are today. |
| Run | One executor invocation. Its run ID is 32 lower-case hex characters. |
| Point | A recorded restore point. Its ID is `baseline` or `sp-<stage>-<first 8 characters of the run ID>`; `stable` is an alias for the newest tagged point. |
| Public export | Non-secret status that root writes for the conductor to read (section 11). |

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
7. **Pinned and vetted.** Every tool has a recorded version, a verified source
   and a recorded security review, under one of the version policies in
   decision D2 (section 13). A newer version becomes a pin only through
   `registry resolve`. Nothing floats at apply time.
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

| Stage (`--scope`) | Linux | macOS | Runs as | Reversible |
|---|---|---|---|---|
| S0 Configure | interview, machine TOML, `plan` | same | setup user (conductor) | yes (a file in the fleet) |
| G0 `recovery` | console recovery tested | same, with the recovery key | human; probe: attestation typed at the gate | n/a |
| S1 `stage` | versioned `/opt` release (T6), state folders, baseline | same | root | no; removed by the documented uninstall |
| S2 `packages` | dpkg repair, apt packages, vendor packages, hardware drivers, mesh VPN client; install guard on SSH and remote desktop | Homebrew formulae and casks (package owner); mesh VPN client from the vendor `.pkg` (root sub-phase) | root, or package owner for Homebrew | yes (ledger) |
| S3 `identity` | hostname, `/etc/hosts` block, accounts and groups | computer names; accounts created with a disabled password | root | yes (homes archived, from T4) |
| G1 `access` | passwords, SSH keys, mesh VPN up, a second tailnet device holding a named user's key | passwords, secure tokens, mesh VPN up (system extension approved), Remote Login and Screen Sharing enabled for named users only, a second tailnet device | human; conductor polls the non-root probes, root check once at the end | n/a |
| S4 `services` | agentctl (atomic sudoers), workloads | none | root | yes |
| S5 `remote` | SSH and firewall hardening, alone, under the dead-man timer; releases the SSH and remote-desktop install guards | SSH limited to named users, Application Firewall checked, under a launchd dead-man | root | yes, under the timer |
| G2 `session` | agent desktop or console session; the conductor's CLI signed in there | same, plus privacy prompts; FileVault verified | human; agent-account probe | n/a |
| S6 `agent` | shell, mise tools, agent settings | same, plus workloads (OrbStack needs the agent's GUI session) | agent account | yes |
| G3 `sign-ins` | provider sign-ins | same | human; agent-account probe | revoke by hand-over |
| S7 `resources` | measured limits (systemd slices); deferrable | a gate until a macOS method is qualified; deferrable | root | yes |
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

### 3. One apply path (T1)

- **`fleetctl run --apply`** exits 2 and prints the executor commands. Preview
  and the read-only `audit` stay. T1 updates every document that shows it
  (context table) and adds a repository check that fails if one returns.
- **Every root script is a phase or a recorded change.**
  - T1 turns `install-local-deb-linux.sh` (vendor packages) and
    `install-ms-s1-r8127-dkms.sh` (a hardware driver) into S2 phases,
    selected by the profile. T5 makes them components.
  - **Remote desktop.** The SSH install guard is extended to the remote
    desktop. NoMachine's service is masked before the package is installed,
    with its record written first. It stays masked until S5 has the firewall
    in place and releases it. This is the
    exposure the SSH guard already prevents (`package_setup.py`
    `guard_new_ssh_install`).
- **Run token.** With `--apply`, a phase starts only if both of these hold:
  - it inherited the executor's held journal lock: the descriptor's device
    and inode match the lock file, and a fresh non-blocking lock attempt
    fails;
  - `AWK_RUN_ID` matches `^[0-9a-f]{32}$` and the run's record in the run
    folder.

  `common.sh` adds only `AWK_RUN_ID` to its clean re-run, validated. This
  rejects tokens left by a crashed run. It guards against bypass by mistake,
  not against root.
- **The run ID and descriptors do not reach tools.** Right after its check,
  the phase removes `AWK_RUN_ID` and `AWK_FAILURE_FD` from its environment.
  - **Python phases** rely on `close_fds`, the default, for every tool they
    start.
  - **Bash phases** close every inherited descriptor except the lock
    descriptors (the journal lock, and the host lock for root runs) and the
    failure descriptor. They keep holding the locks so that no second apply
    can start if the executor is killed. They close each of these for every
    command they run (`cmd {lock}>&- {fd}>&-`). This covers
    every command, not only those run through the `run` helper, so a daemon a
    package starts can never hold the host lock.
- **Staged-root check.** `path_safety.require_staged_root()` is extended to
  every Python phase that runs as root. The root block of `common.sh` gets the
  same check.
- **macOS host lock.** The fix moves the macOS run folder under a root-owned
  0755 parent (section 11). A test runs on real macOS in CI.
- **One OS policy.** The platform check (section 9) replaces all four version
  checks in the context table.
- **CI** gains a macOS job and a Python 3.9 job. Root executor code must run
  on 3.9. Where hosted runners lack a supported macOS version, a recorded run
  on a real machine is the evidence.
- **Fleet scripts** have no apply path of their own; they call the kit's.

### 4. The agent interface (T1, extended by each PR)

- **`setup-workstation.sh next --json`** returns the next action. It finds the
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
- **`explain PHASE --json`** returns what a phase does, its owner, what it
  may touch (from `--describe`, T2), and whether that is reversible.
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
  | 5 | Refused as root: not the staged copy, or not the system interpreter |
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
  Config approval is enforced by the fleet's branch protection: the plan uses
  only the committed profile. A non-root `--approve-plan` is procedural. The
  conductor could pass it itself, so the recipe forbids that, and the receipts
  record who ran the step.
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
  - `setup-workstation.sh resume` does this, and is the command that clears
    exit 4; `--restore-only` stops after the restore.
- **Restore** writes `status=restoring` first. The manifest is replayed in
  reverse, then the validators run (`sshd -t`, `visudo -c`, `ufw status`).
  Restore is idempotent, and a `restoring` phase is always finished before
  anything else runs.
- **Signals and the terminal.**
  - The executor and each phase ignore SIGHUP, and each phase runs in its own
    session. Output goes to the log first; a lost terminal never stops a run.
  - On SIGINT or SIGTERM, the executor stops at the next safe point: it lets
    the running phase finish, or after a grace period stops it, leaving the
    intent record for the next start. It then exits 130.
- **Baseline.** A root step captures it once, before any other scope:
  - from T2 on, as the first action of the first root scope; from T6, as
    part of S1;
  - on macOS, the first scope is `packages`, run by the package owner, so the
    executor refuses it until the root baseline exists.
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
  `changed` ones and recreates `removed` ones.
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
    tool.
- **Failure record.**
  - A phase reports a failure as JSON on a private descriptor the executor
    gives it (`AWK_FAILURE_FD`), never on its output.
  - Tools the phase starts do not inherit the descriptor (section 3), so tool
    output cannot forge a record.
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
    - `machine`: a host fact, written to the host record and drafted into the
      fleet's machine record (section 10b).
- **Fault injection.**
  - **The stubs:** host binaries on PATH (`apt-get`, `dpkg`, `systemctl`,
    `ufw`, `visudo`, `sshd`, `hostnamectl`, `dscl`, `sysadminctl`,
    `launchctl`, `socketfilterfw`), a fake root tree and a fake clock.
  - **The kills:** at the Nth call, or between any two executor steps,
    including during the backup, the intent write, restore, revert, and
    arming and disarming the timer.
  - Production code has no fault hooks.
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
    service that restores the step from its manifest with the staged root and
    the system interpreter, and a timer with `OnActiveSec=10min` and
    `OnBootSec=2min`. A power loss before disarm therefore restores at the
    next boot. The restore names the exact `releases/<rev>`, not `current`,
    so a release swap while the timer is armed cannot change the restore
    code.
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
      into shared Python for both OSes.
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
- **Converge, don't refuse.** Managed files use marked blocks, rewritten with
  a backup. Content outside the markers is kept.
- **dpkg repair first** when dpkg reports a half-configured package.
- **Intent before masks.** The SSH guard record is written as pending before
  `systemctl mask`.
- **Extra ports.** Profile-declared `remote.extra_ports` pass the firewall
  checks in the verifier and in `validate-host.sh`; nothing else does. The
  NoMachine port is opened by S5, which also releases its install guard.
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
  - `[x-org]` holds fleet keys. The kit checks their shape, and a fleet may
    supply a schema fragment for them (section 10b).
  - A new `scripts/migrate-profile.py` previews and writes a schema 4 file and
    never edits in place. `check-profile-migration.py` stays a checker.
- **Interview.** `fleetctl interview --json` emits the questions as data, and
  `--answers FILE` writes a draft profile, which `validate` and `plan` check.
  Secrets are never asked; the interview names the native tool where each one
  is entered.
- **Capability matrix.** `registry/capabilities.toml` gives, per capability:
  the Linux and macOS means, how to verify it, its status per OS and its gaps.
  `plan` refuses a component that is unsupported on the target OS, with the
  reason and any equivalent.
- **The root executor never parses TOML.** `plan --export-plan` runs in the
  user's Python 3.11 and resolves everything into the JSON bundle. That
  includes the platform table from T1 and the schema and registries from T5
  and T6. Root phases on the macOS system Python 3.9 keep working.

### 9. Platforms, tool registry and staging (T1, T6)

- **Platforms (T1).** Only versions the vendor still supports are allowed,
  with no best-effort tier. The rule is fixed; the list lives in
  `registry/platforms.toml`, so it moves with the vendors.
  - **The rule:**
    - Ubuntu LTS releases in Canonical's standard support;
    - the current and previous major macOS, on Apple silicon.
  - **The list is decision D1 for the operator** (section 13). The ADR
    proposes Ubuntu 24.04 and 26.04, and macOS 27 and 26.
  - **What it records** for each version: the architectures, the vendor's
    end-of-support date, and the evidence each release needs (a CI job, or a
    recorded real-machine run).
  - **Client rows** for the browser sign-in tunnel follow the same rule.
  - **Preflight**, in readiness and in the executor, refuses everything else,
    with the reason and the nearest supported version. The table travels in
    the exported plan.
  - **The window follows the vendors.** A release is added once validated,
    and dropped at or before its end-of-support date. The update watch flags
    dates that are coming up, and the doctor warns a machine 90 days ahead.
- **Tool registry (T6).** `registry/tools.toml` records, per tool:
  - its version policy, one of those in decision D2 (section 13): `exact`,
    `os-managed` or `self-updating`;
  - its source;
  - a checksum or signature for anything outside an OS package manager;
  - licence, maintainer and release cadence;
  - the last security review and the next review date;
  - the capability it serves, how it uninstalls, and its update policy.

  OS-managed packages update through the OS security channel, which stays on
  (`unattended-upgrades`). A newer observed version is recorded, not reported
  as drift. This matches ADR 0004.
- **Generated from the registry:** `config/mise.toml`,
  `config/packages.toml`, `mise.lock` (installed with `--locked`) and the
  download pins.
  - CI fails on a stale generated file, or on a `latest` or range value in an
    `exact` pin.
  - An overdue review warns, then fails after the grace period the registry
    states.
- **`registry resolve TOOL` (T6).** Looking up a newer version is allowed. It
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
    - automatic only for low-risk patch updates within a declared range;
    - human approval for minor and major updates, for tools that run as root,
      touch the network or the security boundary, or handle credentials, and
      for any update during a live setup.
  - **Pin and record:** a kit PR (generic) or a fleet PR (company tools), with
    a changelog entry, logged through the transaction layer. Only then is it
    installed.
  - **Never** a floating version at apply time, a silent upgrade, or an
    unrecorded version of an `exact` tool. The T8 doctor reports one as
    drift.
- **Mesh VPN client in S2 (T3).**
  - **Linux:** Tailscale's apt repository joins the approved sources in
    `config/packages.toml`, pinned by its signing-key fingerprint. It moves
    into the registry in T6.
  - **macOS:** a root S2 sub-phase installs the vendor `.pkg`, verified by
    checksum (`installer -pkg`). The app updates itself, so its policy is
    `self-updating`. The Homebrew cask is not used,
    because it runs the same `.pkg` with `sudo` and prompts. Approving the
    system extension is a G1 hand-over.
- **Staging (T6).**
  - **Each release:** `stage-release.sh` copies a reviewed revision to
    `/opt/agent-workstation-kit/releases/<rev>/`, verifies the tree digest,
    makes the tree root-owned, swaps `current` atomically and keeps the last
    N releases.
  - **The fleet checkout** is staged the same way.
  - **The first staging** has no staged copy to run from.
    1. The human downloads the release archive.
    2. They verify it against the digest and the release public key that the
       one-liner embeds (`ssh-keygen -Y verify`).
    3. They extract it as root into a root-owned temporary folder with
       `tar --no-same-owner`.
    4. They run `stage-release.sh` from there.
  - **The command path** becomes
    `sudo /bin/bash /opt/agent-workstation-kit/current/scripts/...`. The
    symlink check in `setup-workstation.sh`, the executor's root check and
    `path_safety.py` accept `current` only when it resolves under `releases/`
    and the whole chain is root-owned.
- **Day-zero one-liner (T6).**
  - It installs, for the setup user and without root, the kit at a release
    tag and one agent CLI from its vendor's verified source.
  - Its trust anchor is the full release public key, embedded in the
    one-liner text and in the docs. The kit archive is verified with
    `ssh-keygen -Y verify` against an allowed-signers file made from that key.
    `ssh-keygen` ships with both OSes.
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
  - discovery of the user settings, every Orca profile and the Codex configs;
  - merge, never remove;
  - workspace grants and `git-net`;
  - Codex managed keys;
  - the Orca setting is read only and handed over.

  It runs as the `agent-settings` phase in S6. Its planners use the T2 layer.
- **Doctor (T8)** is read-only. It checks:
  - every phase's live verifier;
  - the public export and the stable point;
  - drift against the ledger and the machine record, including changes with
    no record and unrecorded versions of `exact` tools;
  - `agent-settings check`;
  - the OS end-of-support date, from 90 days ahead;
  - the earlier doctor items.

  `--admin` runs the root-only checks from the staged root and writes their
  non-secret result to the public export. A fleet passes `--inputs FILE` with
  expected values, which takes over the fleet's interim checks.

### 10a. Browser sign-in tunnel (right after T2)

- **What it is.** `awk-auth`, `awk-open` and managed SSH host entries finish a
  remote CLI's localhost sign-in, or show a workstation page, in the client's
  own browser, over a loopback-only SSH tunnel that closes itself.
- **Why it comes early.** It is needed day to day, it runs only in user space,
  and it needs only T2.
- **Where it runs.** On the client, it uses T2's client mode (section 5) for
  its SSH config writes, logs and undo. On the workstation, its browser hook
  is part of S6.
- **What stays.** Its security properties: loopback only, a time limit
  enforced by a watchdog, no secrets in logs, and refusing readiness lines
  faked by the workstation.
- **Later.** It uses the T3 hand-over format once T3 lands.
- **Gate.** The full gate.
- **Starting point.** The earlier standalone version, which passed its own
  review and stress run but was never opened.

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
| Machine records | `records/HOST.toml` in the fleet: OS support level, where the baseline and stable points are kept, and every hand change. Structured, so the T8 drift check can read it. `propose-change` with kind `machine` drafts these entries. The host keeps a root-only copy for the doctor. | T2 (format), T8 (drift check) |

### 11. Where state lives

| What | Linux | macOS | Mode |
|---|---|---|---|
| Kit releases | `/opt/agent-workstation-kit/releases/<rev>`, `current` | same | root, 0755, not writable by others |
| Kit state top folder | `/var/lib/agent-workstation-kit/` | `/Library/Application Support/agent-workstation-kit/` | root 0755, holding only the folders below |
| Root run folder and host lock | `/run/agent-workstation-kit/` | `/Library/Application Support/agent-workstation-kit/run/` | root 0700 |
| Root journals, receipts, host machine record | `/var/lib/agent-workstation-kit/state/` | `/Library/Application Support/agent-workstation-kit/state/` | root 0700 |
| Public export (summary, per-phase status, redacted failure records, root gate records, the `stable` tag, doctor `--admin` result) | `/var/lib/agent-workstation-kit/public/` | `/Library/Application Support/agent-workstation-kit/public/` | root 0755, files 0644, written atomically, no secrets |
| Root backups, baseline, archives | `/var/backups/agent-workstation-kit/` | `/Library/Application Support/agent-workstation-kit/backups/` | root 0700 |
| Root logs | `/var/log/agent-workstation-kit/<run-id>/` | `/Library/Logs/agent-workstation-kit/<run-id>/` | root 0700, files 0600 |
| Account state, journal, backups, logs | `${XDG_STATE_HOME:-~/.local/state}/agent-workstation-kit/` | same | 0700, files 0600 |
| Account run folder | `$XDG_RUNTIME_DIR/agent-workstation-kit/`, or the account state folder's `run/` when that variable is unset | `${XDG_STATE_HOME:-~/.local/state}/agent-workstation-kit/run/` | 0700 |

- Each root folder's parent chain passes the existing `secure_root_path` rule.
  The kit's own folders are created by the first root scope (T2), not only by
  S1.
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
  - the public-safety scan passes;
  - the macOS and Python 3.9 jobs pass (from T1).
- **Doc tests.** From T2 on, runbook command blocks run in preview mode
  against fixtures. Each block is marked `preview-tested` or `human-only`.
- **Each PR's gate:**
  - tests;
  - an adversarial review of code and docs;
  - a stress run that kills at every boundary;
  - a docs-only walkthrough by a fresh agent;
  - a changelog entry.
- **`AGENTS.md`** drops the old "planned: issue #5, step N" numbering in T1
  and names T-PRs instead.

### 13. Settled questions and operator decisions

The round 1 review settled the open questions:

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
4. **Already-managed hosts** get an `as-found` baseline, captured by a root
   step before any other scope (section 5).
5. **Run token:** the inherited lock descriptor plus a validated run ID
   (section 3), for root and non-root alike.

Decisions for the operator:

- **D1, the platform list (section 9).** Three choices:
  - **macOS policy.** "Current and previous major" is 27 and 26 today; this
    repository's development Mac runs 27.2.1 (checked 2026-11-20). "Every
    version Apple still patches" would also include 15. The amendment's list
    (26 and 15) matches neither today.
  - **Ubuntu 22.04.** It is still in Canonical's standard support until about
    April 2027 (not checked live). It is inside the vendor rule, but the
    amendment also excludes "practically outdated" versions. Is 22.04
    practically outdated?
  - **Clients and cost.**
    - Client rows (for the browser tunnel) follow the same rule, so a client
      laptop on macOS 15 would be refused unless clients get a wider rule.
    - Every listed version needs a CI job or a recorded real-machine run each
      release, and hosted runners may not offer macOS 27 yet.
- **D2, what "pinned" means for each kind of source (principle 7).** The ADR
  proposes three policies and asks one question:
  - **`exact`:** downloads, vendor `.pkg` files (except apps that update
    themselves), mise tools, and packages from
    third-party apt repositories that keep old versions (such as Tailscale's).
  - **`os-managed`:** the Ubuntu archive and Homebrew formulae. A minimum
    version, with the observed version recorded, because these do not keep
    old versions.
  - **`self-updating`:** apps that update themselves outside the kit, such as
    casks marked `auto_updates` and vendor `.deb` files that add their own
    apt repository. A minimum version, the observed version recorded, and not
    reported as drift.
  - **Security updates:** `unattended-upgrades` covers only Ubuntu's
    security origins by default. Should the kit add the origins of the
    third-party repositories it installs, so their packages also get
    automatic security updates?

## Consequences

- One path to apply, revert and audit; documented `fleetctl run --apply`
  commands go away.
- macOS root scopes work again (T1).
- More state on disk (backups, logs, baseline). The doctor reports its size,
  and backups older than the stable point can be pruned by an approved step.
- Phase scripts gain `--describe` and lose prompts. macOS account passwords
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
  - ends with `revert --to baseline` matching the baseline digest, in a
    fixture or a disposable machine.
- The doctor is green, and a docs-only walkthrough passes.

## Pull request order

```text
D0 (this ADR, reviewed alone)
 -> T1 one apply path, run token, vendor-package and driver phases with the remote-desktop
       install guard, macOS host-lock fix, agent interface, platforms, CI jobs
 -> T2 transaction layer, baseline, ledger, revert (no accounts, SSH or firewall yet), logs,
       failure records, client mode
 -> browser sign-in tunnel and host entries, on T2
 -> T3 gates (G0 replaces the recovery flags), hand-overs, scope order, dead-man timer
       (revert of SSH and firewall enabled), mesh VPN in S2, human-steps policy
 -> T4 lockout and convergence fixes, account archive (revert of accounts enabled)
 -> T5 schema, interview, schema 4, capabilities, fleet schema fragment
 -> T6 tool registry and resolve flow, staging, day-zero one-liner, snapshots
 -> update watch (follows T6)
 -> T7 agent settings
 -> T8 doctor, fleet machine-record drift
 -> components: corporate IPsec VPN, explainer refresh
```
