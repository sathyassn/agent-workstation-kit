# Agent and provider identities

[Previous: tooling](05-tooling.md) · [Documentation home](README.md) · [Next: security and resources](07-security-and-resources.md)

The local `agent-01` Unix account is a runtime boundary. It is not a GitHub,
GitLab, Atlassian, or model-provider account. Keep these identity layers separate:

```text
human operator ── agentctl/desktop ── local agent-01 process
                                           |
                  +------------------------+------------------------+
                  |                        |                        |
             GitHub App             GitLab service acct      Atlassian service acct
             short token            scoped/expiring PAT      scoped API key
```

## Naming and scope

Use a stable logical identity per purpose and trust boundary:

```text
<namespace>-agent-<purpose>

ac-agent-dev          normal development automation
ac-agent-dev-pilot    isolated first-machine pilot
ac-agent-docs         documentation-only automation
```

Do not use the machine display name: it is intentionally mutable. Do not name
the external identity exactly after the host or UUID by default: replacing a
machine should not require replacing every provider identity. Put the initiating
human and technical hostname in PR/MR metadata and provider audit logs.

Create a host-specific identity such as `ac-agent-dev-ws001` only when a threat
model requires per-host revocation or blast-radius isolation. A cohort- or
environment-specific identity is usually the better balance. Provider naming
rules differ, so the exact provider names can differ while sharing one logical
purpose; for example, the logical `ac-agent-dev` can be the Atlassian service
account `acagentdev`, because Atlassian currently requires an alphanumeric 6–30
character service-account name.

The private machine profile records the approved provider principal names, but
never passwords, tokens, private keys, recovery codes, or secret-bearing URLs.
The validator blocks recognizable provider-token prefixes, and CI secret
scanning provides an additional gate; neither replaces operator discipline.
The implementation details in this chapter were checked against vendor primary
documentation on 2026-08-25; recheck the source register before each fleet release.

## Where administration happens

An existing organization owner/admin creates and grants each provider identity
from their own trusted administrative workstation. They do **not** need to sign
in as an administrator on the agent workstation.

```text
admin's trusted workstation                 target agent workstation
---------------------------                 ------------------------
create provider identity
grant selected repos/projects/spaces
create scoped, expiring credential
store credential in approved vault  -----> broker/inject credential to agent-01
                                             verify read-only access
                                             test disposable branch + draft PR/MR
                                             remove credential from process
```

Creation and authorization are provider-side administrative actions. Target-host
authentication is a separate credential ceremony performed only after the OS
account, keyring/vault client, and agent tooling exist. Never browse as an
organization admin from the shared `agent-01` desktop.

## GitLab service account

Prefer a group service account for a related project set, or a project service
account for one-project isolation. A group Owner or project Maintainer/Owner
creates it in the applicable **Settings > Service accounts** page, adds only the
required group/projects, and grants the minimum role—commonly Developer, subject
to policy. GitLab service accounts cannot use interactive UI login; they use a
service-account personal access token or an API-managed SSH key.

As checked on 2026-08-25, current GitLab documentation lists service accounts
for Free, Premium, and Ultimate, with edition/offering-specific limits and
version history. Confirm the deployed GitLab version, offering, and current
limits during planning; do not assume parity with GitLab.com.

For `glab` plus branch/MR work, create an expiring token with only the scopes the
tested workflow requires. Current `glab` documentation requires `api` and
`write_repository` for its PAT workflow. Store the token in the organization
vault. On the target, enter the `agent-01` desktop or `agentctl shell`, ensure the
Linux Secret Service keyring is unlocked, and use hidden input rather than a
command argument:

```bash
read -rsp 'GitLab service-account token: ' GLAB_TOKEN
printf '\n'
printf '%s' "$GLAB_TOKEN" | glab auth login --hostname gitlab.example.com --stdin
unset GLAB_TOKEN
glab auth status --hostname gitlab.example.com
```

Stop if `glab` warns that it will use plaintext credential storage. Verify
read-only access, then feature-branch push and a draft MR in a disposable
project. Protect default/release branches; the identity must not approve or
merge its own MR.

## GitHub App

GitHub does not offer a first-class service-account user equivalent to GitLab's.
Prefer an organization-owned GitHub App. An organization owner creates the App,
installs it only on selected repositories, and starts with:

- Metadata: read
- Contents: read/write
- Pull requests: read/write

Add Issues or Workflows permission only for a reviewed need. Keep the App private
key in a central vault or broker—not on every workstation. The broker should mint
a short-lived installation token and make it available only for the command that
needs it. `gh` accepts such a token through `GH_TOKEN`; do not run interactive
`gh auth login` for an App or persist its one-hour installation token.

```text
agent command -> approved broker -> one-hour installation token -> GH_TOKEN
                                                             |
                                                    gh / Git operation
```

Use a managed credential helper/broker for Git-over-HTTPS so the token does not
appear in a remote URL, argument list, profile, or shell history. Verify selected
repository visibility and create a disposable draft PR. If the App cannot support
the workflow, a centrally owned machine user with MFA and an expiring fine-grained
PAT is the fallback; authenticate it under `agent-01` using standard-input/keyring
storage and expect it to consume a seat according to the organization's plan.

An environment variable limits accidental persistence, but it is not an
isolation boundary: other processes running as the same Unix account may be able
to inspect process environments. For concurrent untrusted jobs, isolate the job
with a separate OS identity/container and broker the token at execution time.

## Atlassian Jira and Confluence

For a shared work agent, prefer an Atlassian organization service account. An
organization admin creates it in **Atlassian Administration > Directory >
Service accounts**, assigns only the required Jira/Confluence app access,
project roles, groups, and Confluence space permissions, then creates a scoped
API credential with an expiry. The service account cannot log in interactively
to the Atlassian UI. As checked on 2026-08-25, Atlassian documents this feature
for Guard Standard and Enterprise and requires an alphanumeric 6–30 character
service-account name; verify the current plan and naming rules before creation.

For the official Atlassian Rovo MCP server, ask the organization admin to enable
API-token authentication. Configure the shared agent client to call
`https://mcp.atlassian.com/v1/mcp` with the service-account API key as a Bearer
credential. Inject the header from the approved secret store at process launch;
do not put the literal key in a repository or project-level `mcp.json`. If a
particular client cannot safely resolve an indirect secret, stop and use a
managed connector/secret broker rather than embedding it.

OAuth 2.1 is the preferred interactive **named-human** path, but it is not an
acceptable shared credential inside a multi-operator `agent-01` home. Use it
only in a person's own client profile. Atlassian's service-account API-key path
is intended for non-interactive machine-to-machine use. Endpoint conventions
are client-sensitive: on 2026-08-25 Atlassian's getting-started guide used
`/v1/mcp/authv2` for managed interactive onboarding, while its OAuth bearer-token
example used `/v1/mcp`. Follow the current client-specific official guide and
test authentication instead of copying a stale endpoint.

There is no need to install an unreviewed third-party Jira CLI merely to obtain
Jira/Confluence access. Prefer the official Rovo MCP server or scoped REST API.
Test with a non-sensitive project/space: read first, then create/update a test
work item or page, and confirm delete/admin operations are absent unless
explicitly required.

## Model providers

| Tool | Shared work default | Personal single-operator option |
|---|---|---|
| Codex CLI | API project service account/key or approved workload federation | Sign in with ChatGPT or an API key |
| Claude Code | Anthropic API/enterprise cloud role/gateway | Vendor-permitted individual subscription login |
| Grok Build | Team API/workload identity | Vendor-permitted individual login |

Do not put one person's subscription session in a multi-operator work home.
Confirm each provider's current terms and organization policy. Store credentials
in 1Password, Bitwarden, an enterprise vault, OS keychain, or short-lived broker;
never in profiles, dotfiles, logs, command arguments, or this repository.

## Attribution and recertification

Use a functional commit identity approved by the organization. Record the
initiating human, technical hostname, agent session, and provider principal in
the PR/MR body or audit metadata. Never falsify authorship.

At least quarterly, and on staff/host/policy changes, review repository and
space membership, provider permissions, token age/last-use data, unused
credentials, and branch protections. Rotation must be tested before the old
credential is revoked.
