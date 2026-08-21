# Architecture and operating model

## Goals

- Keep sustained agent, browser, build, and container load off operator laptops.
- Support long-lived Codex, Claude Code, and Grok Build sessions.
- Permit multiple named people to supervise one shared agent workspace.
- Preserve individual authentication while keeping the runtime non-administrative.
- Recover independently of the operating system through remote KVM.
- Use the same operating model on Linux and future macOS nodes where the OS permits it.

## Account layers

```text
Human identity       srao / jdoe
                           |
OS runtime identity  agt-ai-01
                           |
Model identity       OpenAI / Anthropic / xAI workload identity
                           |
SCM identity         GitLab service account / GitHub App
```

These identities are deliberately separate. Do not store a human's personal model or source-control session in the shared agent home.

## Access paths

### Primary: graphical workspace

```text
NoMachine authentication:  named human
Desktop owner:              agt-ai-01
Terminal/process owner:     agt-ai-01
```

NoMachine Enterprise Desktop is the initial product because all operators share one physical desktop. Use NoMachine Workstation only when a single Linux host must provide independent concurrent graphical desktops.

### Secondary: terminal workspace

```text
SSH as named human
        |
     agentctl
        |
shell or tmux session as agt-ai-01
```

### Recovery

Remote KVM is for power, firmware, boot, disk-unlock, display, and remote-access failure. It is not required for Playwright.

## Multi-node design

Prefer several reasonably sized nodes to one maximum-size server. Assign projects or workload classes to nodes, keep at least one recovery path independent of the node, and validate capacity using real burn-in measurements.
