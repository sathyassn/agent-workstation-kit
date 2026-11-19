# Remote Mac acceptance recipe

[Documentation home](../README.md) · [macOS setup](../03-macos-setup.md) · [Resumable setup](resumable-setup.md)

Use for an Apple silicon Mac mini or Studio. An existing-host reference test
reached a Mac through Orca pairing and Apple Screen Sharing. That observation
does not qualify a fresh install, managed enrollment, sleep or encrypted reboot.
Record new results on the actual host and installed vendor versions.

## Choose roles and access paths

| Selection | Self-managed example | Organization-managed example |
|---|---|---|
| Host | `home-mac-001`; owner approves local settings | `acme-mac-001`; approved fleet profile and MDM policy |
| Viewer | Named operator on a separate device | Named operator on an approved managed client |
| Shell/files | Named-user SSH/SFTP | Allowlisted named user, approved keys or organization auth |
| Orca SSH worktree | Project runs as the named SSH user | Same boundary; authorize agent-account delegation separately |
| Optional Orca Desktop host | Intended graphical app owner and selected project | Approved app distribution, graphical account and privacy policy |
| Occasional Mac-to-Mac GUI | Named-user Apple Screen Sharing | Named-user Screen Sharing if policy permits; Remote Management for managed administration |
| Cross-platform GUI | Selected NoMachine mode | Approved NoMachine mode/license and attributable user access |

Choose each path independently. Remote Login enables SSH/SFTP; it does not
enable Screen Sharing or Orca hosting. A named login does not authorize copying
its credentials into `agent-NN`. Inventory host and viewer OS/build, app version,
app owner, active graphical session, selected addresses/ports and recovery owner
in private evidence. Validate the installed UI and vendor guide before offering
setting instructions or automation. Pairing links and invitations are credentials;
keep them out of profiles, screenshots, logs and checkpoint journals.

## Check transport before pairing

1. Confirm the viewer and host are present on the intended private network.
   A Tailscale peer/ping result proves presence, not service-port access.
2. On the host, confirm Orca is sharing from the intended graphical session.
   Inspect its sharing UI and actual listener, for example the read-only macOS
   `lsof -nP -iTCP -sTCP:LISTEN`. Match the process owner, listening interface,
   port and advertised endpoint. A loopback listener is not remotely reachable;
   a wildcard listener still needs approved firewall/network policy. Record
   permission-denied inspection as unknown, not absent.
3. From the actual viewer, probe the exact private address and selected port.
   On macOS, use `nc -vz -G 5 HOST_PRIVATE_ADDRESS ORCA_PORT` after replacing
   placeholders. On Ubuntu use `nc -vz -w 5 HOST_PRIVATE_ADDRESS ORCA_PORT`;
   on Windows use `Test-NetConnection -ComputerName HOST_PRIVATE_ADDRESS -Port ORCA_PORT`.
   Probe SSH 22 and Screen Sharing TCP 5900 separately if selected.
4. If LAN TCP succeeds but tailnet TCP fails, inspect the address/interface,
   destination listener, host firewall and both networks' access policy.
   Successful tailnet ping plus successful LAN TCP does not prove tailnet TCP.
   Stop before pairing until the intended service is reachable.
5. With approved policy and TCP reachability, let the human complete pairing.
   Verify the intended host/project/user, perform a harmless project operation,
   disconnect the viewer, and reconnect to the same host and worktree. Separately
   restart the host app with approval and verify reconnect and retained work.

For an optional cross-tailnet diagnostic, obtain both owners' approval, share
only the single selected host with `RECIPIENT_IDENTITY`, and review a grant for
only `REVIEWED_HOST` and `tcp:ORCA_PORT`. Have the named recipient accept through
the approved private channel. Check both networks' policy, device eligibility
and sharing restrictions in the current Tailscale guide; do not replace policy
with allow-all or invite the recipient into the entire tailnet as a workaround.
Set an expiry/revocation owner and remove temporary access through an approved
change. Keep all real identities, grants and sharing arrangements private.

## Verify named-user Apple Screen Sharing

For Mac-to-Mac control, a human reviews the host's current **System Settings →
General → Sharing** UI. Where policy permits, select Screen Sharing and **Only
these users**, then add the intended named user. Keep legacy shared VNC-password
access off. Screen Sharing and Remote Management are alternatives; an existing
managed Remote Management policy requires its administrator's decision.

After approval of the host setting and narrow private TCP 5900 access, confirm
the listener and viewer-to-host TCP connection. On the viewing Mac, open Screen
Sharing, connect to the approved private hostname/address, and authenticate as
the allowed named host user through the app's credential prompt. Verify which
desktop/session is being controlled; do not assume the login reaches the intended
agent desktop. Confirm control mode, then verify view-only behavior separately
where required. Check that an unapproved user is denied.

Test clipboard sharing with disposable non-secret text and drag-and-drop with a
disposable file only if selected and permitted. Record each result separately;
GUI control does not prove file transfer. Viewer settings can capture system
shortcuts and trackpad gestures, so test their local/remote destination before
operating destructive shortcuts. Clipboard and transfer features depend on the
selected mode, versions and policy.

## Reconnect, sleep and recovery outcomes

Record authenticated desktop access, optional file transfer, clipboard, viewer
reconnect, host-app restart, host reboot and sleep/wake as separate outcomes.
Test the approved sleep policy and lock/unlock state without changing managed
power settings implicitly. Repeat named-user SSH/SFTP, Orca and GUI checks after
an approved reboot and FileVault unlock. A running macOS remote service does not
establish preboot access: separately prove a FileVault-authorized human can
unlock via the tested local console or independent recovery path. Never store
recovery material in evidence. Keep recovery available until all selected paths
work again.

Use the `listener`, `tcp`, `orca`, `desktop`, `transfer`, `sleep` and `reboot`
checkpoints in the resumable plan. A failed or deferred result remains pending.

Sources checked 2026-11-19: [Apple Screen Sharing settings](https://support.apple.com/en-ie/guide/mac-help/mh11848/mac/1000),
[Apple Screen Sharing client](https://support.apple.com/en-sg/guide/mac-help/mh14066/mac),
[Tailscale device sharing](https://tailscale.com/docs/features/sharing).

## Executable transport checks

`scripts/verify-remote-access.py` now performs the repeatable TCP, named-user SSH,
SFTP hash roundtrip and independent SSH reconnect tests after a default preview.
It also has a separate host-local listener inspection mode. Use the commands in
the [automated setup runbook](resumable-setup.md#automate-access-and-workload-checks).
These results do not substitute for Orca pairing/app reconnect, Screen Sharing
control, sleep/wake or FileVault preboot evidence.
