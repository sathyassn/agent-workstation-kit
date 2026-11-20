"""Build a portable, content-bound execution plan from a validated profile."""
from __future__ import annotations
import hashlib
import json
from pathlib import Path
import subprocess
import sys


def plan_digest(plan: dict) -> str:
    return hashlib.sha256(json.dumps(plan, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def export_plan(root: Path, fleet_root: Path, profile_path: Path, profile: dict,
                *, recovery: bool = False, context: str | None = None,
                ssh_source_ip: str | None = None) -> dict:
    import fleetctl
    from package_setup import selected_packages
    from setup_scopes import expected_owner
    raw = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], cwd=root, check=True, capture_output=True).stdout
    revision = subprocess.run(["git", "rev-parse", "HEAD"], cwd=root, check=True, text=True, capture_output=True).stdout.strip()
    files = {}
    for item in sorted(set(raw.split(b"\0")) - {b""}):
        name = item.decode()
        path = root / name
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"source must be regular files: {name}")
        files[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    phases = []
    for name in fleetctl.PHASES:
        phase = fleetctl.phase_for(profile, name, apply=True, recovery=recovery, connection_context=context, ssh_source_ip=ssh_source_ip)
        command = list(phase.command) if phase.command else None
        if command:
            command[0] = "KIT/" + str(Path(command[0]).relative_to(root))
        owner = expected_owner(name, profile["machine"]["platform"])
        phases.append({"name": name, "command": command, "owner": owner, "note": phase.note})
    platform = profile["machine"]["platform"]
    tooling = profile["tooling"]
    return {
        "schema": 1, "kit_version": fleetctl.VERSION, "revision": revision,
        "source_files": files, "profile": profile,
        "profile_path": str(profile_path.resolve().relative_to(fleet_root.resolve())),
        "profile_sha256": hashlib.sha256(profile_path.read_bytes()).hexdigest(),
        "user_python": "python3.11+",
        "packages": selected_packages(platform, {"apt": tooling.get("apt_packages", []), "formulae": tooling.get("brew_formulae", []), "casks": tooling.get("brew_casks", [])}),
        "phases": phases,
    }
