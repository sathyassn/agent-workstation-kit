"""Read-only postconditions for execution receipts; no provider authentication."""
from __future__ import annotations
from decimal import Decimal
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import sys
import subprocess


def probe(command, *, env=None):
    try:
        return subprocess.run(command, check=False, text=True, capture_output=True, timeout=60, env=env)
    except (OSError, subprocess.TimeoutExpired):
        return subprocess.CompletedProcess(command, 1, "", "unavailable")


def same_file(source, target):
    return target.is_file() and source.read_bytes() == target.read_bytes()


def identity_matches(profile):
    machine = profile["machine"]
    platform = machine["platform"]
    path = Path("/etc/agent-workstation-kit/identity.toml") if platform == "linux" else Path("/Library/Application Support/Agent Workstation Kit/identity.toml")
    if not path.is_file() or path.is_symlink() or path.stat().st_uid != 0 or stat.S_IMODE(path.stat().st_mode) != 0o644:
        return False
    if path.parent.is_symlink() or path.parent.stat().st_uid != 0 or stat.S_IMODE(path.parent.stat().st_mode) != 0o755:
        return False
    values = {}
    try:
        for line in path.read_text().splitlines():
            if " = " in line and line.split(" = ", 1)[0] != "schema_version":
                key, value = line.split(" = ", 1)
                values[key] = json.loads(value)
    except (ValueError, OSError):
        return False
    expected = {key: machine[key] for key in ("hostname", "display_name", "uuid", "asset_tag", "platform", "role")}
    expected["namespace"] = profile["deployment"]["namespace"]
    if values != expected:
        return False
    if platform == "macos":
        return all(probe(["scutil", "--get", key]).stdout.strip() == value for key, value in (("HostName", machine["hostname"]), ("LocalHostName", machine["hostname"]), ("ComputerName", machine["display_name"])))
    if any(probe(["hostnamectl", flag]).stdout.strip() != value for flag, value in (("--static", machine["hostname"]), ("--transient", machine["hostname"]), ("--pretty", machine["display_name"]))):
        return False
    addresses = probe(["ip", "-o", "addr", "show"])
    resolved = probe(["getent", "hosts", machine["hostname"]])
    if addresses.returncode or resolved.returncode:
        return False
    local = set()
    try:
        for line in addresses.stdout.splitlines():
            fields = line.split()
            for family in ("inet", "inet6"):
                if family in fields:
                    local.add(ipaddress.ip_address(fields[fields.index(family) + 1].split("/")[0]))
        return any((address := ipaddress.ip_address(line.split()[0])).is_loopback or address in local for line in resolved.stdout.splitlines())
    except (ValueError, IndexError):
        return False


def account_problems(profile):
    """Return a reason for every declared-account postcondition that fails.

    Only the agent account must use zsh: the accounts phase creates it with
    zsh, while existing human and admin accounts are preserved unchanged.
    """
    accounts = profile["accounts"]
    group = "sudo" if profile["machine"]["platform"] == "linux" else "admin"
    problems = []
    for name in [accounts["agent"], *accounts["humans"], *accounts["admins"]]:
        try:
            entry = pwd.getpwnam(name)
        except KeyError:
            problems.append(name + " does not exist")
            continue
        if entry.pw_uid == 0:
            problems.append(name + " has UID 0")
            continue
        home = Path(entry.pw_dir)
        if not home.is_dir() or home.stat().st_uid != entry.pw_uid:
            problems.append(name + " does not own an existing home directory")
        if name == accounts["agent"] and not entry.pw_shell.endswith("zsh"):
            problems.append(name + " (agent) does not use zsh")
        memberships = probe(["id", "-nG", name])
        if memberships.returncode:
            problems.append("cannot read group memberships of " + name)
            continue
        groups = memberships.stdout.split()
        if (name in accounts["admins"]) != (group in groups):
            problems.append(name + (" is not" if name in accounts["admins"] else " must not be") + " in " + group)
        if name == accounts["agent"] and "docker" in groups:
            problems.append(name + " is in the root-equivalent docker group")
    if profile["machine"]["platform"] == "linux":
        for suffix in ("operators", "viewers"):
            group_name = accounts["agent"] + "-" + suffix
            if probe(["getent", "group", group_name]).returncode:
                problems.append("group " + group_name + " does not exist")
                continue
            for person in accounts[suffix]:
                if group_name not in probe(["id", "-nG", person]).stdout.split():
                    problems.append(person + " is not in " + group_name)
    for service in accounts.get("services", []):
        try:
            entry = pwd.getpwnam(service)
        except KeyError:
            problems.append("service account " + service + " does not exist")
            continue
        if entry.pw_uid == 0 or not entry.pw_shell.endswith(("nologin", "false")):
            problems.append("service account " + service + " has UID 0 or a login shell")
        memberships = probe(["id", "-nG", service])
        if memberships.returncode or set(memberships.stdout.split()) & {"sudo", "admin", "docker"}:
            problems.append("service account " + service + " has privileged or unreadable groups")
    return problems


def sudo_grant_problems(names, run=None):
    """Prove that each named account has no sudoers command grant.

    Group checks miss a direct sudoers entry such as `agent ALL=(ALL) ALL`.
    `sudo -l -U` (root only) reports the effective policy; anything other than
    an explicit "is not allowed to run sudo" fails closed.
    """
    run = run or probe
    problems = []
    for name in names:
        result = run(["sudo", "-n", "-l", "-U", name], env={**os.environ, "LC_ALL": "C"})
        output = (result.stdout or "") + (result.stderr or "")
        if "may run the following commands" in output:
            problems.append(name + " has a sudoers command grant")
        elif "is not allowed to run sudo" not in output:
            problems.append("cannot prove that " + name + " has no sudoers command grant")
    return problems


def accounts_match(profile):
    accounts = profile["accounts"]
    # Human operators hold the scoped agentctl grant by design; the agent and
    # service accounts must hold none.
    problems = account_problems(profile) + sudo_grant_problems([accounts["agent"], *accounts.get("services", [])])
    for problem in problems:
        print("REASON accounts: " + problem)
    return not problems


def sshd_effective(output):
    """Parse `sshd -T`, which prints one line per value for list keywords."""
    values = {}
    for line in output.splitlines():
        fields = line.split(maxsplit=1)
        if len(fields) == 2:
            values.setdefault(fields[0].lower(), []).append(fields[1])
    return values


# Loopback plus an address in the Tailscale CGNAT range, so `Match Address`
# blocks for either path are evaluated.
SSHD_TEST_ADDRESSES = ("127.0.0.1", "100.64.0.1")
SSHD_REQUIRED = (("permitrootlogin", "no"), ("passwordauthentication", "no"), ("kbdinteractiveauthentication", "no"))


def sshd_conditional_problems(users, run=None):
    """Evaluate `sshd -T -C` for each user and address so Match blocks apply.

    Plain `sshd -T` shows only the global configuration, so a later
    `Match User x` with `PasswordAuthentication yes` would pass unseen.
    """
    run = run or probe
    problems = []
    for user in users:
        for address in SSHD_TEST_ADDRESSES:
            result = run(["sshd", "-T", "-C", "user=" + user + ",host=localhost,addr=" + address])
            if result.returncode:
                problems.append("cannot read effective sshd policy for " + user + " from " + address)
                continue
            config = sshd_effective(result.stdout)
            for key, expected in SSHD_REQUIRED:
                if config.get(key) != [expected]:
                    problems.append("sshd " + key + " is not " + expected + " for " + user + " from " + address)
    return problems


SSHD_CONFIG = Path("/etc/ssh/sshd_config")
# ChallengeResponseAuthentication is the deprecated alias of KbdInteractive.
SSHD_SENSITIVE = {key for key, _ in SSHD_REQUIRED} | {"challengeresponseauthentication"}


def sshd_match_problems(config=SSHD_CONFIG):
    """Fail closed on any Match block that sets a hardened keyword.

    `sshd -T -C` treats criteria it is not given (LocalAddress, LocalPort,
    RDomain, a real client Host or Address) as not matching, so those blocks
    are never evaluated. The kit's own drop-in uses no Match blocks, so any
    Match that sets these keywords needs a person to review it.
    """
    return _sshd_match_scan(Path(config), 0, False)[0]


def _sshd_match_scan(config, depth, inside):
    """Return (problems, inside_match_after) for one configuration file.

    Relative Include paths resolve against /etc/ssh, as sshd does. A Match
    started in an included file is treated as still open afterwards, so the
    scan can over-report but never miss a block.
    """
    import glob
    if depth > 8:
        return ["sshd Include nesting is too deep to verify"], inside
    try:
        lines = config.read_text().splitlines()
    except OSError:
        return ["cannot read sshd configuration " + str(config)], inside
    problems = []
    for line in lines:
        fields = line.split("#", 1)[0].replace("=", " ", 1).split()
        if not fields:
            continue
        keyword = fields[0].lower()
        if keyword == "match":
            inside = True
        elif keyword == "include":
            for pattern in fields[1:]:
                pattern = pattern.strip('"')
                pattern = pattern if pattern.startswith("/") else "/etc/ssh/" + pattern
                for included in sorted(glob.glob(pattern)):
                    found, opened = _sshd_match_scan(Path(included), depth + 1, inside)
                    problems += found
                    inside = inside or opened
        elif inside and keyword in SSHD_SENSITIVE:
            problems.append("sshd Match block in " + str(config) + " sets " + keyword + "; review or remove it")
    return problems, inside


def sshd_problems(output, ssh_users, agent):
    config = sshd_effective(output)
    problems = []
    for key, expected in (("permitrootlogin", "no"), ("passwordauthentication", "no"), ("kbdinteractiveauthentication", "no")):
        if config.get(key) != [expected]:
            problems.append("sshd " + key + " is not " + expected)
    allowed = {user for value in config.get("allowusers", []) for user in value.split()}
    denied = {user for value in config.get("denyusers", []) for user in value.split()}
    if allowed != set(ssh_users):
        problems.append("sshd AllowUsers does not exactly match accounts.ssh_users")
    if agent not in denied:
        problems.append("sshd DenyUsers does not include the agent account")
    return problems


def ufw_allows_in(status, rule, interface):
    """True when `ufw status` output allows RULE inbound on INTERFACE.

    Plain `ufw status` prints incoming rules as `ALLOW` with no direction;
    `ufw status verbose` prints `ALLOW IN`. Outgoing (`ALLOW OUT`) and routed
    (`ALLOW FWD`) rules never count.
    """
    for line in status.splitlines():
        fields = line.split()
        if fields[:4] == [rule, "on", interface, "ALLOW"] and (len(fields) < 5 or fields[4] not in ("OUT", "FWD")):
            return True
    return False


RESOURCE_INT_FLAGS = {
    "--memory-reserve-percent": (5, 50),
    "--memory-high-percent": (10, 90),
    "--memory-max-percent": (20, 95),
    "--swap-max-percent": (0, 50),
    "--oomd-pressure-percent": (10, 99),
    "--cpu-weight": (1, 10000),
    "--reserve-cores": (0, 1024),
    "--zram-percent": (0, 100),
}
RESOURCE_DEFAULTS = {
    "--memory-reserve-percent": None,
    "--reserve-fixed-gib": None,
    "--memory-high-percent": None,
    "--memory-max-percent": None,
    "--swap-max-percent": None,
    "--oomd-pressure-percent": 60,
    "--cpu-weight": None,
    "--reserve-cores": None,
    "--swappiness": "auto",
    "--zram-percent": 0,
    "--tmp": "auto",
}
# Receipt input keys, in the order apply-resource-policy-linux.sh records them.
RESOURCE_INPUT_KEYS = (
    ("reserve_fixed_gib", "--reserve-fixed-gib"),
    ("memory_reserve_percent", "--memory-reserve-percent"),
    ("memory_high_percent", "--memory-high-percent"),
    ("memory_max_percent", "--memory-max-percent"),
    ("swap_max_percent", "--swap-max-percent"),
    ("cpu_weight", "--cpu-weight"),
    ("reserve_cores", "--reserve-cores"),
    ("oomd_pressure_percent", "--oomd-pressure-percent"),
    ("swappiness", "--swappiness"),
    ("zram_percent", "--zram-percent"),
    ("tmp", "--tmp"),
    ("allow_small_reserve", "--allow-small-reserve"),
)
AGENT_SLICE = Path("/etc/systemd/user/agent-work.slice")
AGENT_PARENT_SLICE = Path("/etc/systemd/user/agent.slice.d/50-agent-workstation-kit.conf")
RESOURCE_RECEIPT = Path("/var/lib/agent-workstation-kit/resource-policy/receipt.tsv")
CGROUP_CONTROLLERS = Path("/sys/fs/cgroup/cgroup.controllers")
OOMD_BINARIES = (Path("/usr/lib/systemd/systemd-oomd"), Path("/lib/systemd/systemd-oomd"))
DISPLAY_MANAGERS = (
    "display-manager.service",
    "gdm.service",
    "gdm3.service",
    "sddm.service",
    "lightdm.service",
)
TIMESPAN_UNITS_USEC = {
    "us": 1,
    "ms": 1_000,
    "s": 1_000_000,
    "min": 60 * 1_000_000,
    "h": 3600 * 1_000_000,
    "d": 86400 * 1_000_000,
    "w": 7 * 86400 * 1_000_000,
}
_WHOLE_NUMBER = re.compile(r"(0|[1-9][0-9]{0,5})")
_GIB_NUMBER = re.compile(r"(0|[1-9][0-9]{0,5})(?:\.([0-9]{1,6}))?")
_TIMESPAN_TOKEN = re.compile(r"([0-9]{1,12}(?:\.[0-9]{1,6})?)(us|ms|s|min|h|d|w)")


def parse_systemd_timespan_usec(text):
    """Parse systemd's formatted timespan into microseconds, or None for infinity.

    Accepts space-separated parts in us, ms, s, min, h, d, w (such as '1min',
    '2min 4s', '14s', '500ms'). Raises ValueError on empty or malformed text.
    """
    if not isinstance(text, str):
        raise ValueError("timespan must be a string")
    stripped = text.strip()
    if not stripped:
        raise ValueError("empty systemd timespan")
    if stripped == "infinity":
        return None
    if stripped == "0":
        return 0
    total = Decimal(0)
    for token in stripped.split():
        match = _TIMESPAN_TOKEN.fullmatch(token)
        if not match:
            raise ValueError("malformed systemd timespan token: " + token)
        part = Decimal(match.group(1)) * TIMESPAN_UNITS_USEC[match.group(2)]
        if part != int(part):
            raise ValueError("sub-microsecond systemd timespan: " + token)
        total += part
    return int(total)


def strict_int(name, raw, lower, upper):
    """Mirror normalize_int in common.sh: check the text before arithmetic.

    At most six digits and no leading zero, so 18446744073709551666, -1, 08,
    abc and '' are refused; a bool is never a number.
    """
    if isinstance(raw, bool) or not _WHOLE_NUMBER.fullmatch(str(raw)):
        raise ValueError(name + " must be a whole number with at most six digits and no leading zeros")
    value = int(str(raw))
    if not lower <= value <= upper:
        raise ValueError(name + " must be from " + str(lower) + " through " + str(upper))
    return value


def strict_gib(name, raw):
    """Mirror normalize_gib in common.sh; return the normalised text (6.50 -> 6.5)."""
    match = None if isinstance(raw, bool) else _GIB_NUMBER.fullmatch(str(raw))
    if not match:
        raise ValueError(name + " must be a positive number with at most six digits before and after the point")
    whole, fraction = match.group(1), (match.group(2) or "").rstrip("0")
    if whole == "0" and not fraction:
        raise ValueError(name + " must be a positive number")
    return whole + ("." + fraction if fraction else "")


def resource_options(command=(), profile=None):
    """Return the resource flags of a resources phase command, with the script defaults.

    Profile fields and flags are checked as the script checks them, before any
    arithmetic; an invalid value raises ValueError so the verifier fails closed.
    """
    options = dict(RESOURCE_DEFAULTS)
    options["--measure"] = False
    options["--allow-small-reserve"] = False
    resources = (profile or {}).get("resources", {})
    for field, flag in (("os_memory_reserve_percent", "--memory-reserve-percent"), ("reserve_fixed_gib", "--reserve-fixed-gib"),
                        ("reserve_cores", "--reserve-cores"), ("cpu_weight", "--cpu-weight")):
        if field in resources:
            raw = resources[field]
            # fleetctl passes str(value); a float such as 8.0 arrives as "8.0".
            options[flag] = strict_gib("resources." + field, raw) if flag == "--reserve-fixed-gib" else strict_int("resources." + field, raw, *RESOURCE_INT_FLAGS[flag])
    args = list(command or ())
    index = 0
    while index < len(args):
        arg = args[index]
        if arg in ("--measure", "--allow-small-reserve"):
            options[arg] = True
            index += 1
        elif arg in RESOURCE_DEFAULTS and index + 1 < len(args):
            raw = args[index + 1]
            if arg == "--reserve-fixed-gib":
                options[arg] = strict_gib(arg, raw)
            elif arg == "--tmp":
                if raw not in ("auto", "disk", "keep"):
                    raise ValueError("--tmp must be auto, disk or keep")
                options[arg] = raw
            elif arg == "--swappiness" and raw in ("auto", "keep"):
                options[arg] = raw
            elif arg == "--swappiness":
                options[arg] = strict_int(arg, raw, 0, 200)
            else:
                options[arg] = strict_int(arg, raw, *RESOURCE_INT_FLAGS[arg])
            index += 2
        else:
            index += 1
    return options


def resource_receipt_inputs(options):
    """Return the receipt's input lines (key -> text) that these options imply."""
    inputs = {}
    for key, flag in RESOURCE_INPUT_KEYS:
        value = options[flag]
        inputs[key] = "-" if value is None else ("true" if value is True else "false" if value is False else str(value))
    return inputs


def host_swap_total_kib(swaps_path=Path("/proc/swaps")):
    """Sum active swap sizes in KiB from /proc/swaps; return 0 when none or unreadable."""
    try:
        lines = Path(swaps_path).read_text().splitlines()
    except OSError:
        return 0
    total = 0
    for line in lines:
        fields = line.split()
        if len(fields) >= 3 and fields[0] != "Filename" and fields[2].isdigit():
            total += int(fields[2])
    return total


def host_zram_swap_kib(swaps_path=Path("/proc/swaps")):
    """Sum active /dev/zram* swap sizes in KiB from /proc/swaps."""
    try:
        lines = Path(swaps_path).read_text().splitlines()
    except OSError:
        return 0
    total = 0
    for line in lines:
        fields = line.split()
        if len(fields) >= 3 and fields[0].startswith("/dev/zram") and fields[2].isdigit():
            total += int(fields[2])
    return total


def host_cpu_cores(cpuinfo_path=Path("/proc/cpuinfo")):
    """Count CPU cores from /proc/cpuinfo, falling back to os.cpu_count()."""
    try:
        count = sum(1 for line in Path(cpuinfo_path).read_text().splitlines() if re.match(r"^processor\s*:", line))
        if count >= 1:
            return count
    except OSError:
        pass
    return os.cpu_count() or 1


def host_desktop_present(run=None):
    """True when graphical.target or a display manager service is active."""
    run = run or probe
    if run(["systemctl", "is-active", "--quiet", "graphical.target"]).returncode == 0:
        return True
    return any(run(["systemctl", "is-active", "--quiet", dm]).returncode == 0 for dm in DISPLAY_MANAGERS)


def measured_non_agent_kib(uid, cgroup_root=Path("/sys/fs/cgroup")):
    """Return 1.5x the memory (KiB) in use outside the agent's agent.slice.

    Sums memory.current of every top-level cgroup (init.scope included) and
    subtracts the agent.slice subtree. Informational only: never sizes a value.
    """
    root = Path(cgroup_root)
    total_bytes = 0
    found = False
    for candidate in sorted(root.glob("*/memory.current")):
        try:
            text = candidate.read_text().strip()
        except OSError:
            continue
        if text.isdigit():
            total_bytes += int(text)
            found = True
    if not found:
        raise ValueError("no top-level cgroup memory.current is readable")
    agent = root / ("user.slice/user-%d.slice/user@%d.service/agent.slice/memory.current" % (uid, uid))
    try:
        text = agent.read_text().strip()
        if text.isdigit() and int(text) <= total_bytes:
            total_bytes -= int(text)
    except OSError:
        pass
    return (total_bytes * 3) // 2048


def resource_values(profile, total, command=(), *, swap_total_kib=None, cpu_cores=None, desktop=None, planned_zram_kib=None, active_zram_kib=None):
    """Mirror reserve-based sizing in common.sh and apply-resource-policy-linux.sh.

    swap_total_kib is the active swap (disk plus active_zram_kib). Whenever
    planned_zram_kib > 0 (from --zram-percent), it replaces active_zram_kib in
    the swap total rather than adding to it.
    """
    gib = 1024 * 1024
    min_slice = 2 * gib
    options = resource_options(command, profile)
    if options["--reserve-fixed-gib"] is not None and options["--memory-reserve-percent"] is not None:
        raise ValueError("cannot combine --reserve-fixed-gib with --memory-reserve-percent")

    if swap_total_kib is None:
        swap_total_kib = host_swap_total_kib()
        if active_zram_kib is None:
            active_zram_kib = host_zram_swap_kib()
    elif active_zram_kib is None:
        active_zram_kib = 0
    if active_zram_kib < 0 or active_zram_kib > swap_total_kib:
        raise ValueError("active zram swap cannot exceed total swap")
    if planned_zram_kib is None:
        planned_zram_kib = total * options["--zram-percent"] // 100
    if cpu_cores is None:
        cpu_cores = host_cpu_cores()
    if desktop is None:
        desktop = host_desktop_present()

    reserve_pct = options["--memory-reserve-percent"]
    if reserve_pct is not None:
        reserve_kib = total * reserve_pct // 100
    else:
        role_default_kib = (8 if desktop else 2) * gib
        if options["--reserve-fixed-gib"] is not None:
            fixed_kib = int(float(options["--reserve-fixed-gib"]) * gib + 0.5)
        else:
            fixed_kib = role_default_kib
        reserve_kib = max(fixed_kib, fixed_kib + total * 2 // 100)

    max_pct = options["--memory-max-percent"]
    high_pct = options["--memory-high-percent"]
    if max_pct is not None:
        maximum = total * max_pct // 100
        if maximum < min_slice:
            raise ValueError("the agent memory ceiling is below 2 GiB")
        outside = total - maximum
        if (outside < reserve_kib or outside < min_slice) and not options["--allow-small-reserve"]:
            raise ValueError("the agent memory ceiling leaves less than the reserve or 2 GiB without --allow-small-reserve")
    else:
        if total - reserve_kib < min_slice:
            raise ValueError("host RAM minus reserve is below 2 GiB")
        maximum = total - reserve_kib

    if high_pct is not None:
        high = total * high_pct // 100
        if high <= 0 or high >= maximum:
            raise ValueError("MemoryHigh must be positive and below MemoryMax")
    else:
        margin = min(maximum * 10 // 100, 8 * gib)
        high = maximum - margin
        if high <= 0 or high >= maximum:
            raise ValueError("MemoryHigh must be positive and below MemoryMax")

    swap_pct = options["--swap-max-percent"]
    swap_basis = (swap_total_kib - active_zram_kib) + planned_zram_kib if planned_zram_kib > 0 else swap_total_kib
    if swap_pct is not None:
        swap_max = total * swap_pct // 100
    elif swap_basis == 0:
        swap_max = 0
    else:
        swap_max = min(swap_basis * 25 // 100, 8 * gib)

    if options["--reserve-cores"] is not None:
        reserved_cores = options["--reserve-cores"]
        if reserved_cores < 0 or reserved_cores >= cpu_cores:
            raise ValueError("reserved cores must be less than total CPU cores")
    else:
        reserved_cores = min(max((cpu_cores + 19) // 20, 2), 4)
        if cpu_cores <= reserved_cores:
            reserved_cores = cpu_cores - 1
    cpu_quota = (cpu_cores - reserved_cores) * 100

    cpu_weight = options["--cpu-weight"]
    return {
        "CPUWeight": str(50 if cpu_weight is None else cpu_weight),
        "CPUQuota": str(cpu_quota) + "%",
        "TasksMax": str(max(4096, min(16384, total // gib * 128))),
        "MemoryHigh": str(high) + "K",
        "MemoryMax": str(maximum) + "K",
        "MemorySwapMax": str(swap_max) + "K",
        "ManagedOOMMemoryPressureLimit": str(options["--oomd-pressure-percent"]) + "%",
        "reserve_kib": str(reserve_kib),
    }


def read_resource_receipt(path=RESOURCE_RECEIPT):
    """Parse a version 2 resource-policy receipt into its recorded parts."""
    path = Path(path)
    # The receipt, its folder and the kit's state folder; /var and /var/lib too
    # for the installed location.
    checked = [path, path.parent, path.parent.parent] + ([Path("/var/lib"), Path("/var")] if path == RESOURCE_RECEIPT else [])
    if any(item.is_symlink() for item in checked):
        raise ValueError("the resource receipt or a parent is a symbolic link")
    lines = path.read_text().splitlines()
    if lines[:1] != ["version\t2"] or len(lines) < 2 or not lines[1].startswith("agent\t"):
        raise ValueError("unsupported resource receipt format")
    receipt = {"agent": lines[1].split("\t", 1)[1], "input": {}, "reading": {}, "value": {}, "complete": False}
    for line in lines[2:]:
        fields = line.split("\t")
        if fields[0] in ("input", "reading", "value") and len(fields) == 3:
            receipt[fields[0]][fields[1]] = fields[2]
        elif fields[0] == "state" and len(fields) == 2:
            if fields[1] == "complete":
                receipt["complete"] = True
            elif fields[1] == "migrating":
                receipt["complete"] = False
            else:
                raise ValueError("unknown resource receipt state: " + fields[1])
    return receipt


def receipt_resource_values(receipt, profile, command, agent):
    """Derive the slice values from the receipt's approved inputs and readings.

    The verifier never re-reads RAM, swap or cores: it checks that the receipt
    belongs to this agent and plan, then recomputes from the recorded readings
    and requires the recorded values to match.
    """
    if not receipt["complete"]:
        raise ValueError("the resource apply did not finish")
    if receipt["agent"] != agent:
        raise ValueError("the resource receipt belongs to another account")
    options = resource_options(command, profile)
    if receipt["input"] != resource_receipt_inputs(options):
        raise ValueError("the resource receipt's approved inputs differ from the plan")
    reading = receipt["reading"]
    if reading.get("role") not in ("desktop", "headless"):
        raise ValueError("the resource receipt lacks the host role")
    numbers = {key: strict_reading(reading.get(key)) for key in ("ram_kib", "swap_kib", "zram_planned_kib", "cpu_cores")}
    active_zram_kib = strict_reading(reading["zram_kib"]) if "zram_kib" in reading else 0
    if active_zram_kib > numbers["swap_kib"]:
        raise ValueError("the resource receipt zram_kib exceeds swap_kib")
    if numbers["zram_planned_kib"] != numbers["ram_kib"] * options["--zram-percent"] // 100:
        raise ValueError("the resource receipt zram_planned_kib does not match --zram-percent")
    values = resource_values(profile, numbers["ram_kib"], command, swap_total_kib=numbers["swap_kib"], cpu_cores=numbers["cpu_cores"],
                             desktop=reading["role"] == "desktop", planned_zram_kib=numbers["zram_planned_kib"],
                             active_zram_kib=active_zram_kib)
    recorded = receipt["value"]
    expected = {
        "reserve_kib": values["reserve_kib"],
        "memory_max_kib": values["MemoryMax"][:-1],
        "memory_high_kib": values["MemoryHigh"][:-1],
        "swap_max_kib": values["MemorySwapMax"][:-1],
        "cpu_weight": values["CPUWeight"],
        "cpu_quota_percent": values["CPUQuota"][:-1],
        "tasks_max": values["TasksMax"],
        "oomd_pressure_percent": values["ManagedOOMMemoryPressureLimit"][:-1],
    }
    for key, value in expected.items():
        if recorded.get(key) != value:
            raise ValueError("the resource receipt value " + key + " does not match its inputs")
    return values


def strict_reading(text):
    if text is None or not re.fullmatch(r"[0-9]{1,15}", text):
        raise ValueError("the resource receipt has an unreadable reading")
    return int(text)


def effective_resource_problems(agent, uid, values, run=None, controllers_path=CGROUP_CONTROLLERS):
    """Compare the values systemd holds for both slices with the expected ones.

    Requires the cpu and memory controllers to be available and delegated to
    the agent's user manager, then reads the effective properties.
    """
    run = run or probe
    problems = []
    try:
        available = Path(controllers_path).read_text().split()
    except OSError:
        available = []
    delegated = run(["systemctl", "show", "--property=DelegateControllers", "--value", "user@%d.service" % uid])
    delegated_names = delegated.stdout.split() if delegated.returncode == 0 else []
    for controller in ("cpu", "memory"):
        if controller not in available:
            problems.append("the cgroup v2 " + controller + " controller is not available")
        if controller not in delegated_names:
            problems.append("user@%d.service does not delegate the %s controller" % (uid, controller))
    user = ["systemctl", "--user", "--machine=" + agent + "@", "show"]
    expected = {
        "agent-work.slice": {
            "MemoryHigh": str(int(values["MemoryHigh"][:-1]) * 1024),
            "MemoryMax": str(int(values["MemoryMax"][:-1]) * 1024),
            "MemorySwapMax": str(int(values["MemorySwapMax"][:-1]) * 1024),
            "TasksMax": values["TasksMax"],
            "CPUQuotaPerSecUSec": int(values["CPUQuota"][:-1]) * 10_000,
        },
        "agent.slice": {"CPUWeight": values["CPUWeight"]},
    }
    for unit, properties in expected.items():
        result = run([*user, unit, "--property=" + ",".join(properties)])
        if result.returncode:
            problems.append("cannot read the effective settings of " + unit)
            continue
        effective = dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
        for key, value in properties.items():
            if key == "CPUQuotaPerSecUSec":
                raw = effective.get(key)
                try:
                    actual_usec = None if raw is None else parse_systemd_timespan_usec(raw)
                except ValueError:
                    problems.append("%s effective %s is malformed (%s), expected %dus (%s)" % (unit, key, raw, value, values["CPUQuota"]))
                    continue
                if actual_usec != value:
                    problems.append("%s effective %s is %s, expected %dus (%s)" % (unit, key, "unset" if raw is None else raw, value, values["CPUQuota"]))
            elif effective.get(key) != value:
                problems.append("%s effective %s is %s, expected %s" % (unit, key, effective.get(key, "unset"), value))
    return problems


def agent_processes_outside_slice(root, uid, proc_root="/proc"):
    """List "PID NAME CGROUP" for agent processes of uid outside agent-work.slice.

    Uses the same shell predicate as validate-host.sh. Returns None when the
    check cannot run, so a caller fails closed.
    """
    check = 'source "$1"; agent_processes_outside_slice "$2" "$3"'
    result = probe(["bash", "-c", check, "verify", str(root / "scripts/lib/common.sh"), proc_root, str(uid)])
    return result.stdout.splitlines() if result.returncode == 0 else None


def agent_slice_text(agent, values, oomd):
    """Render agent-work.slice exactly as apply-resource-policy-linux.sh writes it."""
    lines = ["# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.", "[Unit]", "Description=Agent work ceiling for " + agent, "ConditionUser=" + agent, "", "[Slice]"]
    lines += [key + "=" + values[key] for key in ("CPUQuota", "TasksMax", "MemoryHigh", "MemoryMax", "MemorySwapMax")]
    if oomd:
        lines += ["ManagedOOMMemoryPressure=kill", "ManagedOOMMemoryPressureLimit=" + values["ManagedOOMMemoryPressureLimit"]]
    return "\n".join(lines) + "\n"


def agent_parent_slice_text(agent, values):
    """Render the agent.slice drop-in that holds the agent's CPUWeight."""
    lines = ["# Managed by agent-workstation-kit apply-resource-policy-linux.sh; undo with --revert.", "[Unit]", "ConditionUser=" + agent, "", "[Slice]", "CPUWeight=" + values["CPUWeight"]]
    return "\n".join(lines) + "\n"


def verify_phase(plan, name, root):
    """Return false for unknown/unprovable state; never promote an exit code alone."""
    profile = plan["profile"]
    agent = profile["accounts"]["agent"]
    platform = profile["machine"]["platform"]
    try:
        phase = next(item for item in plan["phases"] if item["name"] == name)
        command = phase["command"]
        if name == "base":
            argv = [str(root / command[0][4:]), *(arg for arg in command[1:] if arg != "--apply"), "--check"]
            env = os.environ.copy()
            env["AWK_PYTHON"] = sys.executable
            ok = probe(argv, env=env).returncode == 0
        elif name == "identity":
            ok = identity_matches(profile)
        elif name == "accounts":
            ok = accounts_match(profile)
        elif name == "shell":
            home = Path(pwd.getpwuid(os.geteuid()).pw_dir)
            ref = profile["tooling"]["antidote_ref"]
            current = probe(["git", "-C", str(home / ".antidote"), "rev-parse", "HEAD"])
            expected = probe(["git", "-C", str(home / ".antidote"), "rev-parse", ref + "^{commit}"])
            ok = current.returncode == expected.returncode == 0 and current.stdout.strip() == expected.stdout.strip() and all(same_file(root / source, home / target) for source, target in (("dotfiles/zshrc.managed", ".zshrc"), ("dotfiles/zsh_plugins.txt", ".zsh_plugins.txt")))
        elif name == "user-tooling":
            tools = ["node", "python", "bun", "pnpm", "gh", "glab", "herdr", "codex", "claude"]
            if profile["tooling"]["gws"] == "install":
                tools.append("gws")
            local_mise = Path(pwd.getpwuid(os.geteuid()).pw_dir) / ".local/bin/mise"
            mise = str(local_mise) if local_mise.is_file() else "mise"
            home = Path(pwd.getpwuid(os.geteuid()).pw_dir)
            desired = (root / "config/mise.toml").read_bytes()
            if profile["tooling"]["gws"] == "install":
                desired += b'\n# Optional profile-selected tool.\n"npm:@googleworkspace/cli" = "latest"\n'
            config = home / ".config/mise/config.toml"
            ok = config.is_file() and config.read_bytes() == desired
            ok = ok and all(probe([mise, "exec", "--", tool, "--version"]).returncode == 0 for tool in tools)
            if "mise_sha256" in profile["tooling"]:
                import hashlib
                ok = ok and local_mise.is_file() and hashlib.sha256(local_mise.read_bytes()).hexdigest() == profile["tooling"]["mise_sha256"]
            # Grok remains a separate reviewed vendor installer, never an invented package.
        elif name == "workloads":
            if platform == "macos":
                ok = probe([str(root / "scripts/check-project-workloads.sh"), "--agent", agent, "--check"]).returncode == 0
            else:
                ok = all(shutil.which(tool) for tool in ("podman", "docker", "chromium-browser", "Xvfb")) and all(re.search(r"^" + re.escape(agent) + r":", Path(path).read_text(), re.M) for path in ("/etc/subuid", "/etc/subgid"))
        elif name == "agentctl":
            targets = {"agentctl": "/usr/local/bin/agentctl", "agentctl-session": "/usr/local/libexec/agentctl-session", "agentctl-observe": "/usr/local/libexec/agentctl-observe", "agentctl-policy": "/usr/local/libexec/agentctl-policy"}
            ok = all(same_file(root / "agentctl" / source, Path(target)) and not Path(target).is_symlink() and Path(target).stat().st_uid == 0 and stat.S_IMODE(Path(target).stat().st_mode) == (0o644 if source == "agentctl-policy" else 0o755) for source, target in targets.items())
            config = Path("/etc/agentctl/target.conf").read_text()
            ok = ok and ("AGENTCTL_ACCOUNT='" + agent + "'") in config and ("AGENTCTL_TARGET='" + profile["machine"]["hostname"] + "'") in config
            sudoers = Path("/etc/sudoers.d/60-agentctl-" + agent)
            ok = ok and not sudoers.is_symlink() and sudoers.stat().st_uid == 0 and stat.S_IMODE(sudoers.stat().st_mode) == 0o440
            ok = ok and probe(["visudo", "-cf", str(sudoers)]).returncode == 0
        elif name == "remote-hardening":
            ssh = probe(["sshd", "-T"])
            problems = ["cannot read effective sshd policy"] if ssh.returncode else sshd_problems(ssh.stdout, profile["accounts"]["ssh_users"], agent)
            problems += sshd_conditional_problems([*profile["accounts"]["ssh_users"], agent])
            problems += sshd_match_problems()
            for problem in problems:
                print("REASON remote-hardening: " + problem)
            ok = not problems
            ufw = probe(["ufw", "status"])
            ok = ok and ufw.returncode == 0 and "Status: active" in ufw.stdout
            port = str(profile["remote"]["nomachine_port"])
            ok = ok and all(ufw_allows_in(ufw.stdout, rule, "tailscale0") for rule in ("22/tcp", port + "/tcp", port + "/udp"))
            # Use the same restrictive persisted-rule/default parser as the installer.
            check = 'source "$1"; ufw_default_policies_are_safe "$(cat /etc/default/ufw)" || exit 1; rules=$(LC_ALL=C ufw show added) || exit 1; if sed -n "/^ufw /p" <<<"$rules" | first_unsafe_ufw_rule "$2"; then exit 1; fi'
            ok = ok and probe(["bash", "-c", check, "verify", str(root / "scripts/lib/common.sh"), port]).returncode == 0
            tailscale = probe(["tailscale", "status", "--json"])
            ok = ok and tailscale.returncode == 0 and json.loads(tailscale.stdout).get("BackendState") == "Running"
            ok = ok and (probe(["systemctl", "is-active", "--quiet", "ssh.service"]).returncode == 0 or probe(["systemctl", "is-active", "--quiet", "ssh.socket"]).returncode == 0)
        elif name == "resources":
            # Values come from the receipt's approved inputs and readings, never
            # from a fresh reading of RAM, swap or cores.
            values = receipt_resource_values(read_resource_receipt(), profile, command, agent)
            uid = pwd.getpwnam(agent).pw_uid
            oomd = any(item.exists() for item in OOMD_BINARIES)
            problems = []
            for path, expected in ((AGENT_SLICE, agent_slice_text(agent, values, oomd)), (AGENT_PARENT_SLICE, agent_parent_slice_text(agent, values))):
                if path.parent.is_symlink() or path.is_symlink() or not path.is_file() or path.stat().st_uid != 0 or stat.S_IMODE(path.stat().st_mode) != 0o644 or path.read_text() != expected:
                    problems.append(str(path) + " is missing, not root-owned 0644, or differs from the receipt")
            problems += effective_resource_problems(agent, uid, values)
            if not Path("/var/lib/systemd/linger/" + agent).exists():
                problems.append("lingering is not enabled for " + agent)
            outside = agent_processes_outside_slice(root, uid)
            if outside is None:
                problems.append("cannot inspect agent process placement")
            for line in outside or []:
                problems.append("agent process outside agent-work.slice: " + line)
            # The agent's own user configuration outranks /etc/systemd/user.
            config = Path(pwd.getpwnam(agent).pw_dir) / ".config/systemd"
            for unit in ("agent-work.slice", "agent.slice"):
                if any(item.exists() for item in (config / "user" / unit, config / "user" / (unit + ".d"), config / "user.control" / (unit + ".d"))):
                    problems.append("the agent account overrides " + unit)
            if oomd and probe(["systemctl", "is-active", "--quiet", "systemd-oomd.service"]).returncode != 0:
                problems.append("systemd-oomd is not active")
            for problem in problems:
                print("REASON resources: " + problem)
            ok = not problems
        elif name == "audit":
            # The executor always runs the read-only host audit once per audit
            # invocation, then verifies these extra declared-profile conditions.
            ok = identity_matches(profile) and accounts_match(profile)
        else:
            ok = False
    except (OSError, KeyError, TypeError, ValueError, StopIteration):
        ok = False
    print(("PASS " if ok else "FAIL ") + name + " live postconditions")
    return bool(ok)
