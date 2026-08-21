# Onboarding profile and controller

Every machine starts from one reviewed, non-secret TOML profile. TOML was chosen over YAML because Python 3.11+ parses it from the standard library: profile validation works before PyYAML, `yq`, or other bootstrap dependencies exist.

Ubuntu Desktop includes a suitable Python in the supported baseline, and the base package script declares `python3` explicitly. On a Mac without Python 3.11+, run the reviewed Homebrew bootstrap first, then use the profile controller for the remaining phases.

## Lifecycle

```text
OS/work/personal example
        |
        v
local pilot profile OR reviewed private fleet profile; never secrets
        |
        +--> validate draft --> render phased plan --> human/security review
        |                                          |
        |                                          v
        +<-- fix all "ask" values <--------- state = "approved"
                                                   |
                                                   v
                       preview one phase --> approve --> apply one phase
                                                   |
                                                   v
                                  audit live host against same profile
```

## Create the machine profile

Choose the nearest template:

```text
cp config/profiles/work.example.toml config/profiles/ai-node-01.local.toml
```

or:

```text
cp config/profiles/personal.example.toml config/profiles/ai-node-01.local.toml
```

For macOS work nodes, start from `config/profiles/macos-work.example.toml`. Use the [field reference](01b-profile-field-reference.md) for every allowed value and example.

For a shared private fleet, place approved non-secret profiles in `config/fleet/<machine-id>.toml` and review changes through PR/MR. Keep personal experiments and sensitive inventory labels in ignored `.local.toml` files. Validate all committed fleet profiles with:

```text
./scripts/validate-fleet.py config/fleet
```

Fill in the machine, account, remote-access, tooling, identity, security, backup, and maintenance decisions. `ask` is an intentional unresolved state. Do not enter passwords, tokens, private keys, recovery keys, or secret-bearing URLs.

`tooling.install_agents` must remain `true` for this baseline: every node is expected to provide Codex, Claude Code, and Grok Build. Optional tools such as `gws` remain an explicit per-profile choice.

`accounts.ssh_users` is the complete OpenSSH allowlist. Include the temporary/bootstrap recovery administrator during the pilot if it must retain SSH access; remove it from the profile and reapply/revalidate policy when that account is retired. Any local account omitted from this list is denied SSH even if it has an authorized key.

The work and personal templates share the same operating model. The work template additionally defaults to workload model identities, non-human source-control identities, endpoint-management review, and an organization-owned backup/maintenance path. Personal profiles may use a named person's model subscription only when there is one operator and vendor terms permit it.

## Validate and review

Draft validation catches unknown/missing keys, wrong types, malformed or overlapping account names, unsafe authentication combinations, unsupported settings, and source-control omissions:

```text
./scripts/fleetctl.py validate config/profiles/ai-node-01.local.toml
./scripts/fleetctl.py plan config/profiles/ai-node-01.local.toml
```

After every decision is resolved and the responsible human has reviewed the rendered plan, set `state = "approved"` and run:

```text
./scripts/fleetctl.py validate config/profiles/ai-node-01.local.toml --ready
```

Apply commands refuse a draft or unresolved profile. Profile approval is a configuration gate; it does not replace the separate human approval immediately before each privileged phase.

## Run one phase at a time

The controller never runs the whole build as one opaque operation. Preview and apply a single phase, validate it, and only then continue. For example:

```text
./scripts/fleetctl.py run config/profiles/ai-node-01.local.toml accounts
sudo ./scripts/fleetctl.py run config/profiles/ai-node-01.local.toml accounts --apply
```

Remote hardening has an additional recovery interlock:

```text
./scripts/fleetctl.py run config/profiles/ai-node-01.local.toml remote-hardening
sudo ./scripts/fleetctl.py run config/profiles/ai-node-01.local.toml remote-hardening \
  --apply --confirm-recovery-tested
```

Run `shell` and `user-tooling` while logged in as the declared `agt-*` user. The Linux `workloads` phase installs rootless container compatibility, Chromium, Xvfb, and browser libraries. Project Playwright versions and browsers remain project-owned. macOS account creation, privacy permissions, FileVault, remote-access policy, workloads, and resource controls include explicit human/MDM validation gates.

## Post-setup and maintenance audit

On Linux, run from a separate named administrator session:

```text
sudo ./scripts/fleetctl.py run config/profiles/ai-node-01.local.toml audit
```

The profile audit checks declared account existence, home ownership, shells, role groups, and effective SSH allowlists. It then invokes the host audit, which checks hidden sudoers grants, persisted UFW rules, services, passwords, resources, tools, encryption, and updates. Both exit nonzero on failed controls. GUI authorization, KVM/disk unlock, Tailscale grants, branch protection, provider billing, backup restore, and realistic load still require the acceptance evidence in the operations guide.
