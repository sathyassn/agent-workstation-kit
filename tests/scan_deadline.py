"""Deadline check for the public-scan tests, safe against stalls and host load.

A probe runs in a forked child that is killed at the deadline, so code that
never returns still fails the test. Callers use inputs of about one million
characters: linear scanner code needs well under a second there, and quadratic
code needs many minutes, so the deadline separates them with room for a busy
host. It does not detect a slower constant factor or N log N growth.
"""

from __future__ import annotations

import os
import signal
import time
import traceback
import unittest
from collections.abc import Callable

LARGE_INPUT = 1_000_000
DEADLINE_SECONDS = 15.0


def assert_finishes(
    test_case: unittest.TestCase,
    label: str,
    run: Callable[[], object],
    seconds: float = DEADLINE_SECONDS,
) -> None:
    """Fail unless `run` returns without an exception within `seconds`."""
    pid = os.fork()
    if pid == 0:
        status = 1
        try:
            run()
            status = 0
        except BaseException:  # noqa: BLE001 - reported to the parent by exit status
            traceback.print_exc()
        finally:
            os._exit(status)
    deadline = time.monotonic() + seconds
    while True:
        done, wait_status = os.waitpid(pid, os.WNOHANG)
        if done:
            break
        if time.monotonic() > deadline:
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
            test_case.fail(f"{label} did not finish within {seconds:g} s")
        time.sleep(0.01)
    test_case.assertEqual(
        0, os.waitstatus_to_exitcode(wait_status), f"{label} failed in the child; see its traceback"
    )
