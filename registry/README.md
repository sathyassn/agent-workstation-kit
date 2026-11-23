# Registry

Single-source TOML registries for the kit:

- `units.toml`: unit catalog (domain, stage, privilege, dependencies per OS).
- `presets.toml`: purpose-by-tier presets and best-practice defaults.
- `tools.toml`: pinned Go libraries, build tools and managed CLIs with their
  source, licence and security review dates.

This folder is embedded into `wskit` by `assets.go` at the module root.
