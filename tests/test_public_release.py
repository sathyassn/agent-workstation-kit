#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "check_public_release", ROOT / "tests/check_public_release.py"
)
assert SPEC and SPEC.loader
public_release = importlib.util.module_from_spec(SPEC)
sys.modules["check_public_release"] = public_release
SPEC.loader.exec_module(public_release)


class PublicReleaseTests(unittest.TestCase):
    def make_repository(self, root: Path, *, version: str = "1.2.3") -> None:
        for name in public_release.REQUIRED:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.touch()
        (root / "VERSION").write_text(f"{version}\n", encoding="utf-8")
        (root / "CHANGELOG.md").write_text(
            f"# Changelog\n\n## {version} — Unreleased\n",
            encoding="utf-8",
        )

    def test_draft_allows_missing_license(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            self.assertEqual(
                [],
                public_release.deterministic_failures(
                    root, require_license=False, tracked=[]
                ),
            )

    def test_strict_check_requires_license(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            failures = public_release.deterministic_failures(
                root, require_license=True, tracked=[]
            )
            self.assertTrue(any("LICENSE is missing" in item for item in failures))

    def test_invalid_semver_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root, version="release-one")
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=[]
            )
            self.assertIn("VERSION is not valid Semantic Versioning", failures)

    def test_changelog_must_include_version(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            (root / "CHANGELOG.md").write_text("# Changelog\n", encoding="utf-8")
            failures = public_release.deterministic_failures(
                root, require_license=False, tracked=[]
            )
            self.assertIn("CHANGELOG.md has no entry for VERSION", failures)

    def test_private_or_generated_tracked_paths_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repository(root)
            failures = public_release.deterministic_failures(
                root,
                require_license=False,
                tracked=["config/profiles/node.local.toml", "reports/audit.txt"],
            )
            self.assertEqual(2, len(failures))

    def test_missing_git_is_reported_without_traceback(self) -> None:
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            public_release.subprocess,
            "run",
            side_effect=FileNotFoundError,
        ):
            tracked, warning = public_release.tracked_files(Path(directory))
            self.assertIsNone(tracked)
            self.assertEqual("Git is unavailable", warning)

    def test_non_worktree_is_reported_without_traceback(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            tracked, warning = public_release.tracked_files(Path(directory))
            self.assertIsNone(tracked)
            self.assertEqual(
                "the directory is not a readable Git worktree",
                warning,
            )


if __name__ == "__main__":
    unittest.main()
