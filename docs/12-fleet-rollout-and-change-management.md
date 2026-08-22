# Fleet rollout and change management

## Two repositories

```text
public-capable agent-workstation-kit
  generic scripts, schemas, tests, examples, guides, skill
                    |
                    | version pinned by kit.lock
                    v
private workstation-fleet
  real machines, UUIDs, asset tags, assignments, private policy, retirement log
```

Use this split for both personal and work fleets. Create `workstation-fleet` in
the relevant private organization or personal namespace. Fork the toolkit only
if local code must diverge; retain an upstream link for reviewed generic
updates, but never merge them without the private fleet's canary tests.

## Change flow

```text
issue --> focused branch --> checks --> PR/MR --> human/security review
                                                   |
                                                   v
                                         one non-critical canary
                                                   |
                                      audit + realistic burn-in
                                             /             \
                                         rollback      small cohorts
                                                            |
                                                     audit every node
```

1. Record purpose, affected hosts, rollback, approvals, and acceptance evidence.
2. Validate the toolkit and private fleet; inspect exact privileged previews.
3. An agent may prepare commits and PR/MR text. It must ask before creating a
   remote, pushing, opening the PR/MR, deploying, or changing visibility.
4. An agent identity cannot approve or merge its own change.
5. Canary every OS, hardware, memory, and workload class represented.
6. Stop on security, recovery, driver, resource-pressure, or data-integrity failure.

## Fleet identity

- Hostname: `<namespace>-<class>-<NNN>`; never reuse a retired name.
- Persistent UUIDv4: generated once and retained through rebuilds.
- Asset tag, hardware serial, BIOS, and Tailscale node identity: private inventory.
- Local account names may repeat; `hostname/account` is the unique principal.
- `state` records final desired readiness, not temporary pilot management. Keep
  a work node `draft` until its required MDM/EDR state is actually present.

Stage toolkit, OS, kernel/DKMS, agent CLI, browser, container, NoMachine,
Tailscale, and KVM firmware updates on one suitable node. Do not auto-remediate
privileged drift or run opaque all-host commands.
