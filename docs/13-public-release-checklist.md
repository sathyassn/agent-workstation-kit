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

## Keyed public-safety scan

`tests/check_public_release.py` scans every tracked file, and every tracked
path, for two things:

- **Private names**: personal names and private network names, matched by
  HMAC-SHA256 digests in `tests/public_scan_rules.json`. The rule file holds
  only lowercase 64-character hex digests, never a name.
- **Private IPv4 addresses**: 10/8, 172.16/12, 192.168/16, the 100.64/10
  CGNAT range and 169.254/16 link-local, found by a built-in detector that
  needs no key.

Address policy, accepted with its trade-offs:

| Form | Flagged | Why |
|---|---|---|
| Four groups inside a range | Always | An address. A four-part version number that falls in a range is flagged too; give it a reviewed exception. |
| Three groups inside 172.16/12, 192.168/16, 100.64/10 or 169.254/16 | Always | These prefixes rarely mean anything else. |
| Three groups inside 10/8 | Only with a trailing dot, `/` CIDR or `.x`/`.*` | A bare `10.15.7` is how version numbers look. |
| Two groups for 172.16/12, 192.168/16, 169.254/16 | Always | Distinctive prefixes. |
| Two groups for 10/8 or 100.64/10 | Only with a continuation as above | A bare `10.5` or `100.75` is usually a number. |
| A prefix whose continuation starts the next line | On the first line, when it ends with the dot | The trailing dot is the continuation. |

Octets are read as decimal, so zero-padding is folded. A letter glued to
either end of an address does not hide it. More than four groups, or an octet
above 255, is not an address.

Before matching, text is normalised: JSON `\u` escapes are decoded,
compatibility forms are folded (NFKD, then NFKC), format and zero-width
characters and combining marks are removed (so accents fold) and, in a
second pass, read as word breaks, Latin
lookalikes from Cyrillic and Greek are folded, and apostrophes are dropped.
Punctuation inside a word is also tried deleted, so `ab!cd` is read as `abcd`. A name split across two
lines, with or without a hyphen, is still found. Files must be UTF-8 or UTF-16
text; anything else fails unless it is a reviewed binary exception.

Findings never print a matched value. When anything in a path matches,
including a value split across a folder and a file name, the whole path is
shown as `<redacted-ID>`, where the ID is keyed, so only the key holder can
link it to a name. Other paths are printed with control characters escaped,
so a file name cannot forge a log line. Errors never echo input text.

The scan reads each distinct object once, however many paths share it, and
fails closed above 100,000 tree entries, a 64 MiB listing, 512 MiB of
distinct content or 10,000 findings. Its caches hold only short lines and are
bounded by size.

Where the scan runs:

| Run | Key | Rules and scanner |
|---|---|---|
| `public-scan / keyed scan` (`.github/workflows/public-scan.yml`), on every pull request into `main`, including retargets and edits, and every push to `main` | `PUBLIC_SCAN_HMAC_KEY`, an environment secret of the `public-scan` environment, passed only to the scan step | From `main`'s current commit. The head is fetched as git data and scanned with `--tree`; its code never runs. |
| `CI / repository-validation`, `make ci-check`, `make public-check` | None: the public synthetic key | From the checkout. This job runs pull request code, so it never receives the key. |
| Any other run without a key: local runs, `make ci-check`, and CI jobs that run pull request code | None: falls back to synthetic, printing `WARNING: keyed private-name rules not checked here; the public-scan workflow runs them.` | From the checkout. |

In keyed mode every rule must hold at least one digest; an empty or partly
empty rule file fails with `no keyed rules populated for: ...`. Synthetic runs
allow empty production rules.

Because the keyed scan reads its rules and exceptions from the base commit, a
pull request that adds a reviewed exception or a digest takes effect only
after it merges. Land such a change in its own pull request first.

Reviewed exceptions live in `tests/public_scan_exceptions.json`:

- `private_ipv4`: an exact path and the canonical addresses or prefixes it may
  hold, with a reason. The same value in another file is still flagged.
- `binary`: an exact path and the sha256 of its content, with a reason. Any
  change to the content fails until the hash is reviewed and updated.

Owner setup:

1. Create the untracked local key file: exactly 64 lowercase hex characters
   (32 random bytes), readable only by you.

   ```bash
   install -d -m 0700 .secrets
   (umask 077 && openssl rand -hex 32 > .secrets/public-scan-hmac)
   ```

   Never commit `.secrets/public-scan-hmac`. `.gitignore` excludes `.secrets/`,
   and the scanner fails closed if a key file is tracked, symlinked, or has
   group or other permission bits. The check covers the mode bits only, not
   ACLs; do not grant ACL access to the file.

2. Create the GitHub Actions environment `public-scan` with its deployment
   branches limited to `main`, and set the environment secret
   `PUBLIC_SCAN_HMAC_KEY` there to the exact contents of
   `.secrets/public-scan-hmac` (**Settings -> Environments**, or
   `gh secret set PUBLIC_SCAN_HMAC_KEY --env public-scan < .secrets/public-scan-hmac`).
   Do not create a repository-level secret of that name: a repository secret
   is readable by any workflow a branch adds, while an environment secret
   limited to `main` is not. Delete any repository-level copy.

3. Compute digests for private names from stdin, without putting private
   values in command arguments or shell history:

   ```bash
   python3 tests/check_public_release.py --hash-rule personal-name
   python3 tests/check_public_release.py --hash-rule private-network-name
   ```

   Enter one value per line and press Ctrl-D, or pipe from a private file
   outside the repository. Paste the resulting digests into the matching
   `digests` list in `tests/public_scan_rules.json`, run `make public-check`
   with the key present to confirm it passes, and commit only
   `tests/public_scan_rules.json`.

4. Make `public-scan / result` a required status check on `main`, next to
   the existing required checks, and require code-owner review so the
   `.github/CODEOWNERS` rules apply to CI and to the scan's own files.

Require `result`, not `keyed scan`: the `keyed scan` job is skipped for pull
requests into other branches, and GitHub counts a skipped required job as a
pass. `result` always runs and fails unless the keyed scan succeeded for a
pull request into `main` or a push to it. Every pull request event, including
retargeting to `main`, reruns the workflow, so an earlier result for the same
head is replaced by a real scan.

Residual risk: a required check is matched by name, and a workflow in a pull
request could report a job of the same name. The code-owner rule on
`.github/` means any such workflow needs the owner's review, and the real
keyed job runs as a visible deployment to the `public-scan` environment on the
pull request.

- [ ] `PUBLIC_SCAN_HMAC_KEY` is a secret of the `public-scan` environment, limited to `main`, and no repository-level copy exists.
- [ ] Every rule in `tests/public_scan_rules.json` holds HMAC-SHA256 digests,
      and keyed `make public-check` passes.
- [ ] `public-scan / result` is a required status check on `main`, and code-owner review is required.

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
- [ ] Optional package/app source and update/rollback ownership reviewed; added
      APT/Homebrew selections are automated, custom sources remain separate.
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

## Release candidate 0.2.0-rc.7 automation

The follow-up now includes actual owner-scope execution with preview, explicit
host/scope approval, live postconditions, automatic private receipts and failure
resume. Linux and Mac package sets are declarative; fresh Linux mise bootstrap
uses fixed, signature-reviewed source hashes. Mac local accounts use hidden
vendor prompts, managed accounts are verified, and the shared workload helper
automates project-locked browser and digest-pinned rootless container probes on
Ubuntu/Podman and Mac/OrbStack. SSH/SFTP
transport checks are executable.

- [ ] Independent review of the new executor, privileged staging checks, fresh
      SSH-install guard, account helper and live-verification semantics.
- [ ] Physical Ubuntu APT/postinst/masking/firewall failure recovery and reboot
      qualification, including preservation of an existing administrator's SSH.
- [ ] Physical Mac `sysadminctl` create/repeat behavior, secure-token consequences,
      MDM account verification, native Homebrew owner and OrbStack/privacy paths.
- [ ] Real selected project Playwright/browser and digest-pinned container runs;
      real client/host SSH/SFTP plus Orca and Screen Sharing reconnect.

Disposable execution fixtures and mocked package/network tests establish software
control flow; they do not establish these physical-host results.

Local development validation: 212 automated tests, strict `make ci-check` and
`make public-check` pass. Native Apple Bash package/account previews and the
guided terminal were exercised without applying host changes. Signed mise
checksums were verified against the vendor public key. The pending live checks
above remain required before rollout.
