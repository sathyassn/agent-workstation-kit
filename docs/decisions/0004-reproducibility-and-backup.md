# ADR 0004: Use mise first; treat reproducibility and backup separately

- Status: accepted for the initial pilot
- Date: 2026-08-20

## Decision

Use `mise` as the default cross-platform runtime and CLI version manager on
Ubuntu and macOS. Keep operating-system packages in apt or Homebrew and keep
the managed shell configuration in this repository.

Do not make NixOS, nix-darwin, or Home Manager a prerequisite for the first
deployment. They may be introduced selectively after the pilot when a package
or configuration has a clear reproducibility benefit and an owner is prepared
to maintain it.

Treat configuration reconstruction and data recovery as two different systems:

- this repository reconstructs declared machine configuration;
- encrypted backups protect repositories, unpushed work, user data, and other
  state that cannot be reconstructed;
- credentials remain in the selected password/secrets manager, never here.

## Rationale

The fleet must support both Linux and macOS without making the initial build
dependent on a second operating-system abstraction. `mise` gives the required
runtime consistency with substantially less bootstrap and troubleshooting
surface. Nix can improve reproducibility, but it does not replace backups and
its macOS and NixOS operating models are different enough to warrant a later,
deliberate adoption decision.

## Consequences

- Versions in `config/mise.toml` are reviewed and updated deliberately.
- apt and Homebrew state is validated rather than assumed to be bit-identical.
- Backup configuration is a deployment gate and is tested with a restore drill.
- A future Nix experiment must be optional and must not break the documented
  non-Nix recovery path.
