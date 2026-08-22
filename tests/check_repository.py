#!/usr/bin/env python3

from __future__ import annotations

import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MARKDOWN_LINK = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
FORBIDDEN_NAMES = {".env", "id_rsa", "id_ed25519", "credentials.json", "auth.json"}
PLACEHOLDER = re.compile(r"\b(TODO|FIXME|CHANGEME)\b")
SECRET_LIKE = re.compile(r"(?:AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{30,}|glpat-[A-Za-z0-9_-]{20,}|sk-[A-Za-z0-9_-]{24,})")
PERSONAL_PATH = re.compile(r"/(?:Users|home)/[^/\s]+/")


def main() -> int:
    failures: list[str] = []

    for path in ROOT.rglob("*"):
        if ".git" in path.parts or not path.is_file():
            continue
        if path.name in FORBIDDEN_NAMES or path.suffix in {".key", ".pem", ".p12"}:
            failures.append(f"forbidden credential-like file: {path.relative_to(ROOT)}")
        try:
            content = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            content = ""
        if SECRET_LIKE.search(content):
            failures.append(f"secret-like value in {path.relative_to(ROOT)}")
        if PERSONAL_PATH.search(content):
            failures.append(f"personal absolute path in {path.relative_to(ROOT)}")
        if path.suffix != ".md":
            continue
        text = content
        for match in MARKDOWN_LINK.finditer(text):
            target = match.group(1).split("#", 1)[0]
            if not target or "://" in target or target.startswith("mailto:"):
                continue
            resolved = (path.parent / target).resolve()
            if not resolved.exists():
                failures.append(
                    f"broken relative link in {path.relative_to(ROOT)}: {target}"
                )
        if PLACEHOLDER.search(text):
            failures.append(f"unfinished marker in {path.relative_to(ROOT)}")

    required_executables = [
        *ROOT.glob("scripts/*.sh"),
        *ROOT.glob("scripts/*.py"),
        *ROOT.glob("agentctl/*"),
    ]
    for path in required_executables:
        if path.is_file() and not path.stat().st_mode & 0o111:
            failures.append(f"expected executable file is not executable: {path.relative_to(ROOT)}")

    if failures:
        print("Repository checks failed:")
        for failure in failures:
            print(f"- {failure}")
        return 1

    print("Repository checks passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
