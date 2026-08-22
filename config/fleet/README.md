# Local fleet scratch area

This directory is retained only for ignored local experiments. Production fleet
inventory belongs in a separate private repository created from
`templates/private-fleet` and validated with `scripts/validate-fleet.py`.

- Name each profile `<machine.hostname>.toml`.
- Never commit credentials or private inventory to the public toolkit.
- Use `*.local.toml` here if a short-lived local profile is necessary.
