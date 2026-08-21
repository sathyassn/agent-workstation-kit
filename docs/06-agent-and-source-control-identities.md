# Agent and source-control identities

## Model-provider rule

Shared `agt-*` homes use workload or organization-approved identities, not cached personal subscriptions.

| Tool | Preferred shared authentication | Human gate |
|---|---|---|
| Codex CLI | OpenAI API project service account or other organization-approved API identity | API-project owner creates, scopes, budgets, and rotates identity |
| Claude Code | Anthropic Console/API, Bedrock, Vertex, or approved gateway | Admin creates key/role and budget |
| Grok Build | Team API key, enterprise OIDC, or external auth provider | Admin selects team and policy |

Store credentials in 1Password, Bitwarden, an enterprise vault, OS keychain, or a short-lived credential helper. Never write them into shell startup files, repository profiles, command history, or process arguments.

ChatGPT, Claude, and similar user subscriptions are assigned to people and
must not be turned into a shared login for `agt-*`. A named person may use an
interactive subscription login in their own OS home when the vendor terms and
organization policy allow it. A shared, unattended, or multi-operator agent
home should use an approved API/workload identity; its usage is metered and is
not assumed to be included in a person's desktop subscription. Confirm the
current provider plan and terms before deployment.

## GitLab service account

GitLab service accounts are non-human, non-seat accounts that can perform Git operations and API actions. Use the narrowest project or group scope available.

1. A GitLab Owner/Maintainer/admin creates a project or group service account.
2. Name it by purpose, for example `agent-ai-01-dev` rather than by a person.
3. Add it only to required projects with the minimum role capable of pushing branches and creating merge requests. Usually this is Developer; confirm project policy.
4. Create an expiring personal access token for the service account with only required scopes. Prefer `write_repository` plus the narrow API scope required by `glab`; do not grant admin scope.
5. If SSH is used, add a dedicated service-account SSH key through the GitLab API and keep the private key in the secrets provider.
6. Authenticate `glab` without exposing the token in history. Validate with read-only commands first.
7. Protect default and release branches so the service account cannot push directly.
8. Require human review and pipeline success. The service account may create an MR but may not approve or merge its own MR.

With explicit authorization for the external action, an agent can create a branch/commit, push, and run `glab mr create` when the service account has sufficient repository/API permission. Preparing a local branch does not authorize pushing or opening an MR.

## GitHub identity

Prefer a GitHub App for stable non-human automation:

1. Register an organization-owned private GitHub App.
2. Grant access only to selected repositories.
3. Start with repository `Contents: read/write`, `Pull requests: read/write`, and `Metadata: read`. Add `Workflows` only if editing workflow files is explicitly required.
4. Protect default/release branches and require human review/status checks.
5. Generate short-lived installation tokens through a credential helper or broker rather than storing the App private key in the agent home.
6. Use HTTPS Git with the installation token and GitHub API/`gh api` for PR operations.

Use a machine user only when a GitHub App cannot support the required interactive workflow. The machine user needs its own email, enforced 2FA, organization membership, scoped repository access, and expiring fine-grained token. It consumes a seat where the applicable plan requires one.

An authorized GitHub identity can create branches and PRs after the responsible human authorizes that external action. It must not approve or merge its own PR. For App-based operation, a small token helper may set `GH_TOKEN` only for the child process; for a machine user, `gh auth login` or an approved credential helper can be used.

## Commit attribution

Use a functional Git identity such as:

```text
user.name  = Agent Workspace ai-01
user.email = organization-approved bot or noreply address
```

Record the initiating human in the PR/MR body and agent session metadata rather than falsifying the commit author. Consider signed commits when the provider and key-management design support non-human signing safely.
