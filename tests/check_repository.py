#!/usr/bin/env python3

from __future__ import annotations

import os
import re
import subprocess
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
BARE_KVM_MODEL = re.compile(r"(?<!GL-)\bRM(?:1|1PE|4PE)\b")
HOSTNAME_EXAMPLE = re.compile(
    r"\b([a-z0-9]{2,8})-(?:ws|mac|hv|vws|nas|mgmt|srv)-[0-9]{3}\b",
    re.IGNORECASE,
)
NEUTRAL_EXAMPLE_NAMESPACES = {"ac", "acme", "home", "lab", "ss"}


CHANGELOG_GUARDED = ("scripts/", "config/", "templates/")
RELEASE_HEADING = re.compile(r"^## \[[^\]]+\] - \d{4}-\d{2}-\d{2}$", re.MULTILINE)


def _git(root: Path, *arguments: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["git", "--no-optional-locks", "-C", str(root), *arguments],
                          capture_output=True, text=True, check=False)


def _normal(line: str) -> str:
    return " ".join(line.split())


def unreleased_bullets(text: str) -> set[str]:
    """Return the bullet lines of the [Unreleased] section, whitespace-normalised."""
    section = text.split("\n## [Unreleased]", 1)
    if len(section) < 2:
        return set()
    body = section[1].split("\n## ", 1)[0]
    return {_normal(line) for line in body.splitlines() if line.lstrip().startswith("- ")}


def first_release_heading(text: str) -> str | None:
    """The first version heading after [Unreleased], if it is a dated release."""
    section = text.split("\n## [Unreleased]", 1)
    if len(section) < 2:
        return None
    following = re.search(r"^## .*$", section[1], re.MULTILINE)
    if following and RELEASE_HEADING.fullmatch(following.group(0)):
        return following.group(0)
    return None


def changelog_base(root: Path) -> str | None:
    """The pull request's base: CHANGELOG_BASE in CI, else the merge-base with origin/main."""
    explicit = os.environ.get("CHANGELOG_BASE", "").strip()
    if explicit:
        return explicit
    merge_base = _git(root, "merge-base", "HEAD", "origin/main")
    return merge_base.stdout.strip() if merge_base.returncode == 0 and merge_base.stdout.strip() else None


def changelog_entry_issue(root: Path, base: str | None) -> str | None:
    """Fail a change to scripts/, config/ or templates/ that adds no [Unreleased] bullet."""
    if base is None:
        return None
    # --no-renames: a file moved out of scripts/ still counts as a change there.
    changed = _git(root, "diff", "--name-only", "--no-renames", base)
    untracked = _git(root, "ls-files", "--others", "--exclude-standard")
    if changed.returncode or untracked.returncode:
        return "cannot compare the branch with its base " + base + " to check CHANGELOG.md"
    paths = set(changed.stdout.split()) | set(untracked.stdout.split())
    if not any(path.startswith(CHANGELOG_GUARDED) for path in paths):
        return None
    current = (root / "CHANGELOG.md").read_text(encoding="utf-8")
    previous = _git(root, "show", base + ":CHANGELOG.md")
    before = previous.stdout if previous.returncode == 0 else ""
    # A new entry is a bullet whose text appears nowhere in the base changelog,
    # so moving an old bullet or re-spacing one does not count.
    known = {_normal(line) for line in before.splitlines()}
    if any(bullet not in known for bullet in unreleased_bullets(current)):
        return None
    # A release moves [Unreleased] under a new dated heading directly below it.
    release = first_release_heading(current)
    if release and release not in before.splitlines():
        return None
    return ("this change touches scripts/, config/ or templates/ but adds no bullet under "
            "## [Unreleased] in CHANGELOG.md; add one short entry linked to the pull request")


AGENTS_PATH = re.compile(r"(?<![\w/.-])((?:scripts|config|templates|docs|skills|tests)/[\w./-]*[\w/])")
AGENTS_FLAG = re.compile(r"(?<![\w-])(--[a-z][a-z-]*)")
# A line about work not built yet; its commands are checked once they exist.
PLANNED = re.compile(r"[Pp]lanned: issue #5")


def _help(root: Path, command: list[str], cache: dict) -> str:
    key = tuple(command)
    if key not in cache:
        result = subprocess.run(command, cwd=root, capture_output=True, text=True, timeout=60, check=False)
        cache[key] = result.stdout + result.stderr
    return cache[key]


def _manual_lines(text: str) -> list[str]:
    """Lines of AGENTS.md with shell continuation lines joined."""
    lines, pending = [], ""
    for line in text.splitlines():
        if line.rstrip().endswith("\\"):
            pending += line.rstrip()[:-1] + " "
            continue
        lines.append(pending + line)
        pending = ""
    return lines


def agents_manual_issues(root: Path) -> list[str]:
    """Paths, setup-workstation.sh actions, fleetctl.py subcommands and their flags named
    in AGENTS.md must exist. Flags are checked against that exact subcommand's --help.
    Lines marked `planned: issue #5` have their paths checked but not their commands."""
    manual = root / "AGENTS.md"
    if not manual.is_file():
        return ["AGENTS.md is missing"]
    text = manual.read_text(encoding="utf-8")
    issues = []
    for path in sorted(set(AGENTS_PATH.findall(text))):
        if not (root / path.rstrip("/")).exists():
            issues.append("AGENTS.md names a path that does not exist: " + path)
    cli = (root / "scripts/lib/setup_cli.py").read_text(encoding="utf-8")
    commands = re.search(r"^COMMANDS = \(([^)]*)\)", cli, re.M)
    actions = set(re.findall(r'"([a-z-]+)"', commands.group(1))) if commands else set()
    cache: dict = {}
    wrapper = ["bash", str(root / "scripts/setup-workstation.sh")]
    fleetctl = [sys.executable, str(root / "scripts/fleetctl.py")]
    fleet_global = _help(root, [*fleetctl, "--help"], cache)
    subcommands = set(re.findall(r"\{([a-z,]+)\}", fleet_global)[0].split(",")) if "{" in fleet_global else set()
    for line in _manual_lines(text):
        if PLANNED.search(line):
            continue
        for match in re.finditer(r"setup-workstation\.sh ([a-z][a-z-]*)((?:\s+[^`|]*)?)", line):
            action, rest = match.group(1), match.group(2)
            if action in {"ACTION", "help"}:
                continue
            if action not in actions:
                issues.append("AGENTS.md names an unknown setup-workstation.sh action: " + action)
                continue
            defined = _help(root, [*wrapper, action, "--help"], cache)
            for flag in AGENTS_FLAG.findall(rest):
                if not re.search(re.escape(flag) + r"(?![\w-])", defined):
                    issues.append("AGENTS.md uses " + flag + " with setup-workstation.sh " + action + ", which does not define it")
        for match in re.finditer(r"fleetctl\.py((?:\s+[^`|]*)?)", line):
            tokens = match.group(1).split()
            index = 0
            while index < len(tokens) and tokens[index].startswith("--"):
                index += 2 if tokens[index] == "--fleet-root" else 1
            if index >= len(tokens):
                continue
            subcommand = tokens[index]
            if subcommand not in subcommands:
                issues.append("AGENTS.md names an unknown fleetctl.py subcommand: " + subcommand)
                continue
            defined = _help(root, [*fleetctl, subcommand, "--help"], cache) + fleet_global
            for flag in AGENTS_FLAG.findall(" ".join(tokens)):
                if not re.search(re.escape(flag) + r"(?![\w-])", defined):
                    issues.append("AGENTS.md uses " + flag + " with fleetctl.py " + subcommand + ", which does not define it")
    return issues


def non_neutral_hostname_examples(text: str) -> set[str]:
    """Return organization-specific namespaces used in concrete host examples."""
    return {
        match.group(1).lower()
        for match in HOSTNAME_EXAMPLE.finditer(text)
        if match.group(1).lower() not in NEUTRAL_EXAMPLE_NAMESPACES
    }


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
        if path.name in FORBIDDEN_NAMES or path.suffix.lower() in {".key", ".pem", ".p12", ".pfx"}:
            failures.append(f"forbidden credential-like file: {path.relative_to(ROOT)}")
        try:
            content = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            content = ""
        if SECRET_LIKE.search(content):
            failures.append(f"secret-like value in {path.relative_to(ROOT)}")
        if PERSONAL_PATH.search(content):
            failures.append(f"personal absolute path in {path.relative_to(ROOT)}")
        non_neutral = non_neutral_hostname_examples(content)
        if non_neutral:
            failures.append(
                f"non-neutral hostname namespace(s) in {path.relative_to(ROOT)}: "
                + ", ".join(sorted(non_neutral))
            )
        if path.suffix == ".md" and BARE_KVM_MODEL.search(content):
            failures.append(
                f"bare remote-KVM model shorthand in {path.relative_to(ROOT)}"
            )
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
        *ROOT.glob(".codeflow/git-hooks/*"),
    ]
    for path in required_executables:
        if path.is_file() and not path.stat().st_mode & 0o111:
            failures.append(f"expected executable file is not executable: {path.relative_to(ROOT)}")

    readme_path = ROOT / "README.md"
    readme = readme_path.read_text(encoding="utf-8") if readme_path.is_file() else ""
    if not readme:
        failures.append("root README is missing or empty")
    else:
        for required_link in (
            "docs/README.md",
            "docs/runbooks/day-zero-linux.md",
            "docs/runbooks/day-zero-macos.md",
        ):
            if required_link not in readme:
                failures.append(f"root README does not link required entry point: {required_link}")

    docs_home_path = ROOT / "docs/README.md"
    docs_home = docs_home_path.read_text(encoding="utf-8") if docs_home_path.is_file() else ""
    if not docs_home:
        failures.append("documentation home is missing or empty: docs/README.md")
    elif "## One authoritative page per topic" not in docs_home:
        failures.append("documentation home has no authoritative topic index")
    for document in sorted((ROOT / "docs").rglob("*.md")):
        if document == ROOT / "docs/README.md":
            continue
        relative = document.relative_to(ROOT / "docs").as_posix()
        if relative not in docs_home:
            failures.append(f"documentation map does not link: docs/{relative}")
        if not DOCS_HOME_LINK.search(document.read_text(encoding="utf-8")):
            failures.append(f"documentation page has no home navigation: docs/{relative}")

    reviews = ROOT / "docs/reviews"
    if reviews.is_dir() and any(reviews.glob("*.md")):
        failures.append("historical review transcripts must not be user documentation")

    fleet_scratch = ROOT / "config/fleet"
    if fleet_scratch.is_dir() and any(fleet_scratch.rglob("*")):
        failures.append(
            "config/fleet duplicates the separate private-fleet repository boundary"
        )

    for required in (
        ".codeflow/policy.json",
        ".codeflow/git-hooks/pre-commit",
        ".codeflow/git-hooks/commit-msg",
        ".codeflow/git-hooks/pre-push",
        ".codeflow/git-hooks/pre-merge-commit",
        ".codeflow/git-hooks/reference-transaction",
        "scripts/check_git_discipline.py",
    ):
        if not (ROOT / required).is_file():
            failures.append(f"Codeflow discipline file is missing: {required}")

    workflow = ROOT / ".github/workflows/ci.yml"
    workflow_text = workflow.read_text(encoding="utf-8") if workflow.is_file() else ""
    if "run: make ci-check" not in workflow_text:
        failures.append("hosted CI has no independent make ci-check gate")
    if "sathyassn/codeflow" in workflow_text:
        failures.append("hosted CI depends on a private Codeflow release")
    if "./gitleaks git" not in workflow_text:
        failures.append("hosted secret scan does not use the supported Gitleaks git command")

    failures.extend(agents_manual_issues(ROOT))

    base = changelog_base(ROOT)
    if base is None:
        print("NOTE changelog check skipped: no origin/main ref and no CHANGELOG_BASE to compare with")
    elif issue := changelog_entry_issue(ROOT, base):
        failures.append(issue)

    if failures:
        print("Repository checks failed:")
        for failure in failures:
            print(f"- {failure}")
        return 1

    print("Repository checks passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
