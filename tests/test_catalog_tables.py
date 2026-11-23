"""The catalog_tables.py wrapper delegates to the Go generator and fails if go is missing."""
from __future__ import annotations

import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WRAPPER = ROOT / "tests" / "catalog_tables.py"


def _make_stub_go(directory: Path, exit_code: int) -> None:
    stub = directory / "go"
    stub.write_text(
        f"#!/bin/sh\necho \"stub-go $*\"\nexit {exit_code}\n",
        encoding="utf-8",
    )
    stub.chmod(stub.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


class CatalogTablesWrapperTests(unittest.TestCase):
    def _run_wrapper(self, path_env: str, *args: str) -> subprocess.CompletedProcess[str]:
        env = {k: v for k, v in os.environ.items() if k != "PATH"}
        env["PATH"] = path_env
        return subprocess.run(
            [sys.executable, str(WRAPPER), *args],
            cwd=str(ROOT),
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_stub_go_exits_one_propagates_failure(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _make_stub_go(Path(tmp), 1)
            proc = self._run_wrapper(tmp)
            self.assertEqual(proc.returncode, 1)
            self.assertIn("-check", proc.stdout)

    def test_stub_go_exits_zero_propagates_success(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _make_stub_go(Path(tmp), 0)
            proc = self._run_wrapper(tmp)
            self.assertEqual(proc.returncode, 0)
            self.assertIn("-check", proc.stdout)

    def test_missing_go_fails_for_check_and_write(self) -> None:
        for args in ((), ("--write",)):
            with self.subTest(args=args), tempfile.TemporaryDirectory() as empty_bin:
                proc = self._run_wrapper(empty_bin, *args)
                self.assertNotEqual(proc.returncode, 0)
                combined = proc.stdout + proc.stderr
                self.assertIn("Go is required for the catalog check", combined)


if __name__ == "__main__":
    unittest.main()
