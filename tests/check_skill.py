#!/usr/bin/env python3
"""Portable structural validation for the bundled setup skill."""

from __future__ import annotations

import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SKILL = ROOT / "skills/setup-agent-dev-machine/SKILL.md"
NAME = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")


def main() -> int:
    text = SKILL.read_text(encoding="utf-8")
    failures: list[str] = []
    if not text.startswith("---\n") or "\n---\n" not in text[4:]:
        failures.append("SKILL.md must start with YAML frontmatter")
    else:
        frontmatter = text.split("---\n", 2)[1]
        fields: dict[str, str] = {}
        for line in frontmatter.splitlines():
            if ":" in line:
                key, value = line.split(":", 1)
                fields[key.strip()] = value.strip()
        if set(fields) != {"name", "description"}:
            failures.append("frontmatter must contain only name and description")
        if not NAME.fullmatch(fields.get("name", "")):
            failures.append("skill name must be lowercase kebab-case")
        description = fields.get("description", "")
        if not 20 <= len(description) <= 1024:
            failures.append("skill description must be 20-1024 characters")
    if len(text.splitlines()) > 500:
        failures.append("SKILL.md exceeds the 500-line maintainability limit")

    for target in re.findall(r"\[[^]]+\]\(([^)]+)\)", text):
        if "://" in target or target.startswith("mailto:"):
            continue
        if not (SKILL.parent / target.split("#", 1)[0]).resolve().exists():
            failures.append(f"broken skill-relative link: {target}")

    if failures:
        for failure in failures:
            print(f"- {failure}")
        return 1
    print("Skill structure passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
