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

## Schema 2 to schema 3 migration

Schema 3 adds the assigned display name, explicit GitHub/GitLab/Atlassian
principals, and the durable local identity phase. An older schema-2 profile must
not be relabeled without reviewing those new decisions. Follow
[`runbooks/migrate-v2-to-v3.md`](runbooks/migrate-v2-to-v3.md); its read-only
checker proves established fields did not change.

1. Create a reviewed fleet-repository branch. Keep active schema-2 files under
   `machines/` and draft schema-3 copies under `migration-candidates/`.
2. Add `machine.display_name`; it must use the safe, trimmed ASCII label alphabet and be
   unique across the complete fleet after case folding. Add the source-control principal
   fields and the `[collaboration]` table using the current examples.
3. Set `schema_version = 3`, run the read-only migration checker and individual
   draft validation, then resolve every `ask`. Do not run the ready-only
   whole-fleet validator against drafts or a mixed-schema inventory.
4. After review, mark all candidates approved and replace their matching files
   under `machines/` in one migration commit. Run the whole-fleet validator;
   it must pass before any OS apply.
5. Review the identity preview and Linux `/etc/hosts` impact. Apply `identity`
   to one non-critical canary with console/KVM open,
   `--confirm-recovery-tested --connection-context local-console`,
   and verify hostname, display name, local resolution, and the local record.
6. Run the full audit only after identity succeeds. Promote in small cohorts;
   never let an absent schema-3 identity record become an automatic remediation.

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
- Display name: fleet-unique human label; canonical whitespace and case-insensitive
  comparison prevent visually equivalent duplicates. It is mutable through
  review and never used as a security principal.
- Persistent UUIDv4: generated once and retained through rebuilds.
- Asset tag, hardware serial, BIOS, and Tailscale node identity: private inventory.
- Local account names may repeat; `hostname/account` is the unique principal.
- `state` records final desired readiness, not temporary pilot management. Keep
  a work node `draft` until its required MDM/EDR state is actually present.

Stage toolkit, OS, kernel/DKMS, agent CLI, browser, container, NoMachine,
Tailscale, and KVM firmware updates on one suitable node. Do not auto-remediate
privileged drift or run opaque all-host commands.
