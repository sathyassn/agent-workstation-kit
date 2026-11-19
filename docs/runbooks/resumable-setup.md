# Resumable Ubuntu and macOS setup

[Documentation home](../README.md) · [Ubuntu setup](../02-linux-setup.md) · [macOS setup](../03-macos-setup.md)

```text
OS and recovery -> approved profile -> one exact plan -> phase previews
 -> scoped human approvals -> staged applies -> live checkpoints -> handoff
                                  failure -> stop -> repair -> resume preview
```

Use the setup skill throughout. Start with the matching day-zero guide; the
entry point below begins after Python, a toolkit source checkout and the private
fleet are available. It prints a plan and reads evidence; it never applies host
changes or authenticates a provider.

## Resolve inputs before approval

Use public example profiles as candidate defaults, approved private fleet policy
as the deployment baseline, and reviewed explicit host overrides last. Resolve
these into one complete committed machine profile, including every `ask`, before
planning. There is no implicit three-file merge or hidden policy inheritance.
Self-managed owners approve their choices; organization-managed machines use
approved catalog, enrollment, endpoint and identity policies. Never override an
organization restriction merely because a personal example permits something.

The current schema declares machine/account roles, agent tooling and existing
remote selections. Record optional package/source/version choices, GUI apps,
ordinary SSH/SFTP, Orca SSH worktrees versus Desktop hosting, and GUI access in a
private reviewed supplement until those choices have schema-backed installers.
Include that supplement in checkpoint evidence and invalidate affected approvals
when it changes. Do not add unsupported keys to schema 3 or imply that a plan
installs manual vendor components. Record publisher, distribution URL, checksum
or package signature, installed version, update owner and rollback for each
selected source. Preserve existing user configuration and approve backups before
any managed replacement.

## Preview one plan

Run as the setup owner, using Python 3.11 or newer (on Mac, the selected Homebrew
Python from day zero):

```bash
python3 scripts/setup-workstation.py   --fleet-root /PRIVATE/FLEET_ROOT --profile machines/acme-mac-001.toml
```

For Ubuntu use the approved `machines/acme-ws-001.toml` profile. The output binds
settings, exact source contents, target UUID, profile content, rendered commands
and acceptance checks to a SHA-256 plan ID. Review package sources, identities,
privileged changes, expected prompts and recovery before recording human approval
for that ID in private evidence. Also run whole-fleet validation. A plan is a
preview and checkpoint journal is evidence, never authorization to apply.

Preview each repository phase without `--apply`. At actual trust boundaries,
a human approves the exact command and scope: disk operations, enrollment,
FileVault/LUKS or recovery ceremonies, temporary privileged authorization,
accounts/access changes, and provider MFA/consent. Batch deterministic work only
within that approved scope. Privileged code/profile inputs must be matching
root-owned snapshots from the OS staging guide. Keep elevation temporary, run
`sudo -K`, and retain the tested recovery path. Changed plan content requires
renewed approval of changed scope before continuing.

## Resume from private evidence

Keep a private JSON journal outside this public kit. Example structure, with
real evidence paths and SHA-256 values filled by the operator:

```json
{
  "plan_id": "REVIEWED_PLAN_SHA256",
  "checkpoints": {
    "os-recovery": {
      "status": "verified",
      "evidence": [{"path": "/PRIVATE/EVIDENCE/os-recovery.txt", "sha256": "EVIDENCE_SHA256"}]
    }
  }
}
```

Evidence records the operator, time, target UUID, kit/fleet revisions, OS/build,
command or UI path, direct outcome, selected app/package versions and recovery.
Keep credentials and pairing links out. For an optional path not selected in the approved plan, record a verified
selection decision with its evidence instead of claiming the service was tested.
Use `failed`, `deferred` or omit a
checkpoint when it has no direct passing evidence. Retain partial results within
its evidence file and complete only its remaining checks.

```bash
python3 scripts/setup-workstation.py   --fleet-root /PRIVATE/FLEET_ROOT --profile machines/acme-mac-001.toml   --journal /PRIVATE/EVIDENCE/setup.json
```

Each `phase-NAME` checkpoint covers one rendered fleetctl phase, including
manual platform phases.

The command rechecks evidence hashes and resumes at the first incomplete or
invalid checkpoint. Earlier valid checkpoints remain recorded; its dependent
successors become pending. A changed plan invalidates all prior checkpoints
conservatively. The operator must also invalidate a checkpoint after live drift
(account, package, listener, network policy or app/session changes); file hashes
alone cannot detect host drift or prove an operator's claim. External supplements
are not automatically hashed into the plan ID: attach them as evidence to the
first affected checkpoint and renew the changed-scope approval explicitly.

## Acceptance and qualification

Follow the generated order, using the [remote Mac recipe](remote-mac.md) for a
Mac host. On Ubuntu inspect listeners with `ss -ltnp`; use the actual operator
client for TCP, authentication and reconnect. Check file transfer with a disposable
upload/download and SHA-256, not merely an SFTP prompt. Separate provider sign-in
from a minimal real agent run, browser installation from a working project
preview/Playwright run, and container installation from a rootless smoke run.
Use only reviewed test commands and project data. Provision each role under its
own account; never copy bootstrap credentials.

Qualify Ubuntu and macOS separately: fresh install, interruption mid-phase,
resume, repeat run preserving user changes, account isolation, selected remote
paths, package versions, backup/restore and encrypted reboot. Static tests and
mocked results do not qualify physical hardware. Record untested installer
preparation, unattended OS mechanisms, firmware/drivers, enrollment, sleep,
recovery and application paths as live-machine gaps. Do not promote the fleet pin
or publish a release until the release checklist and human review are complete.
