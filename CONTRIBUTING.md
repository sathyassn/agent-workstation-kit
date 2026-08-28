# Contributing

Contributions should preserve the project's preview-first, fail-closed operating
model. An automation convenience is not worth weakening identity, recovery, or
host-security boundaries.

Contributions are accepted under the Apache License 2.0. Do not submit private
fleet data, organization-only policy, personal paths, or credentials.

## Before opening a change

1. Install Python 3.11+, Make, Git, and ShellCheck. On Ubuntu:

   ```bash
   sudo apt-get update
   sudo apt-get install --yes git make python3 shellcheck
   ```

2. Enable the checked-in hooks for this clone and validate the self-contained
   policy checker:

   ```bash
   git config core.hooksPath .codeflow/git-hooks
   python3 scripts/check_git_discipline.py policy
   ```

3. Create a focused `{type}/{kebab-name}` branch from the current default
   branch. Allowed prefixes are defined in `.codeflow/policy.json`; examples
   include `docs/clarify-day-zero` and `fix/profile-validation`.
4. Use a Conventional Commit subject such as `docs: clarify Linux handoff`.
   Keep the description to 50 characters and the full subject to 72. A body, if
   needed, contains no more than three single-line `-` bullets. Do not add AI
   attribution or emoji.
5. Do not commit credentials, local profiles, infrastructure identifiers, test
   artifacts, or copied vendor packages.
6. Keep deterministic OS changes in idempotent scripts. Keep discovery,
   sequencing, approvals, and explanations in the setup skill or documentation.
7. Preserve preview behavior. A mutating path must require explicit `--apply`;
   privileged or recovery-sensitive changes need the documented human gate.
8. Update examples, tests, documentation, and `CHANGELOG.md` when behavior or
   profile fields change.
9. Run `make ci-check` and `make public-check` before requesting review. Include
   the result plus any
   untested live-machine assumptions in the pull request. Hosted CI validates
   the exact commit range and pull-request body with the same policy.

The checked-in hooks provide fast local feedback. Hosted CI repeats policy,
secret, documentation, and repository checks. Required branch protection is the
merge boundary; a local hook alone is not sufficient enforcement.

## Review expectations

- Keep changes small enough to reason about and roll back.
- Prefer standard-library code and pinned, verified vendor inputs.
- Test negative and repeat-apply behavior, not only the successful first run.
- An agent identity may author a change, but it must not approve or merge its
  own pull request.
- Security reports follow `SECURITY.md`, not the public issue tracker.
- Pull requests use the checked-in template. A human who did not author the
  change approves and merges it after required checks pass.
