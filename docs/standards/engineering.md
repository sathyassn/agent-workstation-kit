# Engineering standard

[Documentation home](../README.md)

These are acceptance criteria for every pull request in this repository. CI
enforces what it can; the review checklist covers the rest. The source is
the user's requirement of 2026-11-21 to follow good software development
practice, relayed by the supervisor and approved as a process change.

## One source of truth

- The schema, the unit catalog (`registry/units.toml`), presets, the tool
  registry and the references registry (`registry/sources.toml`) are each
  declared once.
- Go types, docs tables, interview content and validation are generated
  from them. CI fails on stale generated output, on a definition repeated in
  two places, and on repeated literals (`goconst`, plus a check across the
  TOML registries).

## Types and validation

- IDs and enums are typed, never loose strings: unit IDs, verbs, stages,
  scopes, statuses, exit codes and schema keys.
- Input is validated at every boundary: config, CLI flags, the JSON
  protocol and the output of external tools.
- Errors are typed or sentinel, wrapped with `%w`.
- Constructors make invalid states unrepresentable.

## Go

- Small interfaces at the consumer; no package-level mutable state;
  `context.Context` through every long call.
- `golangci-lint` with `.golangci.yml`, and `govulncheck`, in CI.
- Go CI runs the engine race tests and lints the Go packages with the
  `faultinject` tag on Linux and macOS. `make go-check` runs the tagged race
  tests locally and needs a C compiler for `-race`. The full CodeFlow gate
  also runs tagged lint.
- Go CI and `make go-check` build their own binaries for the release targets
  Linux amd64, Linux arm64 and macOS arm64 with cgo disabled. The check scans
  each binary's symbols, bytes and build settings for fault injection.
- Units never print or call `os.Exit`; they emit events through
  `engine.Tx`. Only `cmd/` writes to the terminal or maps errors to exit
  codes.
- Platform differences sit behind interfaces, not scattered OS branches.
- The `go` line in `go.mod` is a security pin, not only a minimum: CI's
  `setup-go` reads it, so it decides the toolchain CI runs. Raise it when
  `govulncheck` reports a reachable standard-library vulnerability fixed in
  a newer patch release, in the PR that makes the path reachable, or in
  its own build PR when the finding is on `main`.

## Tests

- Table-driven unit tests, with `t.TempDir` and no network.
- Golden and snapshot tests for JSON, plain and terminal output (`teatest`).
- Fuzz tests for every parser: config, URLs and redirect URIs, the JSON
  protocol, and the reverse-proxy allowlist reader.
- Integration tests per unit, through the real engine in a test root.
- End-to-end tests on both OSes: Linux containers and VMs, and macOS CI
  runners. They cover apply, interrupt and resume, and revert to baseline
  with the match digest.
- Coverage floors on `internal/engine`, `internal/planner` and
  `internal/schema`, set by EPC-001 and enforced in CI.

## Defaults

- Every setting applies its best-practice default when it is unset
  (principle 13), shows it in `plan` with its reason, and links its source
  in `registry/sources.toml`. Defaults may differ by purpose, tier and form
  factor, from one table.
- Tests show each default is applied when the setting is unset, and that
  every opt-out works.

## Versioning and changes

- Releases follow Semantic Versioning and are signed. The Go `wskit` starts
  at 1.0.0: pre-releases are `1.0.0-alpha.N` (P1 and P2),
  `1.0.0-beta.N` (P3) and `1.0.0-rc.N` (P4). The Python `0.2.0-rc` line is
  frozen for fixes only and is never released as a final 0.2.0.
- From 1.0.0, breaking config or CLI changes come only in a major version,
  after one minor release of deprecation warnings. The machine config has a
  schema version, and `wskit migrate` previews and reverses config
  migrations.
- JSON output and the `next` schema are versioned.
- Every change to the unit catalog, the registries or presets is noted in
  `CHANGELOG.md`.

## Process

- Small, single-purpose pull requests.
- An ADR for each significant decision.
- The [review checklist](review-checklist.md) for every review.
- Core and safety PRs (engine, planner, schema, root units, CI authority,
  the public-safety scan) get a cross-lineage adversarial review and stress
  evidence before merge.
- Adopting CodeFlow in a repository: before the first adoption PR opens,
  validate `.codeflow/policy.json` against the pinned CodeFlow schema,
  including the keys the security-review job needs (`git.security_review`
  and `git.dep_audit`). The PR that first adds CodeFlow fails "codeflow
  gates" by design, because `main` has no `project.toml` yet, and the PR
  body says so.
- When strict up-to-date checks leave a PR behind its base after another
  merge, merge `main` into it; never force push. Where a replacement branch
  is used instead, verify it with `git range-diff` or an identical tree
  hash. Wait for the checks, then merge with `--match-head-commit`.

## CI authority and scaffold updates

- `ci.yml` and `go.yml` run on pull requests targeting `main`, pushes to
  `main`, and manual dispatches. CodeFlow and the public-safety scan also
  run for pull requests targeting integration branches.
- CodeFlow 3.1.1's workflows use no secret: its policy workflow runs on
  `pull_request_target` and checks out only the base commit, so a pull
  request cannot change the job that judges it. When a later CodeFlow
  release adds an authority job with an App key, that job runs only for
  pull requests from this repository, never from forks, behind an
  environment with a required reviewer, and the upgrade PR proves that fork
  pull requests see no secret.
- CodeFlow scaffold updates (`codeflow update`) land in their own pull
  request, reviewed separately, including every link they add.
- The branch policy allows the `codeflow/` prefix for CodeFlow's ID registry
  branch. CodeFlow accepts prefixes only, so this is the narrowest form
  available; it narrows to the one branch name, `codeflow/registry`, once
  CodeFlow supports that (its issue 922).
- CodeFlow is pinned to the released 3.1.1, with its release digests in
  `.codeflow/project.toml`. A move to a later release is a pull request of
  its own.

## Public identifiers

First-party files name no person, organisation, host or account, with two
documented exceptions, both public project addresses that must resolve: the
repository's own address (`github.com/sathyassn/agent-workstation-kit`, also
the Go module path) and CodeFlow's release download address in the CodeFlow
workflows. Anything else is a public-safety finding.
