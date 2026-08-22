# Changelog

This project follows Semantic Versioning. Version 0.x interfaces may change
between minor releases while live-machine evidence is incorporated.

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
