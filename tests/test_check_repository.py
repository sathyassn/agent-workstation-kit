"""Tests for Git-aware repository scanning and workflow action SHA pinning."""
from __future__ import annotations

import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_repository as scan  # noqa: E402
import verify_action_pins as pin_verifier  # noqa: E402

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

    def test_force_added_ignored_file_with_home_path_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _init_git_repo(root)
            (root / ".gitignore").write_text("target/\n", encoding="utf-8")
            ignored = root / "target/codeflow-gate/run.json"
            ignored.parent.mkdir(parents=True)
            ignored.write_text(f'{{"cwd": "{SAMPLE_HOME}"}}\n', encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "-f", "target/codeflow-gate/run.json"], check=True)

            failures = scan.scan_repository_files(root)
            self.assertEqual(len(failures), 1)
            self.assertIn("personal absolute path in target/codeflow-gate/run.json", failures[0])

    def test_tracked_symlink_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _init_git_repo(root)
            target = root / "real.txt"
            target.write_text("clean\n", encoding="utf-8")
            link = root / "link.txt"
            link.symlink_to("real.txt")
            subprocess.run(["git", "-C", str(root), "add", "real.txt", "link.txt"], check=True)

            failures = scan.scan_repository_files(root)
            self.assertEqual(len(failures), 1)
            self.assertIn("symlink", failures[0])
            self.assertIn("link.txt", failures[0])

    def test_missing_tracked_file_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _init_git_repo(root)
            tracked = root / "gone.txt"
            tracked.write_text("clean\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "gone.txt"], check=True)
            tracked.unlink()

            failures = scan.scan_repository_files(root)
            self.assertEqual(len(failures), 1)
            self.assertIn("missing", failures[0])
            self.assertIn("gone.txt", failures[0])

    def test_tracked_gitlink_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _init_git_repo(root)
            subprocess.run(
                [
                    "git",
                    "-C",
                    str(root),
                    "update-index",
                    "--add",
                    "--cacheinfo",
                    "160000,3d3c42e5aac5ba805825da76410c181273ba90b1,submod",
                ],
                check=True,
            )

            failures = scan.scan_repository_files(root)
            self.assertEqual(len(failures), 1)
            self.assertIn("gitlink", failures[0])
            self.assertIn("submod", failures[0])


SAMPLE_PINS = {
    "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1": "v7.0.1",
    "owner/repo@d8cbb8045d7f43706b9177cceb36c1f91a6644d5": "v7.1.0",
}


class WorkflowActionPinTests(unittest.TestCase):
    def _check_workflow(self, content: str, pins: dict[str, str] | None = None) -> list[str]:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            wf = root / ".github/workflows/sample.yml"
            wf.parent.mkdir(parents=True)
            wf.write_text(content, encoding="utf-8")
            # By default the table holds exactly the sample pins this workflow
            # uses, so the reverse (stale entry) check stays quiet.
            table = (
                {key: tag for key, tag in SAMPLE_PINS.items() if key.split("@")[1] in content}
                if pins is None
                else pins
            )
            pins_file = root / "tests/action_pins.json"
            pins_file.parent.mkdir(parents=True)
            pins_file.write_text(json.dumps(table, indent=2) + "\n", encoding="utf-8")
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

    def test_compact_flow_mapping_fails(self) -> None:
        content = 'jobs:\n  build:\n    steps:\n      - {"uses":"actions/checkout@main"}\n'
        issues = self._check_workflow(content)
        self.assertEqual(1, len(issues), issues)
        self.assertIn("unsupported YAML", issues[0])

    def test_explicit_key_on_one_line_fails(self) -> None:
        content = "jobs:\n  build:\n    steps:\n      - ? uses : actions/checkout@main\n"
        issues = self._check_workflow(content)
        self.assertEqual(1, len(issues), issues)
        self.assertIn("unsupported YAML", issues[0])

    def test_quoted_value_is_not_canonical(self) -> None:
        content = (
            "jobs:\n  build:\n    steps:\n"
            "      - uses: 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' # v7.0.1\n"
        )
        issues = self._check_workflow(content, pins=SAMPLE_PINS)
        self.assertTrue(any("unpinned workflow action" in issue for issue in issues), issues)

    def test_run_line_mentioning_uses_does_not_trip(self) -> None:
        content = (
            "jobs:\n  build:\n    steps:\n"
            "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n"
            "      - run: echo \"this step uses: nothing\"\n"
            "      - run: |\n"
            "          echo uses: inside a block scalar\n"
        )
        self.assertEqual([], self._check_workflow(content))

    def test_stale_table_entry_fails(self) -> None:
        content = (
            "jobs:\n  build:\n    steps:\n"
            "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n"
        )
        issues = self._check_workflow(content, pins=SAMPLE_PINS)
        self.assertEqual(
            ["tests/action_pins.json lists a pin no workflow uses: owner/repo@d8cbb8045d7f43706b9177cceb36c1f91a6644d5"],
            issues,
        )

    def test_restricted_yaml_features_fail(self) -> None:
        head = "jobs:\n  build:\n    steps:\n"
        cases = {
            "escaped quoted key": head + '      - "\\u0075ses": actions/checkout@main\n',
            "explicit key split across lines": head + "      - ? uses\n        : actions/checkout@main\n",
            "flow mapping": head + "      - {name: x, run: echo}\n",
            "quoted key": "'on':\n  push:\n",
            "anchor": "env: &defaults\n  A: b\n",
            "alias": "env: *defaults\n",
            "merge key": "env:\n  <<: *defaults\n",
            "tag": "env:\n  A: !!str b\n",
            "nested flow sequence": "on:\n  push:\n    branches: [[main]]\n",
            "flow mapping in a sequence": "on:\n  push:\n    branches: [a: b]\n",
        }
        for name, content in cases.items():
            with self.subTest(case=name):
                issues = self._check_workflow(content, pins={})
                self.assertTrue(any("unsupported YAML" in issue for issue in issues), issues)

    def test_allowed_plain_yaml_passes(self) -> None:
        content = (
            "on:\n  push:\n    branches: [main, \"integration/**\"]\n"
            "  pull_request_target:\n    types: [opened, synchronize]\n"
            "jobs:\n  build:\n"
            "    if: github.event_name != 'push' && !cancelled()\n"
            "    steps:\n"
            "      - name: Uses an expression\n"
            "        env:\n          REF: ${{ github.event.pull_request.head.sha || github.sha }}\n"
            "        run: echo \"${REF}\"\n"
        )
        self.assertEqual([], self._check_workflow(content, pins={}))

    def test_local_action_passes(self) -> None:
        content = (
            "jobs:\n"
            "  build:\n"
            "    steps:\n"
            "      - uses: ./.github/actions/local-step\n"
        )
        self.assertEqual(self._check_workflow(content), [])

    def test_job_level_reusable_workflow_pinned_passes(self) -> None:
        content = (
            "jobs:\n"
            "  call:\n"
            "    uses: owner/repo/.github/workflows/x.yml@d8cbb8045d7f43706b9177cceb36c1f91a6644d5 # v7.1.0\n"
        )
        self.assertEqual(self._check_workflow(content), [])

    def test_job_level_reusable_workflow_unpinned_fails(self) -> None:
        content = (
            "jobs:\n"
            "  call:\n"
            "    uses: owner/repo/.github/workflows/x.yml@main\n"
        )
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("unpinned workflow action", issues[0])

    def test_local_reusable_workflow_passes(self) -> None:
        content = (
            "jobs:\n"
            "  call:\n"
            "    uses: ./.github/workflows/x.yml\n"
        )
        self.assertEqual(self._check_workflow(content), [])

    def test_flow_mapping_uses_fails(self) -> None:
        content = (
            "jobs:\n"
            "  build:\n"
            "    steps:\n"
            "      - {uses: actions/checkout@main}\n"
        )
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)

    def test_quoted_uses_key_fails(self) -> None:
        content = (
            "jobs:\n"
            "  build:\n"
            "    steps:\n"
            "      - 'uses': actions/checkout@main\n"
        )
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)

    def test_pin_missing_from_table_fails(self) -> None:
        content = (
            "jobs:\n"
            "  build:\n"
            "    steps:\n"
            "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n"
        )
        issues = self._check_workflow(content, pins={})
        self.assertEqual(len(issues), 1)
        self.assertIn("action_pins.json", issues[0])

    def test_pin_comment_differs_from_table_fails(self) -> None:
        content = (
            "jobs:\n"
            "  build:\n"
            "    steps:\n"
            "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.2\n"
        )
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("v7.0.1", issues[0])

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
        # The line is rejected, so its table entry is also reported as unused.
        self.assertEqual(2, len(issues), issues)
        self.assertIn("unpinned workflow action", issues[0])
        self.assertIn("lists a pin no workflow uses", issues[1])

    def test_docker_without_digest_fails(self) -> None:
        content = "      - uses: docker://alpine:3.20\n"
        issues = self._check_workflow(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("unpinned workflow action", issues[0])

    def test_current_repository_workflows_pass(self) -> None:
        self.assertEqual(scan.workflow_action_pin_issues(scan.ROOT), [])


class FormatterGateTests(unittest.TestCase):
    def _load_go_format_modes(self) -> dict[str, dict[str, str]]:
        config = json.loads((scan.ROOT / ".codeflow/test-config.json").read_text(encoding="utf-8"))
        for target in config["targets"]:
            if target["name"] == "go-format":
                return target["modes"]
        self.fail("go-format target not found in .codeflow/test-config.json")

    def test_go_format_modes_fail_when_gofmt_exits_nonzero(self) -> None:
        modes = self._load_go_format_modes()
        with tempfile.TemporaryDirectory() as tmp:
            stub_dir = Path(tmp)
            stub_gofmt = stub_dir / "gofmt"
            stub_gofmt.write_text("#!/bin/sh\nexit 2\n", encoding="utf-8")
            stub_gofmt.chmod(stub_gofmt.stat().st_mode | stat.S_IXUSR)

            env = os.environ.copy()
            env["PATH"] = f"{stub_dir}:{env.get('PATH', '')}"
            for mode_name, mode_cfg in modes.items():
                with self.subTest(mode=mode_name):
                    res = subprocess.run(
                        mode_cfg["command"],
                        shell=True,
                        cwd=tmp,
                        env=env,
                        capture_output=True,
                        text=True,
                        check=False,
                    )
                    self.assertNotEqual(
                        res.returncode,
                        0,
                        f"go-format mode {mode_name!r} succeeded when gofmt exited 2 with empty stdout",
                    )

    def test_go_format_full_fails_when_golangci_lint_fmt_exits_nonzero(self) -> None:
        modes = self._load_go_format_modes()
        with tempfile.TemporaryDirectory() as tmp:
            stub_dir = Path(tmp)
            stub_gofmt = stub_dir / "gofmt"
            stub_gofmt.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
            stub_gofmt.chmod(stub_gofmt.stat().st_mode | stat.S_IXUSR)
            stub_go = stub_dir / "go"
            stub_go.write_text("#!/bin/sh\nexit 2\n", encoding="utf-8")
            stub_go.chmod(stub_go.stat().st_mode | stat.S_IXUSR)

            env = os.environ.copy()
            env["PATH"] = f"{stub_dir}:{env.get('PATH', '')}"
            res = subprocess.run(
                modes["full"]["command"],
                shell=True,
                cwd=tmp,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(
                res.returncode,
                0,
                "go-format full mode succeeded when golangci-lint fmt exited 2 with empty stdout",
            )


class VerifyActionPinsTests(unittest.TestCase):
    PIN = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
    TAG_OBJ = "1111111111111111111111111111111111111111"
    OTHER_COMMIT = "2222222222222222222222222222222222222222"

    def test_annotated_tag_uses_peeled_commit(self) -> None:
        output = (
            f"{self.TAG_OBJ}\trefs/tags/v7.0.1\n"
            f"3d3c42e5aac5ba805825da76410c181273ba90b1\trefs/tags/v7.0.1^{{}}\n"
        )
        self.assertIsNone(pin_verifier.verify_ls_remote_output(self.PIN, "v7.0.1", output))

    def test_lightweight_tag_passes(self) -> None:
        output = "3d3c42e5aac5ba805825da76410c181273ba90b1\trefs/tags/v7.0.1\n"
        self.assertIsNone(pin_verifier.verify_ls_remote_output(self.PIN, "v7.0.1", output))

    def test_missing_tag_fails(self) -> None:
        error = pin_verifier.verify_ls_remote_output(self.PIN, "v7.0.1", "")
        self.assertIsNotNone(error)
        assert error is not None
        self.assertIn("not found", error)

    def test_mismatched_commit_fails(self) -> None:
        output = (
            f"{self.TAG_OBJ}\trefs/tags/v7.0.1\n"
            f"{self.OTHER_COMMIT}\trefs/tags/v7.0.1^{{}}\n"
        )
        error = pin_verifier.verify_ls_remote_output(self.PIN, "v7.0.1", output)
        self.assertIsNotNone(error)
        assert error is not None
        self.assertIn(self.OTHER_COMMIT, error)


if __name__ == "__main__":
    unittest.main()
