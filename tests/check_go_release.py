#!/usr/bin/env python3
"""Cross-build the release targets and inspect the binaries without running them."""

import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
TARGETS = (("linux", "amd64"), ("linux", "arm64"), ("darwin", "arm64"))
REQUIRED_BINARIES = {"wskit", "agentctl"}
FAULT_SYMBOL = re.compile(rb"/internal/engine\.(?:defaultFault\..+|parseKillAt(?:\..+)?)$")
FAULT_ENV_DECLARATION = re.compile(
    rb'^const EnvFaultKillAt = "([A-Za-z_][A-Za-z0-9_]*)"$', re.MULTILINE
)


class FaultInjectionError(Exception):
    """A release binary contains fault-injection evidence."""


class FaultSymbolError(FaultInjectionError):
    """The symbol table contains a fault-injection symbol."""


def read_fault_env_name() -> bytes:
    source = ROOT / "internal/engine/fault_inject.go"
    match = FAULT_ENV_DECLARATION.search(source.read_bytes())
    if match is None:
        raise ValueError(f"cannot read EnvFaultKillAt from {source.relative_to(ROOT)}")
    return match[1]


def check_binary(binary: Path, label: str, env: dict[str, str], fault_env_name: bytes) -> None:
    symbols = subprocess.check_output(["go", "tool", "nm", str(binary)], cwd=ROOT, env=env)
    lines = symbols.splitlines()
    if not any(line.split()[-2:] == [b"T", b"main.main"] for line in lines):
        raise ValueError(f"{label} has no main.main symbol")
    for line in lines:
        if FAULT_SYMBOL.search(line):
            raise FaultSymbolError(
                f"{label} contains fault-injection symbol {line.split()[-1].decode()}"
            )
    if fault_env_name in binary.read_bytes():
        raise FaultInjectionError(
            f"{label} contains fault-injection environment name {fault_env_name.decode()}"
        )

    # A command can leave the entire engine hook unreachable. Its build
    # settings still tell us if the linker was given a fault-injection build.
    info = subprocess.check_output(["go", "version", "-m", "-json", str(binary)], cwd=ROOT, env=env)
    settings = {item["Key"]: item["Value"] for item in json.loads(info)["Settings"]}
    if "faultinject" in settings.get("-tags", "").split(","):
        raise FaultInjectionError(f"{label} was built with the faultinject tag")
    print(f"PASS {label} has no fault-injection symbol, environment name or build tag")


def check_symbol_detection(
    go: Path, binary: Path, build_env: dict[str, str], env: dict[str, str], fault_env_name: bytes
) -> None:
    host = json.loads(subprocess.check_output(
        [go, "env", "-json", "GOHOSTOS", "GOHOSTARCH"], cwd=ROOT, env=build_env
    ))
    host_env = dict(build_env, GOOS=host["GOHOSTOS"], GOARCH=host["GOHOSTARCH"])
    subprocess.run(
        [go, "test", "-c", "-tags", "faultinject", "-ldflags=", "-o", binary, "./internal/engine"],
        check=True,
        cwd=ROOT,
        env=host_env,
    )
    try:
        check_binary(binary, "tagged engine test binary", env, fault_env_name)
    except FaultSymbolError as error:
        print(f"PASS positive control: {error}")
        return
    except FaultInjectionError as error:
        raise ValueError(f"positive control did not detect a fault-injection symbol: {error}") from error
    raise ValueError("positive control did not detect a fault-injection symbol")


def main() -> None:
    fault_env_name = read_fault_env_name()
    env = dict(os.environ)
    # Keep the user's checksum settings so Go can verify a downloaded toolchain.
    goroot = subprocess.check_output(["go", "env", "GOROOT"], cwd=ROOT, env=env, text=True).strip()
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
        check_symbol_detection(go, Path(tmp) / "engine.test", build_env, env, fault_env_name)
        for goos, goarch in TARGETS:
            output = Path(tmp) / f"{goos}-{goarch}"
            output.mkdir()
            target_env = dict(build_env, GOOS=goos, GOARCH=goarch)
            subprocess.run([go, "build", "./..."], check=True, cwd=ROOT, env=target_env)
            subprocess.run(
                [go, "build", "-o", str(output), "./cmd/..."],
                check=True,
                cwd=ROOT,
                env=target_env,
            )
            binaries = sorted(path for path in output.iterdir() if path.is_file())
            if not binaries:
                raise ValueError(f"{goos}/{goarch} release output is empty")
            missing = REQUIRED_BINARIES.difference(binary.name for binary in binaries)
            if missing:
                raise ValueError(f"{goos}/{goarch} release output is missing {', '.join(sorted(missing))}")
            for binary in binaries:
                check_binary(binary, f"{goos}/{goarch} {binary.name}", env, fault_env_name)


if __name__ == "__main__":
    try:
        main()
    except FaultInjectionError as error:
        print(f"FAIL {error}", file=sys.stderr)
        sys.exit(4)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        sys.exit(f"FAIL {error}")
