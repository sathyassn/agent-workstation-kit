# Registry

Single-source TOML registries for the kit:

- `units.toml`: unit catalog (domain, stage, privilege, dependencies per OS).
- `presets.toml`: purpose-by-tier presets and best-practice defaults.
- `tools.toml`: each direct Go library and build tool in `go.mod` (managed CLIs
  join later) with its exact version, `go.sum` h1 sum, source, licence and
  security review dates; `internal/registry` checks it against `go.mod`.

This folder is embedded into `wskit` by `assets.go` at the module root.
