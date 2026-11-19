# <Organization> Agent Workstation Fleet

Replace `<Organization>` and the example paths in this file before the first
commit.

This private repository is the organization-specific desired-state companion to
the public [Agent Workstation Kit](https://github.com/sathyassn/agent-workstation-kit).
If deployment uses a reviewed fork, replace that link with its canonical URL and
record the upstream source in organization policy.

```text
public agent-workstation-kit             private organization fleet
generic scripts, tests and runbooks      real machine desired state
                   \                     /
                    +--- kit.lock -------+
```

The toolkit remains generic. This repository records approved, non-secret
machine profiles, assignments and organization policy. `kit.lock` pins the
exact toolkit version required to validate or deploy this fleet; it must match
the toolkit checkout's `VERSION`.

## Scope and boundaries

This repository may contain:

- Technical hostnames, assigned display names and generated machine UUIDs.
- Organization asset tags and non-secret hardware-profile selections.
- Named-human, assigned-administrator and non-admin agent account declarations.
- Approved remote-access tag intent, endpoint-management state, backup labels
  and other non-secret desired state.
- Non-secret assignment and organization-policy notes.

Do not commit credentials, tokens, passwords, SSH private keys, encryption
recovery material, serial numbers, MAC addresses, exported keychains, private
hardware reports or sensitive deployment evidence. Store those in the
organization's approved asset, vault or evidence system.

## Repository layout

```text
private-fleet/
├── kit.lock                 exact compatible toolkit version
├── .gitignore               excludes the local allocation lock
├── machines/                one <hostname>.toml profile per machine
├── policy/                  organization-only non-secret policy notes
├── assignments/             non-secret account ownership records
├── handoffs/                one <hostname>-bootstrap.md per machine
└── retired-hostnames.txt    append-only hostname retirement ledger
```

Choose the fleet's short technical prefix with `fleetctl init --namespace`.
That namespace must match each technical hostname prefix. The technical
hostname, assigned display name, UUID and asset tag must be fleet-unique.
Generate profiles with `fleetctl init`; never create or replace a UUID manually.
Local allocation locking serializes one checkout, while whole-fleet validation
is the authoritative collision gate across clones and branches.

## Prepare a machine handoff

Allocate the asset and create its profile before preparing the handoff. With
the reviewed toolkit checkout and Python 3.11 or newer, print a draft using
the target's actual profile path.

```bash
python3 "$TOOLKIT_ROOT/scripts/fleetctl.py" --fleet-root "$FLEET_ROOT" \
  setup "$PROFILE" --handoff-draft
```

Set the three variables using the examples under Validate a change below.
Save the reviewed draft under `handoffs/<hostname>-bootstrap.md`. Resolve its
approved OS, access, account ownership, recovery path and next setup step.
Review the profile and handoff together. On the intended target, rerun the
same command without `--handoff-draft` from the reviewed source checkout.
Use the toolkit's `docs/runbooks/guided-fleet-setup.md` for the walkthrough.

## Validate a change

Use separate clean checkouts and replace the paths/profile below:

```bash
TOOLKIT_ROOT='/path/to/agent-workstation-kit'
FLEET_ROOT='/path/to/private-fleet'
PROFILE='machines/<hostname>.toml'

cmp "$TOOLKIT_ROOT/VERSION" "$FLEET_ROOT/kit.lock"

"$TOOLKIT_ROOT/scripts/fleetctl.py" \
  --fleet-root "$FLEET_ROOT" validate "$PROFILE"
"$TOOLKIT_ROOT/scripts/fleetctl.py" \
  --fleet-root "$FLEET_ROOT" plan "$PROFILE"

# Resolve every `ask`, obtain human review and set state = "approved" first.
"$TOOLKIT_ROOT/scripts/fleetctl.py" \
  --fleet-root "$FLEET_ROOT" validate "$PROFILE" --ready
"$TOOLKIT_ROOT/scripts/validate-fleet.py" "$FLEET_ROOT"
```

Normal validation proves profile structure. Readiness validation rejects
unresolved policy choices. Whole-fleet validation enforces unique identities
and consistent shared principals. These checks do not prove that the live
machine, recovery path, endpoint controls or remote access have passed.

## Change and deployment workflow

```text
feature branch
    -> fleetctl init/update
    -> validate + plan
    -> resolve every ask
    -> review complete diff
    -> validate --ready + validate-fleet
    -> reviewed pull request
    -> merge protected default branch
    -> stage exact committed toolkit/fleet snapshots
    -> preview one phase
    -> human-approved apply
    -> audit and evidence
```

Do not apply privileged changes from a user-writable checkout. Follow the
toolkit's controlling day-zero runbook for the target platform. Every
privileged, firmware, recovery, account, remote-access or authentication step
remains a separate human approval gate.

## Governance

- Keep this repository private.
- Protect its default branch and require human review before merge.
- Do not let an agent identity approve or merge its own change.
- Preserve the exact approved profile and `kit.lock` in Git before deployment.
- Record reusable discoveries through the public toolkit's feedback process;
  keep organization policy and deployment evidence private.
