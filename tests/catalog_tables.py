#!/usr/bin/env python3
"""Check or regenerate unit catalog outputs via the Go catalog generator.

Delegates to `go run ./internal/units/catalog/gen/cmd` so the Go generator is
the single renderer for generated Go files and Markdown unit tables. When `go`
is not available on PATH (for example in a Python-only CI job), prints a SKIP
notice and exits 0.
"""
from __future__ import annotations

import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SKIP_MESSAGE = "SKIP catalog tables: Go not available here; the Go CI jobs enforce them"


def main(argv: list[str]) -> int:
    go_bin = shutil.which("go")
    if go_bin is None:
        print(SKIP_MESSAGE)
        return 0
    cmd = [go_bin, "run", "./internal/units/catalog/gen/cmd", "-root", str(ROOT)]
    if "--write" not in argv:
        cmd.append("-check")
    result = subprocess.run(cmd, cwd=str(ROOT), check=False)
    return result.returncode


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
