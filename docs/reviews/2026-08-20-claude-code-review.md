# Independent review record — 2026-08-20

> Historical record for 0.1.0-rc.1. Superseded by the 0.2.0-rc.1 reviews; do
> not use its names or schema as current guidance.

## Review method

- Reviewer: Claude Code 2.1.238 in an interactive, read-only session.
- Scope: profiles, controller, Linux setup and hardening scripts, resource policy,
  validation, shell configuration, tests, and setup skill.
- Standard: adversarial review for security, idempotence, rollback safety,
  consistency, and unsupported assumptions.

## Results

The initial review blocked release with 16 findings. After remediation, the
second pass found three P1 and seven P2 issues. These included unsafe firewall
rule parsing, SSH service/socket handling, non-idempotent optional tooling,
timezone validation, partial plugin installation, subuid/subgid ordering,
cleanup behavior, and interactive shell completion prompts.

Every reported item was fixed and covered by repository checks where a local
test was practical. The final focused review reported:

- unresolved P0/P1/P2 findings: none;
- pilot decision: **GO**;
- fleet-wide decision: **BLOCKED until pilot evidence is recorded**.

## Automated evidence

Before a pilot or proposed change, run:

```bash
make check
```

This validates all example profiles and plans, Python tests, shell behavior,
skill structure, repository hygiene, and ShellCheck findings.

## Required live pilot evidence

1. Apply remote hardening twice; confirm the second run is a clean no-op. Keep
   remote KVM access available until a second named-user SSH session succeeds.
2. Confirm the target Ubuntu release's `ufw show added` output matches the
   fail-closed parser and that input and routed-forward defaults are denied.
3. Confirm whether the node uses `ssh.socket` or `ssh.service`, then verify the
   effective `sshd -T` configuration and drop-in precedence.
4. Apply user tooling twice and confirm the mise configuration and lock data do
   not drift.
5. Resolve the approved Herdr and Grok Build installation sources and record
   their installed versions.
6. Through the agent user's managed zsh, verify Chromium, Xvfb, rootless Podman,
   and the Docker-compatible command path.
7. Verify subuid and subgid ranges exist for the agent account before enabling
   rootless workloads.
8. Verify read-only tmux observation and storage-encryption detection on the
   target filesystem.
9. Complete the documented burn-in and replace provisional resource settings
   with limits derived from observed workloads.
10. Exercise canary-to-cohort rollout and rollback once, then rerun the audit.

## Boundary

This review supports a controlled pilot. It does not substitute for hardware,
network, identity, licensing, or endpoint-policy validation on the intended
Linux and macOS machines.
