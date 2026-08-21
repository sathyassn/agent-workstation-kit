# Contributing

Contributions should preserve the project's preview-first, fail-closed operating
model. An automation convenience is not worth weakening identity, recovery, or
host-security boundaries.

The repository is currently pre-release and has no open-source license.
External contributions cannot be accepted until the owner approves a license;
after that decision, contributions are accepted under the repository license.

## Before opening a change

1. Create a focused branch from the current default branch.
2. Do not commit credentials, local profiles, infrastructure identifiers, test
   artifacts, or copied vendor packages.
3. Keep deterministic OS changes in idempotent scripts. Keep discovery,
   sequencing, approvals, and explanations in the setup skill or documentation.
4. Preserve preview behavior. A mutating path must require explicit `--apply`;
   privileged or recovery-sensitive changes need the documented human gate.
5. Update examples, tests, documentation, and `CHANGELOG.md` when behavior or
   profile fields change.
6. Run `make ci-check` and include the result plus any untested live-machine
   assumptions in the pull request.

## Review expectations

- Keep changes small enough to reason about and roll back.
- Prefer standard-library code and pinned, verified vendor inputs.
- Test negative and repeat-apply behavior, not only the successful first run.
- An agent identity may author a change, but it must not approve or merge its
  own pull request.
- Security reports follow `SECURITY.md`, not the public issue tracker.
