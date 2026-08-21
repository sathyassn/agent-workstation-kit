# Profile field reference

Start from an example; do not write a profile from memory. The examples cover personal Linux, work Linux, and work macOS. Values below are illustrative and never credentials.

| Field | Allowed/example | Meaning |
|---|---|---|
| `schema_version` | `1` | Profile schema version. |
| `profile` | `personal`, `work` | Selects identity and management safeguards. |
| `state` | `draft`, `approved` | Apply requires reviewed `approved`. |
| `machine.id` | `ai-node-01` | Unique stable fleet ID; also the `agentctl` target. |
| `machine.platform` | `linux`, `macos` | Chooses the OS path. |
| `machine.os_family` | `ubuntu`, `macos` | Must match the platform. |
| `machine.role` | `shared-agent-workstation` | Inventory description. |
| `accounts.agent` | `agt-ai-01` | Shared non-admin execution account. |
| `accounts.humans` | `["alice", "bob"]` | Named daily-use OS accounts. |
| `accounts.admins` | `["adm-alice"]` | Separate privileged accounts. |
| `accounts.operators` | `["alice"]` | Humans allowed to control agent sessions. |
| `accounts.viewers` | `["bob"]` | Humans limited to status/read-only observe. |
| `accounts.ssh_users` | humans/admins; include an admin | Complete direct-SSH allowlist and recovery path. |
| `remote.tailscale_tailnet` | organization/domain label | Non-secret tailnet identifier. |
| `remote.tailscale_tags` | `["tag:agent-work"]` | Tags governed by tailnet grants. |
| `remote.nomachine_port` | `4000` | Reviewed TCP/UDP port, tailnet-only. |
| `remote.kvm` | `glinet-comet-rm1`, `none` | Inventory label; use `none` only with another tested console. |
| `remote.desktop_lock_mode` | `dedicated-shared`, `locked` | Explicit shared-desktop policy. |
| `tooling.install_agents` | `true` | Codex, Claude Code, and Grok Build are required. |
| `tooling.gws` | `install`, `skip` | Optional Google Workspace CLI decision. |
| `tooling.secrets_provider` | `1password`, `bitwarden`, `both`, `organization-vault` | Approved secret store; no secret is placed here. |
| `tooling.antidote_ref` | full commit SHA preferred; reviewed tag accepted | Pinned shell-plugin-manager revision. Resolve the tag to a commit during the pilot. |
| `source_control.*_host` | `gitlab.com`, `github.com` | Hostname only, without scheme/path. |
| `source_control.gitlab_identity` | `service-account`, `none` | Non-human GitLab identity decision. |
| `source_control.github_identity` | `app`, `machine-user`, `none` | GitHub automation identity decision. |
| `model_auth.codex/claude/grok` | `api-workload`, `enterprise-federated`, `named-human`, `none` | Provider billing/auth model. Shared work homes cannot use named-human auth. |
| `security.disk_encryption_required` | `true` | Mandatory baseline. |
| `security.remote_scope` | `tailscale-only` | No public SSH/desktop listener exposure. |
| `security.endpoint_management` | `mdm`, `edr`, `mdm-and-edr`, `not-required` | Work/personal endpoint decision. |
| `resources.policy` | `balanced` | Measured soft pressure plus emergency ceiling. |
| `backup.target` | `corporate-backup`, `encrypted-nas` | Non-secret destination label. |
| `backup.retention_days` | `30` | Positive retention target. |
| `maintenance.timezone` | `America/Toronto` | IANA timezone. |
| `maintenance.update_window` | `Sunday 02:00-04:00` | Human-readable approved window. |
| `maintenance.owner` | `platform-team`, `alice` | Accountable person/team label. |

`ask` is valid only while a profile is a draft. `validate --ready` rejects every unresolved apply-time field. The examples’ names, domains, tags, hardware labels, and windows are patterns to replace—not environmental facts.
