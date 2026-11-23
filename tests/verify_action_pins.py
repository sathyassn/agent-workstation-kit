#!/usr/bin/env python3
"""Verify that every pinned GitHub Action commit in tests/action_pins.json matches its remote tag."""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path


PINS_PATH = Path(__file__).resolve().parent / "action_pins.json"
PIN_KEY_RE = re.compile(r"^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@([0-9a-f]{40})$")
SHA_RE = re.compile(r"^[0-9a-f]{40}$")


def load_action_pins(path: Path) -> tuple[dict[str, str] | None, str | None]:
    """Load and validate an action_pins.json table."""
    if not path.is_file():
        return None, f"action pin table is missing: {path}"
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return None, f"cannot read action pin table {path}: {exc}"
    if not isinstance(raw, dict):
        return None, f"action pin table {path} must be a JSON object"
    pins: dict[str, str] = {}
    for key, value in raw.items():
        if not isinstance(key, str) or not PIN_KEY_RE.fullmatch(key):
            return None, f"invalid pin key in {path}: {key!r}"
        if not isinstance(value, str) or not value.strip():
            return None, f"invalid tag value for {key!r} in {path}: {value!r}"
        pins[key] = value.strip()
    return pins, None


def resolve_tag_commit(ls_remote_output: str, tag: str) -> str | None:
    """Parse `git ls-remote` output and return the commit SHA for TAG (peeled `^{}` preferred)."""
    direct_ref = f"refs/tags/{tag}"
    peeled_ref = f"refs/tags/{tag}^{{}}"
    direct_sha: str | None = None
    peeled_sha: str | None = None
    for line in ls_remote_output.splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split()
        if len(parts) < 2:
            continue
        sha, ref = parts[0], parts[1]
        if not SHA_RE.fullmatch(sha):
            continue
        if ref == peeled_ref:
            peeled_sha = sha
        elif ref == direct_ref:
            direct_sha = sha
    return peeled_sha if peeled_sha is not None else direct_sha


def verify_ls_remote_output(pin: str, tag: str, ls_remote_output: str) -> str | None:
    """Compare a pin entry against `git ls-remote` output; return an error string or None."""
    match = PIN_KEY_RE.fullmatch(pin)
    if not match:
        return f"invalid pin entry: {pin!r}"
    expected_sha = match.group(2)
    actual_sha = resolve_tag_commit(ls_remote_output, tag)
    if actual_sha is None:
        return f"{pin} ({tag}): tag refs/tags/{tag} not found in git ls-remote output"
    if actual_sha != expected_sha:
        return f"{pin} ({tag}): expected commit {expected_sha}, remote resolved to {actual_sha}"
    return None


def verify_pin(pin: str, tag: str) -> str | None:
    """Run `git ls-remote` for PIN and verify that TAG resolves to the pinned commit."""
    match = PIN_KEY_RE.fullmatch(pin)
    if not match:
        return f"invalid pin entry: {pin!r}"
    repo = match.group(1)
    url = f"https://github.com/{repo}"
    env = os.environ.copy()
    env["GIT_TERMINAL_PROMPT"] = "0"
    try:
        result = subprocess.run(
            ["git", "ls-remote", url, f"refs/tags/{tag}", f"refs/tags/{tag}^{{}}"],
            capture_output=True,
            text=True,
            env=env,
            timeout=60,
            check=False,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired) as exc:
        return f"{pin} ({tag}): git ls-remote failed: {exc}"
    if result.returncode != 0:
        detail = result.stderr.strip() or f"exit {result.returncode}"
        return f"{pin} ({tag}): git ls-remote {url} failed: {detail}"
    return verify_ls_remote_output(pin, tag, result.stdout)


def main(argv: list[str] | None = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    path = Path(args[0]) if args else PINS_PATH
    pins, load_error = load_action_pins(path)
    if load_error is not None:
        print(f"Action pin verification failed: {load_error}", file=sys.stderr)
        return 1
    assert pins is not None
    failures: list[str] = []
    for pin, tag in sorted(pins.items()):
        error = verify_pin(pin, tag)
        if error is not None:
            failures.append(error)
    if failures:
        print("Action pin verification failed:", file=sys.stderr)
        for failure in failures:
            print(f"- {failure}", file=sys.stderr)
        return 1
    print(f"Verified {len(pins)} action pin(s) in {path.name}.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
