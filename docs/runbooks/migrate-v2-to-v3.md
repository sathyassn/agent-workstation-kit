# Migrate a profile from schema 2 to schema 3

Use a private fleet branch. This is a reviewed data migration, not an OS apply.
Keep the schema-2 source unchanged until the schema-3 candidate passes review.

```text
machines/<host>.toml schema 2 (unchanged)
          |
          v
migration-candidates/<host>.toml schema 3 draft
          |
          v
read-only migration check --> human review --> approved replacements
          |
          v
whole-fleet gate --> one identity canary --> reboot/audit --> cohorts
```

1. Update `kit.lock` to the exact reviewed toolkit `VERSION` on the same branch.
2. Copy every old profile to `migration-candidates/<hostname>.toml` and keep
   each original under `machines/` unchanged. The authoritative fleet gate
   intentionally rejects mixed schemas.
3. Set `schema_version = 3`; do not change established machine, account,
   network, security, resource, backup, or maintenance decisions.
4. Add a unique `machine.display_name`. Use 1–64 ASCII letters, digits, dots,
   underscores, hyphens, and single internal spaces; no leading/trailing space.
5. Add `source_control.gitlab_principal` and
   `source_control.github_principal`. Use the logical purpose name such as
   `ac-agent-dev`; never paste a token.
6. Add `[collaboration]`. Use `ask` while deciding. For an Atlassian service
   account, the actual principal must be 6–30 alphanumeric characters, such as
   `acagentdev`, with `atlassian_mcp_auth = "service-account-api-key"`.
7. Run the non-mutating draft checks for every source/candidate pair:

   ```bash
   ./scripts/check-profile-migration.py old-v2.toml candidate-v3.toml
   ./scripts/fleetctl.py --fleet-root /path/to/private-fleet \
     validate migration-candidates/<hostname>.toml
   ```

8. Resolve every `ask` and obtain human/security review. Set all candidates to
   `approved`, replace their matching `machines/<hostname>.toml` files on the
   migration branch, and retain the old content in Git history. Do not leave a
   mixed schema fleet or apply identity as part of this commit.
9. Run `./scripts/validate-fleet.py /path/to/private-fleet`. It is the
   authoritative cross-profile uniqueness/readiness gate and must pass after
   all replacements land.
10. With console/KVM recovery open, preview and apply only the identity phase to
   one non-critical canary. Confirm Linux `/etc/hosts`, static/transient/runtime
   hostname, local/loopback NSS resolution, display name, and the root-owned record.
11. Reboot, repeat the audit, record evidence, and promote the already-validated
   profiles in small deployment cohorts.

While schema-2 files remain under `machines/`, `fleetctl init` also fails closed
because it cannot prove assigned-name uniqueness. Finish the reviewed profile
migration before allocating another hostname; never bypass this by moving an
active profile outside the private repository.

The private repository is the recovery source of truth. The local identity
record is a non-secret operational copy, not a replacement for fleet history.
