---
name: setup-agent-dev-machine
description: Provision, extend, validate, or audit a dedicated Linux or macOS workstation for long-lived Codex, Claude Code, Grok Build, browser testing, shared agent accounts, remote access, and secure source-control automation. Use when setting up a new agent development machine, reproducing the fleet configuration, adding users or tools, applying resource safeguards, performing maintenance, or checking an existing node against the agent-dev-fleet baseline.
---

# Set Up Agent Dev Machine

Provision from the repository's reviewed scripts and guides. Orchestrate discovery, choices, approvals, execution, and validation; do not improvise privileged configuration when a repository script covers it.

## Locate the repository

When this skill remains under `skills/setup-agent-dev-machine`, treat `../..` from this directory as the repository root. If installed separately, ask for the `agent-dev-fleet` repository path. Refuse to operate from an untrusted or unexpectedly modified copy until the human reviews it.

## Apply the safety contract

- Begin read-only.
- Never ask the user to paste a secret into chat or a command argument.
- Never store secrets in the repository, profiles, shell startup files, logs, or reports.
- Preview every supported script without `--apply`, summarize exact effects, then request human approval before applying.
- Pause before `sudo`, administrator authentication, account/access changes, encryption, firewall, SSH, Tailscale, NoMachine, KVM, MDM, privacy permissions, vendor authentication, repository-role changes, or deletion.
- Keep console/KVM recovery available while changing remote access.
- Preserve existing configuration; stop on conflicts instead of overwriting it.
- Do not weaken security controls to make an agent tool work.
- Do not let an agent identity approve or merge its own PR/MR.

Read [approval-boundaries.md](references/approval-boundaries.md) before any mutating phase.

## Determine the starting phase

Run `scripts/preflight.sh` from the repository and inspect its output. Identify OS/version, hardware, existing accounts, remote access, package managers, installed tools, corporate controls, and whether this is work or personal use.

An agent can begin orchestration after all of these are true:

1. The OS has booted successfully.
2. A named human or bootstrap administrator can log in.
3. Networking works.
4. This repository is present.
5. At least one supported agent CLI is installed and authenticated for the named person performing setup.

Before that point, provide the applicable manual guide only. After the `agt-*` account and its tooling exist, continue user-space provisioning from that account. Do not copy the bootstrap human's cached credentials into it.

## Interview once

Ask one concise consolidated set of unanswered questions. Obtain:

- Work or personal profile.
- Machine ID, OS, and intended role.
- Named humans, separate admins, shared agent account, operators, and viewers.
- Tailscale tailnet/tag policy, NoMachine license, and KVM choice.
- GitLab/GitHub host and desired non-human identity.
- Model-provider workload identities and billing owner.
- 1Password or Bitwarden choice.
- Optional organization tools, including `gws`, cloud CLIs, Kubernetes, IaC, databases, IDEs, VPN, endpoint software, proxy, and internal certificates.
- Backup destination, prohibited data, update window, and compliance requirements.

Treat every `ask` profile value as unresolved. Do not silently install it. Note that `gws` is actively developing and not an officially supported Google product.

Record the answers in an ignored `config/profiles/<machine>.local.toml`, or a reviewed non-secret `config/fleet/<machine>.toml` in an access-controlled private repository. Start from the closest Linux/macOS work/personal example and consult `docs/01b-profile-field-reference.md`. Run validation/plan, resolve every apply-time decision, obtain human review, approve, and run `validate --ready`. Never bypass validation or turn an unanswered choice into a default.

## Execute the applicable workflow

Read [linux-workflow.md](references/linux-workflow.md) for Ubuntu or [macos-workflow.md](references/macos-workflow.md) for macOS. Follow the phases in order:

1. Human-only OS, encryption, firmware, and recovery bootstrap.
2. Read-only preflight.
3. Base packages and security prerequisites.
4. Named, admin, agent, and optional service accounts.
5. Tailscale and KVM recovery; preview/apply enforceable SSH/UFW hardening; then NoMachine validation.
6. `agentctl` on Linux.
7. Shell, terminal, editor, runtime, container, browser, and test tooling.
8. `gh`, `glab`, and selected optional CLIs.
9. Codex, Claude Code, and Grok Build binaries.
10. Human-approved provider and source-control authentication.
11. Resource observation, balanced safeguards, and alerts.
12. Acceptance tests, burn-in, backup/restore, and handoff.

After each phase, run its validation before continuing. On failure, diagnose and stop at the current phase; do not stack later changes onto an unknown state.

## Handle source-control identities

Read repository document `docs/06-agent-and-source-control-identities.md` before configuring Git hosting.

- Prefer a scoped GitLab project/group service account.
- Prefer a repository-scoped GitHub App; use a machine user only when required.
- Install both `glab` and `gh` on every baseline machine.
- Validate with read-only operations first.
- Test branch push and a draft PR/MR in a disposable repository or branch.
- Require protected default branches, human review, and passing checks.
- Put the initiating human/session in the PR/MR description.

## Operate agentctl

Use `agentctl` only after its installer and authorization groups validate successfully.

- `agentctl shell TARGET` opens a fresh agent-account shell; `exit` returns.
- `agentctl attach TARGET SESSION` attaches interactively.
- `agentctl observe TARGET SESSION` attaches read-only.
- `agentctl detach` runs inside an attached tmux session and detaches only the current client. `Ctrl-b d` is equivalent.
- Detach never stops the underlying agents.
- `agentctl stop TARGET SESSION` asks the operator to type the session name; `--yes` is only for separately approved non-interactive maintenance.

## Finish with evidence

Run the profile-driven `scripts/fleetctl.py run PROFILE audit`, `scripts/resource-assessment.sh AGENT_ACCOUNT`, and the acceptance tests in `docs/08-validation-and-operations.md`.

Report:

- Completed, skipped, failed, and manually deferred phases.
- Exact installed versions.
- Accounts and authorization groups without secret material.
- Remote-access paths tested.
- Agent/source-control identities tested.
- Resource baseline and recommended burn-in thresholds.
- Reboot, backup, and restore results.
- Optional tools declined or pending.
- Remaining human approvals, risks, and rollback steps.

Do not declare completion while required validation is missing.

For fleet changes, follow `docs/12-fleet-rollout-and-change-management.md`. An agent may prepare local changes and draft text, but must ask before creating a remote, pushing, or opening a PR/MR. Never change repository visibility; hosted copies remain private unless the owner explicitly approves otherwise.
