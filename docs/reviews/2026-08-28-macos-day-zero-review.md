# macOS day-zero review — 2026-08-28

[Previous review](2026-08-27-day-zero-documentation-review.md) · [Documentation home](../README.md)

Scope: `0.2.0-rc.4` macOS day zero, readiness, root-owned snapshot staging,
operator access and neutral public examples. This is static repository evidence;
it does not certify a live Mac mini or Mac Studio deployment.

## Reviewers and evidence

- Claude Code 2.1.250, Claude Opus, high reasoning: interactive adversarial
  review of the uncommitted macOS path and a focused remediation discussion.
- Grok Build 1.0.5, Grok 4.6 high reasoning: interactive scoped review of the
  current macOS runbooks, readiness command and tests. Candidate findings were
  independently reproduced before acceptance.
- `make ci-check`: 124 Python tests plus repository, shell-behavior, skill and
  deterministic public-draft checks passed after remediation.
- The embedded privileged Bash block passed `/bin/bash -n`; the identity
  installer compiled under Apple Command Line Tools Python 3.9.6; the setup
  skill validator and `git diff --check` passed.
- Apple `shasum --check --strict` and `/opt` ownership/mode were exercised on
  the review Mac rather than inferred from another platform.

## Material findings closed

- A separate privileged Terminal now re-declares every archive path, profile
  and digest. It runs with an empty environment and a system-only `PATH` so
  human-writable Homebrew commands cannot replace privileged tools.
- Snapshot staging creates `/opt` only when absent, otherwise verifies it. An
  existing `/opt/homebrew` remains untouched; only the two named toolkit/fleet
  children are published.
- Staging rejects existing or symlinked targets, mismatched digests, archive
  symlinks/special entries, missing expected files and a version-lock mismatch.
  Publication uses a rollback-capable temporary directory.
- Privileged identity apply executes only the root-owned staged installer with
  exact reviewed arguments. The TOML controller remains unprivileged under
  Homebrew Python.
- Readiness suppresses provider-account output, runs the repository suite with
  its current interpreter, rejects a symlinked profile path and gives a clear
  Python 3.11+ error under Apple's older interpreter.
- Documentation tests enforce the staging link, clean-environment boundary,
  `/opt` behavior, archive-entry checks, signal rollback and required
  `REVIEWED_*` replacement.
- Public examples use neutral namespaces only. The operator selects the prefix
  as `deployment.namespace` in the private machine TOML; validation requires
  the technical hostname prefix to match it.

## Review claims rejected after reproduction

- Grok suggested Apple `shasum` lacked `--strict`; the installed Apple command
  exposes the option and passed a strict checksum verification exercise.
- Grok suggested the restricted privileged `PATH` needed Homebrew. The staged
  command intentionally uses only `/usr/bin`, `/bin`, `/usr/sbin` and `/sbin`;
  adding Homebrew would weaken the trust boundary.
- Grok suggested staging required an empty `/opt`. It does not enumerate or
  remove unrelated children, and the runbook now states this explicitly.

## Gate decisions

| Gate | Decision | Conditions |
|---|---|---|
| Local RC commit | **GO** | Commit the exact checked tree and rerun CI from the clean commit. |
| First supervised macOS pilot | **GO** | Use local console recovery and execute one reviewed phase at a time. |
| Private remote publication | **NO ACTION** | Creating a remote or pushing still requires explicit owner approval. |
| Public visibility | **NO-GO** | Complete live Linux/macOS evidence, legal/contact gates and the public-release checklist. |

## Live evidence still required

- Full Setup Assistant, FileVault recovery/escrow, MDM and reboot exercise on
  the selected Mac hardware.
- Root-owned staging and rollback exercise with real committed toolkit/fleet
  snapshots; no privileged staging command was executed during static review.
- Tailscale, NoMachine/Screen Sharing, SSH, multi-operator access, resource
  load, backup/restore and power-loss recovery.
- Codex, Claude Code, Grok Build, headed browser tests and long-running agent
  sessions under the final non-admin operational account.

[Documentation home](../README.md) · [Production/public checklist](../13-public-release-checklist.md)
