# Network, remote access, and files

[Previous: validation](08-validation-and-operations.md) · [Documentation home](README.md) · [Next: responsibility matrix](10-human-script-agent-matrix.md)

The operator does not need a Mac. The baseline works from macOS, Windows and
Linux workstations, with iPadOS/iOS/Android suitable for monitoring and
approval. Out-of-band recovery requires a deployed, tested KVM.

## Connection model

```text
operator device                      target Linux or Mac
---------------                      -------------------
personal Tailscale identity -------> tagged Tailscale node
        |
        +-- NoMachine client ------> shared agent-NN desktop
        +-- SSH/SFTP client -------> named-human shell/files
        +-- web browser -----------> restricted remote KVM

Every human has an individual identity. No one shares a Tailscale, human or
administrator password merely to control the shared agent desktop.
```

## Operator client matrix

| Operator device | Private path | Graphical desktop | Shell/files | Practical role |
|---|---|---|---|---|
| macOS | Tailscale | NoMachine; Apple Screen Sharing for Mac targets | built-in SSH/SFTP | Full operation |
| Windows | Tailscale | NoMachine | Windows OpenSSH/SFTP client | Full operation |
| Linux | Tailscale | NoMachine | OpenSSH/SFTP | Full operation |
| iPadOS/iOS | Tailscale app | NoMachine app | approved SSH/SFTP app | Monitor and approve; KVM-dependent recovery |
| Android | Tailscale app | NoMachine app | approved SSH/SFTP app | Monitor and approve; KVM-dependent recovery |

Install the operator clients only from vendor-supported stores/packages or the
organization's software catalog. On managed devices, endpoint policy may limit
clipboard, file transfer, screen recording or background VPN operation.

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

- Install Enterprise Desktop on a node that exposes one shared physical
  `agent-NN` desktop. NoMachine documents Enterprise Desktop server packages
  for Windows, macOS, Linux and ARM; verify the supported release before use.
- Do not expose its port publicly; connect over Tailscale.
- Install the NoMachine client on each authorized macOS, Windows, Linux, iPadOS,
  iOS or Android operator device.
- Each human authenticates with an individually attributable target account;
  authorize named users as trusted only for the intended physical desktop.
- Use interactive control for the active operator and view-only for observers.
- Test clipboard, approved file transfer, screen blanking, locking, audio, multiple clients, mobile clients, and unattended reconnect.
- Upgrade to Workstation only when independent Linux virtual desktops are required.

For a Mac target, Apple's Screen Sharing is a useful Mac-to-Mac alternative.
Although macOS can expose VNC compatibility, the uniform Windows/Linux baseline
remains NoMachine; do not enable a separate VNC password merely to avoid the
supported client. Screen Sharing and Remote Management cannot both be enabled.

## SSH

- Enable SSH/Remote Login only after Tailscale access and console recovery work.
- Allow only the profile's named human and assigned administrator accounts.
- Use keys or the approved organization authentication method; disable public
  exposure and direct `agent-NN` SSH.
- Windows and Linux operators use the same `ssh user@tailscale-name` flow as a
  Mac operator. The terminal application differs; the target identity does not.

## Files

- Source code moves through Git whenever possible.
- Use Taildrop or SFTP for occasional files.
- Use SMB over Tailscale only for an explicitly shared working directory, not entire home directories.
- Use Syncthing only for selected non-secret folders with conflict handling understood.
- Do not synchronize model credentials, keychains, browser profiles, `.ssh`, or entire agent homes between nodes.

Git and SFTP are the portable baseline across operator operating systems.
Taildrop is convenient where approved, but it does not replace version control,
backup or a reviewed shared-directory policy.

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
