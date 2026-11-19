# Guided setup for a fleet workstation

[Documentation home](../README.md) · [Linux day zero](day-zero-linux.md) · [macOS day zero](day-zero-macos.md)

Use `fleetctl setup` for a machine that has a private fleet profile. It selects
Linux or macOS from the profile and runs the existing readiness checks. Use
Python 3.11 or newer, with the interpreter prepared in your platform's
day-zero guide. The examples use `python3`.

## Prepare a new target

1. Allocate the asset and hostname. Run `fleetctl init` with the target's
   platform, hardware and accounts, then review and validate the profile.
2. From the kit checkout, print the starter handoff. Save the reviewed draft
   as `handoffs/<hostname>-bootstrap.md` in the private fleet.

   ```sh
   python3 scripts/fleetctl.py --fleet-root "$FLEET_ROOT" \
     setup machines/acme-ws-001.toml --handoff-draft
   ```

   Use the actual target profile path. On Linux the default is Ubuntu 24.04.
   Supply `--expected-ubuntu YY.MM` when the target has a reviewed exception.
   The command prints Markdown and writes no files. When refreshing a handoff,
   compare with the existing file and preserve completed steps and decisions.
3. Fill in the hardware-specific procedure, approved OS, client access, actual
   stopping point, next phase and recovery owners. Review and commit the profile
   and handoff in the private fleet.

## Start or resume on the workstation

After the day-zero prerequisites, open the reviewed **source** kit checkout in
the approved operator's terminal. Set `FLEET_ROOT` to the private fleet source
checkout. Use the same command for a fresh or interrupted setup.

```sh
python3 scripts/fleetctl.py --fleet-root "$FLEET_ROOT" \
  setup machines/acme-ws-001.toml
```

Use the same approved Ubuntu exception as in the handoff, if applicable. To
enforce the reviewed revisions, pass their full commit IDs with
`--kit-revision` and `--fleet-revision`. Compare the printed local revisions
with the deployment approval record. A local revision identifies the checkout.
Check publication or remote freshness independently when required.

- **Wrong directory or revision:** Return to the correct source checkout. Use
  reviewed staged snapshots only for the corresponding privileged apply.
- **Dirty checkout:** Review and preserve the changes, or use a separate clean
  checkout of the approved revision. Readiness never fetches, resets or merges.
- **Lock mismatch:** Select the reviewed kit that matches the private fleet
  lock. A lock change requires separate review and compatibility testing.
- **Not ready:** Resolve the reported failure and rerun. This command tracks
  readiness from live checks each time and never replays provisioning.
- **Ready:** Start the setup agent in the kit source checkout and supply the
  committed host handoff. Have it verify the physical target and inspect what
  was already applied. Preview and approve each remaining phase using the
  platform runbook. Use the named operator for enrollment, credentials and
  recovery. Keep each acceptance outcome in the private host record.

The command reports setup-agent readiness. Apply readiness still requires the
approved profile, the existing per-phase gates and any platform-specific
staging. Hardware and recovery ceremonies require their own live evidence.
