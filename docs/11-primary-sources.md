# Primary source register

Recheck these before a fleet release because installers, supported OS versions, pricing, authentication, and product maturity change.

## Agents

- [OpenAI authentication](https://learn.chatgpt.com/docs/auth)
- [OpenAI API project service accounts](https://platform.openai.com/docs/api-reference/project-service-accounts)
- [OpenAI Codex authentication](https://developers.openai.com/codex/auth)
- [OpenAI Linux desktop preview](https://learn.chatgpt.com/docs/linux/linux-app)
- [Claude Code setup](https://docs.anthropic.com/en/docs/claude-code/getting-started)
- [Claude Code LLM gateway](https://docs.anthropic.com/en/docs/claude-code/llm-gateway)
- [Grok Build](https://docs.x.ai/build/overview)
- [Grok Build enterprise deployment](https://docs.x.ai/build/enterprise)

## Source control

- [GitLab service accounts](https://docs.gitlab.com/user/profile/service_accounts/)
- [GitLab merge-request creation](https://docs.gitlab.com/user/project/merge_requests/creating_merge_requests/)
- [GitLab CLI](https://gitlab.com/gitlab-org/cli)
- [GitHub App permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app)
- [GitHub Apps](https://docs.github.com/en/apps/using-github-apps/about-using-github-apps)
- [GitHub CLI manual](https://cli.github.com/manual/)

## Host tooling

- [Tailscale installation](https://tailscale.com/docs/install)
- [Tailscale access controls and grants](https://tailscale.com/docs/features/access-control)
- [Tailscale device tags](https://tailscale.com/docs/features/tags)
- [NoMachine Enterprise Desktop physical-desktop guide](https://knowledgebase.nomachine.com/DT11R00195)
- [NoMachine trusted users and desktop sharing](https://kb.nomachine.com/DT04U00276)
- [Ubuntu Secure Boot and DKMS/MOK signing](https://documentation.ubuntu.com/security/docs/security-features/platform-protections/secure-boot/)
- [Minisforum MS-S1 Max Canadian product](https://ca.minisforum.com/products/minisforum-ms-s1-max-64gb)
- [Minisforum RTL8127 DKMS source](https://github.com/minisforum-repo/r8127-dkms)
- [GL.iNet Comet X guide](https://docs.gl-inet.com/kvm/en/user_guide/gl-rm4pe/console_guide/)
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
