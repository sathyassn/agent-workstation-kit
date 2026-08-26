# Human, script, and setup-agent responsibilities

| Phase | Human | Script | Setup agent |
|---|---|---|---|
| Purchase/site | Approves hardware, warranty, network, location | None | Researches current availability and records assumptions |
| OS installation | Boots media, encryption, recovery, first admin | None | Presents checklist only |
| Assessment | Provides policy/context | `preflight.sh` | Runs checks and interprets results |
| Onboarding profile | Resolves choices and approves desired state | `fleetctl.py validate/plan` | Interviews once, writes no secrets, explains validation errors |
| Fleet/migration gate | Reviews new identity decisions and preserved fields | `validate-fleet.py`, `check-profile-migration.py` | Runs read-only checks; never silently upgrades inventory |
| Base packages | Approves `sudo` | `bootstrap-*.sh` | Previews, explains, requests approval, validates |
| Machine identity | Approves technical/display names and local record | `install-machine-identity.py` | Compares profile, OS names, ownership, and contents |
| Accounts | Confirms names/roles and approves admin change | `setup-accounts-linux.sh` or managed macOS process | Builds exact plan and checks least privilege |
| Network/RDP/KVM | Completes MFA, vendor login, physical cabling | Vendor/managed installers | Guides and tests without exposing credentials |
| `agentctl` | Approves sudoers delegation | `install-agentctl-linux.sh` | Previews, applies after approval, tests roles |
| User tooling | Selects optional tools | `install-user-tooling.sh`, `install-shell-baseline.sh` | Asks once, installs selection, preserves conflicts |
| Agent CLIs | Selects billing/auth model | User-space installer | Installs binaries and runs non-secret checks |
| Browser/containers | Approves packages and project versions | `install-workloads-linux.sh` or reviewed macOS apps | Validates rootless containers and headed/headless browser tests |
| Credentials | Creates/authorizes identities from a trusted admin machine | No secret-writing script | Explains scopes and waits for sanitized status |
| Provider identity | Creates App/service accounts and branch/space policy | Approved external vault/broker | Guides GitHub/GitLab/Atlassian setup and disposable write test |
| Resources | Approves calculated policy | `apply-resource-policy-linux.sh` | Measures first, explains headroom, validates |
| Operations | Owns update/reboot windows | `fleetctl.py run ... audit`, assessment scripts | Compares live state to the approved profile, produces reports, diagnoses drift |

## Earliest useful agent point

The setup agent can assist after the OS, first named/bootstrap account, network, repository, and one agent CLI are available. It can then orchestrate preview commands immediately. The shared `agent-NN` account becomes the execution environment only after account creation and its user-space tools are installed.

On macOS, an agent cannot replace the human for Setup Assistant, FileVault recovery, MDM, privacy prompts, system extensions, Xcode agreements/signing, or graphical session approval.
