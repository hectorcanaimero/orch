"""Shared PID-liveness helper for `arch` and `dashboard.tunnel`.

Extracted from `orchestrator/arch.py` so both the one-shot arch dispatch and
the long-lived tunnel manager can share a single implementation. `os.kill(pid, 0)`
is the classic POSIX aliveness probe — signal 0 is a no-op that only exercises
the permission/existence check.
"""

from __future__ import annotations

import os


def pid_alive(pid: int) -> bool:
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        # Signal denied means the process exists but isn't ours — treat as alive.
        return True
    except OSError:
        return False
    return True
