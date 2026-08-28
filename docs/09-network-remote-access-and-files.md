# Network, remote access, and files

[Previous: validation](08-validation-and-operations.md) · [Documentation home](README.md) · [Next: responsibility matrix](10-human-script-agent-matrix.md)

## Tailscale

Install from the vendor-supported package or installer. Authenticate the node as a tagged non-human device and use Tailscale grants for new policy. Do not leave the default allow-all policy in a work tailnet.

Suggested policy intent:

```text
approved developers --> agent nodes: SSH and NoMachine
approved admins     --> agent nodes: administrative SSH
approved recovery   --> KVM devices: HTTPS only
agent nodes         --> required source/model/package endpoints
agent nodes         -X-> operator laptops unless explicitly required
```

Keep KVM devices in a separate tag/group and, where possible, a separate LAN/VLAN. Require MFA and restrict management access to administrators.

## NoMachine

- Install Enterprise Desktop on a node that exposes one shared physical `agent-NN` desktop.
- Do not expose its port publicly; connect over Tailscale.
- Register named humans as trusted only for the intended physical desktop.
- Use interactive control for the active operator and view-only for observers.
- Test clipboard, approved file transfer, screen blanking, locking, audio, multiple clients, mobile clients, and unattended reconnect.
- Upgrade to Workstation only when independent Linux virtual desktops are required.

## Files

- Source code moves through Git whenever possible.
- Use Taildrop or SFTP for occasional files.
- Use SMB over Tailscale only for an explicitly shared working directory, not entire home directories.
- Use Syncthing only for selected non-secret folders with conflict handling understood.
- Do not synchronize model credentials, keychains, browser profiles, `.ssh`, or entire agent homes between nodes.

## Remote KVM

During a supervised pilot, local monitor and keyboard are sufficient. Before
unattended office placement, use a tested remote KVM:

- Prefer Comet X (GL-RM4PE) for up to four co-located hosts when its firmware
  and availability meet policy.
- Keep a Comet PoE (GL-RM1PE) as a single-host fallback or spare.
- Remember that Comet X controls one host at a time and is a shared failure
  domain.
- Put the KVM on Tailscale and a restricted management segment. Disable public
  forwarding, use unique credentials and MFA where supported, and maintain it.
- Test WOL and AC recovery before buying power actuators. Do not assume the
  MS-S1 cascade header is ATX-board compatible.

## Headed browser tests

A remote KVM is not a Playwright dependency. For tests that must be watched,
start the browser from a terminal inside the active `agent-NN` graphical session;
NoMachine then shows the same desktop. For unattended tests, use a supported
virtual display/compositor or Playwright's headless mode and retain traces,
screenshots, and video. A dummy HDMI/DisplayPort EDID adapter may help a
particular GPU expose a stable physical desktop, but it is hardware-specific
and must be burn-in tested. The KVM remains the out-of-band recovery path.
