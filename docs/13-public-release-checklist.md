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
- [x] Hosted CI passes on the exact published commit.
- [x] The private conduct-report path is configured and `make public-check` passes.
- [x] Claude and Grok independent reviews are completed and resolved for the
      local RC and supervised pilot scope; durable review evidence belongs in
      the applicable PR or private assurance record, not user documentation.
- [ ] Ubuntu MS-S1 Max pilot, Secure Boot/MOK, RTL8127 kernel-update test,
      NoMachine identity behavior, and realistic four-session load are recorded.
- [ ] macOS guidance is exercised on selected Apple hardware; until then, every
      unverified live-host claim remains explicitly marked as pending evidence.
- [ ] Preview, repeat-apply, rollback, backup/restore, and recovery evidence exists.

## Hosted repository

- [x] Obtain explicit owner approval before creating the public GitHub repository
      or pushing.
- [x] Create the empty public repository without uploading unreviewed data.
- [x] Require PRs, passing CI, independent approval, and protected default branch.
- [x] Require both the repository-validation and secret-scan jobs.
- [ ] Verify the commit, branch, and PR-body policy blocks a deliberately invalid
      hosted test branch.
- [x] Prevent force pushes/deletion; keep Actions read-only by default.
- [x] Enable private vulnerability reporting and verify its repository API state.
- [x] Enable secret scanning, push protection, and Dependabot where the GitHub
      plan supports them.
- [ ] Confirm agent identities cannot approve or merge their own work.
- [x] Clone from the hosted source into a clean directory and rerun all checks.

### First hosted import

An empty repository has no default branch for a pull request. Bootstrap it
without bypassing the protected-branch hook:

```text
reviewed local tree
        |
        v
push the reviewed commit to chore/initial-publish
        |
        v
create main at that exact commit through the GitHub API
        |
        v
run hosted CI -> protect main -> delete bootstrap branch
```

1. Run `make ci-check`, `make public-check`, and the full-history secret scan on
   the exact clean commit approved for publication.
2. Push that commit to `chore/initial-publish`; the pre-push gate runs normally.
3. Create `refs/heads/main` at the same commit with the GitHub API and make it
   the default branch. Confirm the two commit IDs are identical.
4. Run the CI workflow manually on `main`. After both jobs pass, require those
   checks and pull-request review in branch protection.
5. Delete `chore/initial-publish`, clone the public source into a clean directory,
   and rerun both local gates. All later changes use reviewed pull requests.

This one-time bootstrap preserves the reviewed history and never pushes directly
to the protected local branch. It is only valid for a new, empty repository.

## Private fleet repositories

- [ ] Create personal and/or organization fleet repositories as private.
- [ ] Pin `kit.lock`; protect the default branch and validate in CI.
- [ ] Confirm profiles contain no secrets and inventory access is least-privilege.
- [ ] Validate unique technical hostnames, case-insensitive assigned display
      names, UUIDs, asset tags, assignments, and retired names across the whole fleet.
- [ ] Record toolkit revision, reviewer, tests, canary, rollback, and rollout cohort.

## Release candidate 0.2.0-rc.6 follow-up

Historical checked items above describe the earlier publication. Re-establish
release-specific evidence on the final follow-up revision before publication:

- [ ] Existing private feedback tracker reconciled against the complete pilot
      observations; the dropped handoff alone is not the tracker.
- [ ] Independent review of the new readiness, plan and acceptance paths.
- [ ] `make ci-check`, strict `make public-check` and secret scan on the final
      clean commit; hosted checks require an explicitly approved push/PR.
- [ ] Separate Ubuntu and Mac physical-host fresh install, interruption/resume,
      repeat-run user-state preservation and encrypted cold-unlock evidence.
- [ ] Actual listener/address, viewer TCP, named-user SSH/SFTP file transfer,
      Orca connect/reconnect, named-user Screen Sharing or selected GUI results.
- [ ] Provider real runs, project browser preview, container smoke, app restart,
      host reboot, sleep/wake and independent recovery outcomes.
- [ ] Optional package/app source supplement and update/rollback ownership
      reviewed; schema 3 does not automate those new selections.
- [ ] Human approves publication and the private fleet's new pin separately.

The local regression for source-versus-staged readiness covers an ignored archive
inside an unrelated clean parent checkout (OBS-037). Host-context checks cover
unknown and sandbox results (OBS-031). A disposable Git fixture exercises the
shipped hooks through feature worktree creation, linked-worktree commit and ref
packing. Ref packing reproduced a protected-branch/no-upstream failure and now
permits only zero-old-value logical no-op updates before the upstream guard
with `--no-prune`; ref moves and deletions remain guarded. Default pruning still
fails closed because its hook input is ambiguous with deletion. The original OBS-033 reproduction still requires the
private tracker details. The
resumable preview and evidence tests address the handoff's OBS-030/032/035;
transport/workload checks and project transfer documentation address
OBS-038 through OBS-043. These are handoff mappings, not tracker dispositions
or physical-host certification.
