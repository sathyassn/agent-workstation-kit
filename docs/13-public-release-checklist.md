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

## Deterministic public-safety scan

`tests/check_public_release.py` scans every tracked file and every tracked path
for personal and identifying details without any secret or name list. It runs
in the pre-push hook (`make ci-check`), in `make public-check`, and in CI on
both Linux and macOS.

### Detectors and exact allow lists

Every detector defaults to `block` mode in `tests/public_scan_modes.json`:

| Rule | Detects | Exact allow list (matched only after folding) |
|---|---|---|
| `email` | Email addresses | Exact domains `example.com`, `example.org`, `example.net`, `example.invalid` |
| `phone` | International E.164 and formatted phone numbers | Documented placeholder numbers (`+1-555-555-0100`, `+1-555-555-0101`, `+1-555-555-0199`, `+44 20 7946 0000`) |
| `ipv4` | Private and public IPv4 addresses, plus partial private prefixes (RFC 1918, RFC 6598 CGNAT, RFC 3927 link-local) | Documentation ranges `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`; loopback `127.0.0.1`, `127.0.1.1`; `0.0.0.0`, `255.255.255.255` |
| `ipv6` | Private and public IPv6 addresses | Documentation range `2001:db8::/32`; loopback `::1`; unspecified `::` |
| `mac-address` | Colon, hyphen, and dot-quad MAC addresses | RFC 7042 documentation range `00:00:5e:00:53:00` through `00:00:5e:00:53:ff`; null and broadcast |
| `home-path` | `/Users/<name>`, `/home/<name>`, and Windows `Users\<name>` paths | Exact placeholder usernames (`alice`, `bob`, `agent`, `agent-01`, `admin`, `admin-01`, `operator`, `user`, `username`, `example`, `runner`, `ubuntu`, `dev`, `acme`, `lab`) |
| `private-dns` | `*.ts.net`, `*.internal`, and `*.local` names | Exact placeholder hosts (`host-a.ts.net`, `host-a.internal`, `host-a.local`, `host-a.example.ts.net`, `example.ts.net`, `example.internal`, `example.local`, `localhost.local`) |
| `cloud-id` | AWS ARNs/account IDs, GCP project/billing IDs, Azure subscription/tenant IDs, Cloudflare account IDs | Documented placeholder IDs (`123456789012`, `000000000000`, `111122223333`, `example-project`, `acme-example-project`, all-zero and all-one UUIDs) |
| `chat-tracker-id` | Slack, Discord, Atlassian/Jira, and explicit chat/tracker user IDs | Documented placeholder IDs (`U00000000`, `U00EXAMPLE`, `000000000000000000`, `000000:00000000-0000-0000-0000-000000000000`) |
| `secret` | Cloud access keys, GitHub/GitLab/Slack/Tailscale/Google tokens, private key headers | AWS documentation example access key ID |

Before detection, inputs are decoded (UTF-8, UTF-16 with or without BOM, JSON
`\u` and `\/` escapes, URL `%XX` escapes) and folded (NFKD/NFKC, format and
combining marks, Cyrillic/Greek/punctuation lookalikes, case, zero-padded
octets, and glued prefixes). Any file containing NUL bytes is refused as binary
unless `tests/public_scan_binary_exceptions.json` names its exact path and
SHA-256 content hash.

Findings print only the file, line, and rule, never the matched text. If a file
path itself triggers a detector, the entire path is printed as `<redacted-ID>`.
Every blocking failure path fails closed (empty or failed file listing, a
detector raising or skipped, decode errors, or exceeded file-size, entry-count,
or finding caps).

### Detector modes

`tests/public_scan_modes.json` assigns every detector one of `block`, `warn`, or
`off`. All detectors default to `block`. Any detector set to `warn` or `off` is
printed on every run under `Non-default detector modes:`, so a disabled detector
never produces a silent clean result.

### Tracked digest-bound exemptions

Legitimate synthetic fixtures that collide with a detector use tracked records
in `tests/public_scan_exemptions.json` (never plaintext values):

```json
{
  "schema_version": 1,
  "exemptions": [
    {
      "path": "tests/example_fixture.py",
      "rule": "ipv4",
      "digest": "<64-lowercase-hex-sha256-of-folded-value>",
      "reason": "synthetic fixture explanation",
      "approved_by": "@owner",
      "expires": "2027-06-01"
    }
  ]
}
```

Compute a digest from stdin without putting the value on the command line:

```bash
printf '%s\n' '<value>' | python3 tests/check_public_release.py --exemption-digest ipv4
```

Every record requires all six fields (`path`, `rule`, `digest`, `reason`,
`approved_by`, `expires` as `YYYY-MM-DD`). Any expired, undated, malformed, or
incomplete record fails the scan. Every run lists all configured exemptions by
path, rule, and 12-character digest prefix, marked `applied` or `unused`.
Optional keyed-list exemptions (added separately for private repositories) use
their keyed HMAC digest instead.

Hash-bound binary exceptions live in `tests/public_scan_binary_exceptions.json`:

```json
{
  "schema_version": 1,
  "binary": [
    {
      "path": "docs/diagram.png",
      "sha256": "<64-lowercase-hex-sha256-of-file-bytes>",
      "reason": "reviewed documentation diagram"
    }
  ]
}
```

### Trusted base-revision CI and code ownership

- `.github/workflows/public-scan.yml` is the sole authoritative CI privacy
  gate. It runs on `pull_request_target` (targeting any branch) and pushes to
  `main` across both `ubuntu-24.04` and `macos-latest` with no secret. It checks
  out the trusted default branch commit (`github.sha`) with full history for the
  scanner, detectors, mode configuration, exemptions, and binary exceptions,
  fetches the pull request head purely as git objects, and scans every commit in
  `base..head` with `--range` (failing closed on incomplete history). Head code
  never runs, and head edits to exemptions, modes, binary exceptions, or the
  scanner do not take effect until reviewed and merged.
- Both platform jobs (`scan-linux` and `scan-macos`) require exit 0 and the
  positive completion marker `PUBLIC_SCAN_VERIFIED=1` before setting
  `scan_verified=true`. The aggregator job publishes the exact GitHub check
  context `public-scan / result` (via `name: public-scan / result`) and fails
  closed unless both Linux and macOS succeeded and reported `scan_verified=true`.
- Head-run jobs in `.github/workflows/ci.yml` (`repository-validation`,
  `macos-public-scan (non-authoritative head self-test)`) execute pull-request
  head code and are non-authoritative self-tests only: no required privacy or
  identity verdict comes from `.github/workflows/ci.yml`.
- Require `public-scan / result` on `main`, next to the existing required
  checks. Repository protection setup must fail closed if `public-scan / result`
  is absent rather than silently skipping it. Enforcement of code-owner review
  for `.github/CODEOWNERS` (covering the scanner, mode config, exemptions,
  binary exceptions, scan workflows, and `.github/CODEOWNERS` itself) depends on
  the repository protection setup in `TSK-040`.
- Locally, the `pre-push` hook runs `make ci-check` (worktree check) and scans
  every new commit in the pushed range via `tests/check_public_release.py --commits`,
  so a leak in an earlier unpushed commit is blocked even if a later commit
  redacted the tip.

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
