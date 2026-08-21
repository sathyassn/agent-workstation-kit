SHELL := /bin/sh

.PHONY: check repo-check skill-check shell-check unit-check python-check

check: shell-check python-check unit-check skill-check repo-check

python-check:
	@python3 -m py_compile scripts/*.py tests/*.py

shell-check:
	@for file in scripts/*.sh scripts/lib/*.sh agentctl/*; do \
		[ -f "$$file" ] || continue; \
		case "$$file" in *.md|*.yaml) continue ;; esac; \
		bash -n "$$file"; \
	done
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck scripts/*.sh scripts/lib/*.sh agentctl/*; \
	else \
		echo "shellcheck unavailable; syntax validation only"; \
	fi

skill-check:
	@python3 tests/check_skill.py

unit-check:
	@python3 -m unittest discover -s tests -p 'test_*.py'
	@bash tests/test_shell_behaviour.sh

repo-check:
	@python3 tests/check_repository.py
