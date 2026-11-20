#!/usr/bin/env python3
"""Preview or run ordered TCP, named-user SSH reconnect and SFTP transfer checks."""
from __future__ import annotations
import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import socket
import subprocess
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parent / "lib"))
from path_safety import outside_kit


def valid_host(value):
    try:
        ipaddress.ip_address(value)
        return True
    except ValueError:
        return bool(re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?", value)) and ".." not in value


def ssh_command(host, user, port, arguments):
    return ["ssh", "-p", str(port), "-l", user,
            "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
            "-o", "ControlMaster=no", "-o", "ControlPath=none",
            "-o", "ConnectTimeout=5", "-o", "RequestTTY=no",
            "--", host, shlex.join(arguments)]


def invoke(command):
    return subprocess.run(command, check=False, text=True, capture_output=True, timeout=30)


def check_remote(host, ports, user=None, ssh_port=22, transfer=False):
    results = []
    # Never attempt authentication/file transfer until actual viewer TCP succeeds.
    for port in sorted(set([*ports, *([ssh_port] if user else [])])):
        try:
            with socket.create_connection((host, port), timeout=5):
                pass
            results.append({"check": "tcp", "port": port, "status": "pass"})
        except OSError:
            results.append({"check": "tcp", "port": port, "status": "fail"})
    if any(item["status"] == "fail" for item in results) or not user:
        return results
    connected = invoke(ssh_command(host, user, ssh_port, ["id", "-un"]))
    authenticated = connected.returncode == 0 and connected.stdout.strip() == user
    results.append({"check": "named-user-ssh", "status": "pass" if authenticated else "fail"})
    if not authenticated:
        return results
    if transfer:
        nonce = secrets.token_hex(16)
        remote = "/tmp/awk-transfer-" + nonce
        create = "import os; os.mkdir(" + repr(remote) + ", 0o700)"
        created = invoke(ssh_command(host, user, ssh_port, ["python3", "-c", create]))
        if created.returncode:
            results.append({"check": "sftp-roundtrip", "status": "fail"})
            return results
        try:
            with tempfile.TemporaryDirectory(prefix="awk-transfer-") as directory:
                folder = Path(directory)
                upload, download = folder / "upload.bin", folder / "download.bin"
                payload = secrets.token_bytes(256)
                upload.write_bytes(payload)
                batch = folder / "transfer.batch"
                batch.write_text("put " + json.dumps(str(upload)) + " " + json.dumps(remote + "/payload") + "\nget " + json.dumps(remote + "/payload") + " " + json.dumps(str(download)) + "\nbye\n")
                endpoint = user + "@" + ("[" + host + "]" if ":" in host else host)
                command = ["sftp", "-P", str(ssh_port), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ConnectTimeout=5", "-b", str(batch), endpoint]
                copied = invoke(command)
                expected = hashlib.sha256(payload).hexdigest()
                ok = copied.returncode == 0 and download.is_file() and hashlib.sha256(download.read_bytes()).hexdigest() == expected
                results.append({"check": "sftp-roundtrip", "status": "pass" if ok else "fail"})
        finally:
            # Remove only the probe's exact file and newly created directory.
            cleanup = "from pathlib import Path; p=Path(" + repr(remote) + "); (p/'payload').unlink(missing_ok=True); p.rmdir()"
            cleaned = invoke(ssh_command(host, user, ssh_port, ["python3", "-c", cleanup]))
            results.append({"check": "probe-cleanup", "status": "pass" if cleaned.returncode == 0 else "fail"})
    reconnect = invoke(ssh_command(host, user, ssh_port, ["id", "-un"]))
    ok = reconnect.returncode == 0 and reconnect.stdout.strip() == user
    results.append({"check": "independent-ssh-reconnect", "status": "pass" if ok else "fail"})
    return results


def listener(port):
    if sys.platform == "darwin":
        result = invoke(["lsof", "-nP", "-iTCP:" + str(port), "-sTCP:LISTEN", "-F", "pcun"])
    else:
        result = invoke(["ss", "-ltnp", "sport = :" + str(port)])
    # Return raw host-local listener inventory only on explicit request. It contains
    # process names/owners/addresses, not full command lines or pairing links.
    print(result.stdout)
    if result.returncode:
        print("UNKNOWN listener inspection failed or no visible listener; check permissions directly")
        return 1
    if sys.platform != "darwin" and len(result.stdout.strip().splitlines()) <= 1:
        print("FAIL no matching listener visible")
        return 1
    return 0


def main():
    parser = argparse.ArgumentParser(prog="setup-workstation.sh check-access", description=__doc__)
    parser.add_argument("--host")
    parser.add_argument("--tcp-port", type=int, action="append", default=[])
    parser.add_argument("--ssh-user")
    parser.add_argument("--ssh-port", type=int, default=22)
    parser.add_argument("--sftp-roundtrip", action="store_true")
    parser.add_argument("--listener-port", type=int, help="inspect on the host, separately from client checks")
    parser.add_argument("--output", type=Path, help="new private evidence file outside the kit")
    parser.add_argument("--apply", action="store_true", help="perform the explicitly selected checks using existing named-user credentials")
    args = parser.parse_args()
    if args.host and not valid_host(args.host):
        parser.error("use a host name or address, never a URL, command or pairing link")
    if args.ssh_user and not re.fullmatch(r"[a-z_][a-z0-9_-]{0,30}", args.ssh_user):
        parser.error("invalid named-user account")
    if args.sftp_roundtrip and not args.ssh_user:
        parser.error("SFTP roundtrip requires a named SSH user")
    if args.listener_port and (args.host or args.ssh_user or args.tcp_port or args.output):
        parser.error("run host listener inspection separately from viewer checks")
    if not args.listener_port and (not args.host or (not args.tcp_port and not args.ssh_user)):
        parser.error("select host and ports/user, or a local listener port")
    if any(not 1 <= port <= 65535 for port in [*args.tcp_port, args.ssh_port, *([args.listener_port] if args.listener_port is not None else [])]):
        parser.error("ports must be between 1 and 65535")
    if args.output and (args.output.exists() or args.output.is_symlink() or not outside_kit(args.output, Path(__file__).resolve().parents[1])):
        parser.error("evidence must be a new private file outside the kit")
    if not args.apply:
        print("PREVIEW ONLY: host listener inventory, or selected viewer TCP before named-user SSH/SFTP and independent reconnect")
        print("Host:", args.host, "ports:", sorted(set([*args.tcp_port, *([args.ssh_port] if args.ssh_user else [])])), "SSH user:", args.ssh_user, "SFTP:", args.sftp_roundtrip, "local listener:", args.listener_port)
        print("No connection, authentication, file transfer or host setting was changed.")
        return 0
    try:
        if args.listener_port:
            return listener(args.listener_port)
        results = check_remote(args.host, args.tcp_port, args.ssh_user, args.ssh_port, args.sftp_roundtrip)
        evidence = {"host": args.host, "ssh_user": args.ssh_user, "results": results}
        print(json.dumps(evidence, indent=2))
        if args.output:
            descriptor = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            with os.fdopen(descriptor, "w") as stream:
                json.dump(evidence, stream, indent=2)
                stream.write("\n")
        return 0 if all(item["status"] == "pass" for item in results) else 1
    except (OSError, subprocess.SubprocessError) as error:
        print("ERROR remote check: " + str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
