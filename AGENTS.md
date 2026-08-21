# Repository instructions

This repository provisions development machines that run autonomous AI agents.

- Never place passwords, API keys, access tokens, private keys, recovery codes, or exported credential files in this repository.
- Treat `config/profiles/*.example.toml` as templates. Use ignored `*.local.toml` files for experiments; approved non-secret fleet profiles may be reviewed under `config/fleet/` in a private repository.
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
