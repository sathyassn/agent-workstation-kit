## Summary

Describe the problem and the smallest change that addresses it.

## Changes

- List the material behavior, documentation, or policy changes.

## Testing

- Paste the commands and results that verify this change.
- State what was not exercised on real Linux or macOS hardware.

## Safety and approvals

- [ ] Preview behavior remains non-mutating.
- [ ] Privileged, credential, recovery, and destructive boundaries are explicit.
- [ ] Repeat-apply and rollback behavior were considered.
- [ ] Tests and documentation were updated where applicable.
- [ ] `CHANGELOG.md` has an `[Unreleased]` bullet for each change, linked to this PR.
- [ ] `AGENTS.md` is updated if a command, flag or file location changed.
- [ ] `make ci-check` passes (including ShellCheck).
- [ ] Live-machine gaps or assumptions are listed below.
- [ ] The [engineering standard](../docs/standards/engineering.md) holds, and the [review checklist](../docs/standards/review-checklist.md) was used.
- [ ] Each definition has one source, and generated output is current.
- [ ] Core or safety change: a cross-lineage review and stress evidence are linked.

## Live-machine gaps

State what was not exercised on real Linux or macOS hardware. Use “none” only
when the relevant behavior was actually tested.
