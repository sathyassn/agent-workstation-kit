#!/usr/bin/env python3
"""One user-facing command for workstation planning, execution and checks."""
from __future__ import annotations
import importlib.util
import json
import os
from pathlib import Path
import shlex
import shutil
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from terminal_ui import UI
from path_safety import outside_kit

COMMANDS = ("guide", "init", "plan", "run", "resume", "status", "check-access", "workloads", "doctor")


def load(filename):
    path = ROOT / "scripts" / filename
    name = "_setup_cli_" + path.stem.replace("-", "_")
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    sys.modules[name] = value
    spec.loader.exec_module(value)
    return value


def call(filename, arguments):
    old = sys.argv
    try:
        sys.argv = [filename, *arguments]
        return load(filename).main()
    finally:
        sys.argv = old


def help_text(ui):
    ui.heading("Agent Workstation Kit · one setup command")
    ui.table(("Action", "Purpose"), (
        ("guide", "Interactive walkthrough; preview is the default"),
        ("init", "Preview/create a draft profile; --write creates data only"),
        ("plan", "Validate the profile, show settings and export an execution plan"),
        ("run / resume", "Preview or execute one approved owner scope; automatic receipts"),
        ("status", "Read recorded progress and next candidate; no changes"),
        ("check-access", "Preview TCP, named-user SSH/SFTP and reconnect probes"),
        ("workloads", "Project browser/container checks in the current non-root agent account"),
        ("doctor", "Read-only OS/runtime/prerequisite checks"),
    ))
    ui.say("Use scripts/setup-workstation.sh ACTION --help for its options.")
    ui.say("Provisioning run/resume needs the reviewed execution plan and target. Optional probes/workload tests preview first and require explicit --apply.")
    ui.say("Walkthrough: docs/runbooks/resumable-setup.md")


def doctor(ui):
    platform = "macos" if sys.platform == "darwin" else "linux" if sys.platform.startswith("linux") else "unsupported"
    rows = [("OS", platform), ("Python", sys.version.split()[0]), ("Source planning runtime", "ready" if sys.version_info >= (3, 11) else "Python 3.11+ required; use the day-zero guide")]
    for name in ("git", "ssh", "sftp", "brew" if platform == "macos" else "apt-get"):
        rows.append((name, shutil.which(name) or "missing: complete the OS day-zero prerequisite"))
    rows.append(("This executor", "root: staged /opt execution only" if os.geteuid() == 0 else "non-root: package-owner or declared agent scopes"))
    ui.table(("Check", "Result"), rows)
    ui.say("Next: finish docs/runbooks/day-zero-" + ("macos" if platform == "macos" else "linux") + ".md, then plan the approved profile.")
    return 0 if platform != "unsupported" else 2


def path_input(ui, prompt, *, existing=False, new=False, directory=False, default=None):
    while True:
        value = ui.ask(prompt + " (q cancels)", default)
        if value == "q":
            raise EOFError
        if not value or any(ord(character) < 32 for character in value):
            ui.say("Enter a path without control characters.", "yellow")
            continue
        path = Path(value).expanduser().absolute()
        if existing and (not path.exists() or (directory and not path.is_dir()) or (not directory and not path.is_file())):
            ui.say("That " + ("directory" if directory else "file") + " does not exist; check the path.", "yellow")
            continue
        if new and (path.exists() or path.is_symlink() or not outside_kit(path, ROOT)):
            ui.say("Choose a new private path outside the toolkit; existing files are preserved.", "yellow")
            continue
        return path


def init_profile(ui, arguments):
    if sys.version_info < (3, 11):
        ui.say("Draft profile creation needs the named setup owner's Python 3.11+.")
        return 2
    if "--help" in arguments or "-h" in arguments:
        ui.say("setup-workstation.sh init machines/NAME.toml --fleet-root PRIVATE_ROOT [profile options] [--write]")
        ui.say("Preview is default; --write allocates the draft UUID/profile, not an OS account.")
        return call("fleetctl.py", ["init", "--help"])
    if not arguments or arguments[0].startswith("-"):
        ui.say("Select machines/NAME.toml first, then --fleet-root and the context/namespace/hostname/platform/human options. Use init --help.")
        return 2
    output = Path(arguments[0])
    if output.is_absolute() or output.parent != Path("machines") or output.suffix != ".toml":
        ui.say("The draft must be a direct machines/NAME.toml file inside the private configuration root.")
        return 2
    rest = list(arguments[1:])
    write = "--write" in rest
    rest = [value for value in rest if value != "--write"]
    roots = []
    forwarded = []
    index = 0
    while index < len(rest):
        value = rest[index]
        if value == "--fleet-root":
            if index + 1 >= len(rest):
                ui.say("--fleet-root needs a directory.")
                return 2
            roots.append(Path(rest[index + 1]).expanduser().resolve())
            index += 2
        elif value.startswith("--fleet-root="):
            roots.append(Path(value.split("=", 1)[1]).expanduser().resolve())
            index += 1
        else:
            forwarded.append(value)
            index += 1
    if len(roots) != 1 or not roots[0].is_dir() or not outside_kit(roots[0], ROOT):
        ui.say("Select one existing private configuration root outside the public kit.")
        return 2
    if (roots[0] / output).exists() or (roots[0] / output).is_symlink():
        ui.say("That profile already exists and will be preserved. Use plan to review it.")
        return 2
    args = ["--fleet-root", str(roots[0]), "init", str(output), *forwarded]
    if not write:
        ui.say("DRAFT PREVIEW: " + shlex.join(["setup-workstation.sh", "init", str(output), "--fleet-root", str(roots[0]), *forwarded, "--write"]))
        ui.say("No profile/UUID was allocated. With --write, the existing initializer validates choices, checks uniqueness and writes a draft atomically. Resolve every ask and obtain review before plan/apply.")
        return 0
    return call("fleetctl.py", args)


def plan_guide(ui):
    if sys.version_info < (3, 11) or os.geteuid() == 0:
        ui.say("Create the source plan as the named setup owner with Python 3.11+. Root is reserved for reviewed staged execution.", "yellow")
        return 2
    ui.say("Need a new profile first? Use this same entry point's init action (init --help). It previews by default; --write creates only a draft configuration.")
    fleet = path_input(ui, "Private configuration root", existing=True, directory=True)
    profile = path_input(ui, "Approved machine TOML profile", existing=True)
    if profile.resolve().parent != (fleet / "machines").resolve():
        ui.say("Select a direct machines/*.toml profile inside that configuration root.", "yellow")
        return 2
    output = path_input(ui, "New execution-plan output", new=True)
    context = ui.choice("Connection to the target", ("local-console", "tailscale-ssh"), "local-console")
    recovery = ui.choice("Has the independent console/unlock recovery test actually passed?", ("yes", "no"), "no")
    args = ["--fleet-root", str(fleet), "--profile", str(profile), "--export-plan", str(output), "--connection-context", context]
    if recovery == "yes":
        args.append("--confirm-recovery-tested")
    if context == "tailscale-ssh":
        peer = ui.ask("Verified SSH peer captured before sudo (not a credential)")
        import ipaddress
        try:
            ipaddress.ip_address(peer)
        except (ValueError, TypeError):
            ui.say("Enter a valid verified peer IP; no plan was exported.", "yellow")
            return 2
        args += ["--ssh-source-ip", peer]
    output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    code = call("setup-workstation.py", args)
    if code == 0:
        ui.plan_path = str(output)
        ui.say("Next: stage the exact reviewed source/profile/plan for privileged work. Preview the packages scope, then machine, then agent; run resources only after measurement and audit at handoff.")
    return code


def run_guide(ui, status=False):
    plan_path = path_input(ui, "Reviewed execution-plan file", existing=True, default=getattr(ui, "plan_path", None))
    ui.plan_path = str(plan_path)
    scope = ui.choice("Owner scope", ("packages", "machine", "agent", "resources", "audit"), "packages")
    journal_path = path_input(ui, "Private journal for this executing account", default=str(Path.home() / ".local/state/agent-workstation-kit/setup.json"))
    args = ["--plan-file", str(plan_path), "--scope", scope, "--journal", str(journal_path)]
    engine = load("execute-setup.py")
    reviewed_bytes = plan_path.read_bytes()
    plan = json.loads(reviewed_bytes)
    reviewed_approval = engine.approval_id(plan, scope)
    code = call("execute-setup.py", [*args, *(["--status"] if status else [])])
    if code != 0 or status:
        return code
    if plan_path.read_bytes() != reviewed_bytes or engine.approval_id(plan, scope) != reviewed_approval:
        ui.say("Plan or target facts changed during preview; preview again before approving.", "yellow")
        return 2
    selected = engine.SCOPES[plan["profile"]["machine"]["platform"]][scope]
    phases = [phase for phase in plan["phases"] if phase["name"] in selected]
    import pwd
    owner = pwd.getpwuid(os.geteuid()).pw_name
    if any(phase["owner"] == "root" for phase in phases) and os.geteuid() != 0:
        ui.say("This scope needs the approving human at the root-owned staged toolkit. The wizard will not invoke sudo or apply user-writable source.", "yellow")
        ui.say("Next: follow the staged root command in docs/runbooks/resumable-setup.md, preview there, then approve that scope.")
        return 3
    if any(phase["owner"] == "agent" for phase in phases) and owner != plan["profile"]["accounts"]["agent"]:
        ui.say("Next: enter the declared agent's actual session and rerun this entry point. Supply that account a reviewed readable copy of the execution plan.", "yellow")
        return 3
    if os.geteuid() == 0 and ROOT != Path("/opt/agent-workstation-kit"):
        ui.say("Root apply is available only from the reviewed root-owned /opt snapshot. Stage it and preview again there.", "yellow")
        return 3
    target = plan["profile"]["machine"]["hostname"]
    phrase = "APPLY " + target + " " + scope
    ui.say("This can install/configure the displayed scope. Credentials and OS prompts remain with you.")
    response = ui.ask("Type exactly '" + phrase + "' to approve this displayed plan, or press Enter to keep preview only", "")
    if response != phrase:
        ui.say("Preview retained; no phase was applied.")
        return 0
    if os.geteuid() == 0:
        confirmed = ui.choice("Is independent console recovery open and tested now?", ("yes", "no"), "no")
        if confirmed != "yes":
            ui.say("Recovery is required; no phase was applied.")
            return 3
        args.append("--confirm-recovery-tested")
    args += ["--apply", "--approve-plan", reviewed_approval, "--confirm-target", target, "--execution-context", "host"]
    return call("execute-setup.py", args)


def access_guide(ui):
    checker = load("verify-remote-access.py")
    while True:
        host = ui.ask("Private host name/address (no URLs, pairing links or credentials; q cancels)")
        if host == "q":
            raise EOFError
        if host and checker.valid_host(host):
            break
        ui.say("Use a host name or IP address only.", "yellow")
    while True:
        raw = ui.ask("Selected TCP ports, comma separated", "22")
        try:
            ports = [int(item.strip()) for item in raw.split(",")]
            if not ports or any(not 1 <= port <= 65535 for port in ports):
                raise ValueError
            break
        except (ValueError, AttributeError):
            ui.say("Use port numbers from 1 to 65535.", "yellow")
    args = ["--host", host]
    for port in ports:
        args += ["--tcp-port", str(port)]
    user = ui.ask("Named SSH user; leave empty for TCP checks only", "")
    if user:
        import re
        if not re.fullmatch(r"[a-z_][a-z0-9_-]{0,30}", user):
            ui.say("Invalid named user; no connection was made.", "yellow")
            return 2
        args += ["--ssh-user", user]
        while True:
            port = ui.ask("SSH/SFTP port", "22")
            try:
                port = int(port)
                if not 1 <= port <= 65535:
                    raise ValueError
                break
            except (ValueError, TypeError):
                ui.say("Use a port from 1 to 65535.", "yellow")
        args += ["--ssh-port", str(port)]
        if ui.choice("Include a disposable SFTP hash roundtrip?", ("yes", "no"), "no") == "yes":
            args.append("--sftp-roundtrip")
    code = call("verify-remote-access.py", args)
    if code:
        return code
    phrase = "CHECK " + host
    if ui.ask("Type '" + phrase + "' to run these checks with existing known-host credentials, or Enter for preview only", "") != phrase:
        return 0
    return call("verify-remote-access.py", [*args, "--apply"])


def guide(ui):
    if not sys.stdin.isatty() or not sys.stdout.isatty():
        ui.say("The guided walkthrough needs an interactive terminal. Use plan/run/status/check-access flags for scripts or agents.")
        return 2
    ui.heading("Agent Workstation Kit · guided setup")
    ui.say("Preview first. No passwords, tokens or pairing links are collected here.")
    doctor(ui)
    while True:
        ui.table(("Choose", "Action"), (("1", "Create/review the source plan"), ("2", "Preview/run/resume an owner scope"), ("3", "Show recorded status and next action"), ("4", "Check TCP / SSH / SFTP access"), ("q", "Exit")))
        choice = ui.choice("Action", ("1", "2", "3", "4", "q"), "q")
        if choice == "q":
            return 0
        if choice == "1":
            code = plan_guide(ui)
        elif choice == "2":
            code = run_guide(ui)
        elif choice == "3":
            code = run_guide(ui, status=True)
        else:
            code = access_guide(ui)
        ui.outcome(code)
        ui.say()


def main(arguments=None):
    args = list(sys.argv[1:] if arguments is None else arguments)
    plain = "--plain" in args
    args = [arg for arg in args if arg != "--plain"]
    ui = UI(plain=plain)
    if not args:
        args = ["guide"] if sys.stdin.isatty() and sys.stdout.isatty() else ["--help"]
    # Keep the earlier flag form usable while presenting verbs in the walkthrough.
    if args[0] in {"--help", "-h"}:
        help_text(ui)
        return 0
    command = args.pop(0) if not args[0].startswith("-") else "run" if any(arg == "--plan-file" or arg.startswith("--plan-file=") for arg in args) else "plan"
    try:
        if command == "help" or command in {"guide", "doctor"} and args == ["--help"]:
            help_text(ui)
            return 0
        if command == "init":
            return init_profile(ui, args)
        if command == "plan":
            if sys.version_info < (3, 11):
                ui.say("Source planning requires Python 3.11+. Complete the OS day-zero prerequisite or select AWK_PYTHON.")
                return 2
            return call("setup-workstation.py", args)
        if command in {"run", "resume", "status"}:
            return call("execute-setup.py", [*args, *(["--status"] if command == "status" else []), *(["--plain"] if plain else [])])
        if command == "check-access":
            return call("verify-remote-access.py", args)
        if command == "workloads":
            helper = ROOT / "scripts/check-project-workloads.sh"
            import subprocess
            return subprocess.run(["/bin/bash", str(helper), *args], check=False).returncode
        if command in {"doctor", "guide"}:
            if args:
                ui.say("That action takes no additional arguments. Use --help for supported commands.")
                return 2
            return doctor(ui) if command == "doctor" else guide(ui)
        ui.say("Unknown action: " + command + ". Choose " + ", ".join(COMMANDS) + ".")
        return 2
    except (KeyboardInterrupt, EOFError):
        ui.say("Cancelled. Previously verified receipts are retained; no additional phase will be started.")
        return 130 if sys.exc_info()[0] is KeyboardInterrupt else 0
    except (OSError, ValueError, KeyError, TypeError, ImportError) as error:
        ui.say("Stopped: " + str(error), "red")
        ui.say("Check the displayed path/runtime, then retry. No later phase was started.")
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
