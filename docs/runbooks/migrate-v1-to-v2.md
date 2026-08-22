# Migrate a pre-v2 pilot

There is no in-place privileged migration. Preserve the old profile and record
current live state before changing a node.

1. Keep console/KVM recovery open and back up the old profile outside the public
   toolkit.
2. Generate a new v2 draft with `fleetctl init`; copy decisions field by field,
   never credentials.
3. Record a persistent UUID, final hostname, asset tag, named users,
   one-to-one admin assignments, agent roles, and private fleet `kit.lock`.
4. Compare the v2 phase plan with the live node. Preview every phase; do not
   apply a phase merely to make validation green.
5. Manually inspect legacy files before removal:

   ```text
   /etc/ssh/sshd_config.d/00-agent-fleet.conf
   /etc/ssh/sshd_config.d/60-agent-fleet.conf
   /etc/systemd/system/user-UID.slice.d/50-agent-fleet.conf
   ```

6. If a legacy policy is still effective, schedule a recovery-backed change:
   remove or merge it manually, validate effective `sshd -T`/systemd state,
   then preview and apply the v2 phase.
7. Run the full audit, reboot/reconnect test, realistic load, and rollback
   exercise. Mark the v2 profile approved only after live evidence passes.

The fail-closed legacy checks are deliberate: automatic deletion could remove
the only working access or resource policy.
