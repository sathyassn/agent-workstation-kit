#!/usr/bin/env python3
"""Validate a private fleet repository as one consistent, non-secret unit."""

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


def duplicates(values: list[str]) -> list[str]:
    return sorted(value for value, count in Counter(values).items() if count > 1)


def validate_paths(paths: list[Path], *, fleet_root: Path | None = None) -> int:
    if not paths:
        print("ERROR: no fleet profiles supplied", file=sys.stderr)
        return 2
    failures = 0
    profiles: list[tuple[Path, dict]] = []
    for path in paths:
        try:
            profile = fleetctl.load_profile(path)
        except ValueError as exc:
            print(f"ERROR {path.name}: {exc}", file=sys.stderr)
            failures += 1
            continue
        issues = fleetctl.validate_profile(profile, ready=True)
        if path.stem != profile.get("machine", {}).get("hostname"):
            issues.append(fleetctl.Issue("machine.hostname", "must match the profile filename"))
        if issues:
            for issue in issues:
                print(f"ERROR {path.name}:{issue.path}: {issue.message}", file=sys.stderr)
            failures += 1
            continue
        profiles.append((path, profile))
        print(f"PASS {path.name}: {profile['machine']['hostname']}")

    checks = {
        "machine.hostname": [p["machine"]["hostname"] for _, p in profiles],
        "machine.display_name": [fleetctl.comparison_key(p["machine"]["display_name"]) for _, p in profiles],
        "machine.uuid": [p["machine"]["uuid"].lower() for _, p in profiles],
        "machine.asset_tag": [fleetctl.comparison_key(p["machine"]["asset_tag"]) for _, p in profiles],
    }
    for label, values in checks.items():
        repeated = duplicates(values)
        if repeated:
            print(f"ERROR duplicate {label} values: {repeated}", file=sys.stderr)
            failures += 1

    # Human and administrative names are intentionally reusable across hosts,
    # but each hostname/account pair must remain unique.
    principals: list[str] = []
    for _, profile in profiles:
        host = profile["machine"]["hostname"]
        accounts = profile["accounts"]
        principals.extend(f"{host}/{name}" for name in [accounts["agent"], *accounts["humans"], *accounts["admins"], *accounts["services"]])
    repeated = duplicates(principals)
    if repeated:
        print(f"ERROR duplicate host/account principals: {repeated}", file=sys.stderr)
        failures += 1

    provider_bindings: dict[tuple[str, str], set[tuple[str, ...]]] = {}
    for _, profile in profiles:
        scm, collaboration = profile["source_control"], profile["collaboration"]
        bindings = (
            ("gitlab", scm["gitlab_principal"], (scm["gitlab_host"], scm["gitlab_identity"])),
            ("github", scm["github_principal"], (scm["github_host"], scm["github_identity"])),
            (
                "atlassian",
                collaboration["atlassian_principal"],
                (
                    collaboration["atlassian_site"],
                    collaboration["atlassian_identity"],
                    collaboration["atlassian_mcp_auth"],
                ),
            ),
        )
        for provider, principal, definition in bindings:
            if principal != "none":
                provider_bindings.setdefault((provider, fleetctl.comparison_key(principal)), set()).add(definition)
    for (provider, principal), definitions in sorted(provider_bindings.items()):
        if len(definitions) > 1:
            print(f"ERROR inconsistent {provider} principal {principal!r}: {sorted(definitions)}", file=sys.stderr)
            failures += 1

    if fleet_root:
        lock = fleet_root / "kit.lock"
        if not lock.is_file():
            print("ERROR kit.lock: private fleet must pin the toolkit version", file=sys.stderr)
            failures += 1
        elif lock.read_text(encoding="utf-8").strip() != fleetctl.VERSION:
            print(f"ERROR kit.lock: expected {fleetctl.VERSION}", file=sys.stderr)
            failures += 1
        retired = fleet_root / "retired-hostnames.txt"
        if retired.is_file():
            lines = [line.strip() for line in retired.read_text(encoding="utf-8").splitlines()]
            names = {line for line in lines if line and not line.startswith("#")}
            reused = sorted(names & set(checks["machine.hostname"]))
            if reused:
                print(f"ERROR retired hostnames cannot be reused: {reused}", file=sys.stderr)
                failures += 1

    if failures:
        return 1
    platforms = Counter(profile["machine"]["platform"] for _, profile in profiles)
    summary = ", ".join(f"{name}={count}" for name, count in sorted(platforms.items()))
    print(f"Fleet profiles passed: total={len(profiles)}; {summary}")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate all approved TOML profiles in a private fleet repository.")
    parser.add_argument("fleet_root", type=Path)
    args = parser.parse_args()
    machines = args.fleet_root / "machines"
    if machines.is_dir():
        misplaced = sorted(args.fleet_root.glob("*.toml"))
        if misplaced:
            names = ", ".join(path.name for path in misplaced)
            print(f"ERROR: machine profiles must be under machines/: {names}", file=sys.stderr)
            return 1
        nested = sorted(path for path in machines.rglob("*.toml") if path.parent != machines)
        if nested:
            names = ", ".join(str(path.relative_to(args.fleet_root)) for path in nested)
            print(f"ERROR: nested machine profiles are unsupported and would escape the fleet gate: {names}", file=sys.stderr)
            return 1
    directory = machines if machines.is_dir() else args.fleet_root
    paths = sorted(directory.glob("*.toml")) if directory.is_dir() else []
    return validate_paths(paths, fleet_root=args.fleet_root)


if __name__ == "__main__":
    raise SystemExit(main())
