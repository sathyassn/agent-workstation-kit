# Minisforum MS-S1 Max acceptance runbook

[Documentation home](../README.md) · [Day-zero startup](../runbooks/day-zero-linux.md) · [First-pilot evidence](../runbooks/first-linux-pilot.md)

This is the first Linux hardware profile, not a universal recommendation. The
64 GB model has soldered LPDDR5X unified memory, so capacity cannot be upgraded.
Start with two realistic heavy sessions, measure, then increase concurrency.
Add 128 GB nodes later for high-memory workloads rather than assuming every
workload fits the 64 GB pilot.

## Contents

1. [Known hardware facts](#known-hardware-facts)
2. [Unbox and record](#unbox-and-record-human)
3. [Ubuntu and 10GbE](#ubuntu-and-10gbe)
4. [Acceptance gates](#acceptance-gates)

## Known hardware facts

- Ryzen AI Max+ 395: 16 cores/32 threads.
- 64 GB unified LPDDR5X-8000; soldered on the 64 GB model.
- Two Realtek RTL8127 10GbE ports; Wi-Fi 7 provides bootstrap connectivity.
- Six heatpipes, dual turbine fans, 130 W sustained/160 W peak profile.
- Two M.2 slots and a physical x16/electrical PCIe 4.0 x4 expansion slot.
- Internal 320 W PSU and 2U/rack-oriented mounting.

Recheck the [Canadian product page](https://ca.minisforum.com/products/minisforum-ms-s1-max-64gb)
before purchase; price, inventory, firmware, and bundled components change.

## Unbox and record (human)

1. Photograph packaging condition and record purchase/warranty details privately.
2. Record asset tag, chassis/board serials, BIOS version, RAM, SSD SMART identity,
   and both NIC MAC addresses in the private fleet repository or asset system.
3. Update firmware only from a verified Minisforum source with power protected.
4. Enable Secure Boot, TPM, virtualization, IOMMU, and restart-after-power-loss.
5. Do not enable unattended power control until Wake-on-LAN and AC recovery are tested.

Run `scripts/hardware-audit-linux.sh` after Ubuntu installation. Its report
contains serial numbers and must not be committed to the public toolkit.

## Ubuntu and 10GbE

Install Ubuntu 24.04.4 LTS using Wi-Fi, a USB Ethernet adapter, or local media.
The RTL8127 may require Minisforum's out-of-tree DKMS driver. Preview:

```bash
./scripts/install-ms-s1-r8127-dkms.sh
```

The helper pins release `11.015.00-1` to commit
`0b82eab2c29596aa5479690362544d8ce4d61d55` and verifies the downloaded archive
with SHA-256 `6f0baecb54ff88ddfd225423ce2f5a365f0755336288810e67a3b6b88dff261c`.
The immutable commit URL and digest were downloaded and calculated locally on
2026-08-22; changing either value requires a fresh source review and checksum.
Recheck that pin before a later rollout. After explicit
approval, run it with `sudo ... --apply` from an alternate working connection.

Keep Secure Boot enabled. Ubuntu can sign DKMS modules with a machine-specific
MOK. The script explicitly tests whether that key is enrolled and stops with an
interactive `mokutil --import`/console-reboot gate if it is not. The setup agent
must pause.
Do not work around a failure by disabling Secure Boot or blindly blacklisting a
working driver. See Ubuntu's [Secure Boot documentation](https://documentation.ubuntu.com/security/docs/security-features/platform-protections/secure-boot/).

## Acceptance gates

- Two cold boots and two warm reboots with encrypted-disk recovery understood.
- Secure Boot still enabled; `r8127` signed, loaded, and owned by DKMS.
- Both 10GbE ports negotiate at expected speed and sustain an error-free test.
- A normal kernel upgrade rebuilds and loads the module after reboot.
- Ethernet loss does not remove Wi-Fi/USB/local-console recovery.
- CPU, memory, SSD, thermals, and fan behavior pass a sustained workload.
- Headed Playwright works in the `agent-01` desktop without a physical display.
- Four agent sessions are tested progressively; results determine safe concurrency.
- Suspend is disabled for an always-on node; power-loss recovery is tested.

Do not mark the machine `approved` until required endpoint management, backup,
remote access, and organization security controls also pass.
