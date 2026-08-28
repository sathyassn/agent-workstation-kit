# Git discipline

This directory adapts Codeflow's Git discipline without making this public-capable
toolkit depend on the Codeflow binary, project-management tier, or harness
permission presets. The policy is enforced by a standard-library Python checker
and portable Git hook shims kept in this repository.

The narrower adoption is intentional. Human approval is still required before
remote creation, push, pull-request creation, publication, and privileged host
changes. Passing a hook or CI check never supplies that approval.

## Enforcement

```text
developer command -> local hook -> native policy checker -> policy.json
                                                               |
pull request ------> hosted native policy check ----------------+
                    repository test suite
                    hosted secret scan
                                                               |
protected main <---- GitHub branch rules -----------------------+
```

- `.codeflow/policy.json` is the rule source for branches, commits, protected
  refs, pull-request structure, secret scanning, and the pre-push test gate.
- `scripts/check_git_discipline.py` validates that policy and implements the
  local and hosted checks without third-party Python packages.
- `.codeflow/git-hooks/` contains small shims that call the checker.
- `.github/workflows/ci.yml` runs the repository gate, the same policy against
  the explicit pull-request range, and a separate full-history secret scan.
- GitHub branch protection is required after the remote exists. Local hooks are
  fast feedback; hosted CI and branch protection are the merge perimeter.

The local protected-ref hook permits a move to the exact configured upstream
tip, which keeps an ordinary `git pull --ff-only` working. It blocks deletion,
rewind, a local feature-branch fast-forward, or any other protected-branch move.
Creating a missing local protected branch from that exact upstream tip is also
allowed. Contribution commits still belong on a valid topic branch, and direct
pushes to a protected branch remain blocked.

Use `git pull --ff-only` to synchronize a checked-out protected branch. A raw
fetch refspec that writes directly to the local protected ref, such as
`git fetch origin main:main`, is intentionally outside the supported workflow;
fetch remote-tracking refs normally and then use the guarded pull path.

Enable the hooks once in each clone:

```bash
git config core.hooksPath .codeflow/git-hooks
python3 scripts/check_git_discipline.py policy
make ci-check
```

The checker fails closed when the policy is missing or malformed. Hook files are
ShellCheck-validated, and repository tests verify their presence and executable
mode. The pre-push repository gate has a ten-minute ceiling and clears Git's
hook-only repository variables before tests spawn their own temporary Git
repositories. Hosted checks must remain required because a contributor can
disable local hooks in a clone.

This is a repository-specific adoption of Codeflow's branch, commit, secret,
protected-ref, review, and testing discipline. It deliberately does not claim
the full Codeflow scaffold or its planning, ledger, orchestration, or harness
guard features.
