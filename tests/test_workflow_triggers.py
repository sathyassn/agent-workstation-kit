"""Event and check-name contracts for the repository and Go workflows."""

import re
import unittest
from pathlib import Path


WORKFLOWS = Path(__file__).resolve().parents[1] / ".github/workflows"


class HostedWorkflowTests(unittest.TestCase):
    def workflow_section(self, filename: str, key: str) -> str:
        source = (WORKFLOWS / filename).read_text(encoding="utf-8")
        blocks = re.findall(rf"^{key}:\n(.*?)(?=^[^\s#]|\Z)", source, re.MULTILINE | re.DOTALL)
        self.assertEqual(len(blocks), 1, f"{filename}: expected one block for {key!r}")
        return "\n".join(
            line for line in blocks[0].splitlines()
            if line.strip() and not line.lstrip().startswith("#")
        )

    def test_trigger_matrix(self) -> None:
        for filename in ("ci.yml", "go.yml"):
            with self.subTest(workflow=filename):
                # Read the literal 'on' key. Unknown syntax fails the test
                # instead of dropping a filter or event.
                block = self.workflow_section(filename, "on")
                triggers = {}
                for entry in re.split(r"\n(?=  [a-z_]+:)", block):
                    match = re.fullmatch(
                        r"  ([a-z_]+):"
                        r"(?:\n    branches:(?: \[([^\n]+)\]|((?:\n      - [^\n]+)+)))?",
                        entry,
                    )
                    self.assertIsNotNone(match, entry)
                    event, inline, block_list = match.groups()
                    self.assertNotIn(event, triggers)
                    branches = None
                    if inline is not None:
                        branches = tuple(branch.strip() for branch in inline.split(","))
                    elif block_list is not None:
                        branches = tuple(re.findall(r"^      - (.+)$", block_list, re.MULTILINE))
                    triggers[event] = branches

                # Exact event keys and branch allowlists also exclude every
                # other branch (including integration branches) and event.
                self.assertEqual(triggers, {
                    "pull_request": ("main",),
                    "push": ("main",),
                    "workflow_dispatch": None,
                })

    def test_reported_job_names(self) -> None:
        expected = {
            "ci.yml": {
                "repository-validation",
                "secret-scan",
                "macos-host-lock",
                "linux-public-scan (non-authoritative head self-test)",
                "macos-public-scan (non-authoritative head self-test)",
            },
            "go.yml": {"Go (ubuntu-24.04)", "Go (macos-latest)"},
        }
        for filename, expected_names in expected.items():
            with self.subTest(workflow=filename):
                jobs = self.workflow_section(filename, "jobs")
                job_blocks = re.split(r"(?m)^  ([a-z0-9_-]+):\n", jobs)
                names = []
                for job_id, body in zip(job_blocks[1::2], job_blocks[2::2]):
                    name = re.search(r"^    name: (.+)$", body, re.MULTILINE)
                    template = name[1] if name else job_id
                    if "${{ matrix.os }}" in template:
                        matrix = re.search(r"^        os:\n((?:          - [^\n]+\n)+)", body, re.MULTILINE)
                        self.assertIsNotNone(matrix)
                        names.extend(
                            template.replace("${{ matrix.os }}", line.removeprefix("          - "))
                            for line in matrix[1].splitlines()
                        )
                    else:
                        names.append(template)
                self.assertCountEqual(names, expected_names)
