#!/usr/bin/env python3
"""Check deterministic repository prerequisites for a public release."""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from collections.abc import Iterable
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$")
REQUIRED = (
    "README.md",
    "VERSION",
    "CHANGELOG.md",
    "CONTRIBUTING.md",
    "CODE_OF_CONDUCT.md",
    "SECURITY.md",
    "SUPPORT.md",
    ".github/workflows/ci.yml",
    ".github/dependabot.yml",
    ".github/pull_request_template.md",
    ".github/ISSUE_TEMPLATE/bug_report.yml",
    ".github/ISSUE_TEMPLATE/feature_request.yml",
    ".github/ISSUE_TEMPLATE/config.yml",
    "docs/13-public-release-checklist.md",
    "docs/runbooks/first-linux-pilot.md",
    "docs/hardware/minisforum-ms-s1-max.md",
    "templates/private-fleet/README.md",
    "skills/setup-agent-workstation/SKILL.md",
)
CONDUCT_CONTACT_PLACEHOLDER = "owner must add a monitored private conduct-report"


def tracked_files(root: Path) -> tuple[list[str] | None, str | None]:
    """Return tracked paths, or a concise reason when Git context is absent."""
    try:
        result = subprocess.run(
            ["git", "ls-files"],
            cwd=root,
            check=True,
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        return None, "Git is unavailable"
    except subprocess.CalledProcessError:
        return None, "the directory is not a readable Git worktree"
    return result.stdout.splitlines(), None


def deterministic_failures(
    root: Path,
    *,
    require_license: bool,
    tracked: Iterable[str] | None,
) -> list[str]:
    """Evaluate repository metadata without performing network operations."""
    failures: list[str] = []

    for name in REQUIRED:
        if not (root / name).is_file():
            failures.append(f"required public metadata is missing: {name}")

    version_path = root / "VERSION"
    version = version_path.read_text(encoding="utf-8").strip() if version_path.is_file() else ""
    if not SEMVER.fullmatch(version):
        failures.append("VERSION is not valid Semantic Versioning")
    changelog = root / "CHANGELOG.md"
    if version and changelog.is_file() and f"## {version}" not in changelog.read_text(encoding="utf-8"):
        failures.append("CHANGELOG.md has no entry for VERSION")
    readme = root / "README.md"
    if version and readme.is_file() and version not in readme.read_text(encoding="utf-8"):
        failures.append("README.md does not mention VERSION")
    template_lock = root / "templates/private-fleet/kit.lock"
    if version and (not template_lock.is_file() or template_lock.read_text(encoding="utf-8").strip() != version):
        failures.append("templates/private-fleet/kit.lock does not match VERSION")

    if not (root / "LICENSE").is_file() and require_license:
        failures.append("LICENSE is missing; owner/legal license selection is required")
    elif (root / "LICENSE").is_file() and "Apache License\nVersion 2.0" not in (root / "LICENSE").read_text(encoding="utf-8"):
        failures.append("LICENSE is not the selected Apache License 2.0 text")

    conduct = root / "CODE_OF_CONDUCT.md"
    if require_license and conduct.is_file() and CONDUCT_CONTACT_PLACEHOLDER in conduct.read_text(encoding="utf-8").lower():
        failures.append("CODE_OF_CONDUCT.md still has the private conduct-contact placeholder")

    support = root / "SUPPORT.md"
    contributing = root / "CONTRIBUTING.md"
    if support.is_file() and contributing.is_file():
        support_text = support.read_text(encoding="utf-8").lower()
        if "external contributions are not accepted" in support_text:
            failures.append("SUPPORT.md contradicts the contribution policy")

    if tracked is not None:
        for name in tracked:
            if name.endswith(".local.toml") or name.startswith("reports/") or name.startswith("artifacts/"):
                failures.append(f"private/generated path is tracked: {name}")
            if name.startswith("skills/setup-agent-dev-machine/") and (root / name).exists():
                failures.append(f"retired skill path is tracked: {name}")

    return failures


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--allow-missing-license",
        action="store_true",
        help="validate a private draft while retaining the explicit license gate",
    )
    args = parser.parse_args()
    tracked, git_warning = tracked_files(ROOT)
    failures = deterministic_failures(
        ROOT,
        require_license=not args.allow_missing_license,
        tracked=tracked,
    )
    if git_warning and not args.allow_missing_license:
        failures.append(f"full tracked-file check unavailable: {git_warning}")

    if failures:
        print("Public release checks failed:")
        for failure in failures:
            print(f"- {failure}")
        return 1

    if git_warning:
        print(f"Warning: skipped tracked-file inspection because {git_warning}.")
    qualifier = "draft " if args.allow_missing_license else ""
    print(
        f"Deterministic public-release {qualifier}checks passed; "
        "complete docs/13-public-release-checklist.md before publication."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
