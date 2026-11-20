@AGENTS.md

## Claude-specific notes

- In a sandboxed Claude Code session, commands that write outside the checkout,
  read `~/.claude`, or need `sudo` fail by design. Report the failure and hand the
  step over (section 6 of `AGENTS.md`); never disable the sandbox to get past it.
- Use the setup skill in `skills/setup-agent-workstation/` for discovery,
  sequencing and approvals; scripts make the deterministic changes.
