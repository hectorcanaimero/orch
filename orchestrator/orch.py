"""Rupies v2 orchestrator CLI entry point (R-017 + R-018).

The main loop is single-threaded Python; parallelism is OS subprocesses.

Startup order (fail-fast):
    1. Assert CWD == v2/ root (else exit 2).
    2. Load config.yaml + model_router.yaml + tasks.json.
    3. Filter tasks by --only glob.
    4. Validate every task.model is routable (else exit 1).
    5. Build TaskQueue (validates deps + cycles) (else exit 1).
    6. acquire_flock on state/.lock (else exit 3).
    7. If --resume, reconcile the prior run file.
    8. If --dry-run, print the plan table and exit 0 — NO subprocess.
    9. Otherwise, enter the main loop.

Main loop (per tick):
    - Reap terminated children via os.waitpid(-1, WNOHANG).
    - Sweep for per-task timeouts (SIGTERM → 10 s grace → SIGKILL).
    - Refill dispatches while under the global + per-provider semaphore caps.
    - Sleep 200 ms and repeat.

Exit codes (FR-CLI-3):
    0   clean drain (all tasks done, none blocked at end of run).
    1   config error, unrouted model, cycle detected, missing dep, blocked tasks.
    2   CWD contract violation (`v2/` root not detected).
    3   flock contention (another orchestrator holds `state/.lock`).
    130 SIGINT during graceful drain.

Both invocation styles are supported so the walking-skeleton milestone can
run without `pip install -e .`:
    - `python -m orchestrator` (via `__main__.py` if present)
    - `python orchestrator/orch.py` (direct — we self-heal sys.path)
"""

from __future__ import annotations

import argparse
import fnmatch
import json
import logging
import os
import re
import signal
import subprocess
import sys
import time
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


# ---- Import bootstrap ----------------------------------------------------
# When invoked as `python orchestrator/orch.py`, __package__ is empty and
# relative imports break. Self-heal by adding the v2/ root to sys.path and
# switching to absolute imports. When invoked as `python -m orchestrator`
# through __main__.py, the package is set correctly and this branch is a no-op.
if __package__ in (None, ""):  # pragma: no cover — invocation bootstrap
    _here = Path(__file__).resolve().parent.parent
    if str(_here) not in sys.path:
        sys.path.insert(0, str(_here))

from dataclasses import replace as _dc_replace  # noqa: E402

from orchestrator import state as state_mod  # noqa: E402
from orchestrator.budget import BudgetGate, load_budget_config  # noqa: E402
from orchestrator.checkpoints import SemiModeGate, is_critical  # noqa: E402
from orchestrator.dispatcher import (  # noqa: E402
    Backend,
    DispatchResult,
    FailureClass,
    classify_failure,
    get_backend,
    is_version_drift_error,
)
from orchestrator.models import Dispatch, RouteEntry, SpendEntry, Task  # noqa: E402
from orchestrator.paths import ProjectPaths, resolve_project_paths  # noqa: E402
from orchestrator.prompt_builder import render_prompt  # noqa: E402
from orchestrator.router import (  # noqa: E402
    UnroutedModelError,
    load_router,
    validate_all_models,
)
from orchestrator.state import (  # noqa: E402
    CwdViolationError,
    EventLog,
    FlockContentionError,
    RunFile,
    SpendLog,
    _ensure_v2_cwd,
    _utc_now_iso,
    acquire_flock,
    call_task_block,
    call_task_finish,
    call_task_start,
    load_tasks,
    reconcile_in_flight,
    reconcile_run,
    release_task_lock,
    try_acquire_task_lock,
    write_lock_holder,
)
from orchestrator.task_queue import (  # noqa: E402
    MissingDependencyError,
    TaskCycleError,
    TaskQueue,
)


log = logging.getLogger("orchestrator.orch")


# ---- Rich (optional but declared in pyproject) --------------------------


try:
    from rich.console import Console
    from rich.table import Table

    _console = Console()
    _HAVE_RICH = True
except ImportError:  # pragma: no cover — rich is a hard dep, but be defensive
    _console = None  # type: ignore[assignment]
    Table = None  # type: ignore[assignment]
    _HAVE_RICH = False


# ---- CLI ----------------------------------------------------------------


_HELP_EPILOG = """
Exit codes:
  0   clean drain (all reachable tasks done, none blocked)
  1   config error / unrouted model / dependency cycle / blocked tasks
  2   CWD contract violation (must run from v2/ root)
  3   flock contention (another orchestrator holds state/.lock)
  130 SIGINT during graceful drain

CWD contract:
  MUST be invoked from the v2/ repo root — `tasks.json` and
  `scripts/task-start.sh` must be present in the current directory.
"""


def _build_argparser() -> argparse.ArgumentParser:
    """Argparse with all 6 flags per FR-CLI-2 (approved names)."""
    parser = argparse.ArgumentParser(
        prog="orchestrator",
        description="Walk tasks.json DAG and dispatch to claude/codex/opencode CLIs.",
        epilog=_HELP_EPILOG,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--mode",
        choices=("auto", "semi"),
        default="auto",
        help="auto = no prompts; semi = block on critical tasks (default: auto)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Enumerate the plan and exit 0. NO subprocess is spawned.",
    )
    parser.add_argument(
        "--config",
        default="orchestrator/config.yaml",
        help="Path to config.yaml (default: orchestrator/config.yaml)",
    )
    parser.add_argument(
        "--only",
        default=None,
        help="Glob filter on task ids (fnmatch, e.g. 'B-*', 'P0-0[0-3]?')",
    )
    parser.add_argument(
        "--resume",
        default=None,
        metavar="RUN_ID",
        help="Resume a prior run by UUID (reconciles in-flight PIDs).",
    )
    parser.add_argument(
        "--max-tasks",
        type=int,
        default=None,
        metavar="N",
        help="Cap total dispatches this run (default: no cap).",
    )
    parser.add_argument(
        "--task-locks",
        action="store_true",
        help=(
            "Use per-task locks instead of the global .lock. Allows multiple "
            "concurrent orch instances on disjoint tasks; a task already "
            "held by another instance is skipped."
        ),
    )
    # Fase 1 multi-proyecto: --project-root permite invocar orch desde
    # cualquier cwd apuntando a la raíz de OTRO proyecto que tenga el
    # layout esperado (tasks.json + scripts/task-*.sh + orchestrator/).
    # Sin la flag ni ORCH_PROJECT_ROOT, se cae al comportamiento clásico
    # (Path.cwd()) — retrocompatible con rupies.
    parser.add_argument(
        "--project-root",
        default=None,
        metavar="PATH",
        help=(
            "Root path of the target project (tasks.json / scripts / "
            "orchestrator/ live here). Env fallback: ORCH_PROJECT_ROOT. "
            "Default: current working directory."
        ),
    )
    parser.add_argument(
        "--project-id",
        default=None,
        metavar="ID",
        help=(
            "Human/telemetry identifier for the project. "
            "Env fallback: ORCH_PROJECT_ID. "
            "Default: derived from --project-root basename (skipping "
            "generic names like `v2`, `app`, `src`)."
        ),
    )
    parser.add_argument(
        "--budgets-preset",
        default=None,
        metavar="NAME",
        help=(
            "Provider budget preset (Sprint 7) — one of the keys under "
            "`presets:` in budgets.yaml (e.g. conservative/aggressive/shared). "
            "Env fallback: ORCH_BUDGETS_PRESET. Config fallback: "
            "`budgets_preset` in config.yaml. Absent budgets.yaml → gate off."
        ),
    )
    return parser


# ---- Config load --------------------------------------------------------


def _load_config(path: str | Path) -> dict[str, Any]:
    """Load config.yaml with sane defaults for missing keys."""
    import yaml

    from orchestrator.prompt_builder import DEFAULT_SPEC_ROOT

    p = Path(path)
    if not p.exists():
        raise FileNotFoundError(f"config file not found: {p}")
    with open(p, encoding="utf-8") as fh:
        cfg = yaml.safe_load(fh) or {}

    # Fill in defaults so downstream code doesn't need .get() everywhere.
    cfg.setdefault("concurrency", {})
    cfg["concurrency"].setdefault("global_max", 6)
    cfg["concurrency"].setdefault(
        "per_provider", {"claude": 3, "codex": 2, "opencode": 3}
    )
    cfg.setdefault("strict_files_phases", [])
    cfg.setdefault("default_timeout_multiplier", 1.5)
    cfg.setdefault("budget", {"per_dispatch_usd": 5.0})
    # FR-D-4 (design.md §5): 5s wall-clock backoff between a failed dispatch
    # and its retry. Config knob so tests can override to 0 for determinism.
    cfg.setdefault("retry", {})
    cfg["retry"].setdefault("backoff_seconds", 5.0)
    # Fase 3: `spec_root` es el prefijo aplicado a `Task.spec_ref` para
    # armar la línea `Spec ref (READ FIRST):` del prompt. Cada proyecto
    # puede pisar el default rupies (`docs/rewrite-plan`) desde su config.yaml.
    cfg.setdefault("spec_root", DEFAULT_SPEC_ROOT)
    # Sprint 7 — budget guardrails. Both keys optional; when the resolved
    # `budgets.yaml` path doesn't exist, `BudgetGate` runs disabled and
    # everything behaves like pre-Sprint 7 (backwards-compat).
    cfg.setdefault("budgets_config", "budgets.yaml")
    cfg.setdefault("budgets_preset", "conservative")
    return cfg


def _load_budget_gate(
    cfg: dict[str, Any],
    state_dir: Path,
    cli_preset: str | None,
    config_path: Path,
) -> BudgetGate:
    """Instantiate the budget gate from config + CLI + env (Sprint 7).

    Resolution order for the preset (highest priority first):
        1. `--budgets-preset` CLI flag.
        2. `ORCH_BUDGETS_PRESET` env var.
        3. `budgets_preset` in config.yaml.

    Resolution order for the `budgets.yaml` path:
        1. If `budgets_config` is absolute → use as-is.
        2. Try relative to the config.yaml's directory (packaged default).
        3. Try relative to CWD (per-project override).
        Missing at both → disabled gate.
    """
    preset = (
        cli_preset
        or os.environ.get("ORCH_BUDGETS_PRESET")
        or cfg.get("budgets_preset")
        or "conservative"
    )
    budgets_rel = cfg.get("budgets_config") or "budgets.yaml"
    budgets_path = Path(budgets_rel)
    if not budgets_path.is_absolute():
        # Try config-adjacent first (packaged default), then CWD (override).
        candidate = config_path.parent / budgets_rel
        if candidate.exists():
            budgets_path = candidate
        else:
            budgets_path = Path.cwd() / budgets_rel

    try:
        budget_cfg = load_budget_config(budgets_path, preset=preset)
    except ValueError as exc:
        # Bad preset name → fail-fast at startup, not silently degrade.
        raise SystemExit(f"budgets config error: {exc}") from exc

    if budget_cfg is None:
        log.info("budget gate disabled (no budgets.yaml at %s)", budgets_path)
    else:
        log.info(
            "budget gate enabled: preset=%s providers=%s",
            preset,
            list(budget_cfg.providers.keys()),
        )
    return BudgetGate(state_dir=state_dir, config=budget_cfg)


# ---- Fallback route WARN (FR-D-7) --------------------------------------


def _warn_fallback_routes(router: dict[str, RouteEntry]) -> None:
    """Emit exactly one WARN line per router entry with a `fallback_cli_model`.

    FR-D-7 says the orchestrator "MUST emit exactly one WARN line per
    substitution at startup." We chose Option A (no CLI probe): the WARN is
    informational — it announces which routes are drift-resilient. The
    actual fallback swap happens post-run in `_reap_once` when a dispatch
    fails with a version-drift-shaped error message.
    """
    for key, route in sorted(router.items()):
        if route.fallback_cli_model:
            msg = (
                f"WARN: route {key!r} has fallback_cli_model={route.fallback_cli_model!r} "
                f"— will substitute for {route.cli_model!r} on version-drift errors"
            )
            log.warning(msg)
            # Also print so operators watching stdout see the notice even if
            # logging is silenced.
            print(msg, file=sys.stderr)


# ---- Task filtering -----------------------------------------------------


def _filter_by_only(tasks: list[Task], only: str | None) -> list[Task]:
    """Filter tasks by fnmatch glob on `id`. `None` → pass-through."""
    if not only:
        return list(tasks)
    return [t for t in tasks if fnmatch.fnmatchcase(t.id, only)]


# ---- Dry-run plan -------------------------------------------------------


def _print_plan(
    queue: TaskQueue,
    router: dict[str, RouteEntry],
    max_tasks: int | None,
    only: str | None = None,
) -> int:
    """Print a `rich.Table` of the planned dispatches, return count printed.

    Only enumerates the CURRENT ready-set, not the transitive one — the point
    of --dry-run is to prove routes resolve and the first tick has work.
    """
    ready = queue.ready(in_flight_ids=[], only=only)
    if max_tasks is not None:
        ready = ready[:max_tasks]

    if _HAVE_RICH:
        table = Table(title=f"Dry-run plan ({len(ready)} ready)")
        table.add_column("Task ID", style="cyan")
        table.add_column("Phase", justify="right")
        table.add_column("Backend", style="green")
        table.add_column("CLI Model")
        table.add_column("Tier")
        table.add_column("Est h", justify="right")
        for t in ready:
            route = router.get(t.model)
            if route is None:
                table.add_row(t.id, str(t.phase), "?", t.model, "?", str(t.estimate_hours))
            else:
                table.add_row(
                    t.id,
                    str(t.phase),
                    route.backend,
                    route.cli_model,
                    route.tier,
                    str(t.estimate_hours),
                )
        _console.print(table)  # type: ignore[union-attr]
    else:  # pragma: no cover
        print(f"Dry-run plan ({len(ready)} ready)")
        for t in ready:
            route = router.get(t.model)
            b = route.backend if route else "?"
            m = route.cli_model if route else "?"
            print(f"  {t.id}  [{t.phase}]  {b}/{m}  est={t.estimate_hours}h")
    return len(ready)


# ---- In-flight bookkeeping ---------------------------------------------


@dataclass
class InFlight:
    """Runtime metadata for a live child process.

    Keyed by PID in `_in_flight` — the reap loop looks up the entry when
    `os.waitpid(-1, WNOHANG)` returns a PID. `started_at_mono` is used by
    the timeout sweep (monotonic clock, not wall clock).
    """

    task: Task
    route: RouteEntry
    backend: Backend
    dispatch: Dispatch
    started_at_mono: float
    timeout_s: float
    timed_out: bool = False
    task_lock_fd: Any = None  # per-task flock fd when --task-locks; None otherwise


@dataclass
class _RetryItem:
    """One pending re-dispatch scheduled by the reap loop (FR-D-4 / FR-D-7).

    The refill loop drains `retry_queue` BEFORE consulting `queue.ready()`
    so retries never wait behind newly-ready tasks. `route` may differ from
    the original `router.get(task.model)` entry if a version-drift fallback
    swapped `cli_model` for `fallback_cli_model`.

    `retry_earliest_at` is a `time.monotonic()` timestamp. The refill loop
    skips items whose earliest-at is still in the future (design.md §5:
    "one retry ... 5s backoff"). Monotonic clock is immune to wall-clock
    jumps (NTP, DST, manual `date` calls).
    """

    task: Task
    route: RouteEntry
    attempt: int  # next dispatch's Dispatch.attempt (2 on first retry)
    retry_earliest_at: float = 0.0  # monotonic ts; 0.0 = drain immediately


# ---- Concurrency helpers ------------------------------------------------


def _build_semaphores(cfg: dict[str, Any]) -> tuple["_Sem", dict[str, "_Sem"]]:
    """Build the global + per-provider counting semaphores.

    Uses `_Sem` (a thin `threading.Semaphore` wrapper that also tracks the
    current in-flight count for AS-05 observability). Both semaphores use
    non-blocking `try_acquire` in the main loop — a busy tick just waits
    for the next reap to release a slot.
    """
    gmax = int(cfg["concurrency"]["global_max"])
    per_provider = cfg["concurrency"]["per_provider"]
    gsem = _Sem(gmax)
    psem: dict[str, _Sem] = {
        backend: _Sem(int(cap)) for backend, cap in per_provider.items()
    }
    return gsem, psem


class _Sem:
    """Counting semaphore with an observable current count.

    `threading.Semaphore` doesn't expose `_value` portably, so we track it
    ourselves. The lock protects reads/writes so AS-05 assertions can peek
    the current in-flight count between ticks.
    """

    def __init__(self, cap: int):
        import threading

        self.cap = cap
        self._count = 0
        self._lock = threading.Lock()

    def try_acquire(self) -> bool:
        with self._lock:
            if self._count >= self.cap:
                return False
            self._count += 1
            return True

    def release(self) -> None:
        with self._lock:
            self._count = max(0, self._count - 1)

    def current(self) -> int:
        with self._lock:
            return self._count


# ---- Main loop helpers --------------------------------------------------


def _monotonic() -> float:
    return time.monotonic()


def _timeout_for(task: Task, cfg: dict[str, Any]) -> float:
    """Compute the per-dispatch timeout (FR-D-3).

    `estimate_hours * multiplier * 3600` seconds. For tasks with
    `estimate_hours == 0` (dry seed / typo), we still give them 60 s so the
    reap loop can catch a hung child that never emits.
    """
    mult = float(cfg.get("default_timeout_multiplier", 1.5) or 1.5)
    est_h = float(task.estimate_hours or 0)
    seconds = est_h * mult * 3600.0
    if seconds <= 0:
        seconds = 60.0
    return seconds


def _git_diff_paths(cwd: Path) -> list[str]:
    """Return `git diff --name-only HEAD` filenames from `cwd`.

    Never raises: on any git error we return an empty list — the strict-files
    revert is a defensive check, not a correctness one.
    """
    try:
        result = subprocess.run(  # noqa: S603 — trusted local git
            ["git", "diff", "--name-only", "HEAD"],
            check=False,
            capture_output=True,
            text=True,
            cwd=str(cwd),
        )
    except (FileNotFoundError, OSError) as exc:
        log.warning("git diff probe failed: %s", exc)
        return []
    if result.returncode != 0:
        return []
    return [line.strip() for line in result.stdout.splitlines() if line.strip()]


def _git_checkout(cwd: Path, path: str) -> None:
    """`git checkout -- <path>` — reverts unauthorized edits (AS-11)."""
    try:
        subprocess.run(  # noqa: S603 — trusted local git
            ["git", "checkout", "--", path],
            check=False,
            capture_output=True,
            text=True,
            cwd=str(cwd),
        )
    except (FileNotFoundError, OSError) as exc:
        log.warning("git checkout revert failed for %s: %s", path, exc)


def _executed_commands(log_text: str) -> list[str]:
    """Extract only the shell command strings the agent actually EXECUTED.

    All three backends (claude/codex/opencode) log bash tool calls as JSON
    with an `"input":{"command":"..."}` field. We pull those `"command"`
    values out and JSON-unescape them. Content the agent merely *read*
    (file bodies, its own reasoning) lands in tool-output fields, never in
    a `"command"` — so it is excluded, which is the whole point.
    """
    cmds: list[str] = []
    for m in re.finditer(r'"command"\s*:\s*"((?:[^"\\]|\\.)*)"', log_text):
        try:
            cmds.append(json.loads('"' + m.group(1) + '"'))
        except json.JSONDecodeError:
            cmds.append(m.group(1))
    return cmds


# Prefijos que envuelven a un comando real sin serlo (los saltamos para
# encontrar el token que de verdad se ejecuta).
_CMD_WRAPPERS = frozenset({"sudo", "env", "bash", "sh", "command"})


def _detect_id_spoofing(task: Task, log_text: str) -> str | None:
    """Devuelve el id equivocado si se ejecutó `task-finish.sh <id-erróneo>`.

    AS-10: un agente puede invocar el script de cierre con otro task id (por
    error o a propósito). Dos capas para evitar falsos positivos:
      1. Solo miramos los comandos que el agente EJECUTÓ (`_executed_commands`),
         no el log crudo — los propios archivos spec/design/test del orquestador
         contienen `task-finish.sh B` como documentación (falso positivo visto
         en R-001).
      2. Solo cuenta cuando `task-finish.sh` es el COMANDO (primer token del
         comando simple), no un argumento — p. ej. `cat a/task-finish.sh
         b/task-start.sh` NO es una invocación (falso positivo visto en P0-002).
    """
    for cmd in _executed_commands(log_text):
        # Separamos en comandos simples por operadores de shell.
        for seg in re.split(r"&&|\|\||[;\n|]", cmd):
            toks = seg.split()
            # Saltamos wrappers tipo `bash`/`sudo` para llegar al comando real.
            while toks and toks[0] in _CMD_WRAPPERS:
                toks = toks[1:]
            if toks and toks[0].endswith("task-finish.sh") and len(toks) > 1:
                claimed = toks[1].strip("\"'")
                if claimed and claimed != task.id:
                    return claimed
    return None


def _post_run_checks(
    task: Task,
    result: DispatchResult,
    cfg: dict[str, Any],
    cwd: Path,
    log_text: str,
) -> tuple[DispatchResult, str | None]:
    """Apply id-spoofing + strict-files checks. Returns (result, spoof_id_or_None).

    Mutates `result.success` / `result.error_message` in place when a check
    downgrades the outcome to failure (AS-10, AS-11).
    """
    # Id-spoofing: run FIRST so a spoofed success is never trusted.
    spoof_id = _detect_id_spoofing(task, log_text)
    if spoof_id:
        result.success = False
        result.error_message = f"id spoofing detected: agent called task-finish.sh {spoof_id!r}"
        return result, spoof_id

    # Strict-files: only enforced for phases opted into by config.
    strict_phases = set(cfg.get("strict_files_phases", []) or [])
    if task.phase in strict_phases and task.files:
        changed = _git_diff_paths(cwd)
        allowed = set(task.files)
        # Any changed file not in the declared `files[]` is unauthorized.
        unauthorized = [f for f in changed if f not in allowed]
        if unauthorized:
            for path in unauthorized:
                _git_checkout(cwd, path)
            result.success = False
            result.error_message = (
                f"unauthorized edit outside files[]: reverted {unauthorized}"
            )
    return result, None


# ---- Signal handling ----------------------------------------------------


class _DrainFlag:
    """Mutable flag toggled by SIGINT / semi-mode 'q' / --max-tasks reached.

    Wraps a plain bool so it can be shared across the main loop, signal
    handler, and semi-mode gate without leaking a closure.
    """

    def __init__(self) -> None:
        self.set: bool = False
        self.hard_kill_next: bool = False


def _install_sigint(drain: _DrainFlag, in_flight: dict[int, InFlight]) -> None:
    """SIGINT → drain; second SIGINT → SIGKILL every child (proposal Rollback)."""

    def handler(signum, frame):  # noqa: ARG001
        if drain.set:
            # Second SIGINT → kill everything immediately.
            for pid, entry in list(in_flight.items()):
                try:
                    os.kill(pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass
                entry.timed_out = True
            drain.hard_kill_next = True
        else:
            drain.set = True
            log.warning("SIGINT received — draining in-flight; hit again to SIGKILL")

    signal.signal(signal.SIGINT, handler)


# ---- Helpers ------------------------------------------------------------


def _task_status_in_file(task_id: str, tasks_json_path: Path | str = "tasks.json") -> str | None:
    """Read tasks.json (fresh) and return the current `status` for `task_id`.

    Used by the reap loop to detect sub-agents that already called
    `scripts/task-finish.sh` before the CLI wrapper triggered a false-positive
    failure (skills warning, step_finish race, etc.). Returns None if the file
    can't be read or the id is missing.

    Fase 1: `tasks_json_path` acepta ruta explícita (default `"tasks.json"`
    para retrocompat con tests que asumen cwd == v2/).
    """
    import json as _json

    try:
        with open(tasks_json_path, encoding="utf-8") as fh:
            data = _json.load(fh)
    except Exception:  # noqa: BLE001
        return None
    rows = data.get("tasks", []) if isinstance(data, dict) else data
    for row in rows:
        if row.get("id") == task_id:
            return row.get("status")
    return None


# ---- Main loop ----------------------------------------------------------


def _reap_once(
    in_flight: dict[int, InFlight],
    queue: TaskQueue,
    run_file: RunFile,
    event_log: EventLog,
    spend_log: SpendLog,
    cfg: dict[str, Any],
    cwd: Path,
    gsem: _Sem,
    psem: dict[str, _Sem],
    retry_queue: list["_RetryItem"] | None = None,
    router: dict[str, RouteEntry] | None = None,
    task_costs: dict[str, float] | None = None,
) -> int:
    """Reap every child that has already exited (non-blocking).

    Returns the number of children reaped. The reap loop is the ONLY place
    that mutates `in_flight`, `queue._status` (via mark_done/mark_blocked),
    and `run_file`. Keeping it in one function makes AS-05 easier to audit.

    FR-D-4 retry-once: on the FIRST failure (`entry.dispatch.attempt < 2`),
    we do NOT block. Instead we emit a `retry` event and push the task onto
    `retry_queue` for the next `_refill` tick to re-dispatch with
    `attempt=2`. Timeouts skip retry (they're unlikely to be transient and
    the operator wants to see them). If `retry_queue` is None (legacy tests
    that don't wire it up), the retry branch is disabled.

    FR-D-7 version-drift fallback: on failure with a `should_retry_with_fallback`
    flag (set by `is_version_drift_error` post-check) AND the route has a
    `fallback_cli_model`, we swap the route to the fallback via
    `dataclasses.replace` and re-queue instead of blocking. The fallback
    retry consumes the same one-shot slot as FR-D-4.
    """
    reaped = 0
    while True:
        try:
            pid, status = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            # No children at all — reap loop is done.
            break
        if pid == 0:
            # No child has changed state yet.
            break

        entry = in_flight.pop(pid, None)
        if entry is None:
            # Not one of ours — a stray subprocess (test fixture, etc).
            continue

        exit_code = _exit_code_from_status(status)
        log_text = _read_log_safely(entry.dispatch.log_path)

        # If we already flagged this as timed out, force failure.
        if entry.timed_out:
            result = DispatchResult(
                exit_code=exit_code,
                success=False,
                stdout=log_text,
                error_message="orchestrator timeout",
            )
        else:
            try:
                result = entry.backend.parse_result(exit_code, log_text)
            except Exception as exc:  # noqa: BLE001 — parser errors mustn't crash the loop
                log.exception("backend parse_result raised: %s", exc)
                result = DispatchResult(
                    exit_code=exit_code,
                    success=False,
                    stdout=log_text,
                    error_message=f"parse_result raised: {exc}",
                )

        result, spoof_id = _post_run_checks(entry.task, result, cfg, cwd, log_text)

        if spoof_id:
            event_log.emit(
                "id_spoof_detected",
                entry.task.id,
                backend=entry.route.backend,
                claimed_id=spoof_id,
            )

        # Fase B: classify the failure so the retry branch can pick a policy
        # (skip retry vs. retry same vs. retry with fallback vs. longer backoff).
        # Force TIMEOUT / ID_SPOOF for the two failure modes orch owns
        # explicitly — CLI output can be anything in those cases, but the
        # semantics are locked (no retry).
        failure_class: FailureClass | None = None
        if not result.success:
            if entry.timed_out:
                failure_class = FailureClass.TIMEOUT
            elif spoof_id:
                failure_class = FailureClass.ID_SPOOF
            else:
                failure_class = classify_failure(result)
                # FR-D-7: post-run drift detection (backend-agnostic). If the
                # failure is VERSION_DRIFT, flag it so the retry branch swaps
                # in `fallback_cli_model` (if the route has one).
                if failure_class is FailureClass.VERSION_DRIFT:
                    result.should_retry_with_fallback = True

        # Record spend regardless of success (partial cost still real).
        duration_s = max(0.0, _monotonic() - entry.started_at_mono)
        _record_spend(
            spend_log,
            entry,
            result,
            duration_s,
        )
        # FR-D-8: keep a per-task cumulative cost tally so the escalation
        # gate below can enforce `budget.per_dispatch_usd` across attempts.
        # `task_costs` is None only in legacy test wiring; treat it as no-op.
        if task_costs is not None:
            try:
                cost_delta = float(result.cost_usd or 0.0)
            except (TypeError, ValueError):
                cost_delta = 0.0
            task_costs[entry.task.id] = (
                task_costs.get(entry.task.id, 0.0) + cost_delta
            )

        # Persist outcome via scripts + event log.
        if result.success:
            comment = _comment_from_result(result)
            try:
                call_task_finish(
                    entry.task.id, comment, entry.route.cli_model, project_root=cwd
                )
            except Exception as exc:  # noqa: BLE001
                log.exception("call_task_finish failed for %s: %s", entry.task.id, exc)
            event_log.emit(
                "success",
                entry.task.id,
                backend=entry.route.backend,
                cost_usd=result.cost_usd,
                duration_s=duration_s,
            )
            queue.mark_done(entry.task.id)
            run_file.mark_done(entry.task.id)
        else:
            reason = (result.error_message or "unknown failure")[:500]

            # ---- Fase B: FailureClass-driven retry policy -------------------
            # Retry conditions (all must hold):
            #   1. retry_queue is wired up (main loop passes it; legacy tests
            #      that pre-date retry logic pass None → block on first fail).
            #   2. Attempt counter shows we haven't used our attempts yet.
            #      Classic FR-D-4 gives 2 total; FR-D-8 (2026-08 amendment)
            #      extends to 3 total IF the route has an `escalation_model`.
            #   3. The failure class allows retry — TIMEOUT / PERMISSION /
            #      BUDGET / ID_SPOOF are terminal (retrying just wastes budget
            #      or, for ID_SPOOF, invites the agent to spoof again).
            _NON_RETRYABLE = {
                FailureClass.TIMEOUT,
                FailureClass.PERMISSION,
                FailureClass.BUDGET,
                FailureClass.ID_SPOOF,
            }
            # FR-D-8: the second retry (attempt 3) is only allowed if the
            # route has an `escalation_model` AND VERSION_DRIFT already has
            # its own fallback path — do NOT double-swap. So attempt-3 fires
            # for TRANSIENT / RATE_LIMIT / PARSER / OTHER on retryable routes.
            has_escalation = (
                router is not None
                and entry.route.escalation_model is not None
                and entry.route.escalation_model in router
                and failure_class is not None
                and failure_class not in _NON_RETRYABLE
                and failure_class is not FailureClass.VERSION_DRIFT
            )
            # Budget cap check (FR-D-8): attempt 3 still counts against
            # `budget.per_dispatch_usd`. If cumulative spend for this task
            # has already exceeded the cap, refuse to escalate.
            budget_cap = float(
                (cfg.get("budget") or {}).get("per_dispatch_usd", 0.0) or 0.0
            )
            spent_so_far = 0.0
            if task_costs is not None:
                spent_so_far = task_costs.get(entry.task.id, 0.0)
            escalation_allowed = has_escalation and (
                budget_cap <= 0.0 or spent_so_far < budget_cap
            )

            max_attempts = 3 if escalation_allowed else 2
            can_retry = (
                retry_queue is not None
                and entry.dispatch.attempt < max_attempts
                and failure_class is not None
                and failure_class not in _NON_RETRYABLE
            )
            if can_retry:
                next_attempt = entry.dispatch.attempt + 1
                # Decide which route the retry uses:
                #   attempt 2: same route, or fallback if drift
                #   attempt 3 (FR-D-8): escalation route lookup
                retry_route = entry.route
                retry_reason = reason
                is_escalation = False
                to_route_key: str | None = None
                if next_attempt == 3 and router is not None:
                    to_route_key = entry.route.escalation_model
                    retry_route = router[to_route_key]
                    is_escalation = True
                    retry_reason = (
                        f"attempt-3 escalation: {entry.task.model!r} "
                        f"-> {to_route_key!r} ({reason})"
                    )
                elif (
                    result.should_retry_with_fallback
                    and entry.route.fallback_cli_model
                ):
                    retry_route = _dc_replace(
                        entry.route, cli_model=entry.route.fallback_cli_model
                    )
                    retry_reason = (
                        f"version-drift fallback: {entry.route.cli_model!r} "
                        f"-> {entry.route.fallback_cli_model!r} ({reason})"
                    )

                if is_escalation:
                    # FR-D-8: emit `escalate` BEFORE the `retry` event so
                    # timeline consumers see the promotion.
                    event_log.emit(
                        "escalate",
                        entry.task.id,
                        backend=retry_route.backend,
                        attempt=next_attempt,
                        from_route=entry.task.model,
                        to_route=to_route_key,
                        from_cli_model=entry.route.cli_model,
                        to_cli_model=retry_route.cli_model,
                        failure_class=failure_class.value,
                        reason=(reason or "")[:500],
                    )

                event_log.emit(
                    "retry",
                    entry.task.id,
                    backend=retry_route.backend,
                    attempt=next_attempt,
                    reason=retry_reason[:500],
                    cli_model=retry_route.cli_model,
                    failure_class=failure_class.value,
                )
                # Reset the queue status so ready() picks it up again next
                # tick. We use the internal _status map directly because
                # TaskQueue has no `mark_todo` (retry is the only caller).
                queue._status[entry.task.id] = "todo"  # noqa: SLF001
                # Drop from the run-file's in_flight so the next refill can
                # add the fresh dispatch without a stale entry.
                run_file.remove_dispatch(entry.task.id)
                # Enqueue for the refill loop. The backoff depends on the
                # failure class:
                #   - VERSION_DRIFT → 0s (immediate — the fallback model is
                #     a different route; no rate-limit risk).
                #   - RATE_LIMIT    → `retry.rate_limit_backoff_seconds` (60s
                #     default) to let the provider window reset.
                #   - everything else → `retry.backoff_seconds` (5s default).
                #   - escalation (attempt 3) → 0s (different backend/model,
                #     no reason to wait; the failing route is left behind).
                retry_cfg = cfg.get("retry", {}) or {}
                if is_escalation:
                    backoff_s = 0.0
                elif failure_class is FailureClass.VERSION_DRIFT:
                    backoff_s = 0.0
                elif failure_class is FailureClass.RATE_LIMIT:
                    backoff_s = float(
                        retry_cfg.get("rate_limit_backoff_seconds", 60.0)
                    )
                else:
                    backoff_s = float(retry_cfg.get("backoff_seconds", 5.0))
                retry_queue.append(
                    _RetryItem(
                        task=entry.task,
                        route=retry_route,
                        attempt=next_attempt,
                        retry_earliest_at=time.monotonic() + backoff_s,
                    )
                )
                # Release semaphores — the retry re-acquires on next spawn.
                gsem.release()
                psem[entry.route.backend].release()
                # Retry re-acquires its own per-task lock; drop the old one.
                release_task_lock(entry.task_lock_fd)
                reaped += 1
                continue

            # ---- terminal failure (no retry left, or ineligible) ------------
            # False-positive guard: if the sub-agent already called
            # scripts/task-finish.sh (tasks.json status == "done") the CLI
            # wrapper's failure is spurious (skills-shortening warning,
            # step_finish buffering race, etc.). Respect the sub-agent.
            file_status = _task_status_in_file(entry.task.id, cwd / "tasks.json")
            if file_status == "done":
                event_log.emit(
                    "success",
                    entry.task.id,
                    backend=entry.route.backend,
                    reason="sub_agent_finished_despite_detector",
                    detector_reason=reason,
                    exit_code=exit_code,
                    attempt=entry.dispatch.attempt,
                )
                queue.mark_done(entry.task.id)
                run_file.mark_done(entry.task.id)
            else:
                try:
                    call_task_block(
                        entry.task.id, reason, entry.route.cli_model, project_root=cwd
                    )
                except Exception as exc:  # noqa: BLE001
                    log.exception("call_task_block failed for %s: %s", entry.task.id, exc)
                event_type = "timeout" if entry.timed_out else "fail"
                event_log.emit(
                    event_type,
                    entry.task.id,
                    backend=entry.route.backend,
                    reason=reason,
                    exit_code=exit_code,
                    attempt=entry.dispatch.attempt,
                )
                queue.mark_blocked(entry.task.id)
                run_file.mark_blocked(entry.task.id)

        # Release capacity so the refill step can pick up new work.
        gsem.release()
        psem[entry.route.backend].release()
        release_task_lock(entry.task_lock_fd)
        reaped += 1
    return reaped


def _timeout_sweep(
    in_flight: dict[int, InFlight],
    event_log: EventLog,
) -> None:
    """SIGTERM → 10 s grace → SIGKILL any child that has exceeded its budget.

    Timed-out children stay in `in_flight` so the reap loop picks them up
    on the next tick (with `entry.timed_out=True`). We do NOT block here —
    the SIGKILL escalation is scheduled 10 s in the future via a marker
    field on the entry.
    """
    now = _monotonic()
    for pid, entry in list(in_flight.items()):
        if entry.timed_out:
            # Already SIGTERM'd — escalate to SIGKILL after 10 s.
            if now - entry.started_at_mono > entry.timeout_s + 10.0:
                try:
                    os.kill(pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass
            continue
        if now - entry.started_at_mono > entry.timeout_s:
            entry.timed_out = True
            log.warning(
                "task %s exceeded timeout %.1fs — sending SIGTERM to pid %d",
                entry.task.id,
                entry.timeout_s,
                pid,
            )
            try:
                os.kill(pid, signal.SIGTERM)
            except (ProcessLookupError, PermissionError):
                pass
            # Emit timeout event now so the dashboard sees it live.
            event_log.emit(
                "timeout",
                entry.task.id,
                backend=entry.route.backend,
                timeout_s=entry.timeout_s,
            )


def _spawn_one(
    task: Task,
    route: RouteEntry,
    attempt: int,
    cfg: dict[str, Any],
    gsem: _Sem,
    psem: dict[str, _Sem],
    in_flight: dict[int, InFlight],
    run_file: RunFile,
    event_log: EventLog,
    run_id: str,
    state_dir: Path,
    cwd: Path,
    queue: TaskQueue,
    use_task_locks: bool = False,
    budget_gate: BudgetGate | None = None,
) -> bool:
    """Try to acquire semaphores and spawn one task; return True on success.

    Extracted from `_refill` so retry re-dispatches share the exact same
    spawn contract (semaphore acquisition, task-start, prompt render, spawn,
    in-flight bookkeeping, dispatch event) as first-attempt dispatches. The
    only difference for retries is `attempt >= 2` and the caller may have
    swapped `route.cli_model` for the version-drift fallback.
    """
    # ---- per-task lock (opt-in via --task-locks) ----------------------
    task_lock_fd = None
    if use_task_locks:
        task_lock_fd = try_acquire_task_lock(task.id, state_dir)
        if task_lock_fd is None:
            # Another orch instance owns this task — silently skip.
            return False

    # ---- Sprint 7 budget gate (before semaphores so we don't churn) --
    # Checked BEFORE the semaphore so we don't acquire+release when the
    # provider is capped. Skipping here is functionally identical to a full
    # semaphore — the next tick re-checks. Debounced `budget_skip` event so
    # the log doesn't spam (one entry per (task, backend, tick)).
    if budget_gate is not None:
        ok, reason, reset_at = budget_gate.can_dispatch(route.backend)
        if not ok:
            release_task_lock(task_lock_fd)
            if reason is not None:
                event_log.emit(
                    "budget_skip",
                    task.id,
                    backend=route.backend,
                    reason=reason,
                    reset_at=reset_at.strftime("%Y-%m-%dT%H:%M:%SZ")
                    if reset_at
                    else None,
                )
            return False

    # ---- semaphore acquisition (non-blocking) -------------------------
    if not psem[route.backend].try_acquire():
        release_task_lock(task_lock_fd)
        return False
    if not gsem.try_acquire():
        psem[route.backend].release()
        release_task_lock(task_lock_fd)
        return False

    # ---- start the task (side-effecting; must be after semaphore) -----
    try:
        call_task_start(
            task.id,
            author=f"{route.backend}/{route.cli_model}",
            project_root=cwd,
        )
    except Exception as exc:  # noqa: BLE001
        log.exception("call_task_start failed for %s: %s", task.id, exc)
        gsem.release()
        psem[route.backend].release()
        release_task_lock(task_lock_fd)
        queue.mark_blocked(task.id)
        run_file.mark_blocked(task.id)
        event_log.emit(
            "block",
            task.id,
            backend=route.backend,
            reason=f"task-start.sh failed: {exc}",
        )
        return False

    # ---- render prompt + spawn ----------------------------------------
    completed_dep_tasks = _completed_dep_tasks(queue, task)
    try:
        # `spec_root` viene de config.yaml (Fase 3). `_load_config` garantiza
        # el default rupies histórico (`docs/rewrite-plan`) cuando la clave
        # no está presente — no hace falta un fallback defensivo acá.
        prompt_path = render_prompt(
            task=task,
            completed_deps=completed_dep_tasks,
            spec_ref=task.spec_ref or None,
            run_id=run_id,
            state_dir=state_dir,
            project_root=cwd,
            spec_root=cfg["spec_root"],
        )
    except Exception as exc:  # noqa: BLE001
        log.exception("render_prompt failed for %s: %s", task.id, exc)
        gsem.release()
        psem[route.backend].release()
        release_task_lock(task_lock_fd)
        try:
            call_task_block(
                task.id, f"prompt render failed: {exc}", route.cli_model,
                project_root=cwd,
            )
        except Exception:  # noqa: BLE001
            pass
        queue.mark_blocked(task.id)
        run_file.mark_blocked(task.id)
        event_log.emit(
            "block",
            task.id,
            backend=route.backend,
            reason=f"prompt render failed: {exc}",
        )
        return False

    log_path = state_dir / "logs" / f"{task.id}.log"
    log_path.parent.mkdir(parents=True, exist_ok=True)

    backend = get_backend(route.backend, cfg=cfg)
    try:
        dispatch = backend.spawn(
            task=task,
            route=route,
            prompt_path=prompt_path,
            log_path=log_path,
            cwd=cwd,
        )
    except (FileNotFoundError, OSError) as exc:
        log.exception("spawn failed for %s: %s", task.id, exc)
        gsem.release()
        psem[route.backend].release()
        release_task_lock(task_lock_fd)
        try:
            call_task_block(
                task.id, f"spawn failed: {exc}", route.cli_model,
                project_root=cwd,
            )
        except Exception:  # noqa: BLE001
            pass
        queue.mark_blocked(task.id)
        run_file.mark_blocked(task.id)
        event_log.emit(
            "block",
            task.id,
            backend=route.backend,
            reason=f"spawn failed: {exc}",
        )
        return False

    # Preserve the attempt counter on the dispatch object (FR-D-4).
    dispatch.attempt = attempt

    timeout_s = _timeout_for(task, cfg)
    entry = InFlight(
        task=task,
        route=route,
        backend=backend,
        dispatch=dispatch,
        started_at_mono=_monotonic(),
        timeout_s=timeout_s,
        task_lock_fd=task_lock_fd,
    )
    in_flight[dispatch.pid] = entry
    queue.mark_in_flight(task.id)
    run_file.add_dispatch(dispatch)
    event_log.emit(
        "dispatch",
        task.id,
        backend=route.backend,
        pid=dispatch.pid,
        cli_model=route.cli_model,
        attempt=attempt,
    )
    return True


def _refill(
    queue: TaskQueue,
    router: dict[str, RouteEntry],
    cfg: dict[str, Any],
    mode: str,
    gate: SemiModeGate | None,
    gsem: _Sem,
    psem: dict[str, _Sem],
    in_flight: dict[int, InFlight],
    run_file: RunFile,
    event_log: EventLog,
    run_id: str,
    state_dir: Path,
    cwd: Path,
    dispatched_count: int,
    max_tasks: int | None,
    deferred: set[str],
    drain: _DrainFlag,
    retry_queue: list[_RetryItem] | None = None,
    use_task_locks: bool = False,
    only: str | None = None,
    budget_gate: BudgetGate | None = None,
) -> int:
    """Try to dispatch as many ready tasks as capacity allows.

    Returns the new dispatched_count. The refill loop iterates the ready set
    ONCE per tick — if we can't grab a semaphore for task T, we skip it
    (next tick will re-try). This keeps the loop bounded even with 300 ready
    tasks.

    FR-D-4 / FR-D-7: `retry_queue` is drained FIRST (before consulting
    `queue.ready()`) so a task marked for retry never waits behind a
    newly-ready peer. Retry items carry their own (possibly fallback-swapped)
    `RouteEntry` and the incremented attempt counter.
    """
    if drain.set:
        return dispatched_count

    # ---- (1) drain retry queue first --------------------------------------
    if retry_queue:
        remaining: list[_RetryItem] = []
        now = time.monotonic()
        for item in retry_queue:
            if max_tasks is not None and dispatched_count >= max_tasks:
                remaining.append(item)
                continue
            if drain.set:
                remaining.append(item)
                continue
            # FR-D-4 backoff (design.md §5): skip items whose earliest
            # dispatch time is still in the future. Passive wait — the main
            # loop already ticks, so no sleep needed here.
            if item.retry_earliest_at > now:
                remaining.append(item)
                continue
            ok = _spawn_one(
                item.task,
                item.route,
                item.attempt,
                cfg,
                gsem,
                psem,
                in_flight,
                run_file,
                event_log,
                run_id,
                state_dir,
                cwd,
                queue,
                use_task_locks=use_task_locks,
                budget_gate=budget_gate,
            )
            if ok:
                dispatched_count += 1
            else:
                # Couldn't spawn this tick (semaphore full, or spawn failed
                # → already blocked inside _spawn_one). Only requeue if it
                # was a pure capacity miss — i.e. status is still "todo".
                if queue._status.get(item.task.id) == "todo":  # noqa: SLF001
                    remaining.append(item)
        retry_queue[:] = remaining

    # ---- (2) normal ready set ---------------------------------------------
    in_flight_ids = {entry.task.id for entry in in_flight.values()}
    for task in queue.ready(in_flight_ids=in_flight_ids, only=only):
        if task.id in deferred:
            continue
        if max_tasks is not None and dispatched_count >= max_tasks:
            break
        if drain.set:
            break

        route = router.get(task.model)
        if route is None:
            # Validated at startup; this branch is a defensive safety net.
            log.error("route missing at dispatch time for %s (%s)", task.id, task.model)
            continue

        # ---- semi-mode gate ------------------------------------------------
        if mode == "semi" and gate is not None and is_critical(task, router):
            decision = gate.prompt_operator(task)
            if decision == "defer":
                deferred.add(task.id)
                continue
            if decision == "skip":
                try:
                    call_task_block(
                        task.id, "operator skipped", route.cli_model,
                        project_root=cwd,
                    )
                except Exception as exc:  # noqa: BLE001
                    log.exception("skip block failed for %s: %s", task.id, exc)
                event_log.emit(
                    "block",
                    task.id,
                    backend=route.backend,
                    reason="operator skipped",
                )
                queue.mark_blocked(task.id)
                run_file.mark_blocked(task.id)
                continue
            if decision == "quit":
                drain.set = True
                break
            # "dispatch" → fall through.

        ok = _spawn_one(
            task,
            route,
            1,  # first attempt
            cfg,
            gsem,
            psem,
            in_flight,
            run_file,
            event_log,
            run_id,
            state_dir,
            cwd,
            queue,
            use_task_locks=use_task_locks,
            budget_gate=budget_gate,
        )
        if ok:
            dispatched_count += 1

    return dispatched_count


def _drain_wait(
    in_flight: dict[int, InFlight],
    queue: TaskQueue,
    run_file: RunFile,
    event_log: EventLog,
    spend_log: SpendLog,
    cfg: dict[str, Any],
    cwd: Path,
    gsem: _Sem,
    psem: dict[str, _Sem],
    timeout_s: float = 300.0,
    router: dict[str, RouteEntry] | None = None,
    task_costs: dict[str, float] | None = None,
) -> None:
    """Poll `_reap_once` until `in_flight` empty or overall timeout hits.

    Used on SIGINT drain. If children never terminate within `timeout_s`
    we return anyway — the outer handler already SIGKILL'd on second SIGINT.
    """
    deadline = _monotonic() + timeout_s
    while in_flight and _monotonic() < deadline:
        _reap_once(
            in_flight, queue, run_file, event_log, spend_log, cfg, cwd, gsem, psem,
            router=router, task_costs=task_costs,
        )
        _timeout_sweep(in_flight, event_log)
        time.sleep(0.2)


# ---- Small helpers ------------------------------------------------------


def _exit_code_from_status(status: int) -> int:
    """Decode POSIX wait status → conventional exit code."""
    if os.WIFEXITED(status):
        return os.WEXITSTATUS(status)
    if os.WIFSIGNALED(status):
        return -os.WTERMSIG(status)
    return -1


def _read_log_safely(path: str | Path) -> str:
    try:
        return Path(path).read_text(encoding="utf-8", errors="replace")
    except (OSError, FileNotFoundError):
        return ""


def _completed_dep_tasks(queue: TaskQueue, task: Task) -> list[Task]:
    """Look up the completed dep Tasks for prompt rendering.

    All deps of a ready task are guaranteed `done` by `TaskQueue.ready()` —
    we still guard against KeyError from an out-of-band mutation.
    """
    deps: list[Task] = []
    for dep_id in task.dependencies or []:
        try:
            dep = queue._by_id[dep_id]  # noqa: SLF001 — reading only
        except KeyError:
            continue
        deps.append(dep)
    return deps


def _comment_from_result(result: DispatchResult) -> str:
    """Truncate the subprocess stdout to fit the task-finish.sh comment slot."""
    txt = (result.stdout or "").strip()
    if not txt:
        return f"orchestrator: success (cost=${result.cost_usd:.4f})"
    return f"orchestrator: {txt[:200]}"


def _record_spend(
    spend_log: SpendLog,
    entry: InFlight,
    result: DispatchResult,
    duration_s: float,
) -> None:
    """Emit one SpendEntry, tolerating partial data on failures.

    NFR-OBS-2 guarantees exactly one line per completed dispatch — including
    failures — so the dashboard's cost view doesn't miss silent burns.
    """
    try:
        # Fase 2: SpendEntry no incluye `project_id` explícito acá — el
        # `SpendLog` lo enriquece automáticamente al escribir (usa el
        # `project_id` con el que fue construido en main()). Así este sitio
        # no tiene que enterarse del contexto de proyecto.
        spend_log.record(
            SpendEntry(
                ts=_utc_now_iso(),
                task_id=entry.task.id,
                backend=entry.route.backend,
                model=entry.route.cli_model,
                tokens_in=int(result.tokens_in or 0),
                tokens_out=int(result.tokens_out or 0),
                cost_usd=float(result.cost_usd or 0.0),
                duration_s=float(duration_s),
            )
        )
    except Exception as exc:  # noqa: BLE001 — spend logging is best-effort
        log.warning("spend log write failed for %s: %s", entry.task.id, exc)


# ---- main() -------------------------------------------------------------


def _run_dashboard_subcommand(argv: list[str]) -> int:
    """Handle `orch dashboard [flags]` — separate parser to keep the main
    loop's argparser unchanged.

    We do NOT reuse `_build_argparser()` here on purpose: the dashboard has
    a completely different flag set (--port, --host, --reload) and mixing
    them would leak into the main-loop `--help`. Both parsers accept
    `--project-root`/`--project-id` so path resolution stays identical.
    """
    p = argparse.ArgumentParser(
        prog="orch dashboard",
        description="Local read-only dashboard over tasks.json / events / spend.",
    )
    p.add_argument("--port", type=int, default=7420, help="Bind port (default: 7420)")
    p.add_argument("--host", default="127.0.0.1", help="Bind host (default: 127.0.0.1)")
    p.add_argument("--project-root", default=None, metavar="PATH",
                   help="Project root; default = cwd. Env fallback: ORCH_PROJECT_ROOT.")
    p.add_argument("--project-id", default=None, metavar="ID",
                   help="Project id override. Env fallback: ORCH_PROJECT_ID.")
    p.add_argument("--config", default="orchestrator/config.yaml",
                   help="Path to config.yaml (default: orchestrator/config.yaml)")
    p.add_argument("--reload", action="store_true",
                   help="Enable uvicorn --reload (dev only; watches for code changes).")
    args = p.parse_args(argv)

    try:
        from orchestrator.dashboard.server import run as dashboard_run
    except ImportError as exc:
        print(f"dashboard dependencies missing: {exc}", file=sys.stderr)
        print("Install: fastapi >= 0.115, uvicorn[standard] >= 0.30, jinja2 >= 3.1",
              file=sys.stderr)
        return 1
    return dashboard_run(
        port=args.port,
        host=args.host,
        project_root=args.project_root,
        project_id=args.project_id,
        config=args.config,
        reload=args.reload,
    )


def main(argv: list[str] | None = None) -> int:
    """CLI entry point. Return the process exit code (FR-CLI-3)."""
    logging.basicConfig(
        level=os.environ.get("ORCH_LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    # Subcommand routing. `orch dashboard` peels off before the main-loop
    # parser touches argv so the flag namespaces don't collide.
    incoming = sys.argv[1:] if argv is None else argv
    if incoming and incoming[0] == "dashboard":
        return _run_dashboard_subcommand(incoming[1:])

    parser = _build_argparser()
    args = parser.parse_args(argv)

    # ---- (1) Project path resolution + root contract ---------------------
    # Fase 1 multi-proyecto: `paths.project_root` centraliza la resolución
    # de tasks.json / config.yaml / model_router.yaml / state/. Cuando el
    # usuario no pasa --project-root (ni ORCH_PROJECT_ROOT), `paths.project_root
    # == Path.cwd()` → comportamiento histórico intacto (rupies desde v2/).
    paths = resolve_project_paths(
        project_root_arg=args.project_root,
        project_id_arg=args.project_id,
        config_arg=args.config,
    )
    log.info(
        "project_root=%s project_id=%s config=%s",
        paths.project_root,
        paths.project_id,
        paths.config_yaml,
    )

    # Historic guard: cuando corremos SIN --project-root, exigimos cwd
    # con basename `v2` (contrato duro rupies, AS-08). Con --project-root
    # ese chequeo pierde sentido — el usuario ya pidió otro root — y lo
    # saltamos. `paths.ensure_valid()` reemplaza el guard genérico.
    #
    # Fase 2: `paths.explicit_root` unifica la decisión ("¿vino de flag/env?").
    if not paths.explicit_root:
        if Path.cwd().name != "v2":
            print(
                f"orchestrator must be run from v2/ root; cwd={Path.cwd()}",
                file=sys.stderr,
            )
            return 2
    try:
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    # cwd (variable histórica) se conserva para no reescribir todos los
    # callsites; ahora apunta al `project_root` resuelto.
    cwd = paths.project_root

    # ---- (2) config + router + tasks -------------------------------------
    try:
        cfg = _load_config(paths.config_yaml)
    except FileNotFoundError as exc:
        print(f"config error: {exc}", file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    try:
        router = load_router(paths.router_yaml)
    except Exception as exc:  # noqa: BLE001
        print(f"router load failed: {exc}", file=sys.stderr)
        return 1

    # FR-D-7: emit a startup WARN for every router entry that declares a
    # `fallback_cli_model`. We don't probe the CLIs here (Option A per the
    # design closeout): the actual substitution fires post-run, when a
    # dispatch fails with a "model not found"-style error. The WARN lets the
    # operator see which routes are drift-resilient before any dispatch.
    _warn_fallback_routes(router)

    try:
        tasks = load_tasks(paths.tasks_json)
    except Exception as exc:  # noqa: BLE001
        print(f"tasks.json load failed: {exc}", file=sys.stderr)
        return 1

    # ---- (3) --only sanity check -----------------------------------------
    # `--only` is a DISPATCHER-scope filter, not a graph-scope one: applying
    # it before queue construction would break `_validate_deps` for any task
    # whose deps sit outside the glob (e.g. `--only P0-013` where P0-013
    # depends on an already-`done` P0-012). We validate/build the FULL DAG
    # below and pass `args.only` to `queue.ready(only=...)` at dispatch time.
    # Still, if the glob matches NOTHING at all, fail fast with a clear
    # message (preserves prior operator UX).
    if args.only and not _filter_by_only(tasks, args.only):
        print(f"no tasks match --only={args.only!r}", file=sys.stderr)
        return 1

    # ---- (4) route validation --------------------------------------------
    try:
        validate_all_models(tasks, router)
    except UnroutedModelError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    # ---- (5) queue construction (cycle + missing-dep detection) ----------
    try:
        queue = TaskQueue(tasks)
    except (TaskCycleError, MissingDependencyError) as exc:
        print(str(exc), file=sys.stderr)
        return 1

    # ---- (6) flock -------------------------------------------------------
    state_dir = paths.state_dir
    lock_path = state_dir / ".lock"
    # --task-locks: skip the global lock; concurrency guarded per-task in
    # `_spawn_one` via `try_acquire_task_lock`. `lock_fd` stays None; the
    # finally-close in main() is None-safe.
    if args.task_locks:
        lock_fd = None
    else:
        try:
            lock_fd = acquire_flock(lock_path)
        except FlockContentionError as exc:
            print(str(exc), file=sys.stderr)
            # Best-effort event emit into a scratch events file (the run's own
            # file doesn't exist yet — we haven't picked a run-id).
            try:
                scratch = EventLog(
                    state_dir / "events-contention.jsonl",
                    project_id=paths.project_id,
                )
                scratch.emit(
                    "flock_contention",
                    "-",
                    backend="",
                    holder_run_id=exc.holder_run_id or "",
                )
            except Exception:  # noqa: BLE001
                pass
            return 3

    # ---- (6b) reap orphaned in-flight PIDs from prior crashed runs -------
    # Any run file left with an entry in `in_flight` whose PID is dead
    # (crashed CLI, closed shell, macOS terminal session torn down…) will
    # otherwise appear "live" in the dashboard forever. Sweep them once at
    # startup so we begin from a clean slate; the main loop below repeats
    # the sweep periodically. Never raises past its own boundary.
    reap_report = reconcile_in_flight(state_dir, project_id=paths.project_id)
    if reap_report.get("reconciled"):
        log.info(
            "startup reconcile: reaped %d orphan(s) from %d in-flight entry(ies): %s",
            len(reap_report["reconciled"]),
            reap_report.get("checked", 0),
            reap_report["reconciled"],
        )

    # ---- (7) run-file (new or resumed) -----------------------------------
    try:
        if args.resume:
            run_id = args.resume
            run_path = state_dir / f"run-{run_id}.json"
            if not run_path.exists():
                print(f"resume: no run file at {run_path}", file=sys.stderr)
                if lock_fd is not None:
                    lock_fd.close()
                return 1
            run_file = RunFile.load(run_path)
        else:
            run_id = str(uuid.uuid4())
            run_file = RunFile.create(state_dir, run_id=run_id, mode=args.mode)

        if lock_fd is not None:
            write_lock_holder(lock_fd, run_id=run_id, pid=os.getpid())

        events_path = state_dir / f"events-{run_id}.jsonl"
        event_log = EventLog(events_path, project_id=paths.project_id)
        spend_log = SpendLog(state_dir, project_id=paths.project_id)

        # ---- resume reconciliation ---------------------------------------
        if args.resume:
            tasks_by_id = {t.id: t for t in tasks}
            report = reconcile_run(
                run_file, event_log, tasks_by_id, project_root=paths.project_root
            )
            log.info(
                "resume reconcile: adopted=%s reverted=%s errors=%s",
                report.adopted,
                report.reverted,
                report.errors,
            )
            # Reflect reverts in the queue so they don't stay marked in-progress.
            for tid in report.reverted:
                if tid in queue._status:  # noqa: SLF001
                    queue.mark_blocked(tid)
            for tid in report.adopted:
                # If the reconciler adopted-alive, keep in-progress; if it
                # adopted-done (dead+dirty), mark done in the queue too.
                if tid in run_file.state.completed and tid in queue._status:  # noqa: SLF001
                    queue.mark_done(tid)
                elif tid in run_file.state.in_flight:
                    if tid in queue._status:  # noqa: SLF001
                        queue.mark_in_flight(tid)

        # ---- (8) --dry-run ------------------------------------------------
        if args.dry_run:
            count = _print_plan(queue, router, args.max_tasks, only=args.only)
            # AS-08: NO run/spend/events files touched — but we already made
            # the run file above (create) which is a side effect. Remove it
            # so dry-run stays clean.
            try:
                run_file.path.unlink(missing_ok=True)
            except OSError:
                pass
            # Also unlink the events file if we created one empty on init.
            try:
                if events_path.exists() and events_path.stat().st_size == 0:
                    events_path.unlink()
            except OSError:
                pass
            log.info("dry-run planned %d dispatches", count)
            return 0

        # ---- (9) main loop -----------------------------------------------
        gsem, psem = _build_semaphores(cfg)
        in_flight: dict[int, InFlight] = {}
        retry_queue: list[_RetryItem] = []
        # FR-D-8: cumulative USD spend per task_id across attempts. The reap
        # loop reads this to enforce the per-dispatch budget cap before
        # firing an attempt-3 escalation.
        task_costs: dict[str, float] = {}
        drain = _DrainFlag()
        deferred: set[str] = set()
        gate: SemiModeGate | None = (
            SemiModeGate(router) if args.mode == "semi" else None
        )
        # Sprint 7 — provider budget guardrails (loads `budgets.yaml` if
        # present; otherwise the gate is disabled and this is a no-op).
        budget_gate = _load_budget_gate(
            cfg,
            state_dir,
            cli_preset=args.budgets_preset,
            config_path=paths.config_yaml,
        )
        _install_sigint(drain, in_flight)

        dispatched_count = 0
        # Periodic orphan-PID sweep: piggybacks on the existing tick, no
        # new thread. 60 s is coarse enough to be near-free (one `os.kill`
        # per in-flight entry) yet responsive enough that a crashed CLI
        # doesn't linger on the dashboard past a minute.
        _RECONCILE_INTERVAL_SEC = 60.0
        last_reconcile_ts = time.monotonic()
        while True:
            _reap_once(
                in_flight, queue, run_file, event_log, spend_log, cfg, cwd, gsem, psem,
                retry_queue=retry_queue,
                router=router,
                task_costs=task_costs,
            )
            _timeout_sweep(in_flight, event_log)

            if drain.set and not in_flight:
                break

            if not drain.set:
                dispatched_count = _refill(
                    queue,
                    router,
                    cfg,
                    args.mode,
                    gate,
                    gsem,
                    psem,
                    in_flight,
                    run_file,
                    event_log,
                    run_id,
                    state_dir,
                    cwd,
                    dispatched_count,
                    args.max_tasks,
                    deferred,
                    drain,
                    retry_queue=retry_queue,
                    use_task_locks=args.task_locks,
                    only=args.only,
                    budget_gate=budget_gate,
                )

            # Sprint 7 — sleep-until-reset when every provider is capped.
            # We only long-sleep when there's nothing in flight (otherwise the
            # reap loop needs the 200 ms tick to catch child exits). Chunked
            # to 30 s max so SIGINT / draining still feels responsive.
            if (
                not drain.set
                and not in_flight
                and budget_gate.all_capped()
            ):
                reset_at = budget_gate.earliest_reset()
                if reset_at is not None:
                    now = datetime.now(timezone.utc)
                    wait_s = max(1.0, (reset_at - now).total_seconds())
                    chunk = min(wait_s, 30.0)
                    event_log.emit(
                        "budget_pause",
                        "-",
                        backend="all",
                        reset_at=reset_at.strftime("%Y-%m-%dT%H:%M:%SZ"),
                        wait_seconds=round(wait_s, 1),
                    )
                    log.info(
                        "all providers capped; sleeping %.1fs (next reset %s)",
                        chunk,
                        reset_at.strftime("%H:%M UTC"),
                    )
                    time.sleep(chunk)
                    continue

            # Terminate condition: nothing ready, nothing in flight, no retries pending.
            if not in_flight and not queue.ready(only=args.only) and not retry_queue:
                # Give deferred tasks a chance in semi-mode by re-prompting.
                if args.mode == "semi" and deferred:
                    # Snapshot then clear; ready() will offer them again.
                    log.info("re-prompting %d deferred tasks", len(deferred))
                    deferred.clear()
                    continue
                break

            # Periodic orphan sweep across ALL run files — reaps in-flight
            # entries whose PID died (crash / closed shell / OS session
            # tear-down). Runs at most every `_RECONCILE_INTERVAL_SEC`.
            now_mono = time.monotonic()
            if now_mono - last_reconcile_ts >= _RECONCILE_INTERVAL_SEC:
                last_reconcile_ts = now_mono
                tick_report = reconcile_in_flight(state_dir, project_id=paths.project_id)
                if tick_report.get("reconciled"):
                    log.info(
                        "tick reconcile: reaped %d orphan(s): %s",
                        len(tick_report["reconciled"]),
                        tick_report["reconciled"],
                    )

            time.sleep(0.2)

        # ---- final drain on SIGINT ---------------------------------------
        if drain.set and in_flight:
            _drain_wait(
                in_flight, queue, run_file, event_log, spend_log, cfg, cwd, gsem, psem,
                router=router, task_costs=task_costs,
            )
            return 130

        # ---- exit code ---------------------------------------------------
        blocked_count = len(run_file.state.blocked)
        if blocked_count > 0:
            log.warning("run completed with %d blocked task(s)", blocked_count)
            return 1
        return 0
    finally:
        # Always release the flock (None when --task-locks).
        if lock_fd is not None:
            try:
                lock_fd.close()
            except Exception:  # noqa: BLE001
                pass


if __name__ == "__main__":
    sys.exit(main())
