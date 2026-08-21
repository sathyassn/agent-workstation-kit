# Public release checklist

Complete this checklist for the exact commit intended for publication. A green
repository test run is necessary, but it does not replace the legal, privacy,
security, and live-pilot decisions below.

## 1. Release identity

- [ ] `VERSION` is valid Semantic Versioning and matches `CHANGELOG.md`.
- [ ] The release notes distinguish repository validation from live fleet
      certification.
- [ ] A signed or annotated `v<version>` tag is created from the reviewed commit.
- [ ] The selected open-source license is present as `LICENSE` and has owner or
      legal approval.

Run `make public-check`. It deliberately fails when `LICENSE` is absent.

## 2. Privacy and intellectual property

- [ ] The full Git history—not only the current files—contains no credentials,
      private hostnames, customer data, proprietary text, personal paths, or
      organization-only configuration.
- [ ] Resolve the known original-root-commit personal example identifiers:
      with owner approval, replace the unpublished history with one clean root
      commit before the first push, then inspect and rescan the new history.
- [ ] Example people, domains, machines, and networks are clearly fictional.
- [ ] Vendor names are descriptive references; the README does not imply vendor
      sponsorship or endorsement.
- [ ] The repository owner has the right to publish every file and dependency.

Run an approved secret scanner against the complete Git history immediately
before publication. If history must be rewritten, rescan the rewritten result.

## 3. Engineering evidence

- [ ] `make ci-check` passes on a clean checkout, including ShellCheck.
- [ ] The hosted CI workflow passes with read-only repository permissions.
- [ ] Relative documentation links resolve.
- [ ] Preview, negative, repeat-apply, rollback, and recovery behavior are tested.
- [ ] The primary-source register has been rechecked for changed installers,
      operating-system support, authentication, and product behavior.
- [ ] A clean Ubuntu pilot and the documented burn-in gates are recorded.
- [ ] macOS claims are either exercised on supported hardware or explicitly
      marked as unverified guidance.

## 4. GitHub repository settings

- [ ] Start private, inspect the rendered repository, then approve visibility
      separately.
- [ ] Require pull requests, passing CI, and at least one independent approval
      on the default branch.
- [ ] Prevent force pushes and branch deletion on the default branch.
- [ ] Enable secret scanning, push protection, and private vulnerability
      reporting where the hosting plan supports them.
- [ ] Set default GitHub Actions workflow permissions to read-only and prevent
      Actions from creating or approving pull requests.
- [ ] Require approval for workflows from all outside contributors and restrict
      allowed actions to the reviewed policy.
- [ ] Enable and test the private vulnerability-reporting path required by
      `SECURITY.md`.
- [ ] Replace the conduct-report placeholder in `CODE_OF_CONDUCT.md` with a
      real, monitored private address.
- [ ] Configure issue labels/templates, repository topics, and an accurate short
      description.
- [ ] Add the final repository Security-tab URL to the issue-template contact
      links after the private remote exists.
- [ ] Configure Dependabot for pinned GitHub Actions and review its proposed
      updates rather than accepting them automatically.
- [ ] Confirm that agent identities cannot approve or merge their own changes.

## 5. Publication gate

- [ ] Review `git status`, the final diff, commit authorship, and tag target.
- [ ] Obtain explicit owner approval to create the remote and push.
- [ ] Obtain separate explicit approval before changing visibility to public.
- [ ] After publication, clone into a temporary clean directory and rerun all
      checks from the public source.

Record the reviewer, commit, test evidence, license decision, pilot status, and
publication approvals in the release notes.
