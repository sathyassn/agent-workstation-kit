SHELL := /bin/sh
PYTHON ?= python3

.PHONY: check ci-check public-check public-draft-check repo-check skill-check shell-check unit-check python-check

check: shell-check python-check unit-check skill-check repo-check public-draft-check

ci-check: REQUIRE_SHELLCHECK=1
ci-check: check

public-check:
	@$(PYTHON) tests/check_public_release.py

public-draft-check:
	@$(PYTHON) tests/check_public_release.py --allow-missing-license

python-check:
	@$(PYTHON) -m py_compile scripts/*.py tests/*.py

shell-check:
	@for file in scripts/*.sh scripts/lib/*.sh agentctl/*; do \
		[ -f "$$file" ] || continue; \
		case "$$file" in *.md|*.yaml) continue ;; esac; \
		bash -n "$$file"; \
	done
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck scripts/*.sh scripts/lib/*.sh agentctl/*; \
	elif [ "$(REQUIRE_SHELLCHECK)" = "1" ]; then \
		echo "shellcheck is required for ci-check" >&2; \
		exit 1; \
	else \
		echo "shellcheck unavailable; syntax validation only (use make ci-check for the strict gate)"; \
	fi

skill-check:
	@$(PYTHON) tests/check_skill.py

unit-check:
	@$(PYTHON) -m unittest discover -s tests -p 'test_*.py'
	@bash tests/test_shell_behaviour.sh

repo-check:
	@$(PYTHON) tests/check_repository.py
