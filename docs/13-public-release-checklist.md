# Production and public-release checklist

[Previous: fleet rollout](12-fleet-rollout-and-change-management.md) · [Documentation home](README.md) · [Repository home](../README.md)

Complete this for the exact commit proposed for publication. Repository checks
do not replace legal approval or live-machine evidence.

## Repository and legal

- [x] Generic name: `agent-workstation-kit`.
- [x] Apache License 2.0 present; no dual-license ambiguity.
- [x] Public toolkit separated from private fleet inventory.
- [x] Version and changelog updated together.
- [ ] Owner confirms copyright attribution and publication rights.
- [ ] Replace the private conduct-report placeholder with a monitored channel.
- [x] Scan the complete final Git history with an approved secret scanner.
- [ ] Review the clean root history before its first push.

## Engineering

- [x] `make ci-check` passes on the exact clean local commit.
- [ ] Hosted CI passes on the exact commit proposed for publication.
- [ ] Replace the conduct-contact placeholder, then verify `make public-check`
      passes. It intentionally fails closed while that placeholder remains.
- [x] Claude and Grok independent reviews are recorded and resolved for the
      local RC and supervised Linux pilot scope.
- [ ] Ubuntu MS-S1 Max pilot, Secure Boot/MOK, RTL8127 kernel-update test,
      NoMachine identity behavior, and realistic four-session load are recorded.
- [ ] macOS guidance is exercised on selected Apple hardware; until then, every
      unverified live-host claim remains explicitly marked as pending evidence.
- [ ] Preview, repeat-apply, rollback, backup/restore, and recovery evidence exists.

## Hosted repository

- [ ] Obtain explicit owner approval before creating the GitHub remote or pushing.
- [ ] Create it private first; inspect rendered files, Actions, and history.
- [ ] Require PRs, passing CI, independent approval, and protected default branch.
- [ ] Prevent force pushes/deletion; keep Actions read-only by default.
- [ ] Enable secret scanning, push protection, Dependabot, and private
      vulnerability reporting where the plan supports them.
- [ ] Confirm agent identities cannot approve or merge their own work.
- [ ] Obtain separate explicit approval before changing visibility to public.
- [ ] Clone from the hosted source into a clean directory and rerun all checks.

## Private fleet repositories

- [ ] Create personal and/or organization fleet repositories as private.
- [ ] Pin `kit.lock`; protect the default branch and validate in CI.
- [ ] Confirm profiles contain no secrets and inventory access is least-privilege.
- [ ] Validate unique technical hostnames, case-insensitive assigned display
      names, UUIDs, asset tags, assignments, and retired names across the whole fleet.
- [ ] Record toolkit revision, reviewer, tests, canary, rollback, and rollout cohort.
