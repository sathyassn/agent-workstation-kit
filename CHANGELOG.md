# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, a minor
version may change interfaces while live-machine evidence is incorporated.

Every pull request that changes behaviour adds one short bullet per change under
`[Unreleased]`, in the matching group, linked to the pull request. The release
step is in [docs/maintenance/releasing.md](docs/maintenance/releasing.md).

## [Unreleased]

### Added

- Fault-injection race tests and lint in Linux and macOS CI, the tagged race suite in `make go-check`, and a check that builds and inspects its own binaries for the Go release targets (TSK-042).
- Schema 3 declaration, generated Go types and layered configuration (`schema/profile.toml`, `internal/schema/`, `internal/units/catalog/gen/`, `internal/cli/`, `testdata/profiles/`, TSK-011): declares every Schema 3 field, enum, constraint, metadata entry and safety-floor rule in `schema/profile.toml`; extends the TSK-005 generator (`go generate ./...`) to type-check and atomically generate `internal/schema/types_gen.go` alongside `enums_gen.go`, `catalog_gen.go` and docs tables with rollback on rename failure and staleness checks; implements full Go profile and fleet validation proven at parity against `scripts/fleetctl.py` and `scripts/validate-fleet.py` over 79 fixtures in `testdata/profiles/` plus all example profiles; implements six-layer precedence resolution (`kit-default`, `fleet`, `machine`, `user`, `env`, `flag`) with per-layer type checking, duplicate dotted/nested key refusal, and non-configurable safety-floor refusal (exit 2 naming the rule) across every layer; wires `wskit config get|set|explain|validate` in plain and `--json` modes (preview-by-default for `set`, full key/cross-field validation with secret redaction, metadata-preserving atomic replacement with parent-directory fsync, `WSKIT_*` env layer validation on single-file validate, and running-version default for `--fleet`); and registers `golang.org/x/text` as a direct dependency for Unicode NFKC parity.
- Contributor identity check and optional keyed HMAC digest list (`tests/public_scan_identity.py`, `tests/public_scan_keyed.py`, `tests/check_public_release.py`, TSK-008): reads local git identity, account name/full name, host name, macOS `scutil` / Linux `hostnamectl` names, and Tailscale node/tailnet names in memory at check time (reporting uninstalled Tailscale as absent and failing closed on source errors, timeouts, or missing required values); checks commit author/committer identities in trusted-base CI (`.github/workflows/public-scan.yml`) with incomplete-history refusal and `PUBLIC_SCAN_IDENTITY_VERIFIED=1` verification in `public-scan / result`; keeps the keyed HMAC digest list off by default (`tests/public_scan_modes.json`) with digest-only validation and untracked/CI-secret key loading; supports `block`/`warn`/`off` modes and tracked exemptions (`tests/public_scan_exemptions.json`); covers the new helpers in `.github/CODEOWNERS` and `tests/check_repository.py`; and documents CodeFlow-managed commit hooks and push-time identity enforcement in `docs/13-public-release-checklist.md`.
- `engine.Tx.SetState` and `StateProvider` registry (`internal/engine`, `internal/platform`, TSK-018): enforces SPC-001's order (undeclared-key and unregistered-provider refusal before any provider call, durable snapshot, `Write`, receipt row, with uncancelled bounded restore on write failure and one lock across the sequence) and passes `contracttest.TestTx`; adds `ProviderRegistry` plus Linux (`hostnamectl`) and macOS (`scutil`) `hostname` providers (using `hostname.localname` for the macOS local hostname state key, a pre-1.0 format change from the earlier draft key) with test fakes.
- Full file manifests (`internal/engine`, TSK-018): backs up and restores owner, mode, extended attributes and OS-native ACLs (recording unsupported filesystems as absent, clearing inherited ACLs when none were captured, and keeping the macOS `com.apple.provenance` exemption and symlink refusal), lifting the TSK-013 metadata refusal.
- Verified restore and package-database guard (`internal/engine`, TSK-018): validates the whole manifest before replaying, verifies restored content, metadata and state, runs configured `RestoreValidator` checks, wraps every restore error in `ErrRestoreFailed` (exit 4, intent left unfinished), bumps the intent record version to 2 (recording plan digest kind and plan-time dropped-xattr notes while still reading version 1 records cleanly, pre-1.0), and refuses package-database paths (including case-folded APFS and `/System/Volumes/Data` firmlinks) at backup and restore so packages revert only through the package manager.
- Serialised `SetState` contract check (`internal/contract/contracttest`, TSK-018 AC-4): signals second-call entry before starting the probe wait, checks contiguous event sequences, and documents why proving absence needs a time bound.
- Deterministic public-safety privacy scan (`tests/check_public_release.py`, TSK-007): blocks emails, phone numbers, IPv4/IPv6 addresses (private, public, and partial private prefixes), MAC addresses, real-looking home paths, private DNS names (`*.ts.net`, `*.internal`, `*.local`), cloud account/project IDs, chat/tracker user IDs, and secrets with no secret or name list; decodes and folds JSON/URL escapes, UTF-16, case, confusables, zero-padded octets, and glued prefixes; refuses NUL-byte binaries without a hash-bound exception (`tests/public_scan_binary_exceptions.json`); redacts findings to file, line, and rule only; enforces per-detector modes (`tests/public_scan_modes.json`) and expiring SHA-256 digest-bound exemptions (`tests/public_scan_exemptions.json`); runs from the trusted base revision on Linux and macOS in `.github/workflows/public-scan.yml` and `.github/workflows/ci.yml`; and covers all scan, config, workflow, and `.github/CODEOWNERS` paths with code-owner checks.
- The minimal engine (`internal/engine`, TSK-013): `engine.Tx` with atomic `WriteFile`, `EnsureBlock`, `RemoveBlock`, `Remove` and contained `Run`; per-path backup and manifest, a durable intent record and receipts; restore on failure; `Resume` with an identity check of the executor, a recovery claim, bounded emptying of the containment before the host lock, restore and reapply; the host then journal lock order; a grace period for cancel that never ends a package-manager transaction; a fault-injection harness (a `faultinject` build tag and a kill test at every apply boundary). Containment adapters run each tool in a transient systemd scope on Linux and a launchd job on macOS (`internal/platform/linux`, `internal/platform/darwin`), with fakes in `internal/platform/platformtest`. `cmd/agentctl` refuses everything outside its exact subcommand table with exit 2. Every engine file path is walked from `/` without following links; journal and backup folders are made durable before the intent; supervisors run by trusted absolute paths with a minimal environment, held at a launch gate until their identity is durable; each command's containment is proven empty before the unit moves on, and a containment that cannot be proven empty exits 4 instead of restoring; a live claimant keeps its recovery claim; targets with ACLs or extended attributes are refused (on macOS, the system `com.apple.provenance` attribute excepted) until a full metadata backup exists. A recorded process is signalled only while its identity matches: on Linux through a pidfd opened before the identity check; on macOS, which has no pidfd, the start time is re-read immediately before `kill(2)`, leaving a one-system-call window in which the PID could be reused.
- The `wskit` command tree (`internal/cli`), built with cobra and fang: the SPC-001 command matrix, one command per catalog unit with the unit verbs, the shared flags (`--json`, `--plain`, `--verbose`, `--quiet`, `--apply`, `--config` and `--stage`, with no `--scope`), the per-command flags (`setup --all` and `--unit`, `plan --unit`, `resume --restore-only`, `revert --to`, `disarm --run`), `version`, and bash, zsh and fish completion. Commands not built yet refuse with exit code 2. Errors carry a class that fixes the SPC-001 exit code, and `--json` reports them as `error.schema.json` documents, also when another flag's value is invalid (`github.com/spf13/pflag` becomes a direct dependency for that); `version` and `completion` refuse `--json` until they have a schema. The man page ADR 0005 asks for is deferred to TSK-039 under EPC-003, the CLI epic.
- The event sink (`internal/engine/events`): non-progress events are written to the run log before `Deliver` returns, progress events wait in a bounded buffer, the terminal UI gets every event once and in sequence order through a bounded queue that coalesces only progress events and replays missed events from the run log, writes stay in sequence order when deliveries overlap, coalesced progress never crosses a non-progress event, a stalled UI holds at most 8 waiting progress batches and merges older ones into one `coalesced` marker with the exact number of updates skipped, so a unit's own progress count is never read as skipped updates, `Close` waits until its context ends for the UI to get every event, and the first run-log write or replay error is kept for the engine to fail the step closed.
- `contract.StateProvider.Write`, so `Tx.SetState` can change non-file state: the fakes write through it after the declared-key check and snapshot, and the provider contract test checks read-back and undo through `Restore`.
- The core contracts as a Go package (`internal/contract`): the `Unit`, `Tx`, `DeclarativeTx`, `Sink` and `StateProvider` interfaces with their value and JSON wire types (`Next`, `Handover`, `Failure`, `Explain`, `CLIError`), closed typed enums with decode validation (including generated `UnitID`), JSON schemas with golden fixtures (`schema/json/`, `testdata/json/`), reusable fakes and contract tests (`internal/contract/contracttest`), and the command matrix as a typed table; `github.com/santhosh-tekuri/jsonschema/v6` becomes a direct dependency for schema tests.
- Go CI workflow (`.github/workflows/go.yml`) running `go mod verify`, `gofmt`, `goimports`, `go vet`, `golangci-lint`, `go test -race`, `govulncheck`, `go test ./...`, a release-target binary check and remote action-pin provenance verification (`tests/verify_action_pins.py` against `tests/action_pins.json`) on `ubuntu-24.04` and `macos-latest` with SHA-pinned actions, Go targets in `.codeflow/test-config.json`, and fail-closed Git-aware scanning plus strict workflow action SHA-pinning enforcement in `tests/check_repository.py`.
- Go module (`go.mod`, `go.sum`), package layout under `cmd/` and `internal/`, root data embedding (`assets.go`), stub `wskit` and `agentctl` binaries that print their build version, `registry/tools.toml` pinning each direct Go library and build tool to its version and `go.sum` hash (checked by `internal/registry`, which refuses `replace` and `exclude`), and a `make go-check` target that tests and builds for Linux and macOS without cgo.
- CodeFlow full tier, on the released CodeFlow 3.1.1 with its digests pinned: skills, project records (`project-management/`), product, capabilities and architecture pages, and the Go roadmap with its 32 epics (`docs/plan/roadmap.md`).
- `.golangci.yml`, the lint configuration the Go code will be held to.
- The unit catalog, `registry/units.toml`, with a check that keeps the shared tables in the roadmap, the core spec and ADR 0005 current, and that finds cycles and dangling edges per OS.
- The engineering standard and review checklist (`docs/standards/`), checked from the pull request template.
- The traceability table from design decisions to epics, tasks and tests.
- Epic EPC-033 for a macOS indexing unit, and the `indexing` unit in the catalog; the resources and indexing epics come right after T2, and an `audit-agent` unit joins the catalog. `registry/presets.toml` holds the preset unit sets and best-practice defaults; the catalog marks conditional edges and waits.

### Changed

- Versions: the Python `0.2.0-rc` line is frozen and never released as a final 0.2.0; the Go `wskit` releases as `1.0.0-alpha.N`, `-beta.N`, `-rc.N`, then `1.0.0` (`docs/maintenance/releasing.md`).
- `.codeflow/policy.json` sets `git.security_review` and `git.dep_audit` to `warn`, as CodeFlow 3.1.1 writes them for the full tier, so the CI security review stops failing closed on a missing key.
- The branch policy allows the `codeflow/` prefix, so CodeFlow can publish its ID registry branch (`codeflow/registry`).
- The repository scan allows two exact sandbox wildcards only inside CodeFlow's own folders, and skips link checks there. CodeFlow's model-evaluation skill is left out until its eval case IDs stop matching CodeFlow's own secret scan.

### Fixed

- `fleetctl.py validate` now requires ASCII digits (`[0-9]`) instead of Unicode `\d` in `tooling.mise_version`, rejecting non-ASCII numerals at parity with the Go Schema 3 validator.
- `fleetctl.py run ... --apply` now exits 2 and prints platform-accurate `setup-workstation.sh` executor commands (with recovery/connection-context flags and staged root apply) or the Linux-only error for `agentctl` on macOS, so changes never bypass locks, receipts, source digests or staged-root checks; preview and read-only `audit` stay, docs now show the executor path, and `tests/check_repository.py` fails if a doc shows `fleetctl run ... --apply` again.
- On macOS, `execute-setup.py` now places the privileged host lock under `/Library/Application Support/agent-workstation-kit/run/` (top folder `0755`, run folder created and required at root-owned `0700` on both OSes, checked with `secure_root_path`; a Linux run folder an earlier executor left at `0755` is tightened to `0700` in place on the same inode, any other loose mode is refused) instead of group-writable `/var/run`, with a macOS CI job verifying real root lock contention.
- `install-local-deb-linux.sh` now refuses to install NoMachine `.deb` packages unless the UFW firewall is active with `DEFAULT_INPUT_POLICY=DROP` and `DEFAULT_FORWARD_POLICY=DROP`.
- `agent-work-shell` falls back to the plain shell, with a warning, when it cannot start a scope in `agent-work.slice`, so a terminal never closes immediately.
- The Linux resource policy now caps agent work in a dedicated `agent-work.slice` under the agent's user manager, with memory shares of RAM, systemd-oomd, swap, zram and `/tmp` policy, a receipt and an exact `--revert`; `agentctl start` runs sessions inside it.
- `scripts/agent-work-shell` runs an agent manager's terminals inside `agent-work.slice`; the host audit and phase verifier fail when a claude, codex, grok or agent-manager pty process runs outside it.
- The resource policy overrides vendor oomd defaults so oomd acts only on `agent-work.slice`, shows the current oomd settings in the preview, and treats static, alias and runtime-enabled oomd as enabled.
- An interrupted resource-policy apply no longer blocks `--revert`.
- The Linux resource policy (`scripts/apply-resource-policy-linux.sh`) now uses reserve-based sizing by default (8 GiB desktop or 2 GiB headless fixed reserve plus 2% of RAM, optional `--measure` baseline at 1.5x non-agent slices, `MemoryHigh = MemoryMax - min(10%, 8 GiB)`, `MemorySwapMax = min(25% of swap, 8 GiB)`, `CPUWeight=50`, `CPUQuota` from reserved cores, refusing below 2 GiB), shows every value's derivation in the preview, keeps percentage flags as explicit overrides, and adds optional profile fields `resources.reserve_fixed_gib`, `resources.reserve_cores`, and `resources.cpu_weight`.
- The memory reserve is a share of RAM: `resources.os_memory_reserve_percent` and `--memory-reserve-percent` replace the deprecated `os_memory_reserve_gib` and `--memory-reserve-gib`; the schema migration check accepts that rename.
- The resource policy puts `CPUWeight` on `agent.slice`, the parent that competes with the desktop's `app.slice` and `session.slice`, instead of on `agent-work.slice`, where it had no sibling to divide CPU with.
- Resource-policy apply and revert write every file atomically, hold a lock on the state directory, and record each step before and after it, so an apply or revert stopped at any point can be reverted or finished.
- `--measure` is informational only: it reads every top-level cgroup except the agent's `agent.slice` and never changes the reserve.
- The resource receipt keeps the approved inputs, readings and values; a repeat apply reuses them, and the phase verifier checks the slices against the receipt and against the values systemd holds.
- The resource policy refuses a host whose user manager does not delegate the cpu controller, integer flags that are not plain numbers of at most six digits, a `--memory-max-percent` that leaves less than the reserve or 2 GiB (unless `--allow-small-reserve`), and a symlinked state path.
- Planned zram counts toward the swap total that sizes `MemorySwapMax`, and the preview says when the core reserve is lowered on 1 or 2 CPUs.
- `validate-host.sh` rejects a MemoryMax below 2 GiB, a MemoryHigh not below MemoryMax, a CPUQuota below 100% and a missing MemorySwapMax or `agent.slice` drop-in weight; the profile migration check no longer ignores `reserve_fixed_gib`, `reserve_cores` and `cpu_weight`.
- Resource-policy `CPUWeight` now lives in `/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf` without `ConditionUser=` so it applies to every user's `agent.slice` (backing up any existing drop-in and removing `.d` only if created by apply), new receipts record the formula version and receipts from an earlier version or formula are refused without migration, the verifier parses systemd timespans (`1min`, `2min 4s`, `500ms`, `infinity`) for `CPUQuotaPerSecUSec`, apply/revert verify `/var/lib`, `/var/lib/agent-workstation-kit`, any existing lock file and `/proc/self/fd/9` before locking `/var/lib/agent-workstation-kit/resource-policy.lock` and flush each new state directory and its parent before writing policy files, and `--zram-percent` replaces any active zram size in the swap total rather than ignoring or adding to it.

## [0.2.0-rc.7] - 2026-11-20

### Added

- One setup entry point, `scripts/setup-workstation.sh`, with a guided walkthrough in Rich or plain text; UI packages are optional. ([#6])
- The walkthrough validates input, asks for typed approvals, and shows progress, status, and retry and role hand-over guidance. ([#6])
- Owner-scope execution from a portable reviewed plan, with host and scope approval, live postcondition checks, private receipts and resume after failure. ([#6])
- Profile-selected APT and Homebrew packages and apps, installed only when missing. ([#6])
- A pinned, signature-reviewed mise bootstrap for new Linux accounts. ([#6])
- macOS local-account creation with hidden vendor password prompts, and verification of managed accounts. ([#6])
- Project-locked Playwright browser checks and a digest-pinned OrbStack container probe on macOS. ([#6])
- Executable TCP, SSH, SFTP and reconnect checks (`check-access`). ([#6])
- `fleetctl setup PROFILE --handoff-draft` prints a reviewable host handoff for a fleet-managed target and writes nothing. ([#8])
- `fleetctl setup PROFILE` readiness checks the committed profile and handoff, clean source checkouts and optional exact revisions, then runs the platform readiness launcher. ([#8])
- `fleetctl setup` never applies changes; execution and resume stay with `setup-workstation.sh`. ([#8])
- `fleetctl setup --execution-context` is forwarded to the readiness launcher, which reports NOT READY unless it is `host`. ([#8])
- An expanded private-fleet template: source-toolkit context, placeholders, repository boundaries, validation, deployment flow and governance. ([#8])
- A Bash 4 validation gate (`make bash-check`) that also fails for a shell that is not Bash, with macOS contributor instructions. ([#8])
- Regression and subprocess-fixture tests for preview, approval, failed-phase resume, repeat runs, drift, tampered source and configuration preservation. ([#6])
- An agent operating manual in `AGENTS.md` (boundaries, map, rules, task recipes, output formats, hand-over format), and `CLAUDE.md` that includes it. ([#10])
- A release procedure in `docs/maintenance/releasing.md`: version choice, changelog section, annotated tag and GitHub release, decided by the owner. ([#10])
- `make repo-check` fails when a change to `scripts/`, `config/` or `templates/` adds no `[Unreleased]` changelog bullet. ([#10])
- `make repo-check` fails when `AGENTS.md` names a path, setup action, `fleetctl` subcommand or flag that does not exist. ([#10])

### Changed

- `CHANGELOG.md` follows Keep a Changelog, with an `[Unreleased]` section grouped by kind and one linked bullet per change. ([#10])
- The README reads in order: what the kit is, how it fits together, quick start, then details; the docs index lists every runbook. ([#10])
- macOS readiness uses the Homebrew Bash that the macOS day-zero guide now installs. ([#8])
- Cancelling the walkthrough, including with end of input, exits 130 instead of 0. ([#6])
- Plan preview warns when the working tree differs from its commit. ([#6])
- Root commands in the docs name the staged copy absolutely and run through a system interpreter, and the runbook states this root rule. ([#6])

### Fixed

- Fleet entry points report a failed `git worktree list` as an error with exit 2 instead of a traceback. ([#8])
- Planning refuses a symlinked fleet root, as `fleetctl setup` does. ([#8])
- macOS handoff drafts use the day-zero `"$PYTHON_BIN"` instead of a bare `python3`. ([#8])
- Remote-hardening verification works with more than one SSH user (`sshd -T` prints one `AllowUsers` line per user). ([#6])
- Human and admin accounts keep their own shell; only the agent account must use zsh. Failed account and SSH checks print their reason. ([#6])
- The Ubuntu package check requires each unit to be enabled, not just one of them. ([#6])
- Tailnet firewall verification accepts plain `ufw status` output, which omits `IN` for incoming rules, so a correct hardening never verified. ([#6])
- Verbose `ufw status` output is also accepted, and outgoing and routed rules never count, in the setup verifier and in `validate-host.sh`. ([#6])
- The shared installer works with Apple's system Bash, and new Mac agent shells find the selected Homebrew. ([#6])
- If the SSH guard record cannot be written, its masks are removed, or named for manual removal when that also fails. ([#6])
- A running phase keeps the journal lock until it exits, so killing only the executor cannot let a rerun run the same phase alongside it. ([#6])
- When a leftover background process holds the lock, the refusal says so and names the lock file to inspect with `lsof`. ([#6])
- Preview, status, doctor and readiness write no bytecode caches; readiness sends `make check` bytecode to a temporary prefix. ([#6], [#8])
- The guided plan export creates no folders, and readiness runs `git` without optional locks. ([#6], [#8])

### Security

- Root runs only from a fully root-owned `/opt/agent-workstation-kit`, for preview and status as well as apply. ([#6])
- As root, the entry point uses system PATH tools and the system bash before any check, because sudo on macOS keeps the caller's PATH. ([#6])
- Root APT installs and SSH-guard release run the same staged-copy check. ([#6])
- As root, staged helpers re-run themselves with an empty environment and run Python isolated without bytecode (`python3 -I -B`); the macOS package step refuses root. ([#6])
- A fresh OpenSSH install stays guarded until hardening validates the firewall and SSH policy. ([#6])
- Remote-hardening verification evaluates `sshd -T -C` for every SSH user and the agent, from loopback and a tailnet address. ([#6])
- Because test mode cannot evaluate every `Match` criterion, it also refuses any `Match` block that sets password, keyboard-interactive or root-login options. ([#6])
- Account verification proves that the agent and service accounts hold no sudoers command grant. ([#6])
- Planning, `fleetctl setup`, `fleetctl init --fleet-root` and both readiness launchers refuse a private fleet root inside the public kit. ([#6], [#8])

### Upgrade notes

- Schema 3 profiles remain valid; the new package and account choices are optional.
- Use `scripts/setup-workstation.sh` to export a new execution plan, and preview each owner scope before approving it.
- Existing configuration and conflicting installed versions are preserved; a reviewed merge or upgrade may be needed.
- Keep root applies in matching root-owned `/opt` snapshots, never `sudo` on a user checkout, and use one private journal per executing account.
- Software fixtures do not qualify a fresh physical Ubuntu or Mac install, account and Secure Token behaviour, SSH package scripts and recovery, or GUI and reboot acceptance.
- Keep fleet pins unchanged until independent review and live qualification.

[#6]: https://github.com/sathyassn/agent-workstation-kit/pull/6
[#8]: https://github.com/sathyassn/agent-workstation-kit/pull/8
[#10]: https://github.com/sathyassn/agent-workstation-kit/pull/10

## Earlier untagged release candidates

The release candidates below were recorded before releases were tagged. None
of them has a Git tag or a GitHub release.

### 0.2.0-rc.6 (untagged)

- Added a preview-only resumable setup plan with target/content binding and
  private evidence checkpoints for Ubuntu and macOS.
- Added generic remote Mac listener/TCP-before-pairing, Orca reconnect,
  named-user Apple Screen Sharing, transfer, sleep and recovery acceptance.
- Distinguished source worktrees from staged snapshots, full revision/version
  evidence from remote freshness, and host attestation from sandbox probes.
- Enabled non-pruning ref packing with a protected branch that has no upstream;
  tested the installed hook lifecycle and retained ambiguous deletion guards.
  Default ref pruning remains a documented maintenance limitation.
- Clarified native Ethernet qualification, installer recovery boundaries,
  architecture-specific Homebrew and independent per-role authentication.

Upgrade: schema 3 remains unchanged. Run pilot readiness from the source
worktree in an ordinary target-host terminal with `--execution-context host`.
Keep journals outside the public kit; review the new plan and checkpoint scope
before resuming. Optional app/source choices still require a reviewed private
supplement and manual vendor steps. This candidate has no fresh Ubuntu or Mac
hardware qualification; keep the fleet pin unchanged pending live evidence,
independent review and approved release publication.

### 0.2.0-rc.5 (untagged)

- Closed final cross-model release-review gaps in resource-limit boundaries,
  macOS identity/runtime consistency, remote-access hardening, agent session
  delegation, pilot validation, public CI and contributor/security guidance.
- Added regression coverage for the corrected first-use and policy paths.
- Allowed GitHub-authenticated Dependabot branch names and generated PR/commit
  bodies while retaining conventional subjects, secret scans, and test gates.

### 0.2.0-rc.4 (untagged)

- Promoted macOS to a first-class documented target with an ordered day-zero
  path from Setup Assistant through supervised setup-agent handoff.
- Documented operator access from macOS, Windows, Linux, iPadOS, iOS and Android,
  including transport, desktop, shell, file-transfer and recovery boundaries.
- Replaced environment-specific public examples with neutral namespaces and
  added release checks that reject non-neutral concrete hostname examples.
- Hardened macOS snapshot staging, readiness runtime/profile checks and
  privileged trust boundaries after interactive Claude and Grok reviews.
- Reworked the documentation into one explicit entry path and one authoritative
  page per topic; removed historical model-review transcripts from user docs and
  fully introduced the selected remote KVM products.
- Adopted Codeflow's Git discipline as a self-contained branch, commit,
  pull-request, protected-ref, secret, and pre-push policy with local hooks and
  hosted enforcement; the public toolkit has no private Codeflow dependency.

### 0.2.0-rc.3 (untagged)

- Added one ordered day-zero Linux path from first boot through supervised
  setup-agent and operational-agent handoff.
- Added a non-privileged startup readiness command with unit coverage.
- Added a complete documentation map, page navigation, concise contents lists,
  explicit privileged staging instructions and automated discoverability/link
  checks.
- Require the approved private profile to be committed before staging, publish
  toolkit/fleet snapshots through a rollback-capable temporary area, and reject
  symlinked or dirty private-fleet inputs during readiness checks.

### 0.2.0-rc.2 (untagged)

- Added a fleet-unique, human-friendly `machine.display_name` without weakening
  the stable technical hostname and UUID conventions.
- Added a preview-first identity phase that sets Linux pretty names or macOS
  Computer Names and atomically installs a root-owned local identity manifest.
- Added local identity drift, runtime-hostname, and NSS-resolution checks plus
  serialized, case-insensitive fleet display-name collision prevention. The
  canonical ASCII label alphabet rejects mixed-script and whitespace lookalikes;
  local resolution must terminate on a host interface or loopback address.
- Added explicit GitHub, GitLab, and Atlassian provider-principal inputs plus
  service-account creation, credential-brokering, CLI, MCP, and recertification
  guidance.
- Added schema v3 and a read-only schema-2 migration checker/runbook. Hardened
  the privileged identity directory, hostname-resolution preflight, credential-
  shaped input rejection, no-follow allocation locking, retirement/nested-profile
  gates, multi-operator identity rules, error recovery, and audit coverage.
  Privileged identity and remote-hardening applies now verify Tailscale peers or
  inspect process ancestry, so `sudo` cannot turn an SSH session into an
  apparent local-console session merely by clearing `SSH_CONNECTION`.

### 0.2.0-rc.1 (untagged)

- Renamed the toolkit to `agent-workstation-kit` and adopted the final
  `namespace-class-NNN`, `agent-NN`, and assigned `admin-NN` conventions.
- Added schema v2 with deployment ownership, persistent UUIDv4, asset and
  hardware identity, explicit resource headroom, deferred KVM, and Secure Boot.
- Added safe draft generation, external private-fleet support, toolkit locking,
  cross-profile collision checks, and retired-hostname enforcement.
- Added a reusable private-fleet template and separated public automation from
  private inventory.
- Added Minisforum MS-S1 Max acceptance and pinned RTL8127 DKMS guidance that
  retains Secure Boot, verifies MOK enrollment, builds source unprivileged, and
  tests kernel-update prerequisites.
- Updated Linux/macOS workflows, source-control identities, security boundaries,
  release checks, and the bundled setup skill.
- Made apply mode flag-only even under a hostile inherited environment, split
  read-only and mutating `agentctl` delegation, and added exact tmux targeting.
- Pinned Herdr and shell plugins, added toolkit-lock enforcement to every
  external-fleet command, and added migration and public-consistency checks.
- Made remote hardening require an explicit console/Tailscale-SSH context,
  labeled automated versus manual evidence, and added a fail-closed public
  conduct-contact gate.

### 0.1.0-rc.1 (untagged baseline)

- Added preview-first bootstrap, account, access, tooling, workload, resource,
  and host-audit automation.
- Added the shared-account session broker, initial profiles, tests, and guides.
