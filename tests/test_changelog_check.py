"""The changelog-entry check, run against temporary Git repositories."""
from __future__ import annotations
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("check_repository", ROOT / "tests/check_repository.py")
check = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check)

BASE_LOG = "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- Earlier entry. ([#1])\n\n## [0.1.0] - 2026-01-01\n"


class ChangelogEntryTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.root = Path(self.folder.name)
        self.git("init", "-q", "-b", "main")
        (self.root / "scripts").mkdir()
        (self.root / "scripts/tool.sh").write_text("echo one\n")
        (self.root / "docs").mkdir()
        (self.root / "docs/guide.md").write_text("guide\n")
        (self.root / "CHANGELOG.md").write_text(BASE_LOG)
        self.git("add", "-A")
        self.git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
        self.base = self.git("rev-parse", "HEAD").stdout.strip()

    def git(self, *arguments):
        return subprocess.run(["git", "-C", str(self.root), *arguments], capture_output=True, text=True, check=True)

    def issue(self):
        return check.changelog_entry_issue(self.root, self.base)

    def test_script_change_without_entry_fails(self):
        (self.root / "scripts/tool.sh").write_text("echo two\n")
        self.assertIn("adds no bullet under ## [Unreleased]", self.issue())

    def test_new_untracked_template_without_entry_fails(self):
        (self.root / "templates").mkdir()
        (self.root / "templates/new.toml").write_text("x = 1\n")
        self.assertIsNotNone(self.issue())

    def test_script_change_with_new_unreleased_bullet_passes(self):
        (self.root / "scripts/tool.sh").write_text("echo two\n")
        (self.root / "CHANGELOG.md").write_text(BASE_LOG.replace("- Earlier entry. ([#1])", "- Earlier entry. ([#1])\n- Tool prints two. ([#2])"))
        self.assertIsNone(self.issue())

    def test_bullet_outside_unreleased_does_not_count(self):
        (self.root / "scripts/tool.sh").write_text("echo two\n")
        (self.root / "CHANGELOG.md").write_text(BASE_LOG + "\n- Misplaced entry.\n")
        self.assertIsNotNone(self.issue())

    def test_docs_only_change_needs_no_entry(self):
        (self.root / "docs/guide.md").write_text("better guide\n")
        self.assertIsNone(self.issue())

    def test_release_with_new_dated_heading_passes(self):
        (self.root / "scripts/tool.sh").write_text("echo two\n")
        released = BASE_LOG.replace("## [Unreleased]\n\n### Added\n\n- Earlier entry. ([#1])\n",
                                    "## [Unreleased]\n\n## [0.2.0] - 2026-02-01\n\n### Added\n\n- Earlier entry. ([#1])\n")
        (self.root / "CHANGELOG.md").write_text(released)
        self.assertIsNone(self.issue())

    def test_no_base_skips_and_explicit_base_wins(self):
        self.assertIsNone(check.changelog_entry_issue(self.root, None))
        with mock.patch.dict(os.environ, {"CHANGELOG_BASE": "abc123"}):
            self.assertEqual("abc123", check.changelog_base(self.root))

    def test_unknown_base_fails_closed(self):
        (self.root / "scripts/tool.sh").write_text("echo two\n")
        self.assertIn("cannot compare", check.changelog_entry_issue(self.root, "0" * 40))



class AgentsManualTests(unittest.TestCase):
    def manual(self, text):
        import shutil
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        root = Path(folder.name)
        for source in {item for sources in check.FLAG_SOURCES.values() for item in sources}:
            (root / source).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(ROOT / source, root / source)
        (root / "AGENTS.md").write_text(text)
        return check.agents_manual_issues(root)

    def test_the_real_manual_is_consistent(self):
        self.assertEqual([], check.agents_manual_issues(ROOT))

    def test_missing_path_unknown_action_and_flag_are_reported(self):
        issues = self.manual(
            "Run `scripts/no-such-tool.sh` first.\n"
            "Then `scripts/setup-workstation.sh frobnicate`.\n"
            "Then `scripts/setup-workstation.sh plan --no-such-flag`.\n")
        self.assertIn("AGENTS.md names a path that does not exist: scripts/no-such-tool.sh", issues)
        self.assertIn("AGENTS.md names an unknown setup-workstation.sh action: frobnicate", issues)
        self.assertIn("AGENTS.md uses --no-such-flag with setup-workstation.sh, which does not define it", issues)

    def test_planned_commands_are_not_checked_yet(self):
        self.assertEqual([], self.manual("PR 2 adds `scripts/setup-workstation.sh user --dry-run`.\n"))


if __name__ == "__main__":
    unittest.main()
