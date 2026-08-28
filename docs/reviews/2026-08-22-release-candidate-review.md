# Release-candidate review — 2026-08-22

[Previous review](2026-08-21-public-readiness-review.md) · [Documentation home](../README.md) · [Next review](2026-08-25-identity-and-provider-review.md)

Scope: `0.2.0-rc.1` before its first local commit. This record summarizes
independent read-only reviews; it is not live-machine evidence.

## Reviewers and checks

- Claude Code 2.1.240, Claude Opus 5, interactive plan mode.
- Grok Build 1.0.5, Grok 4.6 high reasoning, interactive plan mode.
- `make ci-check`: pass after all review fixes (37 tests).
- `git diff --cached --check`: pass.
- Strict `make public-check`: expected fail on the missing monitored
  conduct-reporting channel; the draft/RC check passes.

Both reviewers found no P0 or P1 defect after remediation. Grok identified a
bare-filename `BASH_SOURCE` edge case in the `agentctl` installer; it was fixed,
staged, and the full suite rerun. Claude retained one P2 defense-in-depth note:
privileged phases depend on the documented root-owned
`/opt/agent-workstation-kit` staging boundary, while the long-lived `agentctl`
installer additionally checks that boundary before sourcing repository helper
code. The first pilot must verify the staging ownership and mode before any
privileged apply.

## Gate decisions

| Gate | Decision | Conditions |
|---|---|---|
| Local RC commit | GO | Exact final index passes the checks above. |
| First supervised Linux pilot | GO | Keep tested console recovery open and obey every human approval gate. |
| Private GitHub push | GO after owner approval | Create/push nothing until explicitly approved; run the history scan first. |
| Public visibility | NO-GO | Complete the physical pilot, conduct contact, history scan, copyright approval, hosted checks, and every item in the public-release checklist. |

## Live evidence still required

- MS-S1 Max firmware, RTL8127 provenance/build, MOK enrollment, Secure Boot
  module load, reboots, and a kernel upgrade.
- Real Ubuntu `sshd`, UFW, `mokutil`, Tailscale peer rejection, and recovery
  behavior, including repeat apply and rollback exercise.
- NoMachine per-human identity behavior on the shared desktop and live
  `agentctl` operator/viewer authorization.
- Herdr resolution, snap Chromium/Playwright, cgroup placement, four concurrent
  agent sessions, thermal/resource measurements, backup/restore, encryption,
  and power-loss recovery.

Record those results in the first-pilot evidence before promoting beyond the
release candidate or making the repository public.
