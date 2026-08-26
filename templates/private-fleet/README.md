# Private workstation fleet

Keep real inventory, ownership assignments, asset tags, internal hostnames, and
organization policy here—not in the public toolkit. Never commit credentials.

Each profile also records a unique `machine.display_name`. Assigned names use a
trimmed ASCII alphabet with no repeated spaces and uniqueness is checked
case-insensitively, preventing mixed-script, whitespace, and case-only
collisions. Creation is serialized by a local allocation lock; the required
whole-fleet validation remains the authoritative pre-merge gate, especially
across separate clones where a filesystem lock cannot coordinate. The toolkit's `identity` phase
installs a root-owned local copy for self-identification, while this private
repository remains the recovery source of truth.

```text
private-fleet/
├── kit.lock                 exact compatible toolkit version
├── .gitignore               excludes the local allocation lock
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
