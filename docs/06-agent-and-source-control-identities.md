# Agent and source-control identities

The Linux `agent-01` account is only an OS runtime. Provider and Git-hosting
identities are separate accounts with separate owners, scopes, billing, and
revocation. A name such as `agent-01@company.example` may be created where a
vendor requires a mailbox, but it must be organization-owned and governed like
any other workload identity.

## Model providers

| Tool | Shared work default | Personal single-operator option |
|---|---|---|
| Codex CLI | API project service account/key or approved workload federation | Sign in with ChatGPT or an API key |
| Claude Code | Anthropic API/enterprise cloud role/gateway | Vendor-permitted individual subscription login |
| Grok Build | Team API/workload identity | Vendor-permitted individual login |

Official OpenAI documentation states that local Codex clients support either
[ChatGPT subscription sign-in or API-key usage](https://learn.chatgpt.com/docs/auth).
These are different billing/admin paths. Do not assume API usage is included in
a ChatGPT subscription. OpenAI [project service accounts](https://platform.openai.com/docs/api-reference/project-service-accounts)
are non-human API identities; scope, budget, monitor, and rotate them per project.

Do not put one person's subscription session in a multi-operator work home.
Confirm each provider's current terms and organization policy. Store credentials
in 1Password, Bitwarden, an enterprise vault, OS keychain, or short-lived broker;
never in profiles, dotfiles, logs, command arguments, or this repository.

## GitLab service account

An organization owner creates a project/group service account for an explicit
purpose, such as `workstation-agent-dev`. Grant only required projects and the
minimum role—commonly Developer, subject to policy. Use an expiring token or
dedicated SSH key stored in the approved vault. Install and authenticate `glab`
through a hidden-input/local credential ceremony.

Yes, the service account can push a feature branch and open a merge request if
its repository and API scopes allow it:

```text
agent-01 process -- GitLab service account -- feature branch -- draft MR
                                                          |
                                      pipeline + independent human review
                                                          |
                                                human-approved merge
```

Protect default/release branches. The agent identity must not push directly to
them, approve its own MR, or merge it. Opening an MR remains an external action
requiring explicit human authorization.

## GitHub identity

Prefer an organization-owned GitHub App installed only on selected repositories.
Start with `Metadata: read`, `Contents: read/write`, and `Pull requests:
read/write`; add workflow permission only when explicitly required. Issue
short-lived installation tokens through a helper/broker. A GitHub App can author
branches and PRs within those permissions but cannot perform an interactive
`gh auth login` like a person.

Use a machine user only when an App cannot support the workflow. Give it its own
mailbox, enforced MFA, scoped repository access, and an expiring fine-grained
token. It may consume an organization seat. Authenticate `gh` locally without
exposing the token.

## Attribution and audit

Use a functional commit identity such as:

```text
user.name  = Agent Workstation 01
user.email = organization-approved bot or noreply address
```

Record the initiating human, host, and agent session in the PR/MR body or audit
metadata. Never falsify authorship. Test read-only access first, then a branch
push and draft PR/MR in a disposable repository before granting production use.
