#!/usr/bin/env python3
"""Check or regenerate unit catalog outputs via the Go catalog generator.

Delegates to `go run ./internal/units/catalog/gen/cmd` so the Go generator is
the single renderer for generated Go files and Markdown unit tables. Exits
non-zero when `go` is not available on PATH.
"""
from __future__ import annotations

import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MISSING_GO_MESSAGE = "Go is required for the catalog check"


def main(argv: list[str]) -> int:
    go_bin = shutil.which("go")
    if go_bin is None:
        print(MISSING_GO_MESSAGE, file=sys.stderr)
        return 1
    cmd = [go_bin, "run", "./internal/units/catalog/gen/cmd", "-root", str(ROOT)]
    if "--write" not in argv:
        cmd.append("-check")
    result = subprocess.run(cmd, cwd=str(ROOT), check=False)
    return result.returncode


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
