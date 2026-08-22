# Development tooling

## Baseline

- Git, Git LFS, GitHub CLI (`gh`), and GitLab CLI (`glab`).
- Zsh with a lightweight plugin manager, completion, autosuggestions, and syntax highlighting.
- Ghostty, Herdr, and tmux. `agentctl` uses tmux as its portable persistence layer; Herdr remains available as a user-facing terminal workspace.
- VS Code. Zed is optional rather than baseline.
- ChatGPT desktop app is optional on supported Ubuntu releases; Codex CLI is the
  dependable Linux baseline. The Linux desktop app remains preview software and
  does not currently provide Computer Use, so Playwright/browser harnesses remain
  the Linux UI-automation path.
- A version manager, initially `mise`, with per-project versions committed where appropriate.
- Node.js, npm, pnpm, Bun, Python, and project-specific runtimes through the version manager.
- Ubuntu's snap-packaged Chromium plus Xvfb on Linux; Google Chrome on macOS.
  Each project pins Playwright and installs its matching browser build.
- Rootless Podman with Docker CLI/Compose compatibility on Linux; OrbStack's Docker-compatible runtime on macOS, with Docker Desktop as fallback.

The installer resolves moving channels such as `lts` and `latest` to concrete
versions with `mise --pin` and excludes releases newer than seven days when the
backend supplies release dates. The installer copies reviewed `config/mise.toml`
into a previously empty global configuration and stops on conflicts. After the
pilot, record accepted versions and create/commit a `mise.lock` for the Linux
and macOS architectures in use.
The lockfile improves reproducibility and integrity where each backend provides
checksums; it does not make every npm dependency cryptographically equivalent.

On managed work nodes, disable supported background self-updaters for agent
CLIs and update them through the staged maintenance process. Personal nodes may
choose automatic updates, but should still retain a known-good rollback path.

## Optional catalogue

Optional tools are selected during the setup interview and installed only after confirmation. `gws` is never installed automatically. If selected, require a reviewed Google Cloud project, explicit OAuth scopes, credential storage in the chosen secrets manager, and dry-run/field-limited usage guidance for agents.

Install Codex, Claude Code, and Grok Build only from each vendor's current
documented distribution. Do not assume a package name or pipe an unreviewed
moving installer into a work shell. Herdr `0.8.2` is pinned through its official
mise registry entry; older mise releases must be updated rather than silently
falling back to an unpinned installer. Authentication remains a separate gate.

## Authentication

Package installation and authentication are different phases. Scripts install binaries. Humans or approved workload-identity procedures authenticate them. Validation prints authentication status without printing credentials.
