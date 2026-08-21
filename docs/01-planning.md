# Planning worksheet

Complete this before running a mutating script, then record the decisions in the validated [onboarding profile](01a-onboarding-profile.md). The worksheet explains the decisions; the TOML profile is the machine-readable source used for planning, apply gates, and later audit.

## Required decisions

- Deployment profile: work or personal.
- Machine identifier and physical asset owner.
- Ubuntu or macOS version supported by required applications.
- Agent account name, normally `agt-ai-NN`.
- Named human accounts and separate administrator accounts.
- Agent operators and view-only users.
- Tailscale tailnet, ACL tags, device approval, and exit-node policy.
- NoMachine license and trusted-user list.
- Remote KVM model, network placement, MFA, and recovery owner.
- Disk encryption and reboot-unlock process.
- GitLab, GitHub, model-provider, and secrets-provider ownership.
- Backup targets, retention, restore owner, and prohibited data.
- Corporate endpoint, VPN, proxy, certificate, and compliance requirements.

## Optional-tool interview

The setup agent must ask before installing organization-specific tools. Present one consolidated checklist, including:

- Google Workspace CLI (`gws`). It is actively developing and explicitly not an officially supported Google product.
- Google Cloud CLI, AWS CLI, Azure CLI, Kubernetes tools, Terraform/OpenTofu.
- 1Password CLI or Bitwarden CLI.
- Company VPN or endpoint-security software.
- Internal certificate authorities, package registries, proxies, and artifact stores.
- JetBrains tools, Zed, mobile toolchains, databases, and local model runtimes.

Record the answer in an ignored `config/profiles/*.local.toml` file. An `ask` value must never silently become `install` or an approved provider.

## Hardware gate

Before purchase, recheck Ontario availability, warranty, memory configuration, networking, storage endurance, Linux compatibility, and return policy. Pilot one machine under realistic load before deciding the RAM size and count for later nodes.
