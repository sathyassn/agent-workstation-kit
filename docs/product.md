# Agent Workstation Kit: product

[Documentation home](README.md)

## Purpose

Set up, maintain and revert dedicated Ubuntu and macOS machines where AI
coding agents run long-lived work. Every change is previewed, backed up,
verified and reversible, and people keep the critical decisions.

## Users

- **The conductor agent** (an agent CLI on the new machine), which drives
  setup from right after the OS install. It is the preferred runner.
- **The person setting up a machine,** who installs the OS, answers the
  interview, approves root steps and enters every secret.
- **Fleet maintainers** in an organisation, who keep machine config and
  records in a private fleet repository.

## Scope

- Ubuntu LTS releases in Canonical's standard support, and the current and
  previous macOS on Apple silicon (`registry/platforms.toml` from T1).
- One `wskit` binary: units for each part of the setup, stages that compose
  them, a transaction engine, a conductor interface, and fleet commands.
- A public kit with no real machines, people, organisations or secrets; a
  private fleet holds config and records only.

## Non-goals

- Managing machines the kit did not set up, or ones outside the platform
  list.
- Holding or printing secrets, or signing in to services on a person's
  behalf.
- Weakening a security control (sandbox, firewall, disk encryption, branch
  protection) to make a tool work.
- Being an MDM, a configuration-management server or a fleet inventory
  service.
- Reaching into the OS install, firmware or disk encryption.
