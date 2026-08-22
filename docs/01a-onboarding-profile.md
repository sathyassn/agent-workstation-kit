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
  --context work --namespace ac --hostname ac-ws-001 --platform linux \
  --hardware-profile minisforum-ms-s1-max-64gb \
  --asset-tag AC-10001 --human alice --admin admin-01
```

It refuses to overwrite an existing file, creates a canonical UUIDv4 once, and
validates the draft before returning. Keep that UUID for the machine's lifetime.
Do not regenerate it during rebuilds. Hardware serial, Tailscale node identity,
and observed facts belong in the private asset/audit record, not this public repo.

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
unique hostname/UUID/asset tag, host/account principals, and retired names.
Profiles contain no passwords, tokens, private keys, recovery codes, or
secret-bearing URLs.

## Apply one phase

Never run the whole build as one opaque operation. Preview, obtain approval,
apply one phase, and verify it before continuing:

```bash
./scripts/fleetctl.py run /path/to/ac-ws-001.toml accounts
sudo ./scripts/fleetctl.py run /path/to/ac-ws-001.toml accounts --apply
```

Remote hardening also requires an explicit recovery confirmation. The `shell`
and `user-tooling` phases run inside the declared `agent-NN` account. External
authentication remains a human credential ceremony.

An AI setup agent can orchestrate after the OS, first named/bootstrap account,
network, repository, and one authenticated agent CLI exist. It may run read-only
checks and previews, but must pause at every privileged or credential gate.
