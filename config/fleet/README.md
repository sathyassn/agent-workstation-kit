# Fleet profiles

This directory may hold approved, non-secret per-machine TOML profiles when the repository is private and access-controlled.

- Name files `<machine-id>.toml`; keep `machine.id` globally unique.
- Never store passwords, tokens, private keys, recovery keys, cookies, or secret-bearing URLs.
- Review profile changes through PR/MR and validate the directory with `scripts/validate-fleet.py config/fleet`.
- Use ignored `config/profiles/*.local.toml` files for personal experiments or sensitive organizational metadata.
- A profile records desired state; credentials remain in the approved vault/provider.

The directory intentionally ships without a machine profile. Copy an example only when onboarding a real node.
