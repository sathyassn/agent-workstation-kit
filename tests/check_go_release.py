#!/usr/bin/env python3
"""Cross-build the release targets and inspect the binaries without running them."""

import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


TARGETS = (("linux", "amd64"), ("linux", "arm64"), ("darwin", "arm64"))
FAULT_SYMBOL = re.compile(rb"/internal/engine\.(?:defaultFault\..+|parseKillAt(?:\..+)?)$")


class FaultInjectionError(Exception):
    """A release binary contains fault-injection code or was built with its tag."""


def check_binary(binary: Path, label: str, env: dict[str, str]) -> None:
    symbols = subprocess.check_output(["go", "tool", "nm", str(binary)], env=env)
    for line in symbols.splitlines():
        if FAULT_SYMBOL.search(line):
            raise FaultInjectionError(
                f"{label} contains fault-injection symbol {line.split()[-1].decode()}"
            )

    # A command can leave the entire engine hook unreachable. Its build
    # settings still tell us if the linker was given a fault-injection build.
    info = subprocess.check_output(["go", "version", "-m", "-json", str(binary)], env=env)
    settings = {item["Key"]: item["Value"] for item in json.loads(info)["Settings"]}
    if "faultinject" in settings.get("-tags", "").split(","):
        raise FaultInjectionError(f"{label} was built with the faultinject tag")
    print(f"PASS {label} has no fault-injection symbols or build tag")


def main() -> None:
    env = dict(os.environ)
    # Keep the user's checksum settings so Go can verify a downloaded toolchain.
    goroot = subprocess.check_output(["go", "env", "GOROOT"], env=env, text=True).strip()
    go = Path(goroot) / "bin" / "go"
    # CI and make go-check populate the module cache before these builds.
    build_env = dict(
        env,
        CGO_ENABLED="0",
        GOROOT=goroot,
        GOTOOLCHAIN="local",
        GOPROXY="off",
        GONOPROXY="none",
    )
    with tempfile.TemporaryDirectory(prefix="wskit-release-") as tmp:
        for goos, goarch in TARGETS:
            output = Path(tmp) / f"{goos}-{goarch}"
            output.mkdir()
            target_env = dict(build_env, GOOS=goos, GOARCH=goarch)
            subprocess.run([go, "build", "./..."], check=True, env=target_env)
            subprocess.run(
                [go, "build", "-o", str(output), "./cmd/..."],
                check=True,
                env=target_env,
            )
            for name in ("wskit", "agentctl"):
                check_binary(output / name, f"{goos}/{goarch} {name}", env)


if __name__ == "__main__":
    try:
        main()
    except FaultInjectionError as error:
        print(f"FAIL {error}", file=sys.stderr)
        sys.exit(4)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        sys.exit(f"FAIL {error}")
