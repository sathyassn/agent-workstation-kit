#!/usr/bin/env python3

from __future__ import annotations

import fnmatch
import json
import os
import re
import stat
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
# Folders that CodeFlow installs and updates (`codeflow init`, `codeflow update`).
# Their Markdown is upstream text, so its links are not checked here. Their
# content is still scanned for secrets and personal paths.
CODEFLOW_MANAGED_PREFIXES = (".agents/", ".claude/skills/", ".claude/agents/", ".codeflow/.baseline/", ".grok/", ".codex/")
# Exact synthetic values that CodeFlow's files contain, each allowed only in
# those folders: the sandbox deny-list wildcards in its Grok settings. The
# values are assembled from parts so that this file does not match itself.
CODEFLOW_SYNTHETIC_VALUES = frozenset({
    "/Users" + "/*/",
    "/home" + "/*/",
})


def codeflow_managed(relative: str) -> bool:
    return relative.startswith(CODEFLOW_MANAGED_PREFIXES)


def unexpected_matches(pattern: re.Pattern, content: str, relative: str) -> bool:
    for match in pattern.finditer(content):
        if codeflow_managed(relative) and match.group(0) in CODEFLOW_SYNTHETIC_VALUES:
            continue
        return True
    return False
BARE_KVM_MODEL = re.compile(r"(?<!GL-)\bRM(?:1|1PE|4PE)\b")
HOSTNAME_EXAMPLE = re.compile(
    r"\b([a-z0-9]{2,8})-(?:ws|mac|hv|vws|nas|mgmt|srv)-[0-9]{3}\b",
    re.IGNORECASE,
)
NEUTRAL_EXAMPLE_NAMESPACES = {"ac", "acme", "home", "lab", "ss"}
FLEETCTL_RUN_APPLY = re.compile(r"\bfleetctl(?:\.py)?\b[^`\n;]*\brun\b[^`\n;]*--apply\b")
# Design ADR 0005 records the historical problem and decision in prose; fenced
# code blocks are still scanned everywhere so no runnable apply command returns.
FLEETCTL_RUN_APPLY_PROSE_EXEMPT = frozenset({"docs/design/setup-architecture.md"})
FLEETCTL_DOC_ROOTS = ("docs/", "skills/", "templates/")
FLEETCTL_DOC_FILES = frozenset({"README.md", "AGENTS.md"})


def has_fleetctl_run_apply(text: str) -> bool:
    """Return True if TEXT shows a `fleetctl run ... --apply` command."""
    joined = re.sub(r"\\\n[ \t]*", " ", text)
    for line in joined.splitlines():
        for part in re.split(r"&&|\|\||;", line):
            if FLEETCTL_RUN_APPLY.search(part):
                return True
    return False


def fleetctl_run_apply_issues(root: Path) -> list[str]:
    """Fail if any user documentation shows `fleetctl run ... --apply`."""
    issues: list[str] = []
    for path in sorted(root.rglob("*.md")):
        if ".git" in path.parts or not path.is_file():
            continue
        relative = path.relative_to(root).as_posix()
        if codeflow_managed(relative):
            continue
        if not (relative in FLEETCTL_DOC_FILES or relative.startswith(FLEETCTL_DOC_ROOTS)):
            continue
        text = path.read_text(encoding="utf-8")
        if relative in FLEETCTL_RUN_APPLY_PROSE_EXEMPT:
            if any(has_fleetctl_run_apply(block.group(0)) for block in FENCED_BLOCK.finditer(text)):
                issues.append(f"documented fleetctl run --apply command in {relative}")
        elif has_fleetctl_run_apply(text):
            issues.append(f"documented fleetctl run --apply command in {relative}")
    return issues


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


WORKFLOW_PLAIN_USES_LINE = re.compile(r"^\s*(?:-\s+)?uses:(?:\s+(.*))?$")
WORKFLOW_BLOCK_SCALAR_HEADER = re.compile(
    r"^(\s*(?:-\s+)?)(?:[^:#\n]+:\s*)?[|>](?:[1-9]?[+-]?|[+-]?[1-9]?)\s*(?:#.*)?$"
)
# Fail closed: outside a `run:` value or a block scalar, the token `uses`
# followed by a colon, in any quoting, spacing, flow or explicit-key form,
# must be the one canonical line below; anything else is unsupported.
WORKFLOW_ANY_USES_KEY = re.compile(r"(?<![A-Za-z0-9_-])['\"]?uses['\"]?\s*:")
WORKFLOW_RUN_INLINE = re.compile(r"^\s*(?:-\s+)?run:\s+\S")
WORKFLOW_LOCAL_ACTION = re.compile(r"^(\./[^\s'\"#]+)(?:\s+#.*)?$")
WORKFLOW_PINNED_ACTION = re.compile(
    r"^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:/[A-Za-z0-9_./-]+)?@([0-9a-f]{40})\s+#\s*(\S.*?)\s*$"
)
ACTION_PIN_KEY = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$")


# Workflow YAML is restricted to a plain subset, so every key is a plain
# `key:` on its own line and the canonical `uses:` rule is complete. Banned
# outside `run:` values, block scalars and `${{ }}` expressions: explicit
# keys, flow mappings, quoted keys (and so escapes in keys), anchors,
# aliases, merge keys and tags. A flow sequence is allowed only when it holds
# plain or quoted scalars, as in `branches: [main, "release/**"]`.
WORKFLOW_EXPRESSION = re.compile(r"\$\{\{.*?\}\}")
WORKFLOW_QUOTED_SCALAR = re.compile(r"'(?:[^']|'')*'|\"(?:[^\"\\]|\\.)*\"")
WORKFLOW_QUOTED_KEY = re.compile(r"^\s*(?:-\s+)*(?:'(?:[^']|'')*'|\"(?:[^\"\\]|\\.)*\")\s*:(?:\s|$)")
WORKFLOW_FLOW_SEQUENCE = re.compile(r"\[([^\[\]{}]*)\]")
WORKFLOW_NODE_START = r"(?:^|:\s+|-\s+|[\[,]\s*)"
WORKFLOW_BANNED = (
    ("explicit key ('?')", re.compile(r"^\s*(?:-\s+)*\?(?:\s|$)")),
    ("merge key ('<<')", re.compile(r"^\s*(?:-\s+)*<<\s*:")),
    ("anchor ('&')", re.compile(WORKFLOW_NODE_START + r"&\S")),
    ("alias ('*')", re.compile(WORKFLOW_NODE_START + r"\*\S")),
    ("tag ('!')", re.compile(WORKFLOW_NODE_START + r"!\S")),
)


def workflow_syntax_issue(code_only: str) -> str | None:
    """Return the banned YAML feature on a workflow line, or None.

    `code_only` is one line with its comment removed, outside `run:` values
    and block scalars.
    """
    if WORKFLOW_QUOTED_KEY.match(code_only):
        return "quoted key"
    text = WORKFLOW_EXPRESSION.sub("", code_only)
    text = WORKFLOW_QUOTED_SCALAR.sub("x", text)
    for feature, pattern in WORKFLOW_BANNED:
        if pattern.search(text):
            return feature
    # Allowed flow sequences of scalars are removed; any other bracket or
    # brace left is a flow mapping or a nested flow collection.
    for sequence in WORKFLOW_FLOW_SEQUENCE.finditer(text):
        if ":" in sequence.group(1):
            return "flow mapping inside a flow sequence"
    remainder = WORKFLOW_FLOW_SEQUENCE.sub("x", text)
    if "{" in remainder or "}" in remainder:
        return "flow mapping ('{ }')"
    if "[" in remainder or "]" in remainder:
        return "nested or multi-line flow sequence"
    return None


def _strip_yaml_comment(line: str) -> str:
    """Strip a trailing YAML `# ...` comment outside single or double quotes."""
    in_single = False
    in_double = False
    escaped = False
    for index, ch in enumerate(line):
        if escaped:
            escaped = False
            continue
        if in_double:
            if ch == "\\":
                escaped = True
            elif ch == '"':
                in_double = False
            continue
        if in_single:
            if ch == "'":
                in_single = False
            continue
        if ch == '"':
            in_double = True
        elif ch == "'":
            in_single = True
        elif ch == "#" and (index == 0 or line[index - 1].isspace()):
            return line[:index]
    return line


def _load_action_pins_table(root: Path) -> tuple[dict[str, str] | None, str | None]:
    pins_path = root / "tests/action_pins.json"
    if not pins_path.is_file():
        return None, "missing workflow action pin table: tests/action_pins.json"
    try:
        raw = json.loads(pins_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return None, f"cannot parse tests/action_pins.json: {exc}"
    if not isinstance(raw, dict):
        return None, "tests/action_pins.json must be a JSON object"
    pins: dict[str, str] = {}
    for key, value in raw.items():
        if not isinstance(key, str) or not ACTION_PIN_KEY.fullmatch(key):
            return None, f"invalid pin key in tests/action_pins.json: {key!r}"
        if not isinstance(value, str) or not value.strip():
            return None, f"invalid tag value for {key!r} in tests/action_pins.json: {value!r}"
        pins[key] = value.strip()
    return pins, None


def repo_files(root: Path) -> tuple[list[Path], list[str]]:
    """Return tracked and untracked non-ignored regular files from Git, plus any scan errors."""
    try:
        result = subprocess.run(
            [
                "git",
                "--no-optional-locks",
                "-C",
                str(root),
                "ls-files",
                "-z",
                "--cached",
                "--others",
                "--exclude-standard",
            ],
            capture_output=True,
            check=False,
        )
    except FileNotFoundError:
        return [], ["cannot list repository files: git is unavailable"]
    if result.returncode != 0:
        detail = result.stderr.decode("utf-8", errors="replace").strip()
        return [], [f"cannot list repository files via git ls-files: {detail or f'exit {result.returncode}'}"]

    stage_result = subprocess.run(
        ["git", "--no-optional-locks", "-C", str(root), "ls-files", "-z", "--stage"],
        capture_output=True,
        check=False,
    )
    if stage_result.returncode != 0:
        detail = stage_result.stderr.decode("utf-8", errors="replace").strip()
        return [], [f"cannot inspect git index via git ls-files --stage: {detail or f'exit {stage_result.returncode}'}"]
    gitlinks: set[str] = set()
    for entry in stage_result.stdout.split(b"\x00"):
        if not entry or b"\t" not in entry:
            continue
        meta, raw_rel = entry.split(b"\t", 1)
        mode = meta.split(b" ", 1)[0]
        if mode == b"160000":
            gitlinks.add(raw_rel.decode("utf-8", errors="surrogateescape"))

    paths: list[Path] = []
    errors: list[str] = []
    seen: set[str] = set()
    for raw in result.stdout.split(b"\x00"):
        if not raw:
            continue
        rel = raw.decode("utf-8", errors="surrogateescape")
        if rel in seen:
            continue
        seen.add(rel)
        if rel in gitlinks:
            errors.append(f"cannot scan repository path {rel}: gitlink is not a regular file")
            continue
        path = root / rel
        try:
            st = os.lstat(path)
        except FileNotFoundError:
            errors.append(f"cannot scan repository path {rel}: file is missing")
            continue
        except OSError as exc:
            errors.append(f"cannot scan repository path {rel}: {exc.strerror or exc}")
            continue
        if stat.S_ISLNK(st.st_mode):
            errors.append(f"cannot scan repository path {rel}: symlink is not a regular file")
            continue
        if not stat.S_ISREG(st.st_mode):
            errors.append(f"cannot scan repository path {rel}: not a regular file")
            continue
        paths.append(path)
    return sorted(paths), errors


def workflow_action_pin_issues(root: Path) -> list[str]:
    """Every `uses:` in .github/workflows/*.yml must be a plain block `uses:` line with a local `./` path or a 40-hex SHA pin matching tests/action_pins.json."""
    issues: list[str] = []
    workflows_dir = root / ".github/workflows"
    if not workflows_dir.is_dir():
        return issues
    workflow_paths = sorted({*workflows_dir.glob("*.yml"), *workflows_dir.glob("*.yaml")})
    if not workflow_paths:
        return issues

    pins_table, pins_error = _load_action_pins_table(root)
    if pins_error is not None:
        issues.append(pins_error)
    used_pins: set[str] = set()

    for path in workflow_paths:
        if not path.is_file():
            continue
        relative = path.relative_to(root).as_posix()
        block_indent: int | None = None
        for line_no, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
            stripped = line.lstrip(" ")
            indent = len(line) - len(stripped)
            if block_indent is not None:
                if not stripped or indent > block_indent:
                    continue
                block_indent = None

            if not stripped or stripped.startswith("#"):
                continue

            plain_match = WORKFLOW_PLAIN_USES_LINE.match(line)
            if plain_match:
                raw_value = (plain_match.group(1) or "").strip()
                header_match = WORKFLOW_BLOCK_SCALAR_HEADER.match(line)
                if header_match:
                    block_indent = len(header_match.group(1))
                if WORKFLOW_LOCAL_ACTION.fullmatch(raw_value):
                    continue
                pinned_match = WORKFLOW_PINNED_ACTION.fullmatch(raw_value)
                if not pinned_match:
                    issues.append(
                        f"unpinned workflow action in {relative}:{line_no}: {raw_value or '<empty>'} "
                        "(expected './...' or 'owner/repo[/path]@<40-lowercase-hex> # <version>')"
                    )
                    continue
                repo, sha = pinned_match.group(1), pinned_match.group(2)
                comment_tag = pinned_match.group(3).strip()
                pin_key = f"{repo}@{sha}"
                used_pins.add(pin_key)
                if pins_table is not None:
                    expected_tag = pins_table.get(pin_key)
                    if expected_tag is None:
                        issues.append(
                            f"workflow action pin in {relative}:{line_no} is missing from tests/action_pins.json: {pin_key}"
                        )
                    elif comment_tag != expected_tag:
                        issues.append(
                            f"workflow action pin comment in {relative}:{line_no} for {pin_key} is {comment_tag!r}, "
                            f"expected {expected_tag!r} from tests/action_pins.json"
                        )
                continue

            code_only = _strip_yaml_comment(line)
            if not WORKFLOW_RUN_INLINE.match(code_only):
                feature = workflow_syntax_issue(code_only)
                if feature is not None:
                    issues.append(
                        f"unsupported YAML in {relative}:{line_no}: {feature}; "
                        "workflows use plain 'key: value' lines only"
                    )
                    continue
            if not WORKFLOW_RUN_INLINE.match(code_only) and WORKFLOW_ANY_USES_KEY.search(code_only):
                issues.append(
                    f"unsupported uses syntax in {relative}:{line_no}: {stripped} "
                    "(use the canonical line 'uses: owner/repo@<40-hex> # <version>' or 'uses: ./path')"
                )
                continue

            header_match = WORKFLOW_BLOCK_SCALAR_HEADER.match(line)
            if header_match:
                block_indent = len(header_match.group(1))
    if pins_table is not None:
        for stale in sorted(set(pins_table) - used_pins):
            issues.append(f"tests/action_pins.json lists a pin no workflow uses: {stale}")
    return issues


def scan_repository_files(root: Path) -> list[str]:
    """Scan Git-listed repository files for forbidden files, secrets, paths, links and markers."""
    files, list_errors = repo_files(root)
    failures: list[str] = list(list_errors)

    for path in files:
        if ".git" in path.parts:
            continue
        if path.name in FORBIDDEN_NAMES or path.suffix.lower() in {".key", ".pem", ".p12", ".pfx"}:
            failures.append(f"forbidden credential-like file: {path.relative_to(root)}")
        try:
            content = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            content = ""
        except OSError as exc:
            failures.append(f"cannot read repository file {path.relative_to(root)}: {exc.strerror or exc}")
            continue
        relative = path.relative_to(root).as_posix()
        if unexpected_matches(SECRET_LIKE, content, relative):
            failures.append(f"secret-like value in {path.relative_to(root)}")
        if unexpected_matches(PERSONAL_PATH, content, relative):
            failures.append(f"personal absolute path in {path.relative_to(root)}")
        non_neutral = non_neutral_hostname_examples(content)
        if non_neutral:
            failures.append(
                f"non-neutral hostname namespace(s) in {path.relative_to(root)}: "
                + ", ".join(sorted(non_neutral))
            )
        if path.suffix == ".md" and BARE_KVM_MODEL.search(content):
            failures.append(
                f"bare remote-KVM model shorthand in {path.relative_to(root)}"
            )
        if path.suffix != ".md" or codeflow_managed(relative):
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
                    f"broken relative link in {path.relative_to(root)}: {target}"
                )
                continue
            if anchor and resolved.suffix == ".md":
                headings = markdown_headings(resolved.read_text(encoding="utf-8"))
                if anchor not in headings:
                    failures.append(
                        f"broken heading link in {path.relative_to(root)}: {raw_target}"
                    )
        if PLACEHOLDER.search(text):
            failures.append(f"unfinished marker in {path.relative_to(root)}")
    return failures


CODEOWNERS_REQUIRED_PATHS = (
    "tests/check_public_release.py",
    "tests/public_scan_modes.json",
    "tests/public_scan_exemptions.json",
    "tests/public_scan_binary_exceptions.json",
    ".github/workflows/public-scan.yml",
    ".github/workflows/ci.yml",
    ".github/CODEOWNERS",
)
CODEOWNER_TOKEN = re.compile(r"^@[A-Za-z0-9](?:[A-Za-z0-9-]|/[A-Za-z0-9_-])*$")


def _codeowners_match(pattern: str, rel_path: str) -> bool:
    """Match a CODEOWNERS pattern against a repository-relative POSIX path."""
    anchored = pattern.startswith("/")
    clean = pattern.lstrip("/")
    if not clean:
        return False
    if clean.endswith("/"):
        prefix = clean
        if anchored:
            return rel_path.startswith(prefix)
        return rel_path.startswith(prefix) or f"/{prefix}" in f"/{rel_path}"
    if anchored or "/" in clean:
        return fnmatch.fnmatchcase(rel_path, clean)
    return fnmatch.fnmatchcase(Path(rel_path).name, clean) or fnmatch.fnmatchcase(rel_path, clean)


def codeowners_issues(root: Path) -> list[str]:
    """Verify .github/CODEOWNERS covers every scan, config, workflow, and CODEOWNERS path."""
    codeowners = root / ".github/CODEOWNERS"
    if not codeowners.is_file():
        return ["missing .github/CODEOWNERS covering public-safety scan and workflow files"]
    rules: list[tuple[str, list[str]]] = []
    for raw_line in codeowners.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        pattern = parts[0]
        owners = [token for token in parts[1:] if not token.startswith("#")]
        rules.append((pattern, owners))
    issues: list[str] = []
    for required in CODEOWNERS_REQUIRED_PATHS:
        matched_owners: list[str] | None = None
        for pattern, owners in rules:
            if _codeowners_match(pattern, required):
                matched_owners = owners
        if not matched_owners:
            issues.append(f".github/CODEOWNERS does not assign an owner to {required}")
            continue
        if not any(CODEOWNER_TOKEN.fullmatch(owner) for owner in matched_owners):
            issues.append(f".github/CODEOWNERS has no valid owner handle for {required}")
    return issues


def main() -> int:
    failures: list[str] = scan_repository_files(ROOT)

    required_executables = [
        *ROOT.glob("scripts/*.sh"),
        *ROOT.glob("scripts/*.py"),
        ROOT / "scripts/agent-work-shell",
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
    if "run: make repo-ci-check" not in workflow_text:
        failures.append("hosted CI has no independent make repo-ci-check gate")
    if "sathyassn" + "/codeflow" in workflow_text:
        failures.append("hosted CI depends on a private Codeflow release")
    if "./gitleaks git" not in workflow_text:
        failures.append("hosted secret scan does not use the supported Gitleaks git command")

    failures.extend(workflow_action_pin_issues(ROOT))
    failures.extend(codeowners_issues(ROOT))
    failures.extend(agents_manual_issues(ROOT))
    failures.extend(fleetctl_run_apply_issues(ROOT))

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
