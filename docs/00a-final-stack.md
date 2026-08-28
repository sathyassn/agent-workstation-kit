# Final workstation stack

[Previous: architecture](00-architecture.md) · [Documentation home](README.md) · [Next: planning](01-planning.md)

These are defaults, not silent installers. Recheck price, license, OS support,
and organization policy at purchase time.

| Need | Recommended | Runner-up | Why |
|---|---|---|---|
| Linux OS | Ubuntu Desktop LTS | Ubuntu LTS + separately reviewed desktop | Broad vendor/tool support; MS-S1 driver runbook is Ubuntu-specific. |
| Reproducibility | TOML profiles + Python/std-library scripts + mise | Nix/Home Manager later | Predictable bootstrap without making Nix expertise a day-one dependency. |
| Private network | Tailscale grants/tags | Managed WireGuard | Identity-aware private path across macOS, Windows, Linux and mobile. |
| Daily remote desktop | NoMachine Enterprise Desktop | Self-hosted RustDesk Pro | One physical shared desktop with macOS, Windows, Linux and mobile clients. |
| Paid SaaS alternative | Splashtop Remote Access Pro | TeamViewer business | Polished team/mobile UX; introduces vendor cloud and per-user cost. |
| Terminal/session | OpenSSH + `agentctl` + tmux | Eternal Terminal/Mosh where policy allows | Auditable human entry and durable agent sessions. Herdr remains a user-facing workspace. |
| Files | Git + Taildrop/SFTP | scoped Syncthing or SMB over Tailscale | Avoids synchronizing credentials or entire homes. |
| Recovery | Comet X for four hosts, later | Comet PoE RM1PE per host; JetKVM alternative | BIOS/boot recovery; separate from daily desktop. |
| Shell | Zsh + Antidote | Oh My Zsh | Antidote is a small plugin manager; OMZ is a larger framework/theme bundle. |
| Terminal/editor | Ghostty + VS Code | platform terminal; Zed optional | Same core editor on Linux/macOS; no need to mandate Zed. |
| Runtime versions | mise | asdf | Fast multi-runtime manager with project config/lock support. |
| Containers | rootless Podman on Linux; OrbStack on macOS | Docker Engine/Desktop | Avoids Docker-group root equivalence on Linux; OrbStack gives lightweight Docker compatibility on Mac. |
| Secrets | organization vault / 1Password | Bitwarden | Credential ceremony remains human-controlled. |

## NoMachine edition decision

Use **Enterprise Desktop**, not Workstation, for this design. Enterprise Desktop
shares the machine's existing physical desktop and is available for Linux and
macOS. Its listed annual price was USD 44.50 when checked on 2026-08-22.

NoMachine Workstation is Linux-only terminal-server software. “Up to four
virtual Linux desktops” means the same server can create four independent GUI
sessions, each with its own desktop state and processes; users are not merely
viewing the one physical console. Its listed annual price was USD 124.50. This
is useful for four separate desktop environments, but it adds memory/process
competition and is unnecessary when everyone intentionally manages one
`agent-01` workspace. Prices: [NoMachine products](https://store.nomachine.com/products/).

NoMachine clients are available on desktop and mobile platforms. Mobile access
does not require Workstation; the server edition determines host capability,
not whether an iPad can connect.

RustDesk Pro is a credible runner-up when open-source/self-hosted control and a
central address book/2FA/audit/ACL system matter more than NoMachine's physical
desktop model. Its Individual self-hosted plan was listed at USD 11.88/month and
Basic at USD 23.88/month when checked; validate concurrent shared-session and
Linux display behavior in a pilot. [RustDesk pricing](https://rustdesk.com/pricing/).

## Remote desktop versus KVM

```text
NoMachine: OS is healthy --> high-quality daily GUI, clipboard, files, audio
KVM:       OS may be down --> firmware, boot menu, encryption unlock, recovery
```

Keep both eventually. Do not buy RM1 only as a temporary test while the machine
is physically beside you. Buy RM1PE now only if office/unattended placement is
imminent or it will remain a useful spare. Comet X can connect four hosts but
controls one at a time; protect it as a high-privilege shared device.
