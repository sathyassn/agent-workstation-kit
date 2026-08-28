# Day-zero documentation review — 2026-08-27

[Previous review](2026-08-25-identity-and-provider-review.md) · [Documentation home](../README.md)

Scope: `0.2.0-rc.3` documentation structure, fresh Ubuntu startup, setup-agent
handoff, private-fleet revision checks and privileged staging. This is static
repository evidence; it does not certify the physical MS-S1 Max.

## Reviewers and automated evidence

- Claude Code 2.1.247, Claude Opus 5, high reasoning: interactive plan-mode
  review followed by a focused post-fix pass.
- Grok Build 1.0.5, Grok 4.6: interactive adversarial review followed by a
  focused post-fix pass.
- `make check`: 111 Python tests plus repository, shell-behavior, skill and
  deterministic public-draft checks passed.
- `make ci-check` and `git diff --check`: passed after all fixes.

The final focused Claude pass reported no unresolved P1/P2 findings. Grok's
final P2 was missing regression coverage for staging's pre-existing-target and
rollback guards; those assertions were added, and Grok's final focused pass
reported no unresolved P1/P2 findings.

## Material findings closed

- The root README and documentation map provide one discoverable path. The
  first-pilot before-power-on checklist feeds into day zero, which controls the
  install, profile interview, staging and handoff sequence.
- The approved private profile and `kit.lock` are human-reviewed and committed
  before `git archive`; readiness rejects a dirty fleet or an existing profile
  that is not tracked in its exact Git revision.
- Privileged Linux examples use matching root-owned toolkit and private-fleet
  snapshots under `/opt`. A human types each apply in a separate trusted
  terminal and invalidates cached sudo authorization afterward.
- Staging refuses existing targets, extracts both archives into a root-owned
  temporary area, validates expected files and rolls back normal publication
  failures. Interrupted hidden staging evidence is never executable input and
  requires human review before retry.
- The readiness command rejects root execution, the wrong Ubuntu release,
  dirty toolkit/fleet revisions, a symlinked fleet root, missing Codex or
  preflight tooling, failed repository checks and invalid/uncommitted profiles.
- Automated documentation checks enforce discoverability, real navigation
  links, valid anchors, staged privileged examples and the commit-before-archive
  sequence.

## Gate decisions

| Gate | Decision | Conditions |
|---|---|---|
| Local RC commit | **GO** | Commit the exact checked tree and rerun CI from that clean commit. |
| First supervised Linux pilot | **GO** | Use the physical console and begin with the first-pilot before-power-on checklist. |
| Private GitHub publication | **NO ACTION** | Creating a remote or pushing still requires explicit owner approval. |
| Public visibility | **NO-GO** | Complete live Linux/macOS evidence, legal/contact gates, hosted controls and the public-release checklist. |

## Live evidence still required

- Firmware, Secure Boot/MOK, RTL8127, network, thermals and kernel/reboot tests
  on the actual MS-S1 Max.
- NoMachine reconnect and identity behavior, Tailscale recovery, resource/load
  headroom, backup/restore and power-loss recovery.
- Four concurrent long-running sessions with subagents, headed Playwright,
  builds and rootless containers.
- macOS setup and workload validation on the selected Apple hardware.

[Documentation home](../README.md) · [Production/public checklist](../13-public-release-checklist.md)
