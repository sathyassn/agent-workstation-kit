"""Tests for Git-aware repository scanning and workflow action SHA pinning."""
from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_repository as scan  # noqa: E402

SAMPLE_HOME = "/home" + "/alice/work"


def _init_git_repo(root: Path) -> None:
    subprocess.run(["git", "init", "-q", str(root)], check=True)


class GitAwareScanTests(unittest.TestCase):
    def test_ignored_file_with_home_path_is_skipped(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _init_git_repo(root)
            (root / ".gitignore").write_text("target/\n", encoding="utf-8")
            ignored = root / "target/codeflow-gate/run.json"
            ignored.parent.mkdir(parents=True)
            ignored.write_text(f'{{"cwd": "{SAMPLE_HOME}"}}\n', encoding="utf-8")

            self.assertEqual(scan.scan_repository_files(root), [])

    def test_untracked_non_ignored_file_with_home_path_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _init_git_repo(root)
            (root / ".gitignore").write_text("target/\n", encoding="utf-8")
            untracked = root / "notes.txt"
            untracked.write_text(f"path={SAMPLE_HOME}\n", encoding="utf-8")

            failures = scan.scan_repository_files(root)
            self.assertEqual(len(failures), 1)
            self.assertIn("personal absolute path in notes.txt", failures[0])

    def test_fails_closed_when_not_a_git_repo(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            failures = scan.scan_repository_files(Path(tmp))
            self.assertEqual(len(failures), 1)
            self.assertIn("cannot list repository files via git ls-files", failures[0])

    def test_fails_closed_when_git_is_missing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            with mock.patch.object(scan.subprocess, "run", side_effect=FileNotFoundError("git")):
                failures = scan.scan_repository_files(Path(tmp))
            self.assertEqual(len(failures), 1)
            self.assertIn("git is unavailable", failures[0])


class WorkflowActionPinTests(unittest.TestCase):
    def _check_workflow(self, content: str) -> list[str]:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            wf = root / ".github/workflows/sample.yml"
            wf.parent.mkdir(parents=True)
            wf.write_text(content, encoding="utf-8")
            return scan.workflow_action_pin_issues(root)

    def test_good_sha_pin_with_version_comment_passes(self) -> None:
        content = (
            "jobs:\n"
            "  build:\n"
            "    steps:\n"
            "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n"
            "      - uses: owner/repo/sub/action@d8cbb8045d7f43706b9177cceb36c1f91a6644d5 # v7.1.0\n"
        )
        self.assertEqual(self._check_workflow(content), [])

    def test_local_action_passes(self) -> None:
        content = (
            "jobs:\n"
            "  build:\n"
            "    steps:\n"
            "      - uses: ./.github/actions/local-step\n"
        )
        self.assertEqual(self._check_workflow(content), [])

    def test_tag_pin_fails(self) -> None:
        content = "      - uses: actions/checkout@v7.0.1\n"
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("unpinned workflow action", issues[0])

    def test_branch_pin_fails(self) -> None:
        content = "      - uses: actions/checkout@main\n"
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("unpinned workflow action", issues[0])

    def test_short_sha_fails(self) -> None:
        content = "      - uses: actions/checkout@3d3c42e5aac5 # v7.0.1\n"
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("unpinned workflow action", issues[0])

    def test_sha_without_version_comment_fails(self) -> None:
        content = "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n"
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("unpinned workflow action", issues[0])

    def test_docker_without_digest_fails(self) -> None:
        content = "      - uses: docker://alpine:3.20\n"
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("unpinned workflow action", issues[0])

    def test_current_repository_workflows_pass(self) -> None:
        self.assertEqual(scan.workflow_action_pin_issues(scan.ROOT), [])


if __name__ == "__main__":
    unittest.main()
