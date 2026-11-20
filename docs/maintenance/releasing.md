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

## After the release

- Private fleets change `kit.lock` to the new version in a reviewed pull
  request; `validate-fleet.py` checks it matches the kit checkout.
- Record the release wherever the owner tracks releases.
