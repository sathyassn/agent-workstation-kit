# Primary source register

[Documentation home](README.md) · [Topic ownership](README.md#one-authoritative-page-per-topic)

Recheck these before a fleet release because installers, supported OS versions,
pricing, authentication, and product maturity change. Provider identity and
Model Context Protocol (MCP)
claims in release candidate 0.2.0-rc.2 were last checked on 2026-08-25. The
Codex CLI installation and authentication path added in 0.2.0-rc.3 was checked
against the official OpenAI documentation on 2026-08-27.
The GL.iNet remote-KVM product names and guides used in release candidate
0.2.0-rc.4 were checked on 2026-08-28.

## Agents

- [OpenAI authentication](https://learn.chatgpt.com/docs/auth)
- [OpenAI device-code authentication](https://learn.chatgpt.com/docs/auth)
- [OpenAI Codex CLI](https://learn.chatgpt.com/docs/codex/cli)
- [OpenAI standalone Codex installer](https://chatgpt.com/codex/install.sh)
- [OpenAI API project service accounts](https://platform.openai.com/docs/api-reference/project-service-accounts)
- [OpenAI Codex authentication](https://developers.openai.com/codex/auth)
- [OpenAI Linux desktop preview](https://learn.chatgpt.com/docs/linux/linux-app)
- [Claude Code setup](https://docs.anthropic.com/en/docs/claude-code/getting-started)
- [Claude Code LLM gateway](https://docs.anthropic.com/en/docs/claude-code/llm-gateway)
- [Grok Build](https://docs.x.ai/build/overview)
- [Grok Build enterprise deployment](https://docs.x.ai/build/enterprise)

## Source control

- [GitLab service accounts](https://docs.gitlab.com/user/profile/service_accounts/)
- [GitLab CLI authentication and credential storage](https://docs.gitlab.com/cli/authentication/)
- [GitLab merge-request creation](https://docs.gitlab.com/user/project/merge_requests/creating_merge_requests/)
- [GitLab CLI](https://gitlab.com/gitlab-org/cli)
- [GitHub App permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app)
- [GitHub Apps](https://docs.github.com/en/apps/using-github-apps/about-using-github-apps)
- [GitHub credential types](https://docs.github.com/en/organizations/managing-programmatic-access-to-your-organization/github-credential-types)
- [GitHub CLI manual](https://cli.github.com/manual/)

## Collaboration systems

- [Atlassian service accounts](https://support.atlassian.com/user-management/docs/understand-service-accounts/)
- [Atlassian service-account API tokens](https://support.atlassian.com/user-management/docs/manage-api-tokens-for-service-accounts/)
- [Atlassian Rovo MCP setup](https://developer.atlassian.com/cloud/rovo-mcp/guides/getting-started/)
- [Atlassian Rovo MCP service-account authentication](https://developer.atlassian.com/cloud/rovo-mcp/guides/configuring-authentication-via-api-token/)

## Host tooling

- [Homebrew installation](https://docs.brew.sh/Installation)
- [Tailscale installation](https://tailscale.com/docs/install)
- [Tailscale on Windows](https://tailscale.com/docs/install/windows)
- [Tailscale on Linux](https://tailscale.com/docs/install/linux)
- [Tailscale access controls and grants](https://tailscale.com/docs/features/access-control)
- [Tailscale device tags](https://tailscale.com/docs/features/tags)
- [NoMachine supported operating systems and clients](https://www.nomachine.com/support/supported-operating-systems-and-supported-applications)
- [NoMachine Enterprise Desktop physical-desktop guide](https://knowledgebase.nomachine.com/DT11R00195)
- [NoMachine Enterprise Desktop platforms and features](https://www.nomachine.com/enterprise/enterprise-desktop-products/enterprise-desktop)
- [NoMachine trusted users and desktop sharing](https://kb.nomachine.com/DT04U00276)
- [Apple FileVault guide](https://support.apple.com/en-ca/guide/mac-help/mh11785/mac)
- [Apple Remote Login guide](https://support.apple.com/en-lamr/guide/mac-help/mchlp1066/mac)
- [Apple Screen Sharing guide](https://support.apple.com/en-au/guide/mac-help/-mh11848/mac)
- [Ubuntu Secure Boot and DKMS/MOK signing](https://documentation.ubuntu.com/security/docs/security-features/platform-protections/secure-boot/)
- [Minisforum MS-S1 Max Canadian product](https://ca.minisforum.com/products/minisforum-ms-s1-max-64gb)
- [Minisforum RTL8127 DKMS source](https://github.com/minisforum-repo/r8127-dkms)
- [GL.iNet Comet X (`GL-RM4PE`) product page](https://www.gl-inet.com/products/gl-rm4pe/)
- [GL.iNet Comet X (`GL-RM4PE`) console guide](https://docs.gl-inet.com/kvm/en/user_guide/gl-rm4pe/console_guide/)
- [GL.iNet Comet PoE (`GL-RM1PE`) product page](https://www.gl-inet.com/products/gl-rm1pe/)
- [GL.iNet Comet PoE (`GL-RM1PE`) user guide](https://docs.gl-inet.com/kvm/en/user_guide/gl-rm1pe/)
- [mise installation](https://mise.jdx.dev/installing-mise.html)
- [mise lockfiles](https://mise.jdx.dev/dev-tools/mise-lock.html)
- [Herdr installation](https://herdr.dev/docs/install/)
- [Herdr releases](https://github.com/herdrdev/herdr/releases)
- [Ghostty packages](https://ghostty.org/docs/install/binary)
- [Google Workspace CLI project](https://github.com/googleworkspace/cli)

## Source interpretation notes

- The Google Workspace CLI repository explicitly says `gws` is not an officially supported Google product and is pre-1.0/actively developing. Keep it optional.
- Ghostty officially distributes macOS binaries; Ubuntu packages are community-distributed. Require a separate supply-chain decision for Linux.
- The [ChatGPT Linux desktop app](https://learn.chatgpt.com/docs/linux/linux-app)
  is a preview on specified distributions and currently lacks Computer Use. Do
  not make it a prerequisite for Linux browser automation.
- Prefer current Tailscale grants for a new deployment rather than starting with legacy ACL syntax.
