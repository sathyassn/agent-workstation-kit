#!/usr/bin/env python3
"""Preview or execute an approved setup scope with live checks and automatic receipts.

Compatible with the system Python 3.9+ used for staged macOS privileged phases.
No sudo, role switching, provider authentication or journal-supplied commands.
"""
from __future__ import annotations
import argparse
from contextlib import contextmanager
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pwd
import shlex
import stat
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/lib"))
from phase_verification import verify_phase, resource_values

from setup_scopes import SCOPES, SCRIPTS, expected_owner
from terminal_ui import UI
from path_safety import outside_kit


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def file_digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def secure_root_path(path):
    path = path.absolute()
    for entry in (path, *path.parents):
        info = entry.lstat()
        if entry.is_symlink() or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError("privileged input/code must be root-owned and not group/world writable: " + str(entry))


def verify_source(plan, root=ROOT):
    if not isinstance(plan.get("source_files"), dict) or not plan["source_files"]:
        raise ValueError("plan has no source manifest")
    for name, expected in plan["source_files"].items():
        path = Path(name)
        if path.is_absolute() or ".." in path.parts:
            raise ValueError("unsafe source manifest path")
        target = root / path
        if target.is_symlink() or not target.resolve().is_relative_to(root.resolve()) or file_digest(target) != expected:
            raise ValueError("source changed since preview: " + name)


def command_for(phase, root=ROOT):
    command = phase["command"]
    if command is None:
        return None
    if not isinstance(command, list) or not command or not all(isinstance(arg, str) and "\0" not in arg for arg in command):
        raise ValueError("invalid phase command")
    expected = {"KIT/scripts/" + name for name in SCRIPTS[phase["name"]]}
    if command[0] not in expected:
        raise ValueError("phase executable is not an approved kit script")
    executable = root / command[0][4:]
    interpreter = "/usr/bin/python3" if executable.suffix == ".py" else "/bin/bash"
    return [interpreter, str(executable), *command[1:]]


def host_binding():
    if sys.platform.startswith("linux"):
        raw = Path("/etc/machine-id").read_text().strip()
    elif sys.platform == "darwin":
        import re
        result = subprocess.run(["/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice"], text=True, capture_output=True, check=True)
        match = re.search(r'"IOPlatformUUID"\s*=\s*"([A-Fa-f0-9-]+)"', result.stdout)
        raw = match.group(1) if match else ""
    else:
        raw = ""
    if not raw:
        raise ValueError("cannot identify target host for execution receipts")
    return hashlib.sha256(raw.encode()).hexdigest()


def phase_input(plan, phase, host, owner):
    # Documentation edits and unrelated phase scripts do not invalidate an applied phase.
    names = {"scripts/fleetctl.py", "scripts/execute-setup.py", "scripts/lib/phase_verification.py", "scripts/lib/setup_scopes.py", "scripts/lib/common.sh"}
    if phase["command"]:
        names.add(phase["command"][0][4:])
    selected = phase["name"]
    if selected in {"base", "remote-hardening"}:
        names |= {"scripts/lib/package_setup.py", "scripts/install-package-set.py", "config/packages.toml"}
    if selected == "user-tooling":
        names |= {"scripts/install-mise.py", "config/mise-bootstrap.toml", "config/mise.toml"}
    if selected == "shell":
        names |= {name for name in plan["source_files"] if name.startswith("dotfiles/")}
    if selected == "agentctl":
        names |= {name for name in plan["source_files"] if name.startswith("agentctl/")}
    return digest({"command": phase["command"], "host": host, "owner": owner, "profile": plan["profile"], "files": {name: value for name, value in plan["source_files"].items() if name in names}})


def require_supported_linux(path=Path("/etc/os-release")):
    release = {}
    for line in path.read_text().splitlines():
        if "=" in line and not line.startswith("#"):
            key, value = line.split("=", 1)
            release[key] = value.strip().strip('"')
    if release.get("ID") != "ubuntu" or release.get("VERSION_ID") not in {"24.04", "26.04"}:
        raise ValueError("Linux apply supports the reviewed Ubuntu 24.04/26.04 paths only; complete OS qualification before using another image")


def approval_id(plan, scope):
    facts = {"host": host_binding()}
    if scope == "resources" and plan["profile"]["machine"]["platform"] == "linux":
        facts["memory_kib"] = int(next(line.split()[1] for line in Path("/proc/meminfo").read_text().splitlines() if line.startswith("MemTotal:")))
    return digest({"bundle": plan, "scope": scope, "target_facts": facts})


def prepare_journal(path):
    if not path.is_absolute() or path.is_symlink():
        raise ValueError("journal must be an absolute non-symlink path outside the kit")
    if not outside_kit(path, ROOT):
        raise ValueError("journal must be outside the public kit")
    parent = path.parent
    if not parent.exists():
        parent.mkdir(mode=0o700, parents=True)
    info = parent.lstat()
    if parent.is_symlink() or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError("journal directory must be owned by the current executor and mode 0700")
    if os.geteuid() == 0:
        secure_root_path(parent.resolve())
    if path.exists():
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError("existing journal must be an owned regular file with mode 0600")


# Descriptors of the locks this process holds. Each phase inherits them, so an
# flock outlives a killed executor until its running phase has exited too.
HELD_LOCKS = []


@contextmanager
def journal_lock(path, label="this account's journal"):
    lock = path.with_name(path.name + ".lock")
    descriptor = os.open(str(lock), os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(descriptor)
        if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError("unsafe journal lock")
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError("another setup process, or a background process a setup phase left running, holds " + label
                             + "; wait for it to exit before starting another apply (find it with: lsof " + str(lock) + ")") from error
        HELD_LOCKS.append(descriptor)
        try:
            yield
        finally:
            HELD_LOCKS.remove(descriptor)
    finally:
        os.close(descriptor)


@contextmanager
def host_apply_lock():
    if os.geteuid() != 0:
        yield
        return
    runtime = Path("/var/run" if sys.platform == "darwin" else "/run").resolve()
    secure_root_path(runtime)
    directory = runtime / "agent-workstation-kit"
    directory.mkdir(mode=0o755, exist_ok=True)
    secure_root_path(directory)
    # A host-level lock serializes root scopes even if different human operators
    # choose separate journal files. flock releases it automatically on exit.
    with journal_lock(directory / "privileged-setup", label="the host's privileged setup lock"):
        yield


def write_journal(path, data):
    descriptor, temporary = tempfile.mkstemp(prefix=".setup-", dir=str(path.parent))
    try:
        with os.fdopen(descriptor, "w") as stream:
            json.dump(data, stream, sort_keys=True, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def execute(plan, phases, journal, *, runner=None, verifier=None, binding=None):
    runner = subprocess.run if runner is None else runner
    verifier = verify_phase if verifier is None else verifier
    host = host_binding() if binding is None else binding
    owner = pwd.getpwuid(os.geteuid()).pw_name
    records = journal.setdefault("executions", {})
    ui = UI()
    for index, phase in enumerate(phases, 1):
        name = phase["name"]
        ui.heading(str(index) + "/" + str(len(phases)) + " · " + name)
        command = command_for(phase)
        if command is None:
            print("HUMAN GATE " + name + ": " + phase["note"])
            return 3
        key = phase_input(plan, phase, host, owner)
        previous = records.get(name, {})
        if not isinstance(previous, dict):
            raise ValueError("invalid phase receipt: " + name)
        if name != "audit" and previous.get("input_id") == key and previous.get("status") == "verified":
            if verifier(plan, name, ROOT):
                ui.say("SKIP " + name + ": receipt matches and live postconditions still pass", "green")
                continue
            ui.say("DRIFT " + name + ": reapplying the idempotent phase", "yellow")
        print("EXEC " + shlex.join(command), flush=True)
        env = os.environ.copy()
        for name_to_remove in ("APPLY_CHANGES", "PYTHONPATH", "PYTHONHOME", "BASH_ENV", "ENV"):
            env.pop(name_to_remove, None)
        env["AWK_PYTHON"] = sys.executable
        if os.geteuid() == 0:
            env = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HOME": pwd.getpwuid(0).pw_dir, "LC_ALL": "C", "AWK_PYTHON": "/usr/bin/python3", "PYTHONDONTWRITEBYTECODE": "1"}
        preview = [arg for arg in command if arg != "--apply"]
        locks = {"pass_fds": tuple(HELD_LOCKS)} if HELD_LOCKS else {}
        preview_result = subprocess.CompletedProcess(preview, 0) if name == "audit" else runner(preview, env=env, check=False, **locks)
        if preview_result.returncode:
            result = preview_result
        else:
            # The scope approval covers the reviewed scripts and parameters;
            # dynamic/unknown prerequisites still fail before their apply.
            result = runner(command, env=env, check=False, **locks)
        success = result.returncode == 0 and verifier(plan, name, ROOT)
        records[name] = {"input_id": key, "status": "verified" if success else "failed", "exit_code": result.returncode, "time": datetime.datetime.now(datetime.timezone.utc).isoformat(), "host": host, "owner": owner}
        yield_record = journal.get("_journal_path")
        if yield_record:
            write_journal(Path(yield_record), {k: v for k, v in journal.items() if k != "_journal_path"})
        if not success:
            print("STOP " + name + ": command or live postcondition failed; later phases were not run")
            return 1
    return 0


def main():
    parser = argparse.ArgumentParser(prog="setup-workstation.sh run", description=__doc__)
    parser.add_argument("--plan-file", type=Path, required=True)
    parser.add_argument("--scope", choices=("packages", "machine", "agent", "resources", "audit"), required=True)
    parser.add_argument("--journal", type=Path)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--approve-plan")
    parser.add_argument("--confirm-target")
    parser.add_argument("--confirm-recovery-tested", action="store_true")
    parser.add_argument("--status", action="store_true", help="show recorded progress without running phases")
    parser.add_argument("--plain", action="store_true", help="disable optional Rich presentation")
    parser.add_argument("--execution-context", choices=("host", "sandbox", "unknown"), default="unknown")
    args = parser.parse_args()
    try:
        if args.status and args.apply:
            raise ValueError("status is read-only; use run/resume for an approved apply")
        plan = json.loads(args.plan_file.read_text())
        if not isinstance(plan, dict) or type(plan.get("schema")) is not int or plan["schema"] != 1:
            raise ValueError("unsupported execution plan schema")
        verify_source(plan)
        profile = plan["profile"]
        platform = profile["machine"]["platform"]
        if profile.get("state") != "approved":
            raise ValueError("execution bundle must contain an approved profile")
        facts = {"host": host_binding()}
        if args.scope == "resources" and platform == "linux":
            facts["memory_kib"] = int(next(line.split()[1] for line in Path("/proc/meminfo").read_text().splitlines() if line.startswith("MemTotal:")))
            print("Calculated resource policy: " + json.dumps(resource_values(profile, facts["memory_kib"]), sort_keys=True))
        identity = approval_id(plan, args.scope)
        selected = SCOPES[platform][args.scope]
        phases = [phase for phase in plan["phases"] if phase["name"] in selected]
        if [phase["name"] for phase in phases] != selected:
            raise ValueError("plan must contain the exact ordered phases in the selected scope")
        for phase in phases:
            name = phase["name"]
            if phase.get("owner") != expected_owner(name, platform):
                raise ValueError("phase owner does not match the platform/role contract")
        print("Execution plan ID: " + identity)
        print("Target: " + profile["machine"]["hostname"] + " | scope: " + args.scope)
        if args.scope == "packages":
            print("Selected packages: " + json.dumps(plan.get("packages", {}), sort_keys=True))
        from terminal_ui import UI
        ui = UI(plain=args.plain)
        if not args.status:
            ui.table(("Phase", "Run as", "Action"), [(phase["name"], phase["owner"], phase["note"]) for phase in phases])
            for phase in phases:
                command = command_for(phase)
                print("Review command: " + (shlex.join(command) if command else "HUMAN GATE"))
        if args.journal and args.journal.exists():
            saved = json.loads(args.journal.read_text())
            if not isinstance(saved, dict):
                raise ValueError("execution journal must be an object with execution receipts")
            records = saved.get("executions", {})
            if not isinstance(records, dict):
                raise ValueError("invalid execution journal")
            owner = pwd.getpwuid(os.geteuid()).pw_name
            host = host_binding()
            rows = []
            next_phase = None
            for phase in phases:
                record = records.get(phase["name"], {})
                if not isinstance(record, dict):
                    raise ValueError("invalid phase receipt")
                matches = record.get("input_id") == phase_input(plan, phase, host, owner)
                status = "recorded; live recheck on resume" if matches and record.get("status") == "verified" else "failed; retry" if matches and record.get("status") == "failed" else "pending or inputs changed"
                rows.append((phase["name"], status))
                if next_phase is None and status != "recorded; live recheck on resume":
                    next_phase = phase["name"]
            ui.table(("Phase", "Recorded status"), rows)
            ui.say("Next candidate: " + (next_phase or "all recorded; resume rechecks live state before skipping"))
        elif args.status:
            ui.say("No execution journal exists for this account. Next candidate: " + phases[0]["name"])
        if not args.apply:
            print("PREVIEW ONLY: no phase or checkpoint was applied")
            return 0
        from readiness import execution_evidence
        if execution_evidence(args.execution_context)[0] != "PASS":
            raise ValueError("apply requires an ordinary target-host terminal and explicit host attestation")
        if args.approve_plan != identity or args.confirm_target != profile["machine"]["hostname"]:
            raise ValueError("apply requires the exact reviewed plan ID and target hostname")
        actual = "macos" if sys.platform == "darwin" else "linux" if sys.platform.startswith("linux") else "unsupported"
        if actual != platform:
            raise ValueError("plan platform does not match execution host")
        if platform == "linux":
            require_supported_linux()
        owner = pwd.getpwuid(os.geteuid()).pw_name
        if os.geteuid() != 0 and sys.version_info < (3, 11):
            raise ValueError("non-root package/agent scopes require the selected Python 3.11+; use setup-workstation.sh")
        for phase in phases:
            if phase["owner"] == "root" and os.geteuid() != 0:
                raise ValueError("scope needs the approving human's staged privileged command; this runner never invokes sudo")
            if phase["owner"] == "agent" and owner != profile["accounts"]["agent"]:
                raise ValueError("run the agent scope directly in the declared agent account")
            if phase["owner"] == "package-owner" and os.geteuid() == 0:
                raise ValueError("Homebrew must run as its named package owner")
        if os.geteuid() == 0:
            if ROOT != Path("/opt/agent-workstation-kit") or not args.confirm_recovery_tested:
                raise ValueError("root apply requires the root-owned /opt toolkit and tested recovery")
            secure_root_path(ROOT)
            for path in ROOT.rglob("*"):
                secure_root_path(path)
            secure_root_path(args.plan_file)
            relative_profile = Path(plan["profile_path"])
            if relative_profile.is_absolute() or relative_profile.parent != Path("machines") or relative_profile.suffix != ".toml":
                raise ValueError("staged profile must be a direct machines/*.toml file")
            profile_path = Path("/opt/agent-workstation-fleet") / relative_profile
            secure_root_path(profile_path)
            if file_digest(profile_path) != plan["profile_sha256"]:
                raise ValueError("staged profile differs from approved execution plan")
        if not args.journal:
            raise ValueError("apply requires a private per-executor journal")
        with host_apply_lock():
            prepare_journal(args.journal)
            with journal_lock(args.journal):
                journal = json.loads(args.journal.read_text()) if args.journal.exists() else {}
                if not isinstance(journal, dict) or not isinstance(journal.get("executions", {}), dict):
                    raise ValueError("invalid execution journal")
                journal["_journal_path"] = str(args.journal)
                result = execute(plan, phases, journal)
                ui.outcome(result)
                if result == 0:
                    ui.say("Receipt journal: " + str(args.journal))
                    next_scope = {"packages": "machine", "machine": "agent", "agent": "audit (resources require prior measurement)", "resources": "audit", "audit": "OS/GUI/provider/reboot acceptance and handoff"}[args.scope]
                    ui.say("Next: " + next_scope + ". Change executor accounts only through the approved workflow; this command never provisions another role by switching users.")
                return result
    except (OSError, ValueError, KeyError, TypeError, StopIteration, subprocess.SubprocessError) as error:
        print("ERROR execution: " + str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
