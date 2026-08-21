# Fleet rollout and change management

The repository can manage a fleet now; production rollout remains staged. A clean pilot is evidence, not permission to change every node at once.

```text
issue + proposed profile/script change
                  |
                  v
        branch --> make check --> draft PR/MR
                  |                    |
                  |              human/security review
                  |                    |
                  +<------ fix --------+
                                       |
                                       v
                              one non-critical canary
                                       |
                         audit + 24h/multi-day burn-in
                                       |
                         +-------------+-------------+
                         |                           |
                      rollback                 staged cohorts
                                                     |
                                            audit every node
```

## Profile storage

- Personal/pilot-only choices: ignored `config/profiles/<machine>.local.toml`.
- Shared private fleet inventory: reviewed `config/fleet/<machine-id>.toml`; profiles contain no secrets.
- Validate one profile with `fleetctl.py validate ... --ready` and the inventory with `validate-fleet.py config/fleet`.

## Change rules

1. Open an issue describing purpose, affected cohorts, rollback, and acceptance evidence.
2. An agent may prepare a branch, commit, and draft PR/MR text. It must not create a remote, push, open the PR/MR, approve, merge, or deploy without the responsible human’s explicit authorization.
3. CI runs `make check`; reviewers inspect rendered profile plans and privileged diffs.
4. Apply to one non-critical node. Record exact versions, configuration diff, audit output, resource measurements, reboot/reconnect, browser/container tests, and restore evidence.
5. Promote by small cohorts with a stop condition. Never run an opaque all-host command.
6. Revert the reviewed change or run its documented removal path, then re-audit. Resource policy supports `--remove`; access changes require an open KVM/console recovery path.

## Drift and upgrades

- Daily/weekly audits detect host drift; do not auto-remediate privileged drift.
- Stage repository, agent CLI, browser, container, NoMachine, Tailscale, and OS upgrades on the canary.
- Regenerate and review `mise.lock` after approved tool upgrades; do not silently follow `latest` in production.
- The setup agent may propose repo improvements through a PR/MR. A human owns review, merge, rollout, and rollback decisions.
