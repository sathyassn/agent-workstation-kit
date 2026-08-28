# Changelog

This project follows Semantic Versioning. Version 0.x interfaces may change
between minor releases while live-machine evidence is incorporated.

## 0.2.0-rc.5 — Unreleased

- Closed final cross-model release-review gaps in resource-limit boundaries,
  macOS identity/runtime consistency, remote-access hardening, agent session
  delegation, pilot validation, public CI and contributor/security guidance.
- Added regression coverage for the corrected first-use and policy paths.
- Allowed GitHub-authenticated Dependabot updates through the custom branch and
  generated-body policy while retaining commit, secret, and test enforcement.

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
