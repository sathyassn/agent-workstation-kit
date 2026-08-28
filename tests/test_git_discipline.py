#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import io
import os
import subprocess
import sys
import tempfile
import unittest
from argparse import Namespace
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/check_git_discipline.py"
SPEC = importlib.util.spec_from_file_location("check_git_discipline", SCRIPT)
assert SPEC and SPEC.loader
discipline = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = discipline
SPEC.loader.exec_module(discipline)


class GitDisciplineTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.policy = discipline.load_policy()

    def test_policy_is_self_contained_and_fail_closed(self) -> None:
        self.assertIn("docs/", self.policy.branch_prefixes)
        self.assertIn("skills/**", self.policy.breaking_watch_paths)
        raw = discipline.json.loads(
            (ROOT / ".codeflow/policy.json").read_text(encoding="utf-8")
        )
        self.assertNotIn("security", raw)
        self.assertNotIn("human_authorization", raw)

    def test_branch_policy_accepts_only_kebab_contribution_branches(self) -> None:
        discipline.validate_branch("docs/clarify-day-zero", self.policy)
        with self.assertRaisesRegex(discipline.DisciplineError, "protected branch"):
            discipline.validate_branch("main", self.policy)
        with self.assertRaisesRegex(discipline.DisciplineError, "must start"):
            discipline.validate_branch("topic/clarify-day-zero", self.policy)
        with self.assertRaisesRegex(discipline.DisciplineError, "kebab-case"):
            discipline.validate_branch("docs/Clarify_Day_Zero", self.policy)

    def test_commit_policy_accepts_conventional_subject_and_bullets(self) -> None:
        discipline.validate_message(
            "docs: clarify setup order\n\n- align the pilot and day-zero entry paths\n",
            self.policy,
        )

    def test_commit_policy_rejects_legacy_prose_and_attribution(self) -> None:
        for message, expected in (
            ("Prepare release baseline", r"type\(scope\)"),
            ("docs: clarify setup.\n", "must not end"),
            ("docs: clarify setup\n\nThis is prose.\n", "only short"),
            (
                "docs: clarify setup\n\nCo-Authored-By: Codex <bot@example.invalid>\n",
                "AI attribution",
            ),
        ):
            with self.subTest(message=message):
                with self.assertRaisesRegex(discipline.DisciplineError, expected):
                    discipline.validate_message(message, self.policy)

    def test_commit_policy_ignores_discarded_comment_lines(self) -> None:
        discipline.validate_message(
            "docs: clarify setup\n\n# Co-authored-by: Claude <bot@example.invalid>\n",
            self.policy,
        )

    def test_breaking_subject_requires_migration_footer(self) -> None:
        with self.assertRaisesRegex(discipline.DisciplineError, "BREAKING CHANGE"):
            discipline.validate_message("feat!: change profile format\n", self.policy)
        discipline.validate_message(
            "feat!: change profile format\n\nBREAKING CHANGE: migrate profiles first\n",
            self.policy,
        )

    def test_pull_request_sections_depend_on_change_type(self) -> None:
        body = "## Summary\nClear result.\n\n## Changes\n- One change.\n"
        discipline.validate_pr_body(body, self.policy, code_change=False)
        with self.assertRaisesRegex(discipline.DisciplineError, "Testing"):
            discipline.validate_pr_body(body, self.policy, code_change=True)
        discipline.validate_pr_body(
            body + "\n## Testing\n- `make ci-check` passed.\n",
            self.policy,
            code_change=True,
        )

    def test_docs_only_classification_is_conservative(self) -> None:
        self.assertTrue(discipline._is_docs_only(["docs/README.md", "README.md"]))
        self.assertFalse(
            discipline._is_docs_only(["docs/README.md", "scripts/bootstrap.py"])
        )
        self.assertFalse(discipline._is_docs_only(["skills/example/SKILL.md"]))
        self.assertFalse(discipline._is_docs_only(["AGENTS.md"]))
        self.assertFalse(discipline._is_docs_only([]))

    def test_null_object_id_supports_both_git_hash_formats(self) -> None:
        self.assertTrue(discipline._is_zero_oid("0" * 40))
        self.assertTrue(discipline._is_zero_oid("0" * 64))
        self.assertFalse(discipline._is_zero_oid("1" + "0" * 39))

    def test_pre_push_cli_accepts_git_remote_arguments(self) -> None:
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "pre-push",
                "origin",
                "https://github.com/example/project.git",
            ],
            cwd=ROOT,
            input="",
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)


class GitDisciplineIntegrationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.repo = Path(self.tempdir.name)
        self.original_root = discipline.ROOT
        discipline.ROOT = self.repo
        self.policy = discipline.load_policy()
        self._git("init", "-q", "-b", "main")
        self._git("config", "user.name", "Test Operator")
        self._git("config", "user.email", "operator@example.invalid")
        (self.repo / "Makefile").write_text(
            "ci-check:\n\t@true\n", encoding="utf-8"
        )
        (self.repo / "README.md").write_text("# Fixture\n", encoding="utf-8")
        self._git("add", "Makefile", "README.md")
        self._git("commit", "-q", "-m", "chore: initialize fixture")
        self.base = self._git("rev-parse", "HEAD").strip()

    def tearDown(self) -> None:
        discipline.ROOT = self.original_root
        self.tempdir.cleanup()

    def _git(self, *args: str) -> str:
        result = subprocess.run(
            ["git", *args],
            cwd=self.repo,
            check=True,
            capture_output=True,
            text=True,
        )
        return result.stdout

    def _commit(self, path: str, content: str, message: str) -> str:
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8")
        self._git("add", path)
        self._git("commit", "-q", "-m", message)
        return self._git("rev-parse", "HEAD").strip()

    def test_staged_secret_validation_uses_the_real_index(self) -> None:
        (self.repo / ".env").write_text("TOKEN=not-a-real-secret\n", encoding="utf-8")
        self._git("add", "-f", ".env")
        with self.assertRaisesRegex(discipline.DisciplineError, "credential-like"):
            discipline.validate_staged_secrets()

    def test_commit_range_and_ci_run_against_a_real_repository(self) -> None:
        self._git("switch", "-q", "-c", "docs/integration-check")
        head = self._commit(
            "docs/guide.md", "# Guide\n", "docs: add integration guide"
        )
        body = "## Summary\nFixture.\n\n## Changes\n- Add a guide.\n"
        with mock.patch.dict(os.environ, {"PR_BODY": body}, clear=False):
            discipline.command_ci(
                Namespace(
                    base=self.base,
                    head=head,
                    branch="docs/integration-check",
                    pr_body_env="PR_BODY",
                ),
                self.policy,
            )

    def test_skill_markdown_requires_testing_in_ci(self) -> None:
        self._git("switch", "-q", "-c", "docs/skill-contract")
        head = self._commit(
            "skills/example/SKILL.md",
            "# Skill\n",
            "docs: add skill contract",
        )
        body = "## Summary\nFixture.\n\n## Changes\n- Add a skill.\n"
        with mock.patch.dict(os.environ, {"PR_BODY": body}, clear=False):
            with self.assertRaisesRegex(discipline.DisciplineError, "Testing"):
                discipline.command_ci(
                    Namespace(
                        base=self.base,
                        head=head,
                        branch="docs/skill-contract",
                        pr_body_env="PR_BODY",
                    ),
                    self.policy,
                )

    def test_fork_default_branch_is_allowed_as_pull_request_head(self) -> None:
        self._git("switch", "-q", "-c", "docs/fork-source")
        head = self._commit(
            "docs/fork.md", "# Fork\n", "docs: add fork fixture"
        )
        body = "## Summary\nFixture.\n\n## Changes\n- Add a guide.\n"
        with mock.patch.dict(os.environ, {"PR_BODY": body}, clear=False):
            discipline.command_ci(
                Namespace(
                    base=self.base,
                    head=head,
                    branch="main",
                    head_repository="contributor/fork",
                    base_repository="owner/project",
                    pr_body_env="PR_BODY",
                ),
                self.policy,
            )

    def test_pre_push_runs_gate_and_rejects_non_fast_forward(self) -> None:
        self._git("switch", "-q", "-c", "docs/pre-push-check")
        local_oid = self._commit(
            "docs/push.md", "# Push\n", "docs: add push fixture"
        )
        update = (
            f"refs/heads/docs/pre-push-check {local_oid} "
            f"refs/heads/docs/pre-push-check {'0' * 40}\n"
        )
        discipline.run_pre_push(io.StringIO(update), self.policy)

        with self.assertRaisesRegex(discipline.DisciplineError, "force push"):
            discipline.run_pre_push(
                io.StringIO(
                    f"refs/heads/docs/pre-push-check {self.base} "
                    f"refs/heads/docs/pre-push-check {local_oid}\n"
                ),
                self.policy,
            )

    def test_pre_push_gate_has_a_bounded_runtime(self) -> None:
        update = (
            f"refs/heads/docs/timeout-check {self.base} "
            f"refs/heads/docs/timeout-check {'0' * 40}\n"
        )
        timeout = subprocess.TimeoutExpired(["make", "ci-check"], timeout=600)
        with mock.patch.object(discipline.subprocess, "run", side_effect=timeout):
            with self.assertRaisesRegex(discipline.DisciplineError, "10 minutes"):
                discipline.run_pre_push(io.StringIO(update), self.policy)

    def test_pre_push_gate_does_not_inherit_hook_git_context(self) -> None:
        update = (
            f"refs/heads/docs/environment-check {self.base} "
            f"refs/heads/docs/environment-check {'0' * 40}\n"
        )
        completed = subprocess.CompletedProcess(["make", "ci-check"], 0)
        inherited = {
            "GIT_DIR": "/unsafe/git-dir",
            "GIT_WORK_TREE": "/unsafe/work-tree",
            "GIT_INDEX_FILE": "/unsafe/index",
            "PRESERVE_ME": "yes",
        }
        with mock.patch.dict(os.environ, inherited, clear=False):
            with mock.patch.object(
                discipline.subprocess, "run", return_value=completed
            ) as run:
                discipline.run_pre_push(io.StringIO(update), self.policy)
        gate_env = run.call_args.kwargs["env"]
        self.assertNotIn("GIT_DIR", gate_env)
        self.assertNotIn("GIT_WORK_TREE", gate_env)
        self.assertNotIn("GIT_INDEX_FILE", gate_env)
        self.assertEqual(gate_env["PRESERVE_ME"], "yes")

    def test_protected_ref_allows_only_exact_upstream_sync(self) -> None:
        head = self._commit(
            "README.md", "# Updated fixture\n", "docs: update fixture"
        )
        self._git("remote", "add", "origin", ".")
        self._git("config", "branch.main.remote", "origin")
        self._git("config", "branch.main.merge", "refs/heads/main")
        self._git("update-ref", "refs/remotes/origin/main", self.base)
        with self.assertRaisesRegex(discipline.DisciplineError, "upstream sync"):
            discipline.run_reference_transaction(
                "prepared",
                io.StringIO(f"{self.base} {head} refs/heads/main\n"),
                self.policy,
            )
        self._git("update-ref", "refs/remotes/origin/main", head)
        discipline.run_reference_transaction(
            "prepared",
            io.StringIO(f"{self.base} {head} refs/heads/main\n"),
            self.policy,
        )
        with self.assertRaisesRegex(discipline.DisciplineError, "upstream sync"):
            discipline.run_reference_transaction(
                "prepared",
                io.StringIO(f"{head} {self.base} refs/heads/main\n"),
                self.policy,
            )
        with self.assertRaisesRegex(discipline.DisciplineError, "upstream sync"):
            discipline.run_reference_transaction(
                "prepared",
                io.StringIO(f"{head} {'0' * 40} refs/heads/main\n"),
                self.policy,
            )
        discipline.run_reference_transaction(
            "prepared",
            io.StringIO(f"{'0' * 40} {head} refs/heads/main\n"),
            self.policy,
        )

    def test_protected_ref_requires_a_configured_upstream(self) -> None:
        head = self._commit(
            "README.md", "# No upstream fixture\n", "docs: update fixture"
        )
        self._git("update-ref", "refs/remotes/origin/main", head)
        with self.assertRaisesRegex(
            discipline.DisciplineError, "no configured upstream"
        ):
            discipline.run_reference_transaction(
                "prepared",
                io.StringIO(f"{self.base} {head} refs/heads/main\n"),
                self.policy,
            )

    def test_protected_sync_uses_the_configured_non_origin_upstream(self) -> None:
        head = self._commit(
            "README.md", "# Upstream fixture\n", "docs: update upstream fixture"
        )
        self._git("remote", "add", "upstream", ".")
        self._git("update-ref", "refs/remotes/upstream/main", head)
        self._git("config", "branch.main.remote", "upstream")
        self._git("config", "branch.main.merge", "refs/heads/main")
        discipline.run_reference_transaction(
            "prepared",
            io.StringIO(f"{self.base} {head} refs/heads/main\n"),
            self.policy,
        )


if __name__ == "__main__":
    unittest.main()
