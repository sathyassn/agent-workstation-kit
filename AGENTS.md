# Agent operating manual

This file tells an AI agent, or a person, how to work in this repository. Read
it before acting. `CLAUDE.md` includes it.

## 1. What this repository is

- A public kit that sets up Ubuntu and macOS machines where AI coding agents run.
- It holds scripts, profile templates, docs and a template for a private fleet.
- It never holds real machines, people, organisations, hosts or credentials;
  those live in each organisation's private fleet repository.
- Every script previews by default; only `--apply` changes a machine.
- People keep the decisions: sudo, accounts, firewall, remote access, sign-ins,
  licences, merges and releases.

## 2. Where things live

| Path | What it holds |
|---|---|
| `scripts/setup-workstation.sh` | The one setup entry point (plan, run, resume, status, doctor, checks) |
| `scripts/fleetctl.py` | Profiles: `init`, `validate`, `plan`, `setup` (handoff and readiness), `run` one phase |
| `scripts/validate-fleet.py` | Whole-fleet validation of a private fleet checkout |
| `scripts/*.sh`, `scripts/*.py` | Phase scripts the entry points call; each previews by default |
| `scripts/lib/` | Shared code: `common.sh`, verifiers, path safety, UI |
| `config/` | Kit defaults: `mise.toml`, `packages.toml`, example profiles in `config/profiles/` |
| `templates/private-fleet/` | The folder an organisation copies into its private fleet repository |
| `docs/` | Guides; `docs/README.md` is the index, `docs/runbooks/` the task runbooks |
| `skills/setup-agent-workstation/` | The setup skill for agents |
| `tests/` | Unit, fixture and repository checks run by `make check` |
| `CHANGELOG.md`, `VERSION` | Release history and the current version |

## 3. Rules an agent never breaks

- **Preview first.** Run every script without `--apply`, show the preview, and
  apply only after a person approves that exact preview. Where a vendor tool has
  its own dry-run mode and the guide calls for it, use that too.
- **No root unless handed over.** Never run `sudo` or act as root unless the
  operator hands that step to you. Root runs only the absolute staged path with
  a system interpreter: `sudo /bin/bash /opt/agent-workstation-kit/scripts/...`.
  Never `sudo` a file in a user checkout.
- **Never print or commit secrets:** passwords, tokens, keys, recovery codes,
  licence files or pairing links. Never read credential files you do not need.
- **No private details here:** no organisation, person, host, account or
  customer names in this repository. Use the neutral examples (`acme`, `lab`).
- **Stop for a person** before `sudo`, disk-encryption changes, account
  creation, firewall or remote-access changes, vendor sign-in, or destructive
  cleanup. Hand over sign-ins, MFA, licences, purchases, console and reboot
  steps, tailnet policy and approvals (section 6).
- **Attest only what you observed.** Pass `--execution-context host` only when
  you run in an ordinary terminal on the target itself, outside any agent
  sandbox. Otherwise leave the default and hand the readiness run to the person.
- **Never weaken security** (sandbox, branch protection, endpoint protection,
  disk encryption, audit logging, OS security) to make a tool work. Report instead.
- **Preserve user configuration** unless a guide allows a managed replacement
  and a backup exists.
- **No remote or publication actions** without explicit approval: creating a
  remote, pushing, opening a pull request, publishing a release, changing
  visibility. An agent never approves or merges its own pull request.

## 4. Task recipes

Each recipe lists commands, what success looks like, how to verify, and when to
stop. Run commands from the kit checkout unless a step says otherwise.

### Set up a new Ubuntu machine

`FLEET` is the private fleet checkout. `PRIVATE` is the absolute path of a
private folder outside every kit checkout; the kit refuses plans and journals
inside a checkout.

1. Follow [day-zero Linux](docs/runbooks/day-zero-linux.md) up to the readiness
   check. The firmware, installer and disk-encryption steps are human steps.
2. Readiness, run by the person in an ordinary terminal on the target (see the
   attestation rule above): `python3 scripts/fleetctl.py --fleet-root FLEET setup machines/HOST.toml --execution-context host`
   - Success: no `FAIL` line, then `READY: ...` and a hand-off prompt. Exit 0.
   - Stop: on any `FAIL`, report it and ask. Report `NEXT` lines; they are not failures.
3. Export the plan: `scripts/setup-workstation.sh plan --fleet-root FLEET --profile machines/HOST.toml --export-plan PRIVATE/execution-plan.json`
4. Work through the scopes in order: `packages`, `machine`, `agent`, then
   `resources` (only after the node is measured under load) and `audit`.
   - Preview: `scripts/setup-workstation.sh run --plan-file PRIVATE/execution-plan.json --scope SCOPE`
     prints the phases and an execution plan ID. Show it to the person and stop.
   - Apply, after approval: the same command plus `--apply --approve-plan ID --confirm-target HOST --execution-context host --journal JOURNAL`.
     `JOURNAL` is one private journal per executing account, in a folder that
     account owns with mode `0700`.
   - Root scopes are run by the person from the staged copy, with a journal in
     a root-owned folder and `--confirm-recovery-tested` once console recovery
     has actually been tested: on Ubuntu every scope except `agent`, on macOS
     every scope except `packages` and `agent`. See [resumable setup](docs/runbooks/resumable-setup.md).
   - Success: each phase prints `PASS NAME live postconditions`. Exit 0.
   - Verify: `scripts/setup-workstation.sh status --plan-file PRIVATE/execution-plan.json --scope SCOPE --journal JOURNAL`
     lists each phase as recorded and names the next candidate.
   - Stop: exit 3 (a person must act), 2 (wrong account or refused input) or 1 (a phase failed).

These commands change in PR 2 to `setup-workstation.sh admin`, `user` and `doctor`.

### Set up a new Mac

Follow [day-zero macOS](docs/runbooks/day-zero-macos.md), then the same steps as
Ubuntu with a `machines/HOST.toml` whose platform is `macos`. Run the `packages`
scope as the Homebrew owner, never root. Setup Assistant, MDM, FileVault,
privacy prompts and Screen Sharing are human steps.

### Run the doctor and act on its report

1. `scripts/setup-workstation.sh doctor` checks OS, Python, git, ssh and the
   package manager. It changes nothing.
2. For the full host audit, the person runs
   `sudo /bin/bash /opt/agent-workstation-kit/scripts/validate-host.sh AGENT_ACCOUNT`.
3. Act on each `FAIL`: rerun the phase that owns it (preview first), or hand it
   over. Report each `WARN` without acting on it.

`doctor --fix`, `--json` and the update and expiry checks arrive in PR 3.

### Add or remove an optional tool for one machine

1. Edit the machine profile in the private fleet. Today the optional switches
   are `tooling.gws` (`install` or `skip`) and the package lists
   `tooling.apt_packages`, `tooling.brew_formulae` and `tooling.brew_casks`.
2. Validate: `python3 scripts/fleetctl.py --fleet-root FLEET validate machines/HOST.toml --ready`.
   Success prints no `ERROR` lines and exits 0.
3. Commit the profile through the fleet's review, export a new plan, and preview
   the `packages` or `agent` scope.

PR 2a replaces this with one `[components]` table in the profile.

### Add a new component to the kit

1. Add the install step to the phase script that owns it, or the tool to
   `config/mise.toml` or `config/packages.toml`. It must preview by default,
   check before acting, and be idempotent.
2. Add its live check to `scripts/lib/phase_verification.py` and its tests.
3. Record the upstream source in [primary sources](docs/11-primary-sources.md).
4. Add a `CHANGELOG.md` entry and update this file if a command or path changed.
5. Run `make ci-check` and `make public-check`.

### Update after an upstream change

1. Check the component's upstream source in [primary sources](docs/11-primary-sources.md).
2. Change the pin, checksum or config key in a focused pull request with a test.
3. Note the change under `Changed` or `Fixed` in `CHANGELOG.md`.

The update-watch register and `doctor --updates` arrive in PR 3.

### Cut a release

Follow [releasing](docs/maintenance/releasing.md). Prepare the release pull
request; the owner decides, tags and publishes.

### Adopt the kit in a private fleet

1. Copy `templates/private-fleet/` into a new private repository; protect its
   default branch and require review.
2. Set `kit.lock` to the kit release you use.
3. Create a profile: `scripts/setup-workstation.sh init machines/HOST.toml --fleet-root FLEET --context work --namespace NS --hostname HOST --platform linux --human NAME --admin ADMIN`
   previews; add `--write` to create the draft. Review it in the fleet repository.
4. Validate the fleet: `python3 scripts/validate-fleet.py FLEET`. Exit 0 means valid.

See [guided fleet setup](docs/runbooks/guided-fleet-setup.md).

## 5. Reading the output

There is no `--json` output yet; PR 2 adds it. Today, parse these lines:

| Source | Line format |
|---|---|
| Phase verification | `PASS NAME live postconditions`, `FAIL NAME live postconditions`, `REASON NAME: TEXT` |
| Readiness (`fleetctl setup`, `start-*-pilot.py`) | `STATUS NAME: DETAIL` with STATUS `PASS`, `WARN`, `FAIL` or `NEXT`, then `READY` or `NOT READY` |
| `validate-host.sh` | `PASS TEXT`, `WARN TEXT`, `FAIL TEXT` |
| `fleetctl validate` | `ERROR FIELD.PATH: MESSAGE` on stderr |
| Executor | `EXEC COMMAND`, `SKIP NAME: ...`, `DRIFT NAME: ...`, `HUMAN GATE NAME: NOTE`, `STOP NAME: ...` |

Exit codes of `setup-workstation.sh`:

| Code | Meaning |
|---|---|
| 0 | Done, or preview shown |
| 1 | A check or phase failed |
| 2 | Usage error or a refused input, including a root scope or the agent scope run from the wrong account |
| 3 | A person must act (a human gate in the plan, or a hand-over in the guided walkthrough) |
| 5 | Refused as root: not the staged copy, or not the system bash |
| 130 | Cancelled |

## 6. Hand-over format for human-only steps

When a step needs a person, stop and print one block per step:

```text
HAND-OVER
  Machine:  the target workstation | the client Mac | the private fleet repo
  Account:  the account that must run it (for example the admin account)
  Step:     what the person does, in one line
  Command:  the exact command or click path, with placeholders marked
  Verify:   the command the agent reruns afterwards to confirm it
```

Never collect the secret yourself. Do not continue past the step until the
verify command passes.

## 7. Contributing changes

- Work on a `{type}/{kebab-name}` branch; never commit to `main`.
- Use Conventional Commit subjects (`.codeflow/policy.json`), at most three
  short body bullets, and no AI attribution or emoji. Keep each commit focused,
  and mark a breaking interface or schema change explicitly.
- Keep the hooks on: `git config core.hooksPath .codeflow/git-hooks`.
- Every change to `scripts/`, `config/` or `templates/` adds a bullet under
  `## [Unreleased]` in `CHANGELOG.md`; `make repo-check` enforces it.
- When a change adds, renames or removes a command, flag or file location,
  update this file in the same pull request. `make repo-check` checks that the
  paths and `setup-workstation.sh` actions named here exist.
- Run `make check` while working and `make ci-check` plus `make public-check`
  before review. List live-machine gaps in the pull request.
- Treat `config/profiles/*.example.toml` as templates. Use ignored
  `config/profiles/*.local.toml` files only for experiments; approved profiles
  belong in the private fleet repository.
- Run the strict `make public-check` before every release, and complete
  [the release checklist](docs/13-public-release-checklist.md) before a final
  (non-rc) release; see [releasing](docs/maintenance/releasing.md).
- A person reviews and merges every pull request.
