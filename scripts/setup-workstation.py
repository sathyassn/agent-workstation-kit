#!/usr/bin/env python3
"""Preview the setup plan and resume from operator-attested private checkpoints.

This command never applies host changes, authenticates vendors or records
approval. --export-plan writes only the new private plan file it names.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

import fleetctl

sys.path.insert(0, str(Path(__file__).resolve().parent / "lib"))
from path_safety import outside_kit

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
    parser = argparse.ArgumentParser(prog="setup-workstation.sh plan", description=__doc__)
    parser.add_argument("--fleet-root", type=Path, required=True)
    parser.add_argument("--profile", type=Path, required=True)
    parser.add_argument("--journal", type=Path, help="private JSON checkpoint journal; read only")
    parser.add_argument("--details", action="store_true", help="show full profile, internal phase commands and acceptance checklist")
    parser.add_argument("--export-plan", type=Path, help="write a portable execution plan outside the kit; no host apply")
    parser.add_argument("--confirm-recovery-tested", action="store_true")
    parser.add_argument("--connection-context", choices=("local-console", "tailscale-ssh"))
    parser.add_argument("--ssh-source-ip")
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
        if args.fleet_root.expanduser().absolute().is_symlink():
            raise ValueError("the fleet root must not be a symlink; pass its real path")
        fleet_root = args.fleet_root.expanduser().resolve()
        if not outside_kit(fleet_root, ROOT):
            raise ValueError("the private fleet root must be outside every public toolkit worktree")
        if fleetctl.fleet_lock_issue(fleet_root):
            raise ValueError("fleet kit.lock must match toolkit VERSION")
        profile_path = fleetctl.resolve_profile(args.profile, fleet_root)
        if profile_path.is_symlink() or profile_path.resolve().parent != fleet_root / "machines":
            raise ValueError("profile must be a regular direct child of private machines/")
        if args.journal and not outside_kit(args.journal.expanduser(), ROOT):
            raise ValueError("checkpoint journals belong outside the public toolkit")
        profile = fleetctl.load_profile(profile_path)
        issues = fleetctl.validate_profile(profile, ready=True)
        if issues:
            fleetctl.show_issues(issues)
            return 2
        fleet_validation = subprocess.run([sys.executable, str(ROOT / "scripts/validate-fleet.py"), str(fleet_root)], check=False, capture_output=True, text=True)
        if fleet_validation.returncode:
            raise ValueError("configuration-wide validation failed: " + fleet_validation.stderr.strip())
        if fleet_validation.stdout.strip():
            print(fleet_validation.stdout.strip().splitlines()[-1])
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
        dirty = subprocess.run(["git", "--no-optional-locks", "status", "--porcelain"], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()
        if dirty:
            print("WARNING: the working tree differs from that revision; the plan binds the current files, not the commit. Commit or discard the changes before review.")
        if args.details or args.journal:
            print(f"Source review ID (not execution approval): {identity}")
        from terminal_ui import UI
        from setup_scopes import SCOPES, expected_owner
        from package_setup import selected_packages
        ui = UI()
        ui.table(("Setting", "Selection"), [
            ("OS", profile["machine"]["platform"]),
            ("Agent account", profile["accounts"]["agent"]),
            ("Human accounts", ", ".join(profile["accounts"]["humans"])),
            ("Assigned admins", ", ".join(profile["accounts"]["admins"])),
            ("Private access", profile["security"]["remote_scope"]),
            ("Management", profile["security"]["endpoint_management"]),
        ])
        rows = []
        for scope, names in SCOPES[profile["machine"]["platform"]].items():
            phases = [fleetctl.phase_for(profile, name, apply=False, recovery=False) for name in names]
            gates = [phase.name for phase in phases if phase.command is None]
            rows.append((scope, expected_owner(names[0], profile["machine"]["platform"]), ", ".join(names) + ("; human gate: " + ", ".join(gates) if gates else "")))
        ui.table(("Scope", "Run as", "Phases"), rows)
        tools = profile["tooling"]
        packages = selected_packages(profile["machine"]["platform"], {"apt": tools.get("apt_packages", []), "formulae": tools.get("brew_formulae", []), "casks": tools.get("brew_casks", [])})
        for kind, values in packages.items():
            ui.say("Packages (" + kind + "): " + ", ".join(values))
        if args.details:
            print("Full non-secret profile:")
            print(json.dumps(profile, sort_keys=True, indent=2))
            print(result.stdout)
            print("Acceptance checkpoints (operator-attested; file hashes do not prove a live result):")
            for key, detail in ACCEPTANCE:
                print(f"{'PENDING' if key in pending else 'RECORDED'} {key}: {detail}")
        else:
            ui.say("Use --details for the full profile, exact helper commands and OS/GUI/recovery checklist.")
        ui.say("Next acceptance candidate: " + (pending[0] if pending else "all recorded; live review required"))
        if journal and journal.get("plan_id") != identity:
            ui.say("Plan changed: prior manual checkpoints need review.")
        ui.say("Human gates: OS/encryption/enrollment/recovery; privileged approval; account passwords; provider MFA and privacy prompts.")
        if args.export_plan:
            from setup_plan import export_plan, plan_digest
            output = args.export_plan.expanduser().absolute()
            if output.exists() or output.is_symlink() or not outside_kit(output, ROOT):
                raise ValueError("export a new execution plan outside the public kit; existing files are preserved")
            portable = export_plan(ROOT, fleet_root, profile_path, profile,
                                   recovery=args.confirm_recovery_tested,
                                   context=args.connection_context, ssh_source_ip=args.ssh_source_ip)
            descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            with os.fdopen(descriptor, "w") as stream:
                json.dump(portable, stream, sort_keys=True, indent=2)
                stream.write("\n")
            print("Exported execution bundle: " + str(output))
            print("Bundle ID: " + plan_digest(portable))
            print("Next: scripts/setup-workstation.sh --plan-file PLAN --scope packages (preview first)")
        print("See docs/runbooks/resumable-setup.md; this preview grants no apply authorization.")
        return 0
    except (OSError, ValueError, TypeError, subprocess.CalledProcessError) as error:
        print(f"ERROR setup plan: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
