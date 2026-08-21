# Decision: Tailscale, NoMachine, and per-node KVM

- **Status:** accepted
- **Private network:** Tailscale with grants and tagged agent nodes. Runner-up: directly managed WireGuard.
- **Desktop:** NoMachine Enterprise Desktop for one shared physical `agt-*` desktop. Runner-up: self-hosted RustDesk Pro.
- **Terminal:** OpenSSH over Tailscale.
- **Recovery:** GL.iNet Comet-class KVM per node. Runner-up: JetKVM. A managed multi-port KVM is acceptable only with clear isolation and no unacceptable single point of failure.
- **Boundary:** KVM is recovery/console access; it is not the normal RDP experience and is not required for headed Playwright.
