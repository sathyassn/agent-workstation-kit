# Network, remote access, and files

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

- Install Enterprise Desktop on a node that exposes one shared physical `agt-*` desktop.
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

Use one remotely recoverable KVM path per node unless a managed multi-port KVM has redundant power/network and clear port ownership. Validate video, keyboard, BIOS, reboot, power-control accessory, encryption unlock, firmware updates, account recovery, and MFA before relying on it.

## Headed browser tests

A remote KVM is not a Playwright dependency. For tests that must be watched,
start the browser from a terminal inside the active `agt-*` graphical session;
NoMachine then shows the same desktop. For unattended tests, use a supported
virtual display/compositor or Playwright's headless mode and retain traces,
screenshots, and video. A dummy HDMI/DisplayPort EDID adapter may help a
particular GPU expose a stable physical desktop, but it is hardware-specific
and must be burn-in tested. The KVM remains the out-of-band recovery path.
