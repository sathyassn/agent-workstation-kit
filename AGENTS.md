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

Each recipe gives the commands, what success looks like, how to verify it, and
when to stop and ask. Run commands from the kit checkout unless a step says
otherwise. Placeholders:

- `FLEET`: the private fleet checkout.
- `HOST`: the machine's technical hostname, as in `machines/HOST.toml`.
- `PRIVATE`: the absolute path of a private folder outside every kit checkout.
  The kit refuses plans and journals inside a checkout.
- `JOURNAL`: one private journal file per executing account, in a folder that
  account owns with mode `0700`.

Work marked "planned: issue #5, step N" is not built yet. Step N is the order
in the plan on issue #5; it is not a pull request number.

### Set up a new Ubuntu machine

1. Follow [day-zero Linux](docs/runbooks/day-zero-linux.md) up to its readiness
   check. The firmware, installer and disk-encryption steps are human steps.

2. Readiness. The person runs this in an ordinary terminal on the target (see
   the attestation rule in section 3):

   ```bash
   python3 scripts/fleetctl.py --fleet-root FLEET \
     setup machines/HOST.toml --execution-context host
   ```

   - Success: no `FAIL` line, then `READY: ...` and a hand-off prompt. Exit 0.
   - Stop: on any `FAIL`, report it and ask. Report `NEXT` lines; they are not
     failures.

3. Export the plan:

   ```bash
   scripts/setup-workstation.sh plan --fleet-root FLEET \
     --profile machines/HOST.toml --export-plan PRIVATE/execution-plan.json
   ```

   - Success: the settings and scope tables, then `Exported execution bundle`. Exit 0.
   - Stop: on any `ERROR` line.

4. Run the scopes in this order: `packages`, `machine`, `agent`, `resources`
   (only after the node has been measured under load), then `audit`. For each
   scope:

   1. Preview, then show the output to the person and stop:

      ```bash
      scripts/setup-workstation.sh run \
        --plan-file PRIVATE/execution-plan.json --scope SCOPE
      ```

      It prints the phases and an execution plan ID.
   2. Apply after the person approves that preview:

      ```bash
      scripts/setup-workstation.sh run \
        --plan-file PRIVATE/execution-plan.json --scope SCOPE \
        --apply --approve-plan ID --confirm-target HOST \
        --execution-context host --journal JOURNAL
      ```

   3. Verify:

      ```bash
      scripts/setup-workstation.sh status \
        --plan-file PRIVATE/execution-plan.json --scope SCOPE --journal JOURNAL
      ```

      It lists each phase as recorded and names the next candidate.

   - Success: each phase prints `PASS NAME live postconditions`. Exit 0.
   - Stop: exit 3 (a person must act), 2 (wrong account or refused input) or 1
     (a phase failed).

5. **Root scopes are the person's.** On Ubuntu every scope except `agent` runs
   as root. The person runs it from the staged copy, reading the plan staged
   under `/opt/agent-workstation-fleet/plans/`, with a journal in a root-owned
   folder. They add `--confirm-recovery-tested` only once console recovery has
   actually been tested. See [resumable setup](docs/runbooks/resumable-setup.md).

Planned: issue #5, step 2. These steps become `setup-workstation.sh admin`,
`user` and `doctor`.

### Set up a new Mac

1. Follow [day-zero macOS](docs/runbooks/day-zero-macos.md) up to its readiness
   check. Setup Assistant, MDM enrolment, FileVault, privacy prompts and Screen
   Sharing are human steps. Use the day-zero `"$PYTHON_BIN"` wherever a step
   runs Python.
2. Run readiness, export the plan, and run the scopes exactly as in steps 2 to 4
   of the Ubuntu recipe, with a profile whose platform is `macos`.
3. Run the `packages` scope as the named Homebrew owner, never as root. Run the
   `agent` scope in the agent account's own session.
4. **Root scopes are the person's.** On macOS, `machine`, `resources` and
   `audit` run as root from the staged copy, as in step 5 of the Ubuntu recipe.

- Success: readiness ends `READY: ...`, and each scope's phases print `PASS NAME
  live postconditions`. Exit 0.
- Verify: `status` for each scope, as in Ubuntu step 4.3.
- Stop: at every human step above, and on exit 3, 2 or 1.

### Run the doctor and act on its report

1. Run the doctor. It changes nothing:

   ```bash
   scripts/setup-workstation.sh doctor
   ```

2. For the full host audit, the person runs:

   ```bash
   sudo /bin/bash /opt/agent-workstation-kit/scripts/validate-host.sh AGENT_ACCOUNT
   ```

- Success: the doctor's table shows no `missing` entry and exits 0. The audit
  prints no `FAIL` line.
- Verify: rerun both after acting on a finding.
- Stop: for each `FAIL`, either rerun the phase that owns it (preview first) or
  hand it over. Report each `WARN` without acting on it.

`doctor --fix` and the update and expiry checks are planned: issue #5, step 3.

### Add or remove an optional tool for one machine

1. Edit the machine profile in the private fleet. Today the optional switches
   are `tooling.gws` (`install` or `skip`) and the package lists
   `tooling.apt_packages`, `tooling.brew_formulae` and `tooling.brew_casks`.
2. Validate:

   ```bash
   python3 scripts/fleetctl.py --fleet-root FLEET validate machines/HOST.toml --ready
   ```

3. Commit the profile through the fleet's review, export a new plan, and preview
   the `packages` or `agent` scope.

- Success: `validate` prints no `ERROR` line and exits 0, and the scope preview
  lists the added or removed package.
- Verify: after the approved apply, `status` for that scope.
- Stop: on any `ERROR` line, or if the preview changes anything other than the
  tool you edited.

One `[components]` table in the profile replaces these switches (planned:
issue #5, step 4).

### Add a new component to the kit

1. Add the install step to the phase script that owns it, or the tool to
   `config/mise.toml` or `config/packages.toml`. It must preview by default,
   check before acting, and be idempotent.
2. Add its live check to `scripts/lib/phase_verification.py`, with tests.
3. Record the upstream source in [primary sources](docs/11-primary-sources.md).
4. Add a `CHANGELOG.md` entry, and update this file if a command or path changed.
5. Run the gates:

   ```bash
   make ci-check
   make public-check
   ```

- Success: both gates exit 0, and a test shows the preview changes nothing and
  a repeat apply changes nothing.
- Verify: the new tests fail without the change.
- Stop: if the component needs `sudo`, a secret, or a new network source that is
  not pinned and verified. Ask first.

### Update after an upstream change

1. Check the component's upstream source in [primary sources](docs/11-primary-sources.md).
2. Change the pin, checksum or config key in a focused pull request, with a test.
3. Note the change under `Changed` or `Fixed` in `CHANGELOG.md`.

- Success: `make ci-check` and `make public-check` exit 0 with the new pin.
- Verify: the recorded checksum matches the vendor's published value.
- Stop: if the vendor publishes no strong checksum or signature, or if the
  change alters a default or a security setting. Ask first.

The update-watch register and `doctor --updates` are planned: issue #5, step 3.

### Cut a release

Follow [releasing](docs/maintenance/releasing.md). Prepare the release pull
request; the owner decides, tags and publishes.

- Success: the release pull request passes both gates.
- Verify: `make ci-check` and `make public-check` exit 0 on the release branch.
- Stop: before any tag or release; those are the owner's.

### Adopt the kit in a private fleet

1. Copy `templates/private-fleet/` into a new private repository. Protect its
   default branch and require review.
2. Set `kit.lock` to the kit version you use.
3. Preview a profile, then add `--write` to create the draft:

   ```bash
   scripts/setup-workstation.sh init machines/HOST.toml --fleet-root FLEET \
     --context work --namespace NS --hostname HOST --platform linux \
     --human NAME --admin ADMIN
   ```

4. Review the draft in the fleet repository, then validate the whole fleet:

   ```bash
   python3 scripts/validate-fleet.py FLEET
   ```

- Success: the preview names the draft and writes nothing. With `--write`, the
  draft exists. `validate-fleet.py` exits 0.
- Verify: rerun `validate-fleet.py` after every profile change.
- Stop: on any `ERROR`, or if a value would name a real organisation in this
  public repository.

See [guided fleet setup](docs/runbooks/guided-fleet-setup.md).

## 5. Reading the output

There is no `--json` output yet (planned: issue #5, step 2). Today, parse these
lines:

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
  `## [Unreleased]` in `CHANGELOG.md`; `make repo-check` enforces it against
  `origin/main` (or `CHANGELOG_BASE`), and prints a note when it has no base.
- When a change adds, renames or removes a command, flag or file location,
  update this file in the same pull request. `make repo-check` checks that
  every path named here exists, and that each `setup-workstation.sh` action and
  `fleetctl.py` subcommand exists and defines the flags used with it (against
  its own `--help`). It does not check other scripts' flags, prose, or lines
  marked "planned: issue #5".
- Run `make check` while working and `make ci-check` plus `make public-check`
  before review. List live-machine gaps in the pull request.
- Treat `config/profiles/*.example.toml` as templates. Use ignored
  `config/profiles/*.local.toml` files only for experiments; approved profiles
  belong in the private fleet repository.
- Run the strict `make public-check` before every release, and complete
  [the release checklist](docs/13-public-release-checklist.md) before a final
  (non-rc) release; see [releasing](docs/maintenance/releasing.md).
- A person reviews and merges every pull request.
