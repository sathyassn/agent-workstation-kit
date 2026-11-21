# Agent Workstation Kit: architecture

[Documentation home](README.md)

The design decision is ADR 0005, `docs/design/setup-architecture.md`
(PR #13). This page records the code layout and the areas, and links to
decisions instead of repeating them.

## Overview

The kit is moving from Python and shell phase scripts to one Go binary,
`wskit`. Units hold the logic for each part of the setup. One engine runs
every change as a transaction. Stages and the conductor interface compose
units; they hold no unit logic. Data (the schema, registries and templates)
is TOML and text, embedded in the binary.

```text
 person (terminal UI)        conductor agent (JSON)
          \                        /
           v                      v
        internal/cli  ->  internal/ui (two front ends over typed events)
               |
               v
        internal/planner -> internal/engine (transaction layer)
               |                    |
               v                    v
        internal/units/<unit>  ->  internal/platform/{linux,darwin}
               |
               v
        schema/, registry/, dotfiles/, templates/  (embedded data)
```

During the move, the frozen Python and shell under `scripts/` keep working
for fixes only. Each piece is deleted once its Go unit reaches parity, and
each phase has one owner at a time.

## Layout

| Path | What it holds |
|---|---|
| `cmd/wskit/` | `main` only |
| `cmd/agentctl/` | The narrow helper that the agent account's sudoers rule names |
| `internal/cli/` | The command tree, shared verbs and flags, exit codes |
| `internal/engine/` | The transaction layer: preview, backup, intent, apply, verify, receipt, revert, restore points |
| `internal/planner/` | Dependencies, owned paths and conflicts |
| `internal/schema/` | Validation, layered config, generated types, the interview |
| `internal/registry/` | Tool pins, checksums and the bump flow |
| `internal/platform/linux/`, `internal/platform/darwin/` | OS adapters behind interfaces, and the platform and capability matrices |
| `internal/units/<unit>/` | One package per unit, implementing the one `Unit` interface |
| `internal/conductor/` | `next`, `status` and `explain` as JSON |
| `internal/ui/` | The terminal UI and plain or JSON front ends |
| `internal/docsgen/` | Reference docs generated from the schema |
| `internal/redact/` | The one redactor for logs and output |
| `schema/`, `registry/` | TOML, the single sources |
| `dotfiles/`, `templates/` | Embedded with `go:embed` |
| `scripts/bootstrap.sh` | The day-zero one-liner, the only shell left at the end |
| `testdata/`, `e2e/` | Fixtures, and container and VM runs |
| `docs/` | Generated reference and hand-written guides |
| `scripts/`, `agentctl/`, `tests/` (Python and shell) | Frozen until each piece is deleted at parity |

There is no `pkg/`, because nothing here is a public Go API.

## Areas

- **cli:** the command tree and its conventions (`internal/cli`).
- **engine:** the transaction layer and locks (`internal/engine`).
- **units:** one package per unit (`internal/units`).
- **platform:** OS adapters and matrices (`internal/platform`).
- **data:** schema, registries, templates and the code generated from them.
- **fleet:** `wskit fleet` and the private-fleet template.
- **ui:** the terminal UI and plain output (`internal/ui`).
- **legacy:** the frozen Python and shell.
- **docs:** guides and generated reference.
