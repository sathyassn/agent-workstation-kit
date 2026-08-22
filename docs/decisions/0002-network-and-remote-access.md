# Decision: Tailscale, NoMachine, and staged remote KVM

- **Status:** accepted
- **Private network:** Tailscale with grants and tagged agent nodes. Runner-up: directly managed WireGuard.
- **Desktop:** NoMachine Enterprise Desktop for one shared physical `agent-NN` desktop. Runner-up: self-hosted RustDesk Pro.
- **Terminal:** OpenSSH over Tailscale.
- **Recovery:** defer KVM during the physically supervised pilot. Prefer Comet X (GL-RM4PE) for four co-located hosts, with Comet PoE (GL-RM1PE) as fallback/spare. Runner-up: JetKVM. A multi-port device controls only one target at a time and is a shared failure domain.
- **Boundary:** KVM is recovery/console access; it is not the normal RDP experience and is not required for headed Playwright.
