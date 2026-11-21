# Agent Workstation Kit: capabilities

[Documentation home](README.md)

The registry of what the kit does, one entry per capability. Statuses move
planned, building, shipped, deprecated. The plan that builds them is in the
[roadmap](plan/roadmap.md); the design is ADR 0005
(`docs/design/setup-architecture.md`, PR #13).

## CAP-001: Guided setup by an agent

```yaml
id: CAP-001
name: Guided setup by an agent
area: engine
status: planned
verified_by: []
epics: []
adrs: []
```

An agent runs the whole setup from the interview to a tagged stable point,
handing every root step and secret to a person, through `wskit next --json`.

## CAP-002: Reversible changes

```yaml
id: CAP-002
name: Reversible changes
area: engine
status: planned
verified_by: []
epics: []
adrs: []
```

Every change is previewed, backed up and verified, and the machine can go
back to its baseline or any stable point with `wskit revert --to`.

## CAP-003: Standalone units

```yaml
id: CAP-003
name: Standalone units
area: units
status: planned
verified_by: []
epics: []
adrs: []
```

Each part of the setup, such as the shell, the browser tunnel or the VPN
client, can be planned, applied, verified and reverted on its own.

## CAP-004: Fleet config and records

```yaml
id: CAP-004
name: Fleet config and records
area: fleet
status: planned
verified_by: []
epics: []
adrs: []
```

A private fleet repository holds config and machine records only, managed
with `wskit fleet`.

## CAP-005: Phase-script setup (frozen)

```yaml
id: CAP-005
name: Phase-script setup (frozen)
area: legacy
status: shipped
verified_by: ["make ci-check"]
epics: []
adrs: []
```

Today's Python and shell setup (`scripts/setup-workstation.sh`). It is frozen
for fixes only and deleted piece by piece as Go units reach parity.
