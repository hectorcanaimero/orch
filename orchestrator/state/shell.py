"""Shell-out wrappers around `scripts/task-*.sh` (moved verbatim from state.py).

The orchestrator NEVER writes `tasks.json` directly — every status transition
goes through the three scripts here. Kept as free functions (not a class) so
they can be monkeypatched cheaply in tests.
"""

from __future__ import annotations

import logging
import subprocess
from pathlib import Path

log = logging.getLogger(__name__)


class CwdViolationError(Exception):
    """Raised when the orchestrator is invoked from a directory that is not
    `v2/` root (FR-STATE-1). Callers should map to exit-code 2.
    """


def _ensure_v2_cwd(project_root: Path | None = None) -> None:
    """Assert `project_root` (or cwd) contains the expected orchestrator layout.

    Reused by every `call_task_*` wrapper so a mis-invoked orchestrator can't
    ever hit `scripts/task-*.sh` with the wrong root (FR-STATE-1, AS-08 guard).

    Nombre histórico (`_ensure_v2_cwd`) conservado por compatibilidad con
    el resto del código y los tests. Cuando `project_root` es `None`
    validamos `Path.cwd()` (comportamiento clásico rupies). Cuando llega el
    root explícito (Fase 1 multi-proyecto) validamos ese path en su lugar.
    """
    root = Path(project_root) if project_root is not None else Path.cwd()
    if not (root / "tasks.json").exists() or not (root / "scripts" / "task-start.sh").exists():
        raise CwdViolationError(
            f"orchestrator must be run from v2/ root; project_root={root} is "
            "missing tasks.json or scripts/task-start.sh"
        )


# Alias público con nombre no-rupies para nuevo código. Comparte firma.
ensure_project_root = _ensure_v2_cwd


def _run_script(
    cmd: list[str], project_root: Path | None = None
) -> subprocess.CompletedProcess:
    """Invoke a `scripts/task-*.sh` script from `project_root` (o cwd).

    We use `check=False` so a non-zero exit surfaces via warning log; caller
    decides how to handle it. Capturamos stdout/stderr para inspección.

    Fase 1: cuando `project_root` es None se usa `Path.cwd()` — retro-
    compatible con la invocación clásica desde rupies `v2/`. Cuando llega
    un `project_root` explícito, el guard y el `cwd=` de subprocess apuntan
    a ese path (permite ejecutar `orch --project-root /otro/lado` sin `cd`).
    """
    # Late-bind `_ensure_v2_cwd` through `orchestrator.state` so tests that
    # patch the re-export (`patch("orchestrator.state._ensure_v2_cwd")`) can
    # still bypass the guard the same way they did before the refactor.
    import sys

    pkg = sys.modules.get("orchestrator.state")
    guard = getattr(pkg, "_ensure_v2_cwd", _ensure_v2_cwd) if pkg is not None else _ensure_v2_cwd
    guard(project_root)
    log.debug("shell-out: %s", " ".join(cmd))
    exec_cwd = Path(project_root) if project_root is not None else Path.cwd()
    result = subprocess.run(  # noqa: S603 — args are locally constructed, not user shell
        cmd,
        check=False,  # we log a warning on non-zero, but let caller decide
        capture_output=True,
        text=True,
        cwd=str(exec_cwd),
    )
    if result.returncode != 0:
        log.warning(
            "shell script exit=%d cmd=%s stderr=%s",
            result.returncode,
            " ".join(cmd),
            (result.stderr or "").strip(),
        )
    return result


def call_task_start(
    task_id: str,
    author: str = "orchestrator",
    project_root: Path | None = None,
) -> subprocess.CompletedProcess:
    """Wrap `scripts/task-start.sh <id> <author>` (C-1)."""
    return _run_script(
        ["scripts/task-start.sh", task_id, author], project_root=project_root
    )


def call_task_finish(
    task_id: str,
    comment: str,
    model: str,
    project_root: Path | None = None,
) -> subprocess.CompletedProcess:
    """Wrap `scripts/task-finish.sh <id> "<comment>" <model>` (C-2)."""
    return _run_script(
        ["scripts/task-finish.sh", task_id, comment, model],
        project_root=project_root,
    )


def call_task_block(
    task_id: str,
    reason: str,
    model: str,
    project_root: Path | None = None,
) -> subprocess.CompletedProcess:
    """Wrap `scripts/task-block.sh <id> "<reason>" <model>` (C-3)."""
    return _run_script(
        ["scripts/task-block.sh", task_id, reason, model],
        project_root=project_root,
    )
