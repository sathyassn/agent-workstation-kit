# Releasing the kit

[Documentation home](../README.md) · [Changelog](../../CHANGELOG.md) · [Contributing](../../CONTRIBUTING.md)

A release turns the `[Unreleased]` changelog section into a tagged version that
private fleets can pin. The repository owner decides when to release; an agent
prepares the release pull request and never tags or publishes on its own.

```text
merged pull requests, each with an [Unreleased] entry
        |
        v
release PR: pick version -> move [Unreleased] under it -> bump VERSION
        |                                (+ template kit.lock)
        v
owner merges -> owner tags vX.Y.Z[-rc.N] -> owner publishes GitHub release
        |
        v
private fleets set kit.lock to the version (without the v)
```

## Every pull request

- Add one short bullet per change under `## [Unreleased]` in `CHANGELOG.md`.
- Put it in the matching group: Added, Changed, Deprecated, Removed, Fixed or
  Security. Create the group heading if it is missing.
- End the bullet with the pull request link, for example `([#12])`, and add the
  `[#12]: https://github.com/sathyassn/agent-workstation-kit/pull/12` reference
  below the section.
- `make repo-check` fails when a branch changes `scripts/`, `config/` or
  `templates/` without a new `[Unreleased]` bullet. Docs-only changes may add
  one but do not have to.

## Pick the version

The kit is before 1.0, so:

| Change since the last release | Next version |
|---|---|
| New feature, new or changed command, flag, profile field or output | Minor: `0.2.x` to `0.3.0` |
| Fixes only | Patch: `0.2.0` to `0.2.1` |
| Live-machine evidence still pending (see the [release checklist](../13-public-release-checklist.md)) | Release candidate: `0.3.0-rc.1`, then `-rc.2`, and so on |

A breaking change is called out in the `Changed` or `Removed` group and in the
upgrade notes.

## Release pull request

On a branch such as `chore/release-0.3.0-rc.1`:

1. In `CHANGELOG.md`, add a heading `## [0.3.0-rc.1] - YYYY-MM-DD` directly
   under `## [Unreleased]`, so the existing entries now sit under the new
   version. Leave `## [Unreleased]` empty above it, and remove its "Planned as
   release" line.
2. Write `0.3.0-rc.1` to `VERSION` and to `templates/private-fleet/kit.lock`.
3. Update the version mentioned in `README.md`.
4. Run `make ci-check` and `make public-check`; both must pass.
5. Open the pull request. Its body is the new changelog section.

## Tag and publish (owner)

Run the strict `make public-check` before every release. Before a final (non-rc)
release, also complete the [release checklist](../13-public-release-checklist.md).

Tag the release pull request's own merge commit, not whatever `main` holds
later. Verify everything before anything is pushed:

```bash
git fetch origin
RELEASE_SHA=$(gh pr view RELEASE_PR_NUMBER --json mergeCommit --jq .mergeCommit.oid)
git show "$RELEASE_SHA:VERSION"            # must print 0.3.0-rc.1
git tag -a v0.3.0-rc.1 -m "agent-workstation-kit 0.3.0-rc.1" "$RELEASE_SHA"
test "$(git rev-parse 'v0.3.0-rc.1^{commit}')" = "$RELEASE_SHA"
git push origin v0.3.0-rc.1
gh release create v0.3.0-rc.1 --verify-tag --prerelease \
  --title "0.3.0-rc.1" --notes-file RELEASE_NOTES.md
```

- Stop if `VERSION` at that commit is not the release version, or if the `test`
  line fails; a pushed tag and a published release are hard to withdraw.
- `RELEASE_NOTES.md` is a temporary file holding the new changelog section; do
  not commit it.
- Use `--prerelease` for every `-rc.N` version and omit it for a final version.

## Versions

- **The Python line is frozen.** `0.2.0-rc.7` is the last planned release of
  the Python and shell kit. It gets fixes only (`0.2.0-rc.8` and on) and is
  never released as a final `0.2.0`.
- **The Go `wskit` is 1.0.0**, a new major version (ADR 0005 and the
  [roadmap](../plan/roadmap.md)). Pre-releases:
  - `1.0.0-alpha.N` from P1 through P2, whose waves run in parallel;
  - `1.0.0-beta.N` during P3, composition;
  - `1.0.0-rc.N` during P4, acceptance;
  - then `1.0.0`.
- **From 1.0.0,** releases follow Semantic Versioning. Breaking config or CLI
  changes come only in a major version, after one minor release of
  deprecation warnings, and `wskit migrate` moves configs between schema
  versions.
- Every release is signed: the owner signs the checksum file with the
  release key when publishing.

## Criteria for 1.0.0

The final `1.0.0` is cut only when all of these hold (EPC-030):

- [ ] The epics for 1.0.0 in the roadmap are merged, each with its evidence.
- [ ] An agent conducts one clean setup on a supported Linux machine and one
      on a supported macOS machine, from the interview to the stable point,
      with one deliberate interrupt it diagnoses and resumes, covering both
      account topologies and a `developer` run on a portable Mac.
- [ ] `revert --to baseline` passes the match digest on each.
- [ ] The doctor is green, and a docs-only walkthrough passes.
- [ ] The [release checklist](../13-public-release-checklist.md) is complete.

## After the release

- Private fleets change `kit.lock` to the new version in a reviewed pull
  request; `validate-fleet.py` checks it matches the kit checkout.
- Record the release wherever the owner tracks releases.
