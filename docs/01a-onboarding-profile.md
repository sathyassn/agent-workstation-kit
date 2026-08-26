# Onboarding profile and controller

One reviewed TOML profile declares each machine's desired, non-secret state.
Python 3.11+ parses TOML without bootstrap dependencies.

```text
fleetctl init (UUID generated once)
        |
        v
draft profile --> validate --> plan --> resolve every "ask" --> human review
                                                              |
                                                     state = approved
                                                              |
                         preview phase --> approve --> apply --> validate
                                                              |
                                                       live host audit
```

## Create a draft

Prefer the generator to copying an example:

```bash
./scripts/fleetctl.py init /path/to/private-fleet/machines/ac-ws-001.toml \
  --context work --namespace ac --hostname ac-ws-001 --display-name Atlas \
  --platform linux \
  --hardware-profile minisforum-ms-s1-max-64gb \
  --asset-tag AC-10001 --human alice --admin admin-01
```

It refuses to overwrite an existing file, creates a canonical UUIDv4 once, and
validates the draft before returning. Keep that UUID for the machine's lifetime.
Do not regenerate it during rebuilds. Hardware serial, Tailscale node identity,
and observed facts belong in the private asset/audit record, not this public repo.

The technical hostname remains conventional and stable. `machine.display_name`
is the fleet-unique, human-assigned label shown as Linux's pretty hostname or
macOS's Computer Name. It may be changed through a reviewed profile update; it
is never used for authentication, authorization, DNS, or durable joins. Assigned
names use a deliberately small ASCII alphabet to prevent mixed-script lookalikes
and are compared case-insensitively. `fleetctl init` checks both technical and
assigned names before writing; the whole-fleet validator rechecks every approved
profile and is the authoritative merge gate.

The privileged `identity` phase writes the approved non-secret subset to
`/etc/agent-workstation-kit/identity.toml` on Linux or
`/Library/Application Support/Agent Workstation Kit/identity.toml` on macOS.
The file is root-owned mode `0644`, so users and agents can identify the host but
cannot alter its identity. The private fleet repository remains authoritative:
restore from it after reimaging or disk loss.

Profiles can live in an ignored `config/profiles/*.local.toml` during exploration.
Production inventory belongs in a separate private repository created from
[`templates/private-fleet`](../templates/private-fleet). A work organization
should use its own private fleet repository and protected review workflow.

## Validate and plan

```bash
./scripts/fleetctl.py validate /path/to/ac-ws-001.toml
./scripts/fleetctl.py plan /path/to/ac-ws-001.toml
```

Resolve every `ask`. Review identities, privileges, recovery, endpoint controls,
backup, resource headroom, and optional tools such as `gws`. Then set
`state = "approved"` and run:

```bash
./scripts/fleetctl.py validate /path/to/ac-ws-001.toml --ready
./scripts/validate-fleet.py /path/to/private-fleet
```

`validate-fleet.py` checks toolkit compatibility, filename/hostname agreement,
unique hostname/display name/UUID/asset tag, host/account principals, retired
names, and consistent definitions for provider principals shared by multiple hosts.
Profiles contain no passwords, tokens, private keys, recovery codes, or
secret-bearing URLs.

## Apply one phase

Never run the whole build as one opaque operation. Preview, obtain approval,
apply one phase, and verify it before continuing:

```bash
./scripts/fleetctl.py run /path/to/ac-ws-001.toml accounts
sudo ./scripts/fleetctl.py run /path/to/ac-ws-001.toml accounts --apply
```

Machine identity and remote hardening also require an explicit recovery
confirmation when applying. The `shell`
and `user-tooling` phases run inside the declared `agent-NN` account. External
authentication remains a human credential ceremony.

An AI setup agent can orchestrate after the OS, first named/bootstrap account,
network, repository, and one authenticated agent CLI exist. It may run read-only
checks and previews, but must pause at every privileged or credential gate.
