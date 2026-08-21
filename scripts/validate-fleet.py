#!/usr/bin/env python3
"""Validate a directory of approved, non-secret fleet profiles as one unit."""

from __future__ import annotations

import argparse
import importlib.util
import sys
from collections import Counter
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("fleetctl", ROOT / "scripts/fleetctl.py")
assert SPEC and SPEC.loader
fleetctl = importlib.util.module_from_spec(SPEC)
sys.modules["fleetctl"] = fleetctl
SPEC.loader.exec_module(fleetctl)


def validate_paths(paths: list[Path]) -> int:
    if not paths:
        print("ERROR: no fleet profiles supplied", file=sys.stderr)
        return 2

    failures = 0
    machine_ids: list[str] = []
    platforms: Counter[str] = Counter()
    for path in paths:
        try:
            profile = fleetctl.load_profile(path)
        except ValueError as exc:
            print(f"ERROR {path.name}: {exc}", file=sys.stderr)
            failures += 1
            continue
        issues = fleetctl.validate_profile(profile, ready=True)
        if issues:
            for issue in issues:
                print(f"ERROR {path.name}:{issue.path}: {issue.message}", file=sys.stderr)
            failures += 1
            continue
        machine_ids.append(profile["machine"]["id"])
        platforms[profile["machine"]["platform"]] += 1
        print(f"PASS {path.name}: {profile['machine']['id']}")

    duplicates = sorted(name for name, count in Counter(machine_ids).items() if count > 1)
    if duplicates:
        print(f"ERROR duplicate machine.id values: {duplicates}", file=sys.stderr)
        failures += 1

    if failures:
        return 1
    summary = ", ".join(f"{name}={count}" for name, count in sorted(platforms.items()))
    print(f"Fleet profiles passed: total={len(paths)}; {summary}")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate all approved TOML profiles in a fleet directory.")
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    paths = sorted(args.directory.glob("*.toml")) if args.directory.is_dir() else []
    return validate_paths(paths)


if __name__ == "__main__":
    raise SystemExit(main())
