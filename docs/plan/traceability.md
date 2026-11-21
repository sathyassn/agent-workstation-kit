# Traceability: decisions to epics, tasks and tests

[Documentation home](../README.md)

Each decision in ADR 0005 (`docs/design/setup-architecture.md`) maps to the
epic that builds it, the tasks that deliver it, and the tests that prove it.
Tasks are listed once their epic is planned; the P1 spec PR adds P1's.

| ADR decision | Epic | Tasks (planned coverage; "per epic" until the epic is broken down) | Proof |
|---|---|---|---|
| Go, one binary, layout, pinned dependencies (2b) | EPC-001 | TSK-002 | build on both OSes; registry check |
| CI, lint, vulnerability scan, coverage floors (2b, 12) | EPC-001 | TSK-003 | CI runs with planted failures |
| Signed releases, 1.0.0 versioning (9, 12) | EPC-001 | TSK-004 | release artefacts; verify run |
| One source of truth, generated code (2a, 15) | EPC-001 | TSK-005 | staleness and graph checks |
| First install with system tools only (3, 9) | EPC-001 | TSK-006 | shell tests with a fake download |
| Public-safety scan (12) | EPC-002 | TSK-007, TSK-008 | scanner tests |
| One apply path, staged binary, containment, recovery (3, 5) | EPC-003 | P1 spec PR | fault-injection tests |
| Agent interface and output modes (4, 2b) | EPC-003, EPC-004 | P1 spec PR | golden and snapshot tests |
| Transaction layer, baseline, revert, retention (5) | EPC-005 | P1 spec PR | fixture revert with the match digest |
| Units, standalone and composed (2a) | EPC-006 to EPC-022 | per epic | standalone apply and revert per unit |
| Revert everywhere (2c) | every unit epic | per epic | per-unit revert tests; EPC-030 |
| Presets, form factor, topology, platforms (2g) | EPC-003, EPC-014, EPC-023 | P1 spec PR | combination tests; EPC-030 |
| No lock-in (2f) | EPC-005, EPC-022 | per epic | marked-block and drift tests |
| Desired state, tools and MCP (2e) | EPC-011, EPC-012, EPC-025 | per epic | verb and interview parity tests |
| Gates and hand-overs (6) | EPC-023 | per epic | gate probe tests |
| Dead-man timer (6) | EPC-015 | per epic | fault-injection tests per transition |
| Lockout and convergence (7) | EPC-024 | per epic | end-to-end stress run |
| Resources (7a), first right after T2 | EPC-010 | per epic | ceiling, early-OOM and default tests |
| macOS indexing (7a) | EPC-033 | per epic | apply and exact revert with fake tools |
| Best-practice defaults (principle 13) | EPC-010, EPC-033, EPC-025 | per epic | default-when-unset and opt-out tests |
| Schema, interview, input defaults (8) | EPC-003, EPC-025 (questions, flags, parity); EPC-004 renders | per epic | parity and default tests |
| Registry, bump flow, references, staging (9) | EPC-026, EPC-027 | per epic | CI rules; link-health job |
| Agent settings and MCP (10) | EPC-009, EPC-012 | per epic | per-home tests |
| Browser tunnel (10a) | EPC-006 | tunnel spec | threat tests per item |
| Fleet commands (2b, 10b) | EPC-018 | per epic | binding tests |
| VPN unit (10c) | EPC-007 | per epic | address and status tests |
| Organisation units, typed operations (2d) | EPC-029, after 1.0.0 | per epic | sample unit through the engine |
| Acceptance (Acceptance) | EPC-030 | per epic | run records on both OSes |
| Cutover and layout (2b) | EPC-031, EPC-032 | per epic | layout check in CI |
