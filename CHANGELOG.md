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

- `fleetctl.py run ... --apply` now exits 2 and prints the `setup-workstation.sh` executor commands so changes never bypass locks, receipts, source digests or staged-root checks; preview and read-only `audit` stay, docs now show the executor path, and `tests/check_repository.py` fails if a doc shows `fleetctl run ... --apply` again.
- On macOS, `execute-setup.py` now places the privileged host lock under `/Library/Application Support/agent-workstation-kit/run/` (created root-owned `0755` and checked with `secure_root_path`) instead of group-writable `/var/run`, so macOS root scopes can run.
- `install-local-deb-linux.sh` now refuses to install NoMachine `.deb` packages unless the UFW firewall is active.
- `agent-work-shell` falls back to the plain shell, with a warning, when it cannot start a scope in `agent-work.slice`, so a terminal never closes immediately.
- The Linux resource policy now caps agent work in a dedicated `agent-work.slice` under the agent's user manager, with memory shares of RAM, systemd-oomd, swap, zram and `/tmp` policy, a receipt and an exact `--revert`; `agentctl start` runs sessions inside it.
- `scripts/agent-work-shell` runs an agent manager's terminals inside `agent-work.slice`; the host audit and phase verifier fail when a claude, codex, grok or agent-manager pty process runs outside it.
- The resource policy overrides vendor oomd defaults so oomd acts only on `agent-work.slice`, shows the current oomd settings in the preview, and treats static, alias and runtime-enabled oomd as enabled.
- An interrupted resource-policy apply no longer blocks `--revert`.
- The memory reserve is a share of RAM: `resources.os_memory_reserve_percent` and `--memory-reserve-percent` replace the deprecated `os_memory_reserve_gib` and `--memory-reserve-gib`; the schema migration check accepts that rename.

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
