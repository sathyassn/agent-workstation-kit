# Changelog

This project follows Semantic Versioning. Version 0.x interfaces may change
between minor releases while live-machine evidence is incorporated.

## 0.2.0-rc.7 — Unreleased

- Consolidated operator workflow into one command with a guided Rich/plain-text
  walkthrough, validated input, explicit safe approvals, progress, status and
  actionable retry/role-handoff guidance. UI packages are optional.

- Added real owner-scope execution with portable reviewed plans, explicit host/
  scope approval, live verification, automatic private receipts and failure resume.
- Added profile-selected APT/Homebrew packages/apps with missing-only installs;
  guarded fresh OpenSSH installation until validated hardening; and a pinned,
  signature-reviewed mise bootstrap for new Linux accounts.
- Added Mac local-account creation with hidden vendor password prompts, managed
  account verification, project-locked Playwright/browser and digest-pinned
  OrbStack container probes, plus executable TCP/SSH/SFTP/reconnect checks.
- Fixed the shared installer for Apple's system Bash and made new Mac agent
  shells discover the selected Homebrew installation.
- Added regression and subprocess-fixture coverage for preview, approval, failed
  phase resume, repeat runs, drift, tampered source and configuration preservation.
- Fixed remote-hardening verification for profiles with more than one SSH user,
  and stopped requiring zsh for preserved human and admin accounts; failed
  account and SSH postconditions now print their reason.
- Fixed the Ubuntu package check so each required unit must be enabled.
- Root now refuses to run any copy of the entry point other than a fully
  root-owned `/opt/agent-workstation-kit`, for preview and status too.
- Cancelling the walkthrough exits 130; a failed SSH-guard record removes its
  masks; plan preview warns when the working tree differs from its commit.

Upgrade: schema-3 profiles remain valid; new package/account choices are optional.
Use `scripts/setup-workstation.sh` to export a new execution bundle and preview
owner scopes before approving them. Existing configurations and conflicting
installed versions are preserved; a reviewed merge/upgrade may be needed. Keep
root applies in matching root-owned `/opt` snapshots and use per-executor private
journals. Software fixtures do not qualify fresh physical Ubuntu/Mac installation,
account/Secure Token behavior, SSH postinst/recovery or GUI/reboot acceptance.
Keep the fleet pin unchanged pending independent review and live qualification.

## 0.2.0-rc.6 — Unreleased

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

## 0.2.0-rc.5 — Unreleased

- Closed final cross-model release-review gaps in resource-limit boundaries,
  macOS identity/runtime consistency, remote-access hardening, agent session
  delegation, pilot validation, public CI and contributor/security guidance.
- Added regression coverage for the corrected first-use and policy paths.
- Allowed GitHub-authenticated Dependabot branch names and generated PR/commit
  bodies while retaining conventional subjects, secret scans, and test gates.

## 0.2.0-rc.4 — Unreleased

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

## 0.2.0-rc.3 — Unreleased

- Added one ordered day-zero Linux path from first boot through supervised
  setup-agent and operational-agent handoff.
- Added a non-privileged startup readiness command with unit coverage.
- Added a complete documentation map, page navigation, concise contents lists,
  explicit privileged staging instructions and automated discoverability/link
  checks.
- Require the approved private profile to be committed before staging, publish
  toolkit/fleet snapshots through a rollback-capable temporary area, and reject
  symlinked or dirty private-fleet inputs during readiness checks.

## 0.2.0-rc.2 — Unreleased

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

## 0.2.0-rc.1 — Unreleased

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

## 0.1.0-rc.1 — Unreleased baseline

- Added preview-first bootstrap, account, access, tooling, workload, resource,
  and host-audit automation.
- Added the shared-account session broker, initial profiles, tests, and guides.
