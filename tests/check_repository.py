#!/usr/bin/env python3

from __future__ import annotations

import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MARKDOWN_LINK = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
MARKDOWN_HEADING = re.compile(r"^#{1,6}\s+(.+?)\s*$", re.MULTILINE)
FENCED_BLOCK = re.compile(r"^```[^\n]*\n.*?^```\s*$", re.MULTILINE | re.DOTALL)
DOCS_HOME_LINK = re.compile(r"\[Documentation home\]\((?:\.\./)?README\.md\)")
FORBIDDEN_NAMES = {".env", "id_rsa", "id_ed25519", "credentials.json", "auth.json"}
PLACEHOLDER = re.compile(r"\b(TODO|FIXME|CHANGEME)\b")
SECRET_LIKE = re.compile(r"(?:AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{30,}|glpat-[A-Za-z0-9_-]{20,}|sk-[A-Za-z0-9_-]{24,})")
PERSONAL_PATH = re.compile(r"/(?:Users|home)/[^/\s]+/")


def heading_slug(heading: str) -> str:
    """Return the GitHub-style slug used by this repository's simple headings."""
    value = re.sub(r"[^\w\- ]", "", heading.lower(), flags=re.UNICODE)
    return re.sub(r"\s+", "-", value.strip())


def markdown_headings(text: str) -> set[str]:
    """Collect headings while ignoring examples inside fenced code blocks."""
    visible_text = FENCED_BLOCK.sub("", text)
    return {heading_slug(heading) for heading in MARKDOWN_HEADING.findall(visible_text)}


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
            raw_target = match.group(1)
            target, separator, anchor = raw_target.partition("#")
            if not target or "://" in target or target.startswith("mailto:"):
                resolved = path if not target and separator else None
            else:
                resolved = (path.parent / target).resolve()
            if resolved is None:
                continue
            if not resolved.exists():
                failures.append(
                    f"broken relative link in {path.relative_to(ROOT)}: {target}"
                )
                continue
            if anchor and resolved.suffix == ".md":
                headings = markdown_headings(resolved.read_text(encoding="utf-8"))
                if anchor not in headings:
                    failures.append(
                        f"broken heading link in {path.relative_to(ROOT)}: {raw_target}"
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

    readme_path = ROOT / "README.md"
    readme = readme_path.read_text(encoding="utf-8") if readme_path.is_file() else ""
    if not readme:
        failures.append("root README is missing or empty")
    else:
        for required_link in ("docs/README.md", "docs/runbooks/day-zero-linux.md"):
            if required_link not in readme:
                failures.append(f"root README does not link required entry point: {required_link}")

    docs_home_path = ROOT / "docs/README.md"
    docs_home = docs_home_path.read_text(encoding="utf-8") if docs_home_path.is_file() else ""
    if not docs_home:
        failures.append("documentation map is missing or empty: docs/README.md")
    for document in sorted((ROOT / "docs").rglob("*.md")):
        if document == ROOT / "docs/README.md":
            continue
        relative = document.relative_to(ROOT / "docs").as_posix()
        if relative not in docs_home:
            failures.append(f"documentation map does not link: docs/{relative}")
        if not DOCS_HOME_LINK.search(document.read_text(encoding="utf-8")):
            failures.append(f"documentation page has no home navigation: docs/{relative}")

    if failures:
        print("Repository checks failed:")
        for failure in failures:
            print(f"- {failure}")
        return 1

    print("Repository checks passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
