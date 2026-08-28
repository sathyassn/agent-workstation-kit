# Production and public-release checklist

[Previous: fleet rollout](12-fleet-rollout-and-change-management.md) · [Documentation home](README.md) · [Repository home](../README.md)

Complete this for the exact commit proposed for publication. Repository checks
do not replace legal approval or live-machine evidence.

## Repository and legal

- [x] Generic name: `agent-workstation-kit`.
- [x] Apache License 2.0 present; no dual-license ambiguity.
- [x] Public toolkit separated from private fleet inventory.
- [x] Version and changelog updated together.
- [x] Codeflow-derived policy, self-contained checker, local hook shims, and
      hosted policy check are checked in.
- [x] Owner confirms publication under the selected license.
- [x] Private conduct reports use GitHub private vulnerability reporting.
- [x] Scan the complete final Git history with an approved secret scanner.
- [x] Review the complete root history before its first push.

## Engineering

- [x] `make ci-check` passes on the exact clean local commit.
- [ ] Hosted CI passes on the exact commit proposed for publication.
- [ ] Replace the conduct-contact placeholder, then verify `make public-check`
      passes. It intentionally fails closed while that placeholder remains.
- [x] Claude and Grok independent reviews are completed and resolved for the
      local RC and supervised pilot scope; durable review evidence belongs in
      the applicable PR or private assurance record, not user documentation.
- [ ] Ubuntu MS-S1 Max pilot, Secure Boot/MOK, RTL8127 kernel-update test,
      NoMachine identity behavior, and realistic four-session load are recorded.
- [ ] macOS guidance is exercised on selected Apple hardware; until then, every
      unverified live-host claim remains explicitly marked as pending evidence.
- [ ] Preview, repeat-apply, rollback, backup/restore, and recovery evidence exists.

## Hosted repository

- [ ] Obtain explicit owner approval before creating the GitHub remote or pushing.
- [ ] Create it private first; inspect rendered files, Actions, and history.
- [ ] Require PRs, passing CI, independent approval, and protected default branch.
- [ ] Require both the repository-validation and secret-scan jobs; verify the
      commit, branch, and PR-body policy blocks a deliberately invalid test
      branch before publication.
- [ ] Prevent force pushes/deletion; keep Actions read-only by default.
- [ ] Enable secret scanning, push protection, Dependabot, and private
      vulnerability reporting where the plan supports them.
- [ ] Confirm agent identities cannot approve or merge their own work.
- [ ] Obtain separate explicit approval before changing visibility to public.
- [ ] Clone from the hosted source into a clean directory and rerun all checks.

### First hosted import

Do not bypass the protected-branch hook for the initial upload. After the owner
approves remote creation and the first push, use a clean import path:

```text
reviewed local tree
        |
        v
new private repository initialized with README on main
        |
        v
clean clone -> import tree on chore/initial-toolkit-import
        |
        v
CI + human-reviewed PR -> protected main
```

1. Create the private repository with an initial README so `main` exists
   remotely without a local protected-branch push.
2. Clone that repository into a new directory and create
   `chore/initial-toolkit-import` from its `main`.
3. Copy the reviewed tree without its `.git` directory or ignored local files.
4. Run `make ci-check` and `make public-check`, make one reviewed Conventional
   Commit, then push only the import branch.
5. Open a PR, require the repository-validation and secret-scan jobs, obtain
   independent human approval, and merge through the host UI.

This produces a small, reviewable hosted history without disabling hooks or
carrying exploratory local commits into the public-capable repository.

## Private fleet repositories

- [ ] Create personal and/or organization fleet repositories as private.
- [ ] Pin `kit.lock`; protect the default branch and validate in CI.
- [ ] Confirm profiles contain no secrets and inventory access is least-privilege.
- [ ] Validate unique technical hostnames, case-insensitive assigned display
      names, UUIDs, asset tags, assignments, and retired names across the whole fleet.
- [ ] Record toolkit revision, reviewer, tests, canary, rollback, and rollout cohort.
