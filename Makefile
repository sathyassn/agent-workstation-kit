SHELL := /bin/sh
PYTHON ?= python3
BASH_BIN ?= bash

.PHONY: bash-check check ci-check git-discipline-check public-check public-draft-check repo-check skill-check shell-check unit-check python-check

check: bash-check shell-check python-check unit-check skill-check repo-check git-discipline-check public-draft-check

ci-check: REQUIRE_SHELLCHECK=1
ci-check: check

public-check:
	@$(PYTHON) tests/check_public_release.py

public-draft-check:
	@$(PYTHON) tests/check_public_release.py --allow-missing-license

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

unit-check: bash-check
	@$(PYTHON) -m unittest discover -s tests -p 'test_*.py'
	@"$(BASH_BIN)" tests/test_shell_behaviour.sh
	@"$(BASH_BIN)" tests/test_resource_policy.sh

repo-check:
	@$(PYTHON) tests/check_repository.py
	@$(PYTHON) tests/catalog_tables.py

git-discipline-check:
	@$(PYTHON) scripts/check_git_discipline.py policy
