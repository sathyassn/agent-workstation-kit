# Profile field reference

[Previous: profile onboarding](01a-onboarding-profile.md) · [Documentation home](README.md) · [Next: Linux setup](02-linux-setup.md)

Start with `fleetctl init` or an example; do not invent a profile from memory.

| Field | Allowed/example | Purpose | Evidence |
|---|---|---|---|
| `schema_version` | `3` | Current profile contract. | Validator |
| `profile`, `deployment.context` | `personal`, `work` | Must match. | Validator |
| `deployment.namespace` | `acme`, `lab`, `home` | Operator-chosen 2–8 lowercase letters/digits; stored in private TOML and used as the hostname/tag prefix. | Validator/fleet uniqueness |
| `deployment.ownership` | `individual`, `organization` | Work must be organization-owned. | Validator + owner attestation |
| `state` | `draft`, `approved` | Apply requires reviewed `approved`. | Validator + review record |
| `machine.hostname` | `acme-ws-001` | Globally managed `<namespace>-<class>-<NNN>`. | Validator/live host/fleet |
| `machine.display_name` | `Atlas North` | Fleet-unique, case-insensitive ASCII human label; trimmed with no repeated spaces; mutable, never an access-control key. | Validator/local identity/live OS name |
| `machine.uuid` | UUIDv4 | Generated once; immutable across rebuilds. | Validator/fleet |
| `machine.asset_tag` | organization label | Unique private inventory reference. | Fleet + physical inventory |
| `machine.platform` | `linux`, `macos` | Selects OS workflow. | Validator/live host |
| `machine.os_family` | `ubuntu`, `macos` | Must match the selected platform. | Validator/live host |
| `machine.hardware_profile` | `minisforum-ms-s1-max-64gb` | Hardware runbook selector. | Manual inventory |
| `machine.role` | `agent-workstation` | Inventory classification copied into the local identity record. | Validator/local identity |
| `accounts.agent` | `agent-01` | Shared non-admin runtime account. | Live audit |
| `accounts.humans` | `alice`, `bob` | Stable organization/IdP handles. | Live audit + IdP record |
| `accounts.admins` | `admin-01` | Separate privileged accounts. | Live audit + assignment record |
| `accounts.admin_assignments` | `admin-01 = alice` | One named owner per admin account. | Validator + manual attestation |
| `accounts.services` | `svc-purpose` | Optional purpose-specific service accounts. | Live audit/manual purpose record |
| `accounts.operators/viewers` | declared humans | Control or read-only agent-session roles. | Live audit + end-to-end test |
| `accounts.ssh_users` | humans/admins; include an admin | Complete direct-SSH allowlist. | Live `sshd` audit |
| `remote.tailscale_*` | tailnet + `tag:*` | Private network identity/policy. | Manual admin-console evidence |
| `remote.nomachine_port` | `4000` | TCP/UDP port allowed only on the approved private interface. | Validator/live firewall |
| `remote.kvm` | `deferred`, `installed`, `not-required` | Lifecycle state; supervised pilots may defer. | Manual recovery test |
| `remote.preferred_kvm` | `glinet-comet-x-gl-rm4pe` | Four-host target when mature/available. | Inventory only until installed |
| `remote.fallback_kvm` | `glinet-comet-poe-gl-rm1pe` | Per-host fallback/spare. | Inventory only until installed |
| `remote.desktop_lock_mode` | `ask`, `dedicated-shared`, `locked` | Explicit shared desktop choice; `ask` is draft-only. | Manual NoMachine session test |
| `tooling.install_agents` | `true` | Requires the Codex, Claude Code, and Grok Build toolchain. | Validator/live commands |
| `tooling.gws` | `ask`, `install`, `skip` | Optional, asked during onboarding; `ask` is draft-only. | Live command/manual decision |
| `tooling.secrets_provider` | `ask`, `1password`, `bitwarden`, `both`, `organization-vault` | Provider name only; `ask` is draft-only. | Manual vault evidence; never credentials |
| `tooling.antidote_ref` | `ask`, reviewed SHA/tag | Pinned shell plugin manager revision; `ask` is draft-only. | Lock/config inspection |
| `source_control.gitlab_host` | hostname, for example `gitlab.com` | GitLab host without a URL scheme or path. | Validator/provider inventory |
| `source_control.gitlab_identity` | `service-account`, `none` | GitLab workload identity. | Manual provider/API evidence |
| `source_control.gitlab_principal` | `acme-agent-dev`, `none` | Approved provider-side name; no credential. | Provider inventory/manual evidence |
| `source_control.github_host` | hostname, for example `github.com` | GitHub host without a URL scheme or path. | Validator/provider inventory |
| `source_control.github_identity` | `app`, `machine-user`, `none` | GitHub workload identity. | Manual provider/API evidence |
| `source_control.github_principal` | `acme-agent-dev`, `none` | Approved App or fallback machine-user name. | Provider inventory/manual evidence |
| `collaboration.atlassian_site` | `ask`, `company.atlassian.net`, `none` | Atlassian Cloud site hostname; no URL or credential. | Provider inventory/manual evidence |
| `collaboration.atlassian_identity` | `ask`, `service-account`, `named-human`, `none` | `ask` is draft-only; shared work agents cannot use a human identity. | Provider inventory/manual evidence |
| `collaboration.atlassian_principal` | `ask`, `acmeagentdev`, `alice@example.com`, `none` | Atlassian service-account name (6–30 alphanumeric) or approved named-human label/email. | Provider inventory/manual evidence |
| `collaboration.atlassian_mcp_auth` | `ask`, `service-account-api-key`, `oauth-2.1`, `none` | `ask` is draft-only; non-interactive versus interactive MCP path. | Manual MCP authentication test |
| `model_auth.*` | `api-workload`, `enterprise-federated`, `named-human`, `none` | Shared work homes cannot use named-human auth. | Manual provider evidence |
| `security.*_required` | `true` | Disk encryption and Secure Boot baseline. | Live audit + recovery test |
| `security.remote_scope` | `tailscale-only` | Direct remote access is limited to the tailnet. | Validator/live firewall and SSH audit |
| `security.endpoint_management` | `ask`, `mdm`, `edr`, `mdm-and-edr`, `not-required` | Final mobile-device-management (MDM) and endpoint-detection-and-response (EDR) state; `ask` is draft-only. | Manual MDM/EDR evidence |
| `resources.policy` | `measured-balanced` | Soft pressure plus emergency ceiling. | Live audit/load test |
| `resources.os_memory_reserve_gib` | `8` | Headroom used to calculate agent thresholds. | Live policy/load test |
| `resources.os_cpu_reserve_threads` | `2` | Capacity-planning headroom; not hard core pinning. | Capacity record; not enforced isolation |
| `backup.target` | `ask`, approved target | Non-secret backup destination label; `ask` is draft-only. | Manual backup/restore evidence |
| `backup.retention_days` | positive integer, for example `30` | Required retention period. | Validator/backup policy |
| `maintenance.timezone` | IANA zone, for example `America/Toronto` | Interprets the maintenance window. | Validator/live schedule |
| `maintenance.update_window` | `ask`, approved window | Fleet maintenance schedule; `ask` is draft-only. | Manual operations record |
| `maintenance.owner` | `ask`, approved owner label | Accountable maintenance owner; `ask` is draft-only. | Manual operations record |

`ask` is valid only in a draft and only for designated human decisions.
`validate --ready` rejects unresolved apply-time values. Examples are fictional
patterns, not environment facts.

Principal fields are labels only. The validator rejects known GitHub, GitLab,
Atlassian, OpenAI/Anthropic, and xAI token prefixes, but that is defense in depth—not a complete secret
detector. Never paste a credential into a profile; CI secret scanning remains a
separate required control.

Evidence has three classes: validator/live checks, manual evidence captured from
the owning service or recovery test, and inventory-only intent. A green profile
validation proves schema and policy—not that manual desired state exists. The
`audit` phase prints explicit `MANUAL` reminders for fields it cannot prove.
