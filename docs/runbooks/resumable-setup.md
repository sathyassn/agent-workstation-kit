# Automated and resumable Ubuntu and macOS setup

[Documentation home](../README.md) · [Ubuntu setup](../02-linux-setup.md) · [macOS setup](../03-macos-setup.md)

```text
human OS / recovery / enrollment
             |
             v
approved profile -> source plan + execution bundle
                               |
              +----------------+----------------+
              v                v                v
         package scope    machine scope     agent scope
         package owner    staged root       agent account
              |                |                |
              +-------- preview / approval ----+
                               |
                     run scripts automatically
                     verify live postconditions
                     write private receipts
                               |
                       failure -> stop
                       rerun -> recheck -> skip or repair
```

## Start here: one command

After the OS day-zero prerequisites, run the command below. For a target with a
private fleet profile, first draft its handoff and check readiness with
`fleetctl setup` ([guided fleet setup](guided-fleet-setup.md)); this command
then executes and resumes the setup.

```bash
scripts/setup-workstation.sh
```

In a terminal this opens the guided walkthrough. It checks prerequisites, offers
plan creation, scope preview/run/resume, recorded status and access checks.
Choices are validated; Enter keeps safe defaults, existing files are preserved,
and a blank approval never applies a scope. To apply, the human must type the
displayed `APPLY HOSTNAME SCOPE` phrase after reviewing that scope. Account and
vendor password prompts stay with their native tools; the walkthrough never
collects passwords, tokens or pairing links. Ctrl-C exits and retains completed
receipts. Rerun the same entry point to resume.

Rich provides readable tables and phase progress when it is already installed.
There is no mandatory UI dependency or automatic pip installation. The same
walkthrough works in plain text without Rich; `--plain` disables decoration.
Agents, pipes and CI get non-interactive output and explicit CLI flags, with no
unexpected prompts. Root execution uses only the staged OS-owned runtime and
never imports a user-owned UI environment.

You do not need to memorize the helper filenames. These are all actions of the
same command:

```text
setup-workstation.sh init          preview/create a draft profile, data only
setup-workstation.sh plan          review settings and export a plan
setup-workstation.sh run           preview/apply one approved owner scope
setup-workstation.sh resume        the same safe execution/resume path
setup-workstation.sh status        recorded progress; no phase execution
setup-workstation.sh check-access  TCP / SSH / SFTP / reconnect checks
setup-workstation.sh workloads     project browser/rootless-container checks
setup-workstation.sh doctor        prerequisite and next-step guidance
```

For a new profile, `init machines/NAME.toml --fleet-root PRIVATE_ROOT` accepts
the existing initializer's context/namespace/hostname/platform/human options.
It is a preview until `--write` is supplied. This writes a draft configuration,
never an OS account, and preserves existing profiles. Resolve all policy choices,
review and commit the approved profile before proceeding. The walkthrough
expects that approved profile; it never invents management, recovery or identity
policy for you.

Use `ACTION --help` for its arguments. Helper scripts remain separate where OS,
account authority or compatibility requires it; they are implementation details,
not additional normal setup entry points. The normal provisioning scopes are
packages → machine → agent. Resources require measurements; audit follows before
handoff. Switching owners and the staged privileged approval are actual trust
boundaries, so the walkthrough explains those handoffs instead of silently
using sudo or copying another user's credentials.

Use `scripts/setup-workstation.sh` as the entry point. It selects the source
planner or portable executor. The source planner needs Python 3.11+; staged Mac
privileged execution uses the OS-owned Python 3.9+ after Xcode tools are present.
Never run a privileged Python process through a user-owned Homebrew runtime.

## What is automated

| Scope | Ubuntu | macOS |
|---|---|---|
| `packages` | Missing baseline/profile APT packages, Git LFS and update/service prerequisites | Missing Homebrew formulae/casks, Git LFS |
| `machine` | Identity, accounts, remote hardening, agentctl and workload packages | Identity; selected local-account creation or managed-account verification |
| `agent` | Managed shell, verified mise bootstrap when absent, runtimes and selected agent CLIs | Bash-compatible managed shell, runtimes/agent CLIs, OrbStack readiness |
| `resources` | Explicit measured resource-policy apply | Human/managed-policy gate |
| `audit` | Host/account/security checks | Host/account/security checks, with remaining GUI/recovery evidence explicit |

The executor previews each script before its apply, stops at the first failure,
checks live postconditions and records receipts automatically. Repeating a scope
skips only phases whose receipt matches this host/account/inputs and whose live
checks still pass. Changed or missing state causes the idempotent phase to run
again. A changed script, profile or relevant configuration invalidates its
receipt; an unrelated documentation edit does not. New code still needs renewed
plan approval. Receipts describe the stated postconditions, not fresh-install,
provider-authentication, encrypted-boot or GUI certification.

The executor does not invoke `sudo` or switch accounts to provision another
role, and never accepts arbitrary commands from a journal. The existing root
audit can inspect account tooling through its reviewed read-only target-user
probe. Run each scope as its stated owner. It does not grant
new privileges or treat a prior receipt as approval for a changed plan.

## Select settings once

Use public defaults, approved private policy and explicit host overrides to
produce one complete reviewed profile. There is no hidden three-file merge.
The existing schema remains version 3; the following additions are optional:

```toml
# Inside [tooling], for Ubuntu:
apt_packages = ["ripgrep", "sqlite3"]

# Or inside [tooling], for macOS:
brew_formulae = ["ripgrep", "sqlite"]
brew_casks = ["firefox"]

# Inside [accounts], on macOS only:
macos_provisioning = "managed" # or explicitly approved "local"
```

Use only the keys for the selected OS. Selections add to
`config/packages.toml`. Package names cannot contain options, URLs or third-party
taps. The helpers use already-approved repositories, install missing selections,
report installed versions and preserve existing packages. An installed APT
version conflicting with an explicit `name=version` pin stops for review rather
than silently upgrading or downgrading. Brew auto-update and install-time
upgrades are disabled for these runs. Repository/source trust and upgrades remain
explicit reviewed changes.

A new Linux account without mise uses `config/mise-bootstrap.toml`: a fixed
official binary version and architecture-specific SHA-256 values, checked against
the vendor's signed checksum file. An existing installation is preserved.
Optional `tooling.mise_version` plus `tooling.mise_sha256` select a reviewed Linux
override. Macs use the baseline Homebrew installation and do not install a second
mise tree. Provider authentication and Grok's authenticated vendor installer
remain separate ceremonies.

For a fresh Linux OpenSSH package, the package helper guards the service and
socket with kit-owned persistent masks before installation. The remote-hardening
phase releases only those masks after validating SSH configuration and activating
the restrictive firewall. Existing service overrides and masks are preserved.
A failure after release re-masks the newly installed service before firewall
rollback; if that cannot be done, recovery keeps the restrictive firewall.
This path requires actual Ubuntu package/service qualification before rollout.

On self-managed Macs, `macos_provisioning = "local"` previews missing accounts
and creates them through `sysadminctl` during an approved interactive apply.
Passwords go directly to vendor hidden prompts, never into arguments or logs.
Existing accounts/passwords are preserved; an existing role mismatch stops.
Managed mode creates nothing and requires the MDM-provisioned accounts/roles to
exist. Secure Token, FileVault escrow and enrollment remain human/MDM acceptance.

## Export the upfront plan

From the reviewed source checkout, using the selected Python:

```bash
AWK_PYTHON=python3 scripts/setup-workstation.sh plan \
  --fleet-root /PRIVATE/FLEET_ROOT --profile machines/acme-ws-001.toml \
  --confirm-recovery-tested --connection-context local-console \
  --export-plan /PRIVATE/EVIDENCE/execution-plan.json
```

Use `machines/acme-mac-001.toml` for a Mac. On Mac, set `AWK_PYTHON` to the
Homebrew Python 3.11+ selected in day zero. Use `tailscale-ssh` and the captured
`--ssh-source-ip` only for a proven Tailscale SSH context. Recovery confirmations
are human attestations, not substitutes for the real test.

The export contains the validated non-secret profile, source-file hashes, kit
revision, package selection and exact phase arguments. Existing output files are
never overwritten. Planning automatically runs whole-configuration validation. Review the full plan, package sources,
identities, expected prompts and recovery before approval. Stage the exact
committed source/profile revisions using the OS staging guide. Place the reviewed
execution plan in a root-owned path under `/opt/agent-workstation-fleet/plans/`
for privileged scopes. The executor hashes the actual source and staged profile
again; it rejects mismatches. Never stage an uncommitted working-tree plan using
an archive of a different commit.

## Preview and apply each owner scope

As the package owner, preview the selected scope:

```bash
scripts/setup-workstation.sh run \
  --plan-file /PRIVATE/EVIDENCE/execution-plan.json --scope packages
```

This prints an **execution plan ID** bound to the bundle, selected scope and
actual host. The source planner's checkpoint ID is a different identifier;
use the ID printed by this executor for its approval. Show this preview to the
human, then run the same command with:

```text
--apply --approve-plan REVIEWED_EXECUTION_PLAN_ID
--confirm-target REVIEWED_HOSTNAME --execution-context host
--journal /PRIVATE/PER_EXECUTOR_DIRECTORY/setup.json
```

This is syntax guidance; replace all reviewed values before use. The journal's
parent must be owned by the executing account and mode `0700`; the journal and
lock use `0600`. Each account has its own execution journal. Do not share a
root-owned writable journal with the agent account.

Ubuntu package and machine scopes require root; Mac packages require the named
Homebrew owner. A human uses a separately approved staged invocation for root:

```text
cd /opt/agent-workstation-kit
sudo /bin/bash scripts/setup-workstation.sh run \
  --plan-file /opt/agent-workstation-fleet/plans/execution-plan.json \
  --scope machine --apply --approve-plan REVIEWED_EXECUTION_PLAN_ID \
  --confirm-target REVIEWED_HOSTNAME --execution-context host \
  --confirm-recovery-tested --journal /PRIVATE/ROOT_OWNED_DIRECTORY/setup.json
sudo -K
```

Preview from the same staged location without `--apply` first. Root apply
requires the root-owned `/opt` toolkit and profile, with no writable/symlinked
code inputs. Root preview and status follow the same rule. Before it runs any
toolkit code as root, the entry point refuses any copy other than
`/opt/agent-workstation-kit`, and refuses that copy if anything in it is not
root-owned, is group or world writable, or is a symlink. Root package installs
run the same check. It runs native interpreters in a clean environment. Keep elevation
temporary and recovery open. Run the agent scope directly as the intended agent
account, inside its real graphical session where required. Stop for MDM,
FileVault, privacy, provider MFA/consent or an unknown security condition.

Rerun the same approved command after a failure. Successfully verified phases
are rechecked and skipped automatically. Failed/unverified phases are retried;
later phases were not run after the failure. Receipts are written after each
phase, so a process interruption cannot turn a failed phase into a success.

## Automate access and workload checks

Use [remote Mac acceptance](remote-mac.md) for the selected Mac paths. The
cross-platform checker now executes the repeatable transport/file tests:

```bash
scripts/setup-workstation.sh check-access --host HOST_PRIVATE_ADDRESS \
  --tcp-port 22 --tcp-port ORCA_PORT --ssh-user alice --sftp-roundtrip
```

Replace placeholders, review the preview, then add `--apply` to perform exactly
those checks using already-approved credentials and known host keys. It tests
TCP before authentication, verifies the named SSH account, performs a disposable
SFTP upload/download with matching hashes, cleans only its probe files, and
opens an independent SSH connection to test reconnect. It never accepts unknown
host keys, prints credential values or changes host settings. Use optional
`--output /PRIVATE/EVIDENCE/new-result.json` for a new evidence file. Inspect a
host listener separately with `--listener-port PORT`, previewing before apply.
Orca pairing, app reconnect and named-user Apple Screen Sharing still need the
actual application/session acceptance; an SSH success does not prove them.

`scripts/setup-workstation.sh workloads --agent agent-01 --project /APPROVED/PROJECT`
previews installing that project's locked Playwright browser and a real headless
browser smoke test on Ubuntu or Mac. After approval add `--apply`. Optional
`--container-image NAME@sha256:REVIEWED_DIGEST` runs a disposable, network-disabled,
read-only container smoke test. Ubuntu requires proven rootless Podman; Mac uses
the explicit OrbStack context. Global Docker context is preserved. `--start-orbstack` opens the installed app only in the
intended active graphical session; humans complete its license/privacy prompts.

Keep the manual acceptance journal for OS recovery, provider real runs, graphical
access, sleep, reboot and handoff. Its `verified` entries reference absolute
private evidence paths and SHA-256 values; changed evidence invalidates the
checkpoint and later checks. The execution receipts automate provisioning
bookkeeping; they do not prove physical hardware compatibility. Qualify fresh
Ubuntu and Mac installs, partial failure/resume, repeat-run user-state
preservation, encrypted reboot and independent recovery separately before rollout.

For declared purpose services, Linux can create missing `svc-purpose` system
identities with a non-interactive shell and no password/SSH/admin grant. Existing
services are preserved and checked. Mac service identities require their explicit
purpose/MDM policy before the account helper proceeds; it never guesses service
privileges or FileVault eligibility.

Concurrent applies against the same journal stop with an actionable lock error.
Privileged scopes also take a host-level runtime lock, so separate root journals
cannot race account, identity or firewall changes. Locks release on process exit;
a leftover lock file is not a reason to delete user state. If a privileged phase
is killed mid-change, resume begins with its preflight/live checks; unexpected
security or recovery state stops for console review rather than guessing a repair.

The Mac agent scope opens the selected OrbStack app only in the intended active
graphical owner session, then waits up to 60 seconds for its engine. Native
license/privacy prompts remain with the human. A timeout stops with guidance;
resume after completing the prompt. Optional project checks expose
`--engine-timeout` (0–300 seconds); without an explicit app-start request they
fail promptly if the engine is unavailable. Ubuntu project checks explicitly
select local rootless Podman, preserving remote/default engine configuration.
