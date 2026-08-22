# Private workstation fleet

Keep real inventory, ownership assignments, asset tags, internal hostnames, and
organization policy here—not in the public toolkit. Never commit credentials.

```text
private-fleet/
├── kit.lock                 exact compatible toolkit version
├── machines/                one approved <hostname>.toml per host
├── policy/                  organization-only policy notes
├── assignments/             non-secret human/account ownership records
└── retired-hostnames.txt    append-only hostname retirement ledger
```

Validate from a checkout of `agent-workstation-kit`. Both commands enforce the
exact toolkit version in `kit.lock`:

```bash
./scripts/validate-fleet.py /path/to/private-fleet
./scripts/fleetctl.py --fleet-root /path/to/private-fleet validate machines/ac-ws-001.toml --ready
```

Copy this directory into a new private repository. Replace `kit.lock` with the
exact toolkit `VERSION`. Protect its default branch and require human review.
