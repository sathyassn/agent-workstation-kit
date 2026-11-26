SHELL := /bin/sh
PYTHON ?= python3
BASH_BIN ?= bash
IDENTITY_SOURCE ?= $(if $(filter true,$(GITHUB_ACTIONS)),ci,local)

.PHONY: bash-check check ci-check git-discipline-check go-check public-check public-draft-check repo-check repo-ci-check repo-non-privacy-check skill-check shell-check unit-check unit-non-privacy-check unit-public-scan-check python-check

check: bash-check shell-check python-check unit-check skill-check repo-check git-discipline-check public-draft-check

ci-check: REQUIRE_SHELLCHECK=1
ci-check: check

# Non-privacy repository gate for required PR-head CI (`repository-validation`).
# The authoritative privacy verdict comes solely from `.github/workflows/public-scan.yml`.
repo-ci-check: REQUIRE_SHELLCHECK=1
repo-ci-check: bash-check shell-check python-check unit-non-privacy-check skill-check repo-non-privacy-check git-discipline-check

public-check:
	@$(PYTHON) tests/check_public_release.py --identity-source $(IDENTITY_SOURCE)

public-draft-check:
	@$(PYTHON) tests/check_public_release.py --allow-missing-license --identity-source $(IDENTITY_SOURCE)

go-check:
	go test ./...
	go test -race -tags faultinject ./internal/engine/...
	WSKIT_TEST_RELEASE_BINARIES=1 go test -count=1 -timeout 15m -v ./tests/release/

python-check:
	@$(PYTHON) -m py_compile scripts/*.py tests/*.py

bash-check:
	@if ! command -v "$(BASH_BIN)" >/dev/null 2>&1; then \
		echo "Bash 4 or newer is required; command not found: $(BASH_BIN)" >&2; \
		exit 1; \
	fi
	@major=$$("$(BASH_BIN)" -c 'printf "%s\n" "$${BASH_VERSINFO[0]}"' 2>/dev/null); \
	case "$$major" in ''|*[!0-9]*) major=0 ;; esac; \
	if [ "$$major" -lt 4 ]; then \
		"$(BASH_BIN)" --version | sed -n '1p' >&2; \
		echo 'Bash 4 or newer is required for Linux shell validation.' >&2; \
		echo 'On macOS: brew install bash; make BASH_BIN="$$(brew --prefix bash)/bin/bash" ci-check' >&2; \
		exit 1; \
	fi

shell-check: bash-check
	@for file in scripts/*.sh scripts/agent-work-shell scripts/lib/*.sh agentctl/* .codeflow/git-hooks/*; do \
		[ -f "$$file" ] || continue; \
		case "$$file" in *.md|*.yaml) continue ;; esac; \
		"$(BASH_BIN)" -n "$$file"; \
	done
	@if command -v shellcheck >/dev/null 2>&1; then \
		set --; \
		for file in scripts/*.sh scripts/agent-work-shell scripts/lib/*.sh agentctl/* .codeflow/git-hooks/*; do \
			[ -f "$$file" ] || continue; \
			set -- "$$@" "$$file"; \
		done; \
		if [ "$$#" -gt 0 ]; then shellcheck "$$@"; fi; \
	elif [ "$(REQUIRE_SHELLCHECK)" = "1" ]; then \
		echo "shellcheck is required for ci-check" >&2; \
		exit 1; \
	else \
		echo "shellcheck unavailable; syntax validation only (use make ci-check for the strict gate)"; \
	fi

skill-check:
	@$(PYTHON) tests/check_skill.py

unit-non-privacy-check: bash-check
	@$(PYTHON) -B -c 'import os, pathlib, sys, unittest; tdir = os.path.abspath("tests"); sys.path.insert(0, tdir); loader = unittest.TestLoader(); suite = unittest.TestSuite(loader.discover(tdir, pattern=p.name, top_level_dir=tdir) for p in sorted(pathlib.Path(tdir).glob("test_*.py")) if not p.name.startswith("test_public_")); res = unittest.TextTestRunner().run(suite); sys.exit(0 if res.wasSuccessful() and res.testsRun > 0 else 1)'
	@"$(BASH_BIN)" tests/test_shell_behaviour.sh
	@"$(BASH_BIN)" tests/test_resource_policy.sh

unit-public-scan-check:
	@$(PYTHON) -B -m unittest discover -s tests -t tests -p 'test_public_*.py'

unit-check: unit-non-privacy-check unit-public-scan-check

repo-non-privacy-check:
	@$(PYTHON) tests/check_repository.py --skip-privacy-checks
	@$(PYTHON) tests/catalog_tables.py

repo-check:
	@$(PYTHON) tests/check_repository.py
	@$(PYTHON) tests/catalog_tables.py

git-discipline-check:
	@$(PYTHON) scripts/check_git_discipline.py policy
