#!/usr/bin/env python3
"""Verify macOS root host_apply_lock() on the real Application Support run path.

Runs as root on a macOS CI runner without executing any configuration phase.
Compatible with the system Python 3.9+ used for staged macOS privileged phases.
"""
from __future__ import annotations

import importlib.util
import os
from pathlib import Path
import stat
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
CONTEND_REFUSED_EXIT = 42


def load_executor():
    spec = importlib.util.spec_from_file_location("execute_setup", ROOT / "scripts/execute-setup.py")
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load scripts/execute-setup.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules["execute_setup"] = module
    spec.loader.exec_module(module)
    return module


def contend(execution) -> int:
    try:
        with execution.host_apply_lock():
            return 0
    except ValueError as error:
        print(str(error), file=sys.stderr)
        return CONTEND_REFUSED_EXIT


def verify_real_macos_lock() -> int:
    if sys.platform != "darwin":
        print("ERROR: this check must run on macOS", file=sys.stderr)
        return 2
    if os.geteuid() != 0:
        print("ERROR: this check must run as root via sudo", file=sys.stderr)
        return 2

    execution = load_executor()
    if "--contend" in sys.argv[1:]:
        return contend(execution)

    expected_root = Path("/Library/Application Support")
    if execution.MACOS_LOCK_ROOT != expected_root:
        print("ERROR: unexpected MACOS_LOCK_ROOT: " + str(execution.MACOS_LOCK_ROOT), file=sys.stderr)
        return 1

    top = expected_root / "agent-workstation-kit"
    run_dir = top / "run"
    lock_file = run_dir / "privileged-setup.lock"
    contender_cmd = [sys.executable, "-I", "-B", str(Path(__file__).resolve()), "--contend"]

    with execution.host_apply_lock():
        for path, expected_mode, kind in (
            (top, 0o755, "dir"),
            (run_dir, 0o700, "dir"),
            (lock_file, 0o600, "file"),
        ):
            info = path.lstat()
            if path.is_symlink():
                raise AssertionError("lock path must not be a symlink: " + str(path))
            if kind == "dir" and not stat.S_ISDIR(info.st_mode):
                raise AssertionError("expected directory: " + str(path))
            if kind == "file" and not stat.S_ISREG(info.st_mode):
                raise AssertionError("expected regular file: " + str(path))
            if info.st_uid != 0:
                raise AssertionError("expected root ownership on: " + str(path))
            actual_mode = stat.S_IMODE(info.st_mode)
            if actual_mode != expected_mode:
                raise AssertionError(
                    "expected mode " + format(expected_mode, "04o") + " on " + str(path)
                    + ", got " + format(actual_mode, "04o")
                )

        contender = subprocess.run(contender_cmd, capture_output=True, text=True, check=False, timeout=30)
        if contender.returncode != CONTEND_REFUSED_EXIT:
            raise AssertionError(
                "expected second process to be refused with exit " + str(CONTEND_REFUSED_EXIT)
                + ", got " + str(contender.returncode) + ": " + contender.stderr
            )
        if "host's privileged setup lock" not in contender.stderr:
            raise AssertionError("unexpected contender refusal message: " + contender.stderr)

    released = subprocess.run(contender_cmd, capture_output=True, text=True, check=False, timeout=30)
    if released.returncode != 0:
        raise AssertionError(
            "expected lock to be acquirable after release, got exit "
            + str(released.returncode) + ": " + released.stderr
        )

    print("PASS macOS root host_apply_lock verified at " + str(run_dir) + " (mode 0700, contention refused)")
    return 0


if __name__ == "__main__":
    raise SystemExit(verify_real_macos_lock())
