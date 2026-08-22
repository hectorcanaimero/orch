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


def call_task_reset(
    task_id: str,
    project_root: Path | None = None,
    author: str = "orch-reconcile",
) -> bool:
    """Revert a stuck `in-progress` task to `todo` in `tasks.json`.

    Sprint A / Issue #7: called by `reconcile_in_flight` for tasks whose
    recorded PID is dead. Preferred path is the shell script (respects the
    "orchestrator never writes tasks.json directly" contract). Falls back to
    an atomic Python-side rewrite for projects that were initialized before
    task-reset.sh existed — otherwise recovery of legacy projects is broken.

    Returns True when the task WAS in `in-progress` and got reset (or the
    script reported success); False when nothing to do OR the fallback also
    fails. Never raises past its own boundary.

    Contract-preservation note: the "shell scripts own tasks.json" rule was
    written for normal lifecycle transitions (start/finish/block). This is a
    RECOVERY path — the corresponding "undo start" transition has no
    matching script in the historical contract, so the fallback is the
    pragmatic choice for legacy projects.

    Sprint B: lives in `shell.py` (was in the monolithic `state.py`). Uses
    lazy imports of `_atomic_write` / `_utc_now_iso` from `file_backend` to
    avoid a circular import at module load time.
    """
    root = Path(project_root) if project_root is not None else Path.cwd()
    reset_script = root / "scripts" / "task-reset.sh"
    if reset_script.exists():
        try:
            proc = _run_script(
                ["scripts/task-reset.sh", task_id, "--author", author],
                project_root=root,
            )
            return proc.returncode == 0
        except (FileNotFoundError, OSError) as exc:
            log.warning("task-reset.sh invocation failed for %s: %s", task_id, exc)
            # Fall through to the Python fallback.

    return _reset_task_in_place(root / "tasks.json", task_id, author=author)


def _reset_task_in_place(
    tasks_json_path: Path, task_id: str, *, author: str = "orch-reconcile"
) -> bool:
    """Fallback Python revert: `in-progress` → `todo` in tasks.json atomically.

    Used only when `scripts/task-reset.sh` is missing (legacy projects that
    predate Sprint A). Reuses the module's `_atomic_write` primitive so
    readers never see a half-written file. Idempotent: reset of a non-
    in-progress task is a no-op returning False.
    """
    import json

    # Lazy import to avoid circular dep (file_backend imports from shell).
    from .file_backend import _atomic_write, _utc_now_iso

    try:
        raw = json.loads(tasks_json_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        log.warning("could not read %s for in-place reset: %s", tasks_json_path, exc)
        return False

    rows = raw.get("tasks") if isinstance(raw, dict) else raw
    if not isinstance(rows, list):
        return False

    changed = False
    for row in rows:
        if not isinstance(row, dict):
            continue
        if row.get("id") != task_id:
            continue
        if row.get("status") != "in-progress":
            return False  # already at rest, nothing to do
        row["status"] = "todo"
        comments = row.get("comments") or []
        if isinstance(comments, list):
            comments.append(
                {
                    "author": author,
                    "body": "reset from in-progress (orphaned)",
                    "at": _utc_now_iso(),
                }
            )
            row["comments"] = comments
        changed = True
        break

    if not changed:
        return False

    try:
        payload = json.dumps(raw, indent=2).encode("utf-8")
        _atomic_write(tasks_json_path, payload)
        return True
    except OSError as exc:
        log.warning("atomic write of %s failed during reset: %s", tasks_json_path, exc)
        return False
