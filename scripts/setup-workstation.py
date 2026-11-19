#!/usr/bin/env python3
"""Preview the setup plan and resume from operator-attested private checkpoints.

This command never applies changes, authenticates vendors or records approval.
"""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

import fleetctl

ROOT = Path(__file__).resolve().parents[1]
ACCEPTANCE = (
    ("os-recovery", "Exact OS/media/firmware; target disk; encrypted cold unlock and independent recovery"),
    ("profile", "Approved profile, fleet uniqueness, target UUID, package sources and account owners"),
    *((f"phase-{name}", "Preview this fleetctl phase; scoped human approval; staged privileged apply if required; verify preservation") for name in fleetctl.PHASES),
    ("peer", "Viewer and host identities; private peer presence"),
    ("listener", "Host app owner/version/session; advertised endpoint equals actual listener address/port"),
    ("tcp", "Actual viewer-to-host TCP connection for every selected service; firewall and both networks' policy"),
    ("ssh-sftp", "Named-user SSH plus disposable SFTP upload/download and matching SHA-256"),
    ("orca", "Selected SSH worktree or Desktop pairing; authenticated project operation; app reconnect"),
    ("desktop", "Selected GUI: named-user Apple Screen Sharing on Mac or approved cross-platform desktop; control and observer"),
    ("transfer", "Optional GUI file transfer/clipboard separately; approved project transfer and identity boundary"),
    ("workloads", "Each provider real minimal run; project browser preview; pinned Playwright; container smoke"),
    ("sleep", "Approved sleep/wake policy; reconnect and lock behavior"),
    ("reboot", "Human-approved reboot; encrypted unlock; host/app reconnect; repeat named-user access"),
    ("handoff", "Account isolation, backup/restore, partial-failure resume, repeat-run preservation and live gaps"),
)


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def plan_id(source_digest: str, profile_digest: str, target: str, commands: str) -> str:
    return hashlib.sha256(json.dumps([source_digest, profile_digest, target, commands, ACCEPTANCE]).encode()).hexdigest()


def remaining(plan: str, journal: dict) -> list[str]:
    """A checkpoint only survives while its plan, evidence and predecessors do."""
    records = journal.get("checkpoints", {}) if journal.get("plan_id") == plan else {}
    pending = []
    invalid = False
    for key, _ in ACCEPTANCE:
        record = records.get(key, {})
        if not isinstance(record, dict):
            record = {}
        evidence = record.get("evidence", [])
        valid = record.get("status") == "verified" and isinstance(evidence, list) and bool(evidence)
        try:
            valid = valid and all(
                Path(item["path"]).is_absolute()
                and digest(Path(item["path"])) == item["sha256"]
                for item in evidence
            )
        except (OSError, KeyError, TypeError):
            valid = False
        invalid = invalid or not valid
        if invalid:
            pending.append(key)
    return pending


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fleet-root", type=Path, required=True)
    parser.add_argument("--profile", type=Path, required=True)
    parser.add_argument("--journal", type=Path, help="private JSON checkpoint journal; read only")
    args = parser.parse_args()
    try:
        if not (ROOT / ".git").exists():
            raise ValueError("setup plans require the source worktree, not a staged snapshot")
        top = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=ROOT,
                             check=True, capture_output=True, text=True)
        if Path(top.stdout.strip()).resolve() != ROOT:
            raise ValueError("Git root does not match toolkit root")
        revision = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT,
                                  check=True, capture_output=True, text=True).stdout.strip()
        fleet_root = args.fleet_root.expanduser().resolve()
        if fleetctl.fleet_lock_issue(fleet_root):
            raise ValueError("fleet kit.lock must match toolkit VERSION")
        profile_path = fleetctl.resolve_profile(args.profile, fleet_root)
        if profile_path.is_symlink() or profile_path.resolve().parent != fleet_root / "machines":
            raise ValueError("profile must be a regular direct child of private machines/")
        if args.journal and args.journal.expanduser().resolve().is_relative_to(ROOT):
            raise ValueError("checkpoint journals belong outside the public toolkit")
        profile = fleetctl.load_profile(profile_path)
        issues = fleetctl.validate_profile(profile, ready=True)
        if issues:
            fleetctl.show_issues(issues)
            return 2
        # Include the exact current content of source files, including new files.
        files = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                               cwd=ROOT, check=True, capture_output=True).stdout.split(b"\0")
        source = hashlib.sha256()
        for name in sorted(set(files) - {b""}):
            path = ROOT / name.decode()
            if not path.is_file() or path.is_symlink():
                raise ValueError("source must contain only regular tracked/input files")
            source.update(name + b"\0" + path.read_bytes() + b"\0")
        result = subprocess.run([sys.executable, str(ROOT / "scripts/fleetctl.py"),
                                 "--fleet-root", str(fleet_root), "plan", str(args.profile)],
                                check=True, capture_output=True, text=True)
        identity = plan_id(revision + ":" + source.hexdigest(), digest(profile_path), profile["machine"]["uuid"], result.stdout)
        journal = json.loads(args.journal.read_text()) if args.journal else {}
        if not isinstance(journal, dict) or not isinstance(journal.get("checkpoints", {}), dict):
            raise ValueError("journal must be an object with a checkpoints object")
        pending = remaining(identity, journal)
        print(f"PREVIEW ONLY | version={fleetctl.VERSION} | target={profile['machine']['hostname']} uuid={profile['machine']['uuid']}")
        print(f"Source revision: {revision} (current content bound below; remote freshness unverified)")
        print(f"Plan ID: {identity}")
        print("Settings (non-secret profile; public defaults < approved private defaults < reviewed host overrides):")
        print(json.dumps(profile, sort_keys=True, indent=2))
        print(result.stdout)
        print("Human gates: OS/disk/enrollment/recovery; each scoped privileged apply; account/access changes; vendor MFA/consent.")
        print("Recovery: keep tested console/KVM, backups and matching staged revisions; stop at the first failure; sudo -K after each apply.")
        print("Acceptance checkpoints (operator-attested; file hashes do not prove a live result):")
        for key, detail in ACCEPTANCE:
            print(f"{'PENDING' if key in pending else 'RECORDED'} {key}: {detail}")
        print("Next: " + (pending[0] if pending else "all checkpoints recorded; independent live review required"))
        if journal and journal.get("plan_id") != identity:
            print("Plan changed: prior approval/checkpoints invalid; review the changed scope before applying.")
        print("See docs/runbooks/resumable-setup.md; this preview grants no apply authorization.")
        return 0
    except (OSError, ValueError, TypeError, subprocess.CalledProcessError) as error:
        print(f"ERROR setup plan: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
