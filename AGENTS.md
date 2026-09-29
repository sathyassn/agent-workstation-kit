# Repository instructions

This repository provisions development machines that run autonomous AI agents.

- Never place passwords, API keys, access tokens, private keys, recovery codes, or exported credential files in this repository.
- Treat `config/profiles/*.example.toml` as templates. Use ignored
  `config/profiles/*.local.toml` files only for experiments; approved fleet
  profiles belong in the separate private fleet repository.
- Prefer an idempotent script for deterministic system changes. Use the setup skill for discovery, sequencing, approvals, and validation.
- Run every repository script in its default preview mode first; use `--apply`
  only after the preview and approval. Use a vendor tool's own dry-run mode when
  the applicable guide calls for it.
- Stop for human approval before `sudo`, disk encryption changes, account creation, firewall or remote-access changes, vendor authentication, or destructive cleanup.
- Do not weaken branch protection, endpoint protection, disk encryption, audit logging, or OS security to make an agent tool work.
- Do not allow an agent identity to approve or merge its own pull or merge request.
- Preserve existing user configuration unless the applicable guide explicitly authorizes a managed replacement and a backup has been made.
- Never create a GitHub/GitLab remote, push a branch, open a PR/MR, publish a release, or change repository visibility without explicit human approval for that action.
- Any future hosted copy must remain private until the owner explicitly approves a visibility change.
- Run `make check` during development and `make ci-check` before proposing a
  rollout; document every live-machine validation gap.
- Update `VERSION` and `CHANGELOG.md` together for a release. Run the strict
  `make public-check` and complete `docs/13-public-release-checklist.md` before
  requesting publication approval.

## Writing

- **Write plainly.** Everything you write, replies and status updates
  included, is simple, straightforward and clear, with the detail the reader
  needs and no more. Avoid mannered prose, writing that performs for effect:
  slogans, "not X but Y" turns, rhetorical triplets, dramatic fragments,
  stacked hedges, colon reveals, self-narration, ceremonial framing and walls
  of text. State the fact directly. CodeFlow's cf-editorial-review skill lists
  these patterns with plain rewrites in `references/editorial-smells.md`.
- Default to short prose and bullets. Write long prose only when the reader
  asks for it or the artifact is prose by nature, such as an essay or a
  narrative guide.
- A summary anchors the reader: what this is, why it matters and where it
  stands. The details follow as bullets in a logical order.
- Where a flow, structure or comparison carries the point, lead with a figure
  in the form the surface shows: an inline HTML figure where the harness
  renders one, and fenced ASCII in Markdown files (READMEs, docs, records, PR
  bodies) and in terminal output. Never Mermaid.

## Git discipline

- Work on a `{type}/{kebab-name}` branch, never directly on `main` or `master`.
- Use Conventional Commit subjects accepted by `.codeflow/policy.json`.
- Do not add AI attribution or emoji to commit messages or pull-request bodies.
- Keep each commit focused. Use at most three short bullet lines in a commit
  body; mark a breaking interface or schema change explicitly.
- Configure `core.hooksPath=.codeflow/git-hooks` and keep the Codeflow-derived
  policy, self-contained checker, local hooks, hosted check, and remote branch
  protection aligned.
- A human reviews and merges every protected-branch pull request. Policy
  enforcement does not authorize remote creation, push, PR creation,
  publication, deployment, or privileged host changes.
