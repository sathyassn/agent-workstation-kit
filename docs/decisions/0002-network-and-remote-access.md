# Decision: Tailscale, NoMachine, and staged remote KVM

[Previous decision](0001-platform-and-nodes.md) · [Documentation home](../README.md) · [Next decision](0003-shared-agent-account.md)

- **Status:** accepted
- **Private network:** Tailscale with grants and tagged agent nodes. Runner-up: directly managed WireGuard.
- **Desktop:** NoMachine Enterprise Desktop for one shared physical `agent-NN` desktop. Runner-up: self-hosted RustDesk Pro.
- **Terminal:** OpenSSH over Tailscale.
- **Recovery:** defer the remote KVM during the physically supervised pilot,
  then use the current choice in the [selected workstation stack](../00a-final-stack.md#remote-desktop-versus-remote-kvm).
  A multi-port device controls only one target at a time and is a shared failure
  domain.
- **Boundary:** KVM is recovery/console access; it is not the normal remote-desktop experience and is not required for headed Playwright.
