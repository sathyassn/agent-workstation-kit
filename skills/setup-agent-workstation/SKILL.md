---
name: setup-agent-workstation
description: Provision, validate, audit, maintain, or extend a dedicated Ubuntu or macOS workstation for long-lived Codex, Claude Code, Grok Build, browser testing, shared agent accounts, remote access, private fleet inventory, and secure source-control automation. Use for new machine setup, fleet reproduction, account/tool changes, resource safeguards, maintenance, drift checks, or MS-S1 Max onboarding with the agent-workstation-kit baseline.
---

# Set Up Agent Workstation

Use the repository's reviewed profiles, scripts, and guides. Orchestrate
discovery, choices, approvals, execution, and evidence. Do not improvise a
privileged change when a repository workflow covers it.

## Locate and trust the toolkit

When bundled under `skills/setup-agent-workstation`, use `../..` as the toolkit
root. If installed separately, ask for the `agent-workstation-kit` path. Verify
its status/version and stop on unexpected modifications until a human reviews
them. Locate the separate private fleet root when production inventory is used.

Read [approval boundaries](references/approval-boundaries.md) before any
mutating phase.

## Begin read-only

Run `scripts/preflight.sh` and, on Linux, `scripts/hardware-audit-linux.sh`.
Identify OS/version, firmware, Secure Boot/FileVault/LUKS, hardware, accounts,
network, desktop, package managers, installed tools, management controls, and
whether the context is work or personal. Treat hardware audit output as private.

An agent may orchestrate after the OS, first named/bootstrap account, working
network, toolkit checkout, and one authenticated agent CLI exist. Before then,
provide the manual OS checklist. After `agent-NN` exists, run its user-space
phases only in that account. Never copy bootstrap credentials into it.

## Collect inputs once

Ask one concise set of unresolved questions:

- Work/personal context, namespace, hostname/class, ownership, asset tag, hardware.
- Stable human handles, one assigned `admin-NN` per administrator, `agent-NN`,
  operators, viewers, recovery SSH users, and any `svc-purpose` accounts.
- Tailscale policy, NoMachine license/mode, KVM lifecycle/model, and recovery.
- GitLab/GitHub non-human identity and model-provider billing/auth owner.
- 1Password, Bitwarden, or organization vault.
- Optional `gws`, cloud, Kubernetes/IaC, database, IDE, VPN, endpoint, proxy,
  registry, certificate, and internal tooling.
- Backup, data restrictions, maintenance window, and compliance requirements.

Generate a draft with `fleetctl init`; do not hand-create a UUID. Record no
secrets. Keep experiments in ignored `*.local.toml`; keep production inventory
in a private fleet repository created from `templates/private-fleet`.

Run draft validation and plan. Resolve every `ask`, obtain review, set approved,
then run `validate --ready` and `validate-fleet.py`. Never turn an unanswered
choice into a default.

## Execute in order

Read [Ubuntu workflow](references/linux-workflow.md) or
[macOS workflow](references/macos-workflow.md), then perform one phase at a time:

1. Human OS, encryption, firmware, recovery, and initial administrator.
2. Read-only preflight and hardware acceptance.
3. Base packages and security prerequisites.
4. Named human, assigned admin, agent, and optional service accounts.
5. Tailscale, console/KVM recovery, SSH/firewall hardening, then NoMachine.
6. `agentctl` on Linux.
7. Zsh/Antidote, Ghostty, Herdr, tmux, VS Code, mise, runtimes, containers.
8. Chrome/Chromium, Xvfb, and project-pinned Playwright browsers.
9. `gh`, `glab`, selected optional CLIs, then Codex/Claude/Grok binaries.
10. Human-approved provider and source-control credential ceremonies.
11. Resource observation, measured safeguards, monitoring, and alerts.
12. Acceptance tests, kernel/reboot tests, burn-in, backup/restore, and handoff.

Validate after every phase. Stop at the first unknown or failed security,
recovery, driver, identity, or data-integrity condition.

## Preserve identity boundaries

Follow `docs/04-accounts-and-access.md` and
`docs/06-agent-and-source-control-identities.md`.

- Each human uses a named account; `admin-NN` is assigned to exactly one human.
- Shared processes and the shared desktop run as non-admin `agent-NN`.
- Use `agentctl shell` for a child agent shell; `exit` returns to the human shell.
- Use `agentctl detach` or `Ctrl-b d` inside tmux; detaching leaves work running.
- Install `gh` and `glab`. Test read-only operations before branch/PR/MR writes.
- Agent identities may author PRs/MRs but never approve or merge their own work.

## Finish with evidence

Run the profile audit, resource assessment, and relevant acceptance runbooks.
Report completed, skipped, failed, and deferred phases; versions; accounts and
role groups; remote paths; source/model identities; resource/load results;
driver/kernel/reboot state; backup/restore; and remaining approvals/risks.

Do not claim live certification when hardware or organization controls remain
untested. An agent may prepare local changes and draft review text, but must ask
before creating a remote, pushing, opening a PR/MR, deploying, or changing
repository visibility.
