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
from orchestrator.budget import (  # noqa: E402
    BudgetGate,
    _format_reset_eta,
    _format_tokens_short,
    load_budget_config,
    warn_undersized_presets,
)
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
Subcommands (run `orch <cmd> --help` for details):
  orch [FLAGS]              Run the main dispatch loop (default — flags below)
  orch init PATH [FLAGS]    Scaffold a new orch project at PATH
  orch atomize [FLAGS]      Convert markdown specs → tasks.json (diff-first)
  orch dashboard [FLAGS]    Launch the read-only FastAPI dashboard
  orch reset [FLAGS]        Revert stuck in-progress tasks to todo
  orch stop [FLAGS]         Signal a running orch to drain and exit

Exit codes:
  0   clean drain (all reachable tasks done, none blocked)
  1   config error / unrouted model / dependency cycle / blocked tasks
  2   project layout invalid (tasks.json / scripts/task-*.sh missing)
  3   flock contention (another orchestrator holds state/.lock)
  130 SIGINT during graceful drain

Project layout expected at --project-root (or CWD if not set):
  <project>/
  ├── tasks.json                    # your task DAG
  ├── scripts/task-{start,finish,block}.sh
  └── orchestrator/state/           # created on first run

Run `orch init PATH` to scaffold this layout automatically.
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
        "--json",
        action="store_true",
        help=(
            "Emit the dry-run plan as JSON to stdout. Requires --dry-run "
            "(this flag is a no-op outside dry-run mode)."
        ),
    )
    parser.add_argument(
        "--config",
        default=".orchestrator/config.yaml",
        help="Path to config.yaml (default: .orchestrator/config.yaml)",
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
    # Sprint C: -v/-q for informational log control. Overrides ORCH_LOG_LEVEL
    # (env) when explicitly set; otherwise the env var (or INFO default) wins.
    parser.add_argument(
        "-v", "--verbose",
        action="count", default=0,
        help="Increase log verbosity (-v = DEBUG). Overrides ORCH_LOG_LEVEL.",
    )
    parser.add_argument(
        "-q", "--quiet",
        action="store_true",
        help="Silence INFO/WARN output (only ERROR). Overrides ORCH_LOG_LEVEL.",
    )
    return parser


# ---- Config load --------------------------------------------------------


def _load_config(path: str | Path, project_root: str | Path | None = None) -> dict[str, Any]:
    """Load config.yaml via config_loader (Sprint F-3: deep-merge override support)."""
    from orchestrator.config_loader import load_config
    return load_config(path, project_root=project_root)


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
        # Sprint A / Issue #11: warn once per undersized provider so operators
        # know their window can't fit two dispatches back-to-back.
        typical_tokens = int(cfg.get("typical_dispatch_tokens", 200_000) or 0)
        warn_undersized_presets(budget_cfg, preset, typical_tokens)
    return BudgetGate(state_dir=state_dir, config=budget_cfg)


# ---- Fallback route WARN (FR-D-7) --------------------------------------


def _warn_fallback_routes(
    router: dict[str, RouteEntry],
    *,
    verbose: bool = False,
) -> None:
    """Announce which router entries carry a `fallback_cli_model`.

    FR-D-7 said "MUST emit exactly one WARN line per substitution at
    startup." Sprint C revises this: the announcement is INFORMATIONAL
    (the actual fallback swap happens post-run in `_reap_once` when a
    dispatch fails with a version-drift-shaped error). Emitting one WARN
    per route AND also printing to stderr produces a double-emit that
    spams the console every startup.

    Behavior:
      - Non-verbose (default): a single INFO summary line like
        `N route(s) with fallback configured (use -v for detail)`.
      - Verbose: one INFO line per route with the full substitution details.

    Nothing writes to stderr directly anymore — logging owns the channel.
    """
    routes_with_fallback = [
        (key, route)
        for key, route in sorted(router.items())
        if route.fallback_cli_model
    ]
    if not routes_with_fallback:
        return
    if verbose:
        for key, route in routes_with_fallback:
            log.info(
                "route %r has fallback_cli_model=%r — will substitute "
                "for %r on version-drift errors",
                key, route.fallback_cli_model, route.cli_model,
            )
        return
    log.info(
        "%d route(s) with fallback configured (use -v for detail)",
        len(routes_with_fallback),
    )


# ---- Task filtering -----------------------------------------------------


def _filter_by_only(tasks: list[Task], only: str | None) -> list[Task]:
    """Filter tasks by fnmatch glob on `id`. `None` → pass-through."""
    if not only:
        return list(tasks)
    return [t for t in tasks if fnmatch.fnmatchcase(t.id, only)]


# ---- Dry-run plan -------------------------------------------------------


def _build_plan_rows(
    queue: TaskQueue,
    router: dict[str, RouteEntry],
    max_tasks: int | None,
    only: str | None = None,
) -> list[dict[str, Any]]:
    """Pure aggregator: return the plan rows the renderers consume.

    Extracted from the pre-Sprint-C `_print_plan` so both the human table
    renderer and the JSON renderer share the same source of truth. Only
    enumerates the CURRENT ready-set (see original docstring).
    """
    ready = queue.ready(in_flight_ids=[], only=only)
    if max_tasks is not None:
        ready = ready[:max_tasks]

    rows: list[dict[str, Any]] = []
    for t in ready:
        route = router.get(t.model)
        rows.append({
            "task_id": t.id,
            "phase": t.phase,
            "backend": route.backend if route else "?",
            "cli_model": route.cli_model if route else t.model,
            "tier": route.tier if route else None,
            "model": t.model,
            "estimate_hours": t.estimate_hours,
        })
    return rows


def _print_plan_table(rows: list[dict[str, Any]]) -> int:
    """Human rich-table renderer for the dry-run plan."""
    if _HAVE_RICH:
        table = Table(title=f"Dry-run plan ({len(rows)} ready)")
        table.add_column("Task ID", style="cyan")
        table.add_column("Phase", justify="right")
        table.add_column("Backend", style="green")
        table.add_column("CLI Model")
        table.add_column("Tier")
        table.add_column("Est h", justify="right")
        for r in rows:
            table.add_row(
                str(r["task_id"]),
                str(r["phase"]),
                str(r["backend"]),
                str(r["cli_model"]),
                str(r["tier"] or "?"),
                str(r["estimate_hours"]),
            )
        _console.print(table)  # type: ignore[union-attr]
    else:  # pragma: no cover
        print(f"Dry-run plan ({len(rows)} ready)")
        for r in rows:
            print(
                f"  {r['task_id']}  [{r['phase']}]  {r['backend']}/{r['cli_model']}  "
                f"est={r['estimate_hours']}h"
            )
    return len(rows)


def _print_plan_json(rows: list[dict[str, Any]]) -> int:
    """JSON renderer for `orch --dry-run --json`. Emits `{plan: [rows], count}`."""
    payload = {"plan": rows, "count": len(rows)}
    print(json.dumps(payload, default=str, separators=(",", ":")))
    return len(rows)


def _print_run_summary(
    *,
    run_id: str,
    run_file: Any,
    task_costs: dict[str, float] | None,
    deferred: set[str] | None = None,
    defer_reasons: dict[str, str] | None = None,
) -> None:
    """Sprint C end-of-run recap. Called once after the main loop drains.

    Prints a compact table (rich when available) covering:
      - Run id + counts (completed, blocked, deferred, still in-flight)
      - Total cost across the in-memory `task_costs` dict
      - Top 5 costliest tasks

    We use the in-memory `task_costs` (populated in the reap loop) instead
    of re-reading spend files because `SpendEntry` has no `run_id` column
    yet (Sprint C decision #5 — schema bump deferred).
    """
    state = run_file.state
    completed = list(state.completed or [])
    blocked = list(state.blocked or [])
    still_in_flight = list((state.in_flight or {}).keys())
    deferred_list = sorted(deferred or [])

    task_costs = task_costs or {}
    total_cost = round(sum(task_costs.values()), 4)
    top5 = sorted(task_costs.items(), key=lambda kv: kv[1], reverse=True)[:5]

    header = (
        f"Run summary · {run_id[:8]} · completed={len(completed)} "
        f"blocked={len(blocked)} deferred={len(deferred_list)} "
        f"in_flight={len(still_in_flight)} · cost=${total_cost:.4f}"
    )

    if _HAVE_RICH:
        table = Table(title=header)
        table.add_column("METRIC", style="cyan")
        table.add_column("VALUE")
        table.add_row("completed", ", ".join(completed) or "—")
        if blocked:
            table.add_row("blocked", ", ".join(blocked))
        if deferred_list:
            reasons = ", ".join(
                f"{tid}({(defer_reasons or {}).get(tid, 'unknown')})"
                for tid in deferred_list
            )
            table.add_row("deferred", reasons)
        if still_in_flight:
            table.add_row("still_in_flight", ", ".join(still_in_flight))
        if top5:
            top_rows = ", ".join(f"{tid}=${cost:.4f}" for tid, cost in top5)
            table.add_row("top_costs", top_rows)
        _console.print(table)  # type: ignore[union-attr]
    else:  # pragma: no cover
        print(header)
        print(f"  completed: {', '.join(completed) or '—'}")
        if blocked:
            print(f"  blocked  : {', '.join(blocked)}")
        if deferred_list:
            print(f"  deferred : {', '.join(deferred_list)}")
        if still_in_flight:
            print(f"  in_flight: {', '.join(still_in_flight)}")
        if top5:
            print("  top costs:")
            for tid, cost in top5:
                print(f"    {tid}  ${cost:.4f}")


def _print_plan(
    queue: TaskQueue,
    router: dict[str, RouteEntry],
    max_tasks: int | None,
    only: str | None = None,
    *,
    as_json: bool = False,
) -> int:
    """Legacy façade — kept so old callers/tests still work.

    New callers should use `_build_plan_rows` + `_print_plan_table` /
    `_print_plan_json` directly. The `as_json` toggle here exists just to
    make the main-loop wire-up in this file readable.
    """
    rows = _build_plan_rows(queue, router, max_tasks, only=only)
    if as_json:
        return _print_plan_json(rows)
    return _print_plan_table(rows)


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
    worktree_path: Path | None = None


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


def _killpg_or_pid(pid: int, sig: int) -> None:
    """Send `sig` to the process group of `pid`, falling back to the pid.

    Children spawned with `start_new_session=True` become process-group
    leaders — signaling the group catches the CLI plus any subprocesses
    (bash wrappers, sub-agents) it forked. `killpg` requires the pgid
    which we look up via `os.getpgid`. Any error (ProcessLookupError when
    the process already exited, PermissionError for foreign uids) is
    swallowed — callers use this for best-effort cleanup, not correctness.
    """
    try:
        pgid = os.getpgid(pid)
    except (ProcessLookupError, PermissionError, OSError):
        # Fall back to the single-pid signal — old-style children still
        # respond to it.
        try:
            os.kill(pid, sig)
        except (ProcessLookupError, PermissionError, OSError):
            pass
        return
    try:
        os.killpg(pgid, sig)
    except (ProcessLookupError, PermissionError, OSError):
        # If killpg fails (e.g. race, permission), try the pid directly.
        try:
            os.kill(pid, sig)
        except (ProcessLookupError, PermissionError, OSError):
            pass


def _install_sigint(
    drain: _DrainFlag,
    in_flight: dict[int, InFlight],
    wm: "WorktreeManager | None" = None,
) -> None:
    """SIGINT/SIGTERM → drain; second signal → SIGKILL every child group.

    Sprint A / Issue #12: SIGTERM is treated identically to SIGINT so that
    `kill <orch-pid>` and `orch stop` behave the same as Ctrl-C. Both hit
    the same in-memory `_DrainFlag`. On the second signal we escalate:
    SIGKILL the whole process group of every in-flight child (catches
    subprocess-of-subprocess trees, not just the direct CLI).

    Sprint F-2: ``wm`` is accepted for API symmetry. Worktree cleanup on
    hard kill is handled by ``main()`` after ``_drain_wait`` completes.
    """

    def handler(signum, frame):  # noqa: ARG001
        if drain.set:
            # Second signal → kill everything immediately (whole pgroup).
            for pid, entry in list(in_flight.items()):
                _killpg_or_pid(pid, signal.SIGKILL)
                entry.timed_out = True
            drain.hard_kill_next = True
        else:
            drain.set = True
            log.warning(
                "%s received — draining in-flight; hit again to SIGKILL",
                signal.Signals(signum).name if signum else "signal",
            )

    signal.signal(signal.SIGINT, handler)
    # Sprint A / Issue #12: install the same handler for SIGTERM so
    # `orch stop` / `kill <pid>` triggers graceful drain instead of an
    # abrupt exit that would strand children.
    signal.signal(signal.SIGTERM, handler)


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
    wm: "WorktreeManager | None" = None,
    vcs_provider: "VcsProvider | None" = None,
    state_backend: "SqliteBackend | None" = None,
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

        # Worktree push + cleanup (Sprint F-2)
        # push() only on success — don't publish incomplete work.
        # remove() always — best-effort, errors are logged and swallowed.
        # Sprint F-4: after a successful push, if auto_pr is enabled, create a
        # PR and hand off done-marking to the CI poller. pr_created=True means
        # the success block below must NOT call queue.mark_done() yet.
        pr_created = False
        if entry.worktree_path is not None and wm is not None:
            if result.success:
                try:
                    wm.push(entry.task.id)
                except Exception as exc:  # noqa: BLE001
                    log.warning(
                        "worktree push failed for %s (best-effort, not failing task): %s",
                        entry.task.id, exc,
                    )
                else:
                    # push succeeded — optionally open a PR (F-4)
                    _vcs_cfg = cfg.get("vcs") or {}
                    if vcs_provider is not None and state_backend is not None and _vcs_cfg.get("auto_pr"):
                        _base = str((cfg.get("dispatch") or {}).get("base_branch", "main"))
                        _title = getattr(entry.task, "title", entry.task.id) or entry.task.id
                        _spec = getattr(entry.task, "spec_ref", None) or "n/a"
                        _reason = getattr(entry.task, "reason", None) or ""
                        _body = f"Task: `{entry.task.id}`\nSpec: {_spec}\n\n{_reason}".strip()
                        try:
                            pr_url = vcs_provider.create_pr(
                                task_id=entry.task.id,
                                title=_title,
                                body=_body,
                                head=wm.branch_name(entry.task.id),
                                base=_base,
                            )
                        except Exception as exc:  # noqa: BLE001
                            log.warning("create_pr failed for %s: %s", entry.task.id, exc)
                            pr_url = None
                        if pr_url:
                            try:
                                state_backend.set_task_pr(entry.task.id, pr_url)
                                pr_created = True
                                event_log.emit("pr_created", entry.task.id, pr_url=pr_url)
                            except Exception as exc:  # noqa: BLE001
                                log.warning("set_task_pr failed for %s: %s", entry.task.id, exc)
            wm.remove(entry.task.id)

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
            if not pr_created:
                # No PR opened — mark done immediately (standard path).
                queue.mark_done(entry.task.id)
                run_file.mark_done(entry.task.id)
            # else: CI poller (_check_ci_once) will call mark_done on CI success.
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

            retry_cfg = cfg.get("retry", {})
            _base_attempts = int(retry_cfg.get("max_attempts", 2))
            max_attempts = _base_attempts + 1 if escalation_allowed else _base_attempts
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


def _redispatch_with_ci_feedback(
    task_row: dict,
    ci_logs: str,
    cfg: dict,
    wm: "WorktreeManager",
    in_flight: dict,
    run_file: "RunFile",
    event_log: "EventLog",
    spend_log: "SpendLog",
    gsem: "_Sem",
    psem: dict,
    retry_queue: list,
    router: dict,
    task_costs: dict,
    queue: "TaskQueue",
    state_dir: "Path",
    cwd: "Path",
) -> None:
    """Re-dispatch a task whose CI failed, injecting failure logs as context."""
    task_id = task_row["task_id"]
    try:
        wt_path = wm.recreate(task_id)
    except Exception as exc:  # noqa: BLE001
        log.warning("wm.recreate failed for %s; skipping CI re-dispatch: %s", task_id, exc)
        return

    context_file = wt_path / ".orch-ci-feedback.md"
    try:
        context_file.write_text(f"# CI Failure — Please fix\n\n```\n{ci_logs}\n```\n", encoding="utf-8")
    except OSError as exc:
        log.warning("could not write CI feedback file for %s: %s", task_id, exc)

    # Re-queue the task so the refill loop re-dispatches it on the next tick.
    # We mark it back to "todo" and insert a _RetryItem so the backoff + route
    # resolution follow the same path as regular retries.
    task_entry = next((t for t in queue._tasks if t.id == task_id), None)  # noqa: SLF001
    if task_entry is None:
        log.warning("CI re-dispatch: task %s not found in queue._tasks", task_id)
        return

    route_entry = (router or {}).get(task_entry.model)
    if route_entry is None:
        log.warning("CI re-dispatch: no route for task %s model %r", task_id, task_entry.model)
        return

    queue._status[task_id] = "todo"  # noqa: SLF001
    run_file.remove_dispatch(task_id)
    retry_queue.append(
        _RetryItem(
            task=task_entry,
            route=route_entry,
            attempt=1,
            retry_earliest_at=time.monotonic(),
        )
    )
    event_log.emit("ci_redispatch", task_id, backend=route_entry.backend)


def _check_ci_once(
    cfg: dict,
    state_backend: "SqliteBackend",
    vcs_provider: "VcsProvider",
    queue: "TaskQueue",
    wm: "WorktreeManager",
    in_flight: dict,
    run_file: "RunFile",
    event_log: "EventLog",
    spend_log: "SpendLog",
    gsem: "_Sem",
    psem: dict,
    retry_queue: list,
    router: dict,
    task_costs: dict,
    state_dir: "Path",
    cwd: "Path",
    last_check_ts: float,
) -> float:
    """Poll CI status for tasks with pending CI. Returns updated last_check_ts."""
    now = time.monotonic()
    interval = float((cfg.get("vcs") or {}).get("ci_poll_interval_s", 30))
    if now - last_check_ts < interval:
        return last_check_ts

    try:
        pending = state_backend.get_tasks_with_pending_ci()
    except Exception as exc:  # noqa: BLE001
        log.warning("get_tasks_with_pending_ci failed: %s", exc)
        return now

    max_retries = int((cfg.get("vcs") or {}).get("ci_max_retries", 1))
    for task_row in pending:
        task_id = task_row["task_id"]
        pr_url = task_row.get("pr_url", "")
        ci_attempts = int(task_row.get("ci_attempts", 0))
        try:
            ci_status = vcs_provider.get_ci_status(pr_url)
        except Exception as exc:  # noqa: BLE001
            log.warning("get_ci_status failed for %s: %s", task_id, exc)
            continue

        if ci_status == "success":
            try:
                state_backend.set_task_ci_status(task_id, "success")
            except Exception as exc:  # noqa: BLE001
                log.warning("set_task_ci_status(success) failed for %s: %s", task_id, exc)
            queue.mark_done(task_id)
            run_file.mark_done(task_id)
            event_log.emit("ci_success", task_id, pr_url=pr_url)
        elif ci_status == "failure":
            if ci_attempts < max_retries:
                try:
                    ci_logs = vcs_provider.get_ci_logs(pr_url)
                except Exception as exc:  # noqa: BLE001
                    log.warning("get_ci_logs failed for %s: %s", task_id, exc)
                    ci_logs = "(log retrieval failed)"
                _redispatch_with_ci_feedback(
                    task_row=task_row,
                    ci_logs=ci_logs,
                    cfg=cfg,
                    wm=wm,
                    in_flight=in_flight,
                    run_file=run_file,
                    event_log=event_log,
                    spend_log=spend_log,
                    gsem=gsem,
                    psem=psem,
                    retry_queue=retry_queue,
                    router=router,
                    task_costs=task_costs,
                    queue=queue,
                    state_dir=state_dir,
                    cwd=cwd,
                )
                try:
                    state_backend.increment_ci_attempts(task_id)
                    state_backend.set_task_ci_status(task_id, "pending")
                except Exception as exc:  # noqa: BLE001
                    log.warning("CI attempt update failed for %s: %s", task_id, exc)
                event_log.emit("ci_failure_retry", task_id, pr_url=pr_url, attempt=ci_attempts + 1)
            else:
                try:
                    state_backend.set_task_ci_status(task_id, "failure")
                except Exception as exc:  # noqa: BLE001
                    log.warning("set_task_ci_status(failure) failed for %s: %s", task_id, exc)
                queue.mark_blocked(task_id)
                run_file.mark_blocked(task_id)
                event_log.emit("ci_blocked", task_id, pr_url=pr_url, attempts=ci_attempts)
        # status == "pending" → nothing to do yet

    return now


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
            # Sprint A / Issue #12: killpg catches bash wrappers and forks.
            if now - entry.started_at_mono > entry.timeout_s + 10.0:
                _killpg_or_pid(pid, signal.SIGKILL)
            continue
        if now - entry.started_at_mono > entry.timeout_s:
            entry.timed_out = True
            log.warning(
                "task %s exceeded timeout %.1fs — sending SIGTERM to pid %d",
                entry.task.id,
                entry.timeout_s,
                pid,
            )
            _killpg_or_pid(pid, signal.SIGTERM)
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
    defer_reasons: dict[str, str] | None = None,
    wm: "WorktreeManager | None" = None,
    base_branch: str = "main",
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
                # Sprint A / Issue #11: human-readable log line for the operator.
                # `reason` from can_dispatch looks like:
                #   "codex over threshold: 367,000 tokens used, cap 240,000 (60% of 400,000)"
                # We reformat it compactly with the tokens_used/token_budget and ETA.
                used_tokens, budget_tokens = _extract_usage_from_reason(reason)
                eta = _format_reset_eta(reset_at)
                log.info(
                    "deferred %s (%s): window %s/%s, resets in ~%s",
                    task.id,
                    route.backend,
                    _format_tokens_short(used_tokens) if used_tokens else "?",
                    _format_tokens_short(budget_tokens) if budget_tokens else "?",
                    eta,
                )
                # Populate the defer-reason side channel so callers (future
                # `orch status`) can distinguish blocked-by-budget from
                # not-ready-yet. Overwritten each tick — always current.
                if defer_reasons is not None:
                    defer_reasons[task.id] = f"blocked-by-budget:{route.backend}"
            return False
        # If we DID pass the gate this tick, clear any stale deferral marker
        # so `orch status` reflects the fresh state on the next look.
        if defer_reasons is not None:
            defer_reasons.pop(task.id, None)

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

    # ---- worktree creation (Sprint F-2) -----------------------------------
    # When worktree_mode is on, each task gets its own isolated git branch.
    # effective_cwd is the path passed to backend.spawn(); all other uses of
    # cwd (call_task_start, render_prompt, error paths) keep the main root.
    effective_cwd = cwd
    if wm is not None:
        try:
            effective_cwd = wm.create(task.id, base_branch)
        except Exception as exc:  # noqa: BLE001
            log.error("worktree create failed for %s: %s", task.id, exc)
            gsem.release()
            psem[route.backend].release()
            release_task_lock(task_lock_fd)
            try:
                call_task_block(
                    task.id, f"worktree create failed: {exc}", route.cli_model,
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
                reason=f"worktree create failed: {exc}",
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
            cwd=effective_cwd,
        )
    except (FileNotFoundError, OSError) as exc:
        log.exception("spawn failed for %s: %s", task.id, exc)
        gsem.release()
        psem[route.backend].release()
        release_task_lock(task_lock_fd)
        if wm is not None:
            wm.remove(task.id)
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
        worktree_path=effective_cwd if wm is not None else None,
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
    defer_reasons: dict[str, str] | None = None,
    wm: "WorktreeManager | None" = None,
    base_branch: str = "main",
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
                defer_reasons=defer_reasons,
                wm=wm,
                base_branch=base_branch,
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
            defer_reasons=defer_reasons,
            wm=wm,
            base_branch=base_branch,
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
    wm: "WorktreeManager | None" = None,
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
            wm=wm,
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


def _extract_usage_from_reason(reason: str) -> tuple[int, int]:
    """Parse `(tokens_used, token_budget)` out of the budget gate's reason string.

    The reason string produced by `BudgetGate.can_dispatch` looks like:
        "codex over threshold: 367,000 tokens used, cap 240,000 (60% of 400,000)"

    We yank the first two comma-formatted integers as `(used, budget)` from
    that pattern. Returns `(0, 0)` on any parse failure — the caller falls
    back to `"?"` in the log line rather than crashing.
    """
    if not reason:
        return 0, 0
    # First integer: `tokens_used`. Third integer (after "of"): `token_budget`.
    matches = re.findall(r"([\d,]+)", reason)
    used = 0
    budget_val = 0
    if matches:
        try:
            used = int(matches[0].replace(",", ""))
        except ValueError:
            used = 0
    # The `token_budget` is the last number in the parenthesized "of NNN" tail.
    m = re.search(r"of\s+([\d,]+)", reason)
    if m:
        try:
            budget_val = int(m.group(1).replace(",", ""))
        except ValueError:
            budget_val = 0
    return used, budget_val


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
                estimated=bool(getattr(result, "estimated", False)),
            )
        )
    except Exception as exc:  # noqa: BLE001 — spend logging is best-effort
        log.warning("spend log write failed for %s: %s", entry.task.id, exc)


# ---- main() -------------------------------------------------------------


def _run_atomize_subcommand(argv: list[str]) -> int:
    """Handle `orch atomize [flags]` — delegate to `orchestrator.atomize.main`.

    The atomizer parses markdown specs and merges them into `tasks.json`.
    Read-only by default (shows a diff); pass `--apply` to actually write.
    """
    from orchestrator.atomize import main as atomize_main

    return atomize_main(argv)


def _run_task_status_subcommand(argv: list[str]) -> int:
    """Handle `orch task-status <task_id> <status> [--author X] [--note Y]`.

    Sprint B: single-writer helper the shell scripts (`task-{start,finish,
    block,reset}.sh`) shell into. Preserves the single-writer contract while
    routing through the active backend (file → tasks.json via legacy scripts;
    sqlite → tasks_runtime row).

    Exit codes:
      0  success
      1  config / project layout error
      2  unknown task id
      3  illegal transition
    """
    p = argparse.ArgumentParser(
        prog="orch task-status",
        description=(
            "Single-writer helper. Updates task status via the active "
            "state backend. Called by scripts/task-*.sh."
        ),
    )
    p.add_argument("task_id", metavar="TASK_ID")
    p.add_argument(
        "status",
        choices=["backlog", "todo", "in-progress", "done", "blocked"],
    )
    p.add_argument("--author", default="orch", help="Comment author (default: orch)")
    p.add_argument("--note", default="", help="Free-form comment appended to the task")
    p.add_argument("--project-root", default=None, metavar="PATH",
                   help="Project root; default = cwd. Env fallback: ORCH_PROJECT_ROOT.")
    p.add_argument("--project-id", default=None, metavar="ID",
                   help="Project id override. Env fallback: ORCH_PROJECT_ID.")
    p.add_argument("--config", default=".orchestrator/config.yaml",
                   help="Path to config.yaml (default: .orchestrator/config.yaml)")
    args = p.parse_args(argv)

    paths = resolve_project_paths(
        project_root_arg=args.project_root,
        project_id_arg=args.project_id,
        config_arg=args.config,
    )
    try:
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except (FileNotFoundError, Exception) as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    from orchestrator.state import get_backend

    backend = get_backend(paths, cfg)
    # For sqlite, ensure bootstrap has been done so tasks_runtime is seeded.
    try:
        from orchestrator.state import load_tasks

        tasks = load_tasks(paths.tasks_json)
        backend.bootstrap(tasks)
    except Exception:  # noqa: BLE001 — bootstrap is best-effort here
        pass

    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    try:
        backend.set_task_status(
            args.task_id,
            args.status,  # type: ignore[arg-type]
            author=args.author,
            note=args.note,
            ts=ts,
        )
    except KeyError:
        print(f"unknown task id: {args.task_id!r}", file=sys.stderr)
        return 2
    except ValueError as exc:
        print(f"illegal transition: {exc}", file=sys.stderr)
        return 3
    return 0


_SUBCOMMANDS = (
    "init",
    "atomize",
    "dashboard",
    "task-status",
    "migrate",
    "reset",
    "stop",
    "status",
    "tasks",
    "events",
    "logs",
    "graph",
    "doctor",
    "validate",
    "findings",
)


def _print_subcommand_list() -> int:
    """`orch list` — one-line summary of every subcommand."""
    print("orch — Task orchestrator for tasks.json DAG dispatch")
    print()
    print("Commands:")
    print("  orch [FLAGS]              Run the main dispatch loop (default)")
    print("  orch init PATH [FLAGS]    Scaffold a new orch project at PATH")
    print("  orch atomize [FLAGS]      Convert markdown specs → tasks.json (diff-first)")
    print("  orch dashboard [FLAGS]    Launch the read-only FastAPI dashboard")
    print("  orch task-status ID STATUS  Single-writer helper for scripts/task-*.sh")
    print("  orch migrate [FLAGS]      Migrate state/ JSONL → sqlite (backup + rollback)")
    print("  orch reset [FLAGS]        Revert stuck in-progress tasks to todo")
    print("  orch stop [FLAGS]         Signal a running orch to drain and exit")
    print("  orch status [FLAGS]       Project status table (human or --json)")
    print("  orch tasks [FLAGS]        Thin task listing (ID · STATUS · BACKEND · DEPS · PHASE)")
    print("  orch events ID [FLAGS]    Tail events for one task (--tail N / --json)")
    print("  orch logs ID [FLAGS]      Tail the per-task log file (default: --tail 200)")
    print("  orch graph [FLAGS]        Emit a self-contained HTML/SVG plan graph")
    print("  orch doctor [FLAGS]       Read-only preflight (backends, scripts, jq, state)")
    print("  orch validate [FLAGS]     Static graph validation (schema, deps, cycles, routes)")
    print("  orch findings <verb>      Dogfooding loop (capture/list/review/publish/dismiss)")
    print("  orch arch <verb>          Architecture diagram generation via archify skill")
    print("  orch upgrade [--check]    Self-update to the latest GitHub release")
    print("  orch list                 Print this list")
    print("  orch --help               Full main-loop help (flags, exit codes)")
    print()
    print("Docs: https://github.com/hectorcanaimero/orch/blob/main/docs/MANUAL.md")
    return 0


def _run_upgrade_subcommand(argv: list[str]) -> int:
    """Handle `orch upgrade [--check]` — self-update via pipx or pip."""
    import importlib.metadata
    import shutil
    import urllib.request
    from argparse import ArgumentParser

    ap = ArgumentParser(prog="orch upgrade", description="Upgrade orch to the latest release.")
    ap.add_argument("--check", action="store_true", help="Only report whether an upgrade is available; don't install.")
    opts = ap.parse_args(argv)

    # Current installed version.
    try:
        current = importlib.metadata.version("orchestrator")
    except importlib.metadata.PackageNotFoundError:
        print("error: cannot determine installed version", file=sys.stderr)
        return 1

    # Latest release from GitHub.
    api_url = "https://api.github.com/repos/hectorcanaimero/orch/releases/latest"
    try:
        req = urllib.request.Request(api_url, headers={"Accept": "application/vnd.github+json", "User-Agent": "orch-upgrade"})
        with urllib.request.urlopen(req, timeout=10) as resp:
            import json as _json
            data = _json.loads(resp.read())
    except Exception as exc:
        print(f"error: could not reach GitHub ({exc})", file=sys.stderr)
        return 1

    latest_tag = data.get("tag_name", "").lstrip("v")
    assets = data.get("assets", [])
    wheel_url = next((a["browser_download_url"] for a in assets if a["name"].endswith(".whl")), None)

    if not latest_tag or not wheel_url:
        print("error: could not parse latest release metadata", file=sys.stderr)
        return 1

    from packaging.version import Version  # noqa: PLC0415 — stdlib-like, always available via pip

    try:
        is_newer = Version(latest_tag) > Version(current)
    except Exception:
        is_newer = latest_tag != current

    if not is_newer:
        print(f"orch {current} is already the latest release.")
        return 0

    print(f"New release available: {current} → {latest_tag}")

    if opts.check:
        print(f"  Install with: orch upgrade")
        return 0

    # Detect install method: pipx venv path contains "pipx/venvs".
    using_pipx = "pipx" in sys.executable

    if using_pipx:
        pipx_bin = shutil.which("pipx")
        if not pipx_bin:
            print("error: pipx not found in PATH", file=sys.stderr)
            return 1
        print(f"Upgrading via pipx → {wheel_url}")
        result = subprocess.run([pipx_bin, "install", "--force", wheel_url])
    else:
        pip_bin = shutil.which("pip3") or shutil.which("pip")
        if not pip_bin:
            print("error: pip not found in PATH", file=sys.stderr)
            return 1
        print(f"Upgrading via pip → {wheel_url}")
        result = subprocess.run([pip_bin, "install", "--force-reinstall", wheel_url])

    if result.returncode == 0:
        print(f"\norch upgraded to {latest_tag}.")
    else:
        print("error: upgrade command failed", file=sys.stderr)
    return result.returncode


def _run_init_subcommand(argv: list[str]) -> int:
    """Handle `orch init [PATH] [flags]` — routes to batch or interactive.

    Actual parser + wizard implementation live in `orchestrator/init_cmd.py`
    (`run_init_cli`) so the wizard logic stays alongside the scaffolder.
    """
    from orchestrator.init_cmd import run_init_cli

    return run_init_cli(argv)


def _run_reset_subcommand(argv: list[str]) -> int:
    """Handle `orch reset [--requeue] [--only GLOB] [--project-root PATH]`.

    Sprint A / Issue #7: manual escape hatch for stuck in-progress tasks.

    Modes:
        (default)   dry-run — print what WOULD be reverted; touches nothing.
        --requeue   nuclear — revert ALL in-progress tasks to todo (respects
                    --only if set). No PID check; assumes the operator KNOWS
                    the tasks are stuck.
        --only GLOB restrict by fnmatch on task-id (both dry-run and requeue).

    Returns 0 on success (even when dry-run finds zero candidates), 1 on I/O
    error, 2 on invalid project layout (same as main-loop exit conventions).
    """
    import fnmatch as _fnmatch

    p = argparse.ArgumentParser(
        prog="orch reset",
        description=(
            "Revert stuck in-progress tasks to todo. Dry-run by default; "
            "pass --requeue to actually mutate tasks.json."
        ),
    )
    p.add_argument(
        "--requeue",
        action="store_true",
        help="Actually revert (default is dry-run — print candidates only).",
    )
    p.add_argument(
        "--only",
        default=None,
        metavar="GLOB",
        help="Restrict to task ids matching this fnmatch glob.",
    )
    p.add_argument(
        "--project-root",
        default=None,
        metavar="PATH",
        help="Project root. Env fallback: ORCH_PROJECT_ROOT. Default: cwd.",
    )
    p.add_argument(
        "--project-id",
        default=None,
        metavar="ID",
        help="Project id override. Env fallback: ORCH_PROJECT_ID.",
    )
    p.add_argument(
        "--config",
        default=".orchestrator/config.yaml",
        help="Path to config.yaml (default: .orchestrator/config.yaml)",
    )
    args = p.parse_args(argv)

    try:
        paths = resolve_project_paths(
            project_root_arg=args.project_root,
            project_id_arg=args.project_id,
            config_arg=args.config,
        )
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    # Read tasks.json fresh — read-only.
    try:
        with open(paths.tasks_json, encoding="utf-8") as fh:
            raw = json.load(fh)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"could not read {paths.tasks_json}: {exc}", file=sys.stderr)
        return 1

    rows = raw.get("tasks") if isinstance(raw, dict) else raw
    if not isinstance(rows, list):
        print(f"malformed tasks.json at {paths.tasks_json}", file=sys.stderr)
        return 1

    candidates: list[str] = []
    for row in rows:
        if not isinstance(row, dict):
            continue
        if row.get("status") != "in-progress":
            continue
        tid = row.get("id")
        if not isinstance(tid, str):
            continue
        if args.only and not _fnmatch.fnmatchcase(tid, args.only):
            continue
        candidates.append(tid)

    if not candidates:
        print("no in-progress tasks to reset.")
        return 0

    if not args.requeue:
        # Dry-run mode — print candidates, exit 0.
        print(f"would revert {len(candidates)} in-progress task(s) to todo:")
        for tid in candidates:
            print(f"  - {tid}")
        print("\nRe-run with --requeue to actually mutate tasks.json.")
        return 0

    # Actual mutation — route through the active backend so both file and
    # sqlite backends see the reset consistently (single-writer contract).
    # For file backend, `reset_task` internally shells into task-reset.sh
    # (or the _reset_task_in_place Python fallback). For sqlite it hits
    # tasks_runtime directly.
    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    from orchestrator.state import get_backend, load_tasks

    backend = get_backend(paths, cfg)
    try:
        backend.bootstrap(load_tasks(paths.tasks_json))
    except Exception:  # noqa: BLE001 — best-effort bootstrap
        pass

    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    reverted: list[str] = []
    for tid in candidates:
        try:
            backend.reset_task(tid, author="orch-reset", note="reset", ts=ts)
            reverted.append(tid)
        except Exception as exc:  # noqa: BLE001
            print(f"reset failed for {tid}: {exc}", file=sys.stderr)

    print(f"reverted {len(reverted)} task(s) to todo:")
    for tid in reverted:
        print(f"  - {tid}")
    return 0


def _run_stop_subcommand(argv: list[str]) -> int:
    """Handle `orch stop [--project-root PATH] [--grace SECONDS]`.

    Sprint A / Issue #12: locate the newest running orch instance for the
    project (via `state/run-*.json` -> parent_pid) and send it SIGTERM so
    it drains cleanly. If the process doesn't exit within the grace window
    we exit with 1 (operator decides whether to escalate — we don't
    SIGKILL from here).

    Exit codes:
        0 — orch was signaled and exited within grace.
        1 — no running orch found for the project OR still alive after grace.
        2 — invalid project layout.
    """
    p = argparse.ArgumentParser(
        prog="orch stop",
        description=(
            "Send SIGTERM to the running orch for a project and wait for it "
            "to drain. Does NOT SIGKILL — operator decides if force is needed."
        ),
    )
    p.add_argument(
        "--project-root",
        default=None,
        metavar="PATH",
        help="Project root. Env fallback: ORCH_PROJECT_ROOT. Default: cwd.",
    )
    p.add_argument(
        "--project-id",
        default=None,
        metavar="ID",
        help="Project id override. Env fallback: ORCH_PROJECT_ID.",
    )
    p.add_argument(
        "--config",
        default=".orchestrator/config.yaml",
        help="Path to config.yaml (default: .orchestrator/config.yaml)",
    )
    p.add_argument(
        "--grace",
        type=float,
        default=30.0,
        metavar="SECONDS",
        help="Seconds to wait for orch to exit after SIGTERM (default: 30).",
    )
    args = p.parse_args(argv)

    try:
        paths = resolve_project_paths(
            project_root_arg=args.project_root,
            project_id_arg=args.project_id,
            config_arg=args.config,
        )
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    # Find the newest run file that reports a live parent_pid.
    state_dir = paths.state_dir
    candidates: list[tuple[float, int, Path]] = []
    for run_path in state_dir.glob("run-*.json"):
        if run_path.suffix != ".json":
            continue
        try:
            with open(run_path, encoding="utf-8") as fh:
                raw = json.load(fh)
        except (OSError, json.JSONDecodeError):
            continue
        pid = int(raw.get("parent_pid", 0) or 0)
        if pid <= 0:
            continue
        # Alive check via os.kill(pid, 0).
        try:
            os.kill(pid, 0)
        except (ProcessLookupError, PermissionError):
            # ProcessLookupError → dead; PermissionError → foreign, skip.
            continue
        except OSError:
            continue
        candidates.append((run_path.stat().st_mtime, pid, run_path))

    if not candidates:
        print("no running orch found for this project.", file=sys.stderr)
        return 1

    candidates.sort(reverse=True)  # newest first
    _, target_pid, run_path = candidates[0]
    print(f"signaling orch pid={target_pid} (run={run_path.name})...")
    try:
        os.kill(target_pid, signal.SIGTERM)
    except (ProcessLookupError, PermissionError) as exc:
        print(f"could not signal pid {target_pid}: {exc}", file=sys.stderr)
        return 1

    # Wait for exit within grace window.
    deadline = time.monotonic() + max(0.1, args.grace)
    while time.monotonic() < deadline:
        try:
            os.kill(target_pid, 0)
        except ProcessLookupError:
            print(f"orch pid={target_pid} exited cleanly.")
            return 0
        except (PermissionError, OSError):
            # Foreign / can't probe — assume exit and return success.
            print(f"orch pid={target_pid} no longer signalable (assumed exited).")
            return 0
        time.sleep(0.2)

    print(
        f"orch pid={target_pid} still alive after {args.grace:.0f}s grace. "
        "Send SIGKILL manually if needed (`kill -9 {pid}`).",
        file=sys.stderr,
    )
    return 1


_STATUS_CHOICES = ("backlog", "todo", "in-progress", "done", "blocked", "blocked-by-budget")


def _parse_status_list(raw: str | None) -> set[str] | None:
    """Turn `--status todo,done` into {"todo", "done"}. None → None."""
    if not raw:
        return None
    parts = [p.strip() for p in raw.split(",") if p.strip()]
    if not parts:
        return None
    return set(parts)


def _resolve_paths_from_argv(args: argparse.Namespace) -> ProjectPaths:
    """Shared path-resolution helper for the read-only observability subcommands.

    Every command in Sprint C carries the same `--project-root` /
    `--project-id` / `--config` triple; extract the wiring here.
    """
    return resolve_project_paths(
        project_root_arg=args.project_root,
        project_id_arg=args.project_id,
        config_arg=args.config,
    )


def _add_common_project_flags(p: argparse.ArgumentParser) -> None:
    p.add_argument("--project-root", default=None, metavar="PATH",
                   help="Project root; default = cwd. Env fallback: ORCH_PROJECT_ROOT.")
    p.add_argument("--project-id", default=None, metavar="ID",
                   help="Project id override. Env fallback: ORCH_PROJECT_ID.")
    p.add_argument("--config", default=".orchestrator/config.yaml",
                   help="Path to config.yaml (default: .orchestrator/config.yaml)")


def _run_task_subcommand(args: list[str]) -> int:
    """Dispatch `orch task <subcommand>`.

    Sub-commands: set
    """
    if not args or args[0] not in ("set",):
        print("usage: orch task <subcommand>")
        print("subcommands: set")
        return 1
    if args[0] == "set":
        return _run_task_set_subcommand(args[1:])
    return 1


def _run_task_set_subcommand(argv: list[str]) -> int:
    """Handle `orch task set --id TASK [--model MODEL] [--status STATUS] [--backend BACKEND] [--milestone MILESTONE]`.

    Writes directly to the SQLite `tasks_definition` and `tasks_runtime` tables.
    Requires state.backend = sqlite.

    Exit codes:
      0 — all mutations applied
      1 — missing flags / file backend
      3 — illegal status transition (ValueError from SqliteBackend)
    """
    p = argparse.ArgumentParser(prog="orch task set")
    p.add_argument("--id", required=True, dest="task_id",
                   help="Task ID, e.g. F1.1.T3")
    p.add_argument("--model", default=None,
                   help="Override the model for this task in tasks_definition.")
    p.add_argument("--status", default=None,
                   help="Set the task status (e.g. done, in-progress, blocked).")
    p.add_argument("--backend", default=None, dest="task_backend",
                   help="Override the backend for this task in tasks_definition.")
    p.add_argument(
        "--milestone",
        default=None,
        dest="task_milestone",
        help="Assign the task to a milestone ID.",
    )
    _add_common_project_flags(p)
    parsed = p.parse_args(argv)

    if not any([parsed.model, parsed.status, parsed.task_backend, parsed.task_milestone]):
        print(
            "error: at least one of --model, --status, --backend, --milestone is required",
            file=sys.stderr,
        )
        return 1

    try:
        paths = _resolve_paths_from_argv(parsed)
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    from orchestrator.state import get_backend
    from orchestrator.state.sqlite_backend import SqliteBackend

    backend = get_backend(paths, cfg)
    if not isinstance(backend, SqliteBackend):
        print(
            "error: orch task set requires state.backend = sqlite",
            file=sys.stderr,
        )
        return 1

    try:
        if parsed.model:
            backend.set_task_model(parsed.task_id, parsed.model)
            print(f"task {parsed.task_id}: model -> {parsed.model}")

        if parsed.task_backend:
            backend.set_task_backend(parsed.task_id, parsed.task_backend)
            print(f"task {parsed.task_id}: backend -> {parsed.task_backend}")

        if parsed.task_milestone:
            backend.set_task_milestone(parsed.task_id, parsed.task_milestone)
            print(f"task {parsed.task_id}: milestone -> {parsed.task_milestone}")

        if parsed.status:
            import datetime as _dt
            ts = _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
            backend.set_task_status(
                parsed.task_id,
                parsed.status,  # type: ignore[arg-type]
                author="operator",
                note="manual set via orch task set",
                ts=ts,
            )
            print(f"task {parsed.task_id}: status -> {parsed.status}")

    except KeyError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 3

    return 0


def _run_status_subcommand(argv: list[str]) -> int:
    """Handle `orch status [--json] [--only GLOB] [--status STATUSES]`.

    Prints a project status table (rich when available) or a compact JSON
    object when `--json` is passed. Reads the aggregate via
    `observability.build_status_snapshot`.

    Exit codes:
      0 — snapshot rendered
      1 — project layout invalid / config load failed
    """
    p = argparse.ArgumentParser(
        prog="orch status",
        description="Project status: tasks · costs · last events · run summary.",
    )
    p.add_argument("--json", action="store_true", help="Emit the raw snapshot as JSON.")
    p.add_argument("--only", default=None, metavar="GLOB",
                   help="Restrict task rows to ids matching this fnmatch glob.")
    p.add_argument("--status", default=None, metavar="LIST",
                   help="Comma-separated status filter (e.g. `todo,in-progress`).")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        paths = _resolve_paths_from_argv(args)
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    from orchestrator.observability import build_status_snapshot

    snapshot = build_status_snapshot(
        paths,
        cfg,
        only=args.only,
        status_filter=_parse_status_list(args.status),
    )

    if args.json:
        print(json.dumps(snapshot, default=str, separators=(",", ":")))
        return 0

    _render_status_table(snapshot)
    return 0


def _render_status_table(snap: dict[str, Any]) -> None:
    """Human renderer for `orch status`. Rich when available, plain otherwise."""
    project = snap.get("project", {}) or {}
    totals = snap.get("totals", {}) or {}
    cost = snap.get("cost", {}) or {}
    latest = snap.get("latest_run") or {}

    total_n = totals.get("_total", 0)
    non_meta = {k: v for k, v in totals.items() if not k.startswith("_")}
    totals_str = ", ".join(f"{k}={v}" for k, v in sorted(non_meta.items())) or "0 tasks"
    run_line = (
        f"run {latest.get('run_id', '—')[:8]} · {latest.get('status', '—')} · "
        f"in_flight={latest.get('in_flight_count', 0)}"
    ) if latest else "no runs yet"

    header = (
        f"Project {project.get('project_id', '?')} · backend={project.get('backend', '?')} "
        f"· {total_n} tasks ({totals_str}) · ${cost.get('project_total_usd', 0):.4f} · {run_line}"
    )

    rows = snap.get("tasks", []) or []
    if _HAVE_RICH:
        table = Table(title=header)
        table.add_column("ID", style="cyan")
        table.add_column("STATUS")
        table.add_column("BACKEND/MODEL", style="green")
        table.add_column("LAST EVENT")
        table.add_column("COST", justify="right")
        table.add_column("PHASE", justify="right")
        for r in rows:
            table.add_row(
                str(r.get("id", "")),
                str(r.get("status", "")),
                f"{r.get('backend', '?')}/{r.get('cli_model', '?')}",
                r.get("last_event_human") or "—",
                f"${float(r.get('cost_usd', 0.0)):.4f}",
                str(r.get("phase", "")),
            )
        _console.print(table)  # type: ignore[union-attr]
    else:  # pragma: no cover — rich is a hard dep in practice
        print(header)
        print(f"  {'ID':<20} {'STATUS':<16} {'BACKEND/MODEL':<28} {'LAST EVENT':<28} {'COST':>10}  PHASE")
        for r in rows:
            print(
                f"  {str(r.get('id','')):<20} {str(r.get('status','')):<16} "
                f"{(r.get('backend','?')+'/'+r.get('cli_model','?')):<28} "
                f"{str(r.get('last_event_human') or '—'):<28} "
                f"${float(r.get('cost_usd', 0.0)):>9.4f}  {r.get('phase','')}"
            )


def _run_tasks_subcommand(argv: list[str]) -> int:
    """Handle `orch tasks [--status STATUSES] [--only GLOB] [--json]`.

    Thinner cousin of `orch status`: columns are ID · STATUS · BACKEND/MODEL
    · DEPS · PHASE. Same aggregation pipeline; different renderer.
    """
    p = argparse.ArgumentParser(
        prog="orch tasks",
        description="List every task with status + routing + deps.",
    )
    p.add_argument("--json", action="store_true", help="Emit as JSON array of rows.")
    p.add_argument("--only", default=None, metavar="GLOB",
                   help="Restrict task rows to ids matching this fnmatch glob.")
    p.add_argument("--status", default=None, metavar="LIST",
                   help="Comma-separated status filter (e.g. `todo,in-progress`).")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        paths = _resolve_paths_from_argv(args)
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    from orchestrator.observability import build_status_snapshot

    snapshot = build_status_snapshot(
        paths,
        cfg,
        only=args.only,
        status_filter=_parse_status_list(args.status),
    )
    rows = snapshot.get("tasks", []) or []

    if args.json:
        # Emit only the trimmed set of fields relevant to `orch tasks`.
        trimmed = [
            {
                "id": r["id"],
                "status": r["status"],
                "backend": r["backend"],
                "cli_model": r["cli_model"],
                "phase": r["phase"],
                "dependencies": r["dependencies"],
            }
            for r in rows
        ]
        print(json.dumps(trimmed, default=str, separators=(",", ":")))
        return 0

    if _HAVE_RICH:
        table = Table(title=f"Tasks ({len(rows)} shown)")
        table.add_column("ID", style="cyan")
        table.add_column("STATUS")
        table.add_column("BACKEND/MODEL", style="green")
        table.add_column("DEPS")
        table.add_column("PHASE", justify="right")
        for r in rows:
            deps = ", ".join(r.get("dependencies", []) or []) or "—"
            table.add_row(
                str(r.get("id", "")),
                str(r.get("status", "")),
                f"{r.get('backend', '?')}/{r.get('cli_model', '?')}",
                deps,
                str(r.get("phase", "")),
            )
        _console.print(table)  # type: ignore[union-attr]
    else:  # pragma: no cover
        print(f"Tasks ({len(rows)} shown)")
        for r in rows:
            deps = ",".join(r.get("dependencies", []) or []) or "-"
            print(
                f"  {r.get('id',''):<20} {r.get('status',''):<16} "
                f"{r.get('backend','?')}/{r.get('cli_model','?'):<20} "
                f"deps=[{deps}] phase={r.get('phase','')}"
            )
    return 0


def _run_events_subcommand(argv: list[str]) -> int:
    """Handle `orch events <task-id> [--tail N] [--json] [--run RUN_ID]`.

    Streams the recorded event rows for a single task via
    `StateBackend.iter_events(task_id=..., limit=..., run_id=...)`. The
    file backend has no monotonic id → newest-N tail is buffered client
    side; the sqlite backend uses ORDER BY id DESC LIMIT N (see commit 1).

    Exit codes:
      0 — rows emitted (including the empty case).
      1 — config / layout error.
    """
    p = argparse.ArgumentParser(
        prog="orch events",
        description="Tail event rows for one task (default: last 20).",
    )
    p.add_argument("task_id", metavar="TASK_ID")
    p.add_argument("--tail", type=int, default=20, metavar="N",
                   help="Cap at the last N events (default: 20; 0 = all).")
    p.add_argument("--run", default=None, metavar="RUN_ID",
                   help="Restrict to one run id (default: every recorded run).")
    p.add_argument("--json", action="store_true",
                   help="Emit as a JSON array (one row per event).")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        paths = _resolve_paths_from_argv(args)
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    from orchestrator.state import get_backend

    backend = get_backend(paths, cfg)
    limit = None if args.tail <= 0 else int(args.tail)
    rows = list(
        backend.iter_events(run_id=args.run, task_id=args.task_id, limit=limit)
    )

    if args.json:
        print(json.dumps(rows, default=str, separators=(",", ":")))
        return 0

    if not rows:
        print(f"(no events for task {args.task_id!r})")
        return 0

    if _HAVE_RICH:
        table = Table(title=f"Events for {args.task_id} (showing {len(rows)})")
        table.add_column("TS", style="cyan")
        table.add_column("EVENT")
        table.add_column("BACKEND")
        table.add_column("RUN")
        table.add_column("EXTRA")
        for r in rows:
            extra = r.get("extra") or {}
            extra_str = ", ".join(f"{k}={v}" for k, v in sorted(extra.items()))
            table.add_row(
                str(r.get("ts", "")),
                str(r.get("event_type", "")),
                str(r.get("backend", "")),
                str(r.get("run_id", ""))[:8],
                extra_str,
            )
        _console.print(table)  # type: ignore[union-attr]
    else:  # pragma: no cover
        print(f"Events for {args.task_id} (showing {len(rows)})")
        for r in rows:
            print(
                f"  {r.get('ts','')}  {r.get('event_type','')}  "
                f"backend={r.get('backend','')}  run={str(r.get('run_id',''))[:8]}"
            )
    return 0


def _run_logs_subcommand(argv: list[str]) -> int:
    """Handle `orch logs <task-id> [--tail N] [--all]`.

    Reads the raw per-task log file at
        `<state_dir>/logs/<task-id>.log`
    and streams the tail. This is a plain file — backend-agnostic on
    purpose (log content is written by the dispatched CLI subprocess,
    not the state backend).

    Exit codes:
      0 — bytes emitted.
      1 — config / layout error.
      2 — log file does not exist.
    """
    p = argparse.ArgumentParser(
        prog="orch logs",
        description="Tail the per-task log file (default: last 200 lines).",
    )
    p.add_argument("task_id", metavar="TASK_ID")
    p.add_argument("--tail", type=int, default=200, metavar="N",
                   help="Cap at the last N lines (default: 200).")
    p.add_argument("--all", action="store_true",
                   help="Print the entire log (ignores --tail).")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        paths = _resolve_paths_from_argv(args)
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    log_path = paths.state_dir / "logs" / f"{args.task_id}.log"
    if not log_path.exists():
        print(
            f"no log file for task {args.task_id!r} at {log_path}",
            file=sys.stderr,
        )
        return 2

    try:
        with open(log_path, "r", encoding="utf-8", errors="replace") as fh:
            lines = fh.readlines()
    except OSError as exc:
        print(f"could not read {log_path}: {exc}", file=sys.stderr)
        return 1

    if args.all or args.tail <= 0:
        tail = lines
    else:
        tail = lines[-int(args.tail):]

    # Preserve trailing newlines from the file — sys.stdout.write handles both.
    for line in tail:
        sys.stdout.write(line)
    return 0


def _run_graph_subcommand(argv: list[str]) -> int:
    """Handle `orch graph [--out plan.html] [--only GLOB] [--open]`.

    Renders a self-contained HTML/SVG plan graph using the shared
    `observability.build_status_snapshot` aggregator + the pure
    `orchestrator.graph.build_html` renderer. No CDN, no external CSS —
    everything is inline so the output works offline.

    Ceiling: ~500 tasks (documented in the module docstring). Beyond that,
    pass `--only`.

    Exit codes:
      0 — HTML file written.
      1 — config / layout error / write failure.
    """
    p = argparse.ArgumentParser(
        prog="orch graph",
        description=(
            "Render a self-contained HTML/SVG snapshot of the project DAG. "
            "Zero external dependencies. Ceiling ~500 tasks — use --only "
            "for larger projects."
        ),
    )
    p.add_argument("--out", default="plan.html", metavar="PATH",
                   help="Destination file (default: plan.html in cwd).")
    p.add_argument("--only", default=None, metavar="GLOB",
                   help="fnmatch glob restricting nodes rendered.")
    p.add_argument("--open", action="store_true",
                   help="Open the HTML in the default browser after writing.")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        paths = _resolve_paths_from_argv(args)
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    from orchestrator.graph import build_html
    from orchestrator.observability import build_status_snapshot

    snapshot = build_status_snapshot(paths, cfg, only=args.only)
    html = build_html(snapshot)
    out_path = Path(args.out)
    try:
        out_path.write_text(html, encoding="utf-8")
    except OSError as exc:
        print(f"could not write {out_path}: {exc}", file=sys.stderr)
        return 1
    print(f"wrote {out_path} ({len(snapshot.get('tasks') or [])} nodes)")

    if args.open:
        try:
            import webbrowser

            webbrowser.open(out_path.resolve().as_uri())
        except Exception as exc:  # noqa: BLE001
            log.warning("could not open browser: %s", exc)
    return 0


def _run_doctor_subcommand(argv: list[str]) -> int:
    """Handle `orch doctor [--json] [--only CHECK]` — read-only preflight.

    Sprint D / Issue #9. Runs every environment probe (backends installed,
    scripts executable, jq present, state dir writable, sqlite DB opens
    when applicable) and prints a checklist. All checks are pure reads —
    the doctor never mutates anything on disk.

    Exit codes: 0 all ok, 1 warnings only, 2 any error.
    """
    p = argparse.ArgumentParser(
        prog="orch doctor",
        description=(
            "Read-only preflight: verify backends, scripts, jq, config, "
            "and state backend are ready to dispatch."
        ),
    )
    p.add_argument("--json", action="store_true",
                   help="Emit the full check report as JSON on stdout.")
    p.add_argument("--only", default=None, metavar="CHECK",
                   help="Restrict to checks matching this substring (e.g. 'backend').")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        paths = _resolve_paths_from_argv(args)
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    # Doctor should still work even when tasks.json/scripts are missing —
    # that's precisely the failure mode operators call it for. The shared
    # builder handles that: it swallows a config parse failure into a
    # `config.parse` check and keeps probing the environment.
    from orchestrator.doctor import build_doctor_report

    payload = build_doctor_report(
        paths, config_loader=_load_config, only=args.only,
    )
    exit_code = int(payload["exit_code"])

    if args.json:
        print(json.dumps(payload, default=str, separators=(",", ":")))
        return exit_code

    _render_doctor_report(payload)
    return exit_code


def _resolve_budgets_path(paths: "ProjectPaths", cfg: dict[str, Any]) -> Path | None:
    """Mirror the resolution rules from the main loop for `budgets.yaml`.

    Order of precedence:
        1. Absolute path in config → use as-is.
        2. Relative → try project_root first, then orchestrator/ subdir.
    """
    raw = cfg.get("budgets_config") or "budgets.yaml"
    candidate = Path(raw)
    if candidate.is_absolute():
        return candidate
    for root in (paths.project_root, paths.project_root / "orchestrator"):
        p = (root / candidate).resolve()
        if p.exists():
            return p
    # Return the most likely path so callers get a helpful "not present" note.
    return (paths.project_root / candidate).resolve()


def _resolve_sqlite_path(paths: "ProjectPaths", cfg: dict[str, Any]) -> Path | None:
    """Resolve `state.sqlite_path` (relative to state_dir, or absolute)."""
    raw = ((cfg.get("state") or {}) or {}).get("sqlite_path")
    if not raw:
        return paths.state_dir / "orch.db"
    candidate = Path(raw)
    if candidate.is_absolute():
        return candidate
    return (paths.state_dir / candidate).resolve()


def _render_doctor_report(payload: dict[str, Any]) -> None:
    """Human renderer for `orch doctor`. Rich when available."""
    symbols = {"ok": "✓", "warn": "⚠", "error": "✗", "skip": "○"}
    colors = {"ok": "green", "warn": "yellow", "error": "red", "skip": "dim"}

    header = (
        f"orch doctor · project={payload['project']['id']} · backend={payload['backend']}"
    )
    summary = payload["summary"]
    summary_line = (
        f"{summary['ok']} ok · {summary['warn']} warn · "
        f"{summary['error']} error · {summary['skip']} skip"
    )

    if _HAVE_RICH:
        table = Table(title=header, show_lines=False)
        table.add_column("", width=2)
        table.add_column("CHECK", style="cyan")
        table.add_column("STATUS")
        table.add_column("DETAIL")
        for c in payload["checks"]:
            status = c["status"]
            sym = symbols.get(status, "?")
            color = colors.get(status, "white")
            table.add_row(
                f"[{color}]{sym}[/{color}]",
                c["name"],
                f"[{color}]{status}[/{color}]",
                c["detail"],
            )
        _console.print(table)  # type: ignore[union-attr]
        _console.print(f"[bold]{summary_line}[/bold]")  # type: ignore[union-attr]
        # Remediation hints — grouped so noise stays low when things are fine.
        remediation = [c for c in payload["checks"] if c.get("remediation")]
        if remediation:
            _console.print()  # type: ignore[union-attr]
            _console.print("[bold]Remediation:[/bold]")  # type: ignore[union-attr]
            for c in remediation:
                _console.print(f"  {c['name']}: {c['remediation']}")  # type: ignore[union-attr]
    else:  # pragma: no cover — rich is a hard dep in practice
        print(header)
        print("=" * len(header))
        for c in payload["checks"]:
            sym = symbols.get(c["status"], "?")
            print(f"  {sym} {c['name']:<32} {c['status']:<6} {c['detail']}")
        print(summary_line)
        for c in payload["checks"]:
            if c.get("remediation"):
                print(f"  -> {c['name']}: {c['remediation']}")


def _run_validate_subcommand(argv: list[str]) -> int:
    """Handle `orch validate [--json] [--files]` — static graph validation.

    Sprint D / Issue #10. Runs every whole-graph validator (schema, deps,
    cycles, unresolved routes, undersized budgets, config shape) WITHOUT
    dispatching any subprocesses or probing external binaries — the doctor
    covers runtime probes.

    Exit codes: 0 clean, 1 warnings only, 2 any error.
    """
    p = argparse.ArgumentParser(
        prog="orch validate",
        description=(
            "Static validation of tasks.json + config: schema, dependencies, "
            "cycles, route resolution, budget preset sanity. No I/O beyond "
            "reading the config files."
        ),
    )
    p.add_argument("--json", action="store_true",
                   help="Emit the full validation report as JSON on stdout.")
    p.add_argument("--files", action="store_true",
                   help="Also check that parent dirs of each task.files[] entry exist + are writable.")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        paths = _resolve_paths_from_argv(args)
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    from orchestrator import preflight

    errors: list[preflight.ValidationError] = []

    # Config shape — always runs.
    errors.extend(preflight.validate_config_shape(paths.config_yaml))

    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except Exception as exc:  # noqa: BLE001
        cfg = {}
        errors.append(
            preflight.ValidationError(
                task_id=None,
                field="config",
                kind="schema.config",
                message=f"config load failed: {exc}",
            )
        )

    # Router keys — load defensively so downstream validators can still run.
    router_keys: list[str] = []
    try:
        from orchestrator.router import load_router

        router = load_router(paths.router_yaml)
        router_keys = list(router.keys())
    except FileNotFoundError:
        errors.append(
            preflight.ValidationError(
                task_id=None,
                field="model_router.yaml",
                kind="router.missing",
                message=f"router file not found: {paths.router_yaml}",
                remediation="Run `orch init` to scaffold, or create the file by hand.",
            )
        )
    except Exception as exc:  # noqa: BLE001
        errors.append(
            preflight.ValidationError(
                task_id=None,
                field="model_router.yaml",
                kind="router.parse",
                message=f"router load failed: {exc}",
            )
        )

    # Tasks — same defensive load.
    tasks_list: list = []
    try:
        from orchestrator.state import load_tasks

        tasks_list = load_tasks(paths.tasks_json)
    except FileNotFoundError:
        errors.append(
            preflight.ValidationError(
                task_id=None,
                field="tasks.json",
                kind="tasks.missing",
                message=f"tasks file not found: {paths.tasks_json}",
                remediation="Run `orch init` to scaffold, or create tasks.json by hand.",
            )
        )
    except Exception as exc:  # noqa: BLE001
        errors.append(
            preflight.ValidationError(
                task_id=None,
                field="tasks.json",
                kind="tasks.parse",
                message=f"tasks load failed: {exc}",
            )
        )

    if tasks_list:
        errors.extend(preflight.validate_graph(
            tasks_list,
            router_keys=router_keys if router_keys else None,
        ))
        if args.files:
            errors.extend(preflight.validate_files_writable(tasks_list, paths.project_root))

    # Preset sanity — only meaningful if budgets file + preset resolved.
    budgets_path = _resolve_budgets_path(paths, cfg)
    budgets_preset = cfg.get("budgets_preset")
    typical = int(cfg.get("typical_dispatch_tokens", 200_000) or 200_000)
    errors.extend(preflight.validate_preset_sanity(
        budgets_path if budgets_path and budgets_path.exists() else None,
        budgets_preset,
        typical,
    ))

    exit_code = preflight.exit_code_for_errors(errors)

    # Group errors by kind for the summary payload.
    summary: dict[str, int] = {}
    for e in errors:
        summary[e.kind] = summary.get(e.kind, 0) + 1

    payload = {
        "project": {
            "id": paths.project_id,
            "root": str(paths.project_root),
        },
        "errors": [e.as_json() for e in errors],
        "summary": {
            "total": len(errors),
            "by_kind": summary,
            "errors": sum(1 for e in errors if e.severity == "error"),
            "warnings": sum(1 for e in errors if e.severity == "warn"),
        },
        "exit_code": exit_code,
    }

    if args.json:
        print(json.dumps(payload, default=str, separators=(",", ":")))
        return exit_code

    _render_validate_report(payload)
    return exit_code


def _render_validate_report(payload: dict[str, Any]) -> None:
    """Human renderer for `orch validate`. Rich when available."""
    errors = payload["errors"]
    header = f"orch validate · project={payload['project']['id']}"
    summary = payload["summary"]
    summary_line = (
        f"{summary['errors']} error(s) · {summary['warnings']} warning(s) "
        f"· {summary['total']} total"
    )

    if not errors:
        if _HAVE_RICH:
            _console.print(f"[bold green]✓ {header}: no issues found[/bold green]")  # type: ignore[union-attr]
        else:  # pragma: no cover
            print(f"✓ {header}: no issues found")
        return

    # Group by kind so the operator sees related rows together.
    by_kind: dict[str, list[dict[str, Any]]] = {}
    for e in errors:
        by_kind.setdefault(e["kind"], []).append(e)

    if _HAVE_RICH:
        _console.print(f"[bold]{header}[/bold]")  # type: ignore[union-attr]
        for kind, rows in sorted(by_kind.items()):
            table = Table(title=f"{kind} ({len(rows)})")
            table.add_column("TASK", style="cyan")
            table.add_column("FIELD")
            table.add_column("MESSAGE")
            for r in rows:
                color = "red" if r["severity"] == "error" else "yellow"
                table.add_row(
                    r.get("task_id") or "-",
                    r.get("field", ""),
                    f"[{color}]{r['message']}[/{color}]",
                )
            _console.print(table)  # type: ignore[union-attr]
        _console.print(f"[bold]{summary_line}[/bold]")  # type: ignore[union-attr]
        remediation = [r for r in errors if r.get("remediation")]
        if remediation:
            _console.print()  # type: ignore[union-attr]
            _console.print("[bold]Remediation:[/bold]")  # type: ignore[union-attr]
            for r in remediation:
                tag = r.get("task_id") or "-"
                _console.print(f"  [{tag}] {r['kind']}: {r['remediation']}")  # type: ignore[union-attr]
    else:  # pragma: no cover
        print(header)
        for kind, rows in sorted(by_kind.items()):
            print(f"\n[{kind}] ({len(rows)}):")
            for r in rows:
                print(f"  - {r.get('task_id') or '-'} · {r['field']}: {r['message']}")
        print(summary_line)


def _run_findings_subcommand(argv: list[str]) -> int:
    """Handle `orch findings <verb> ...` — dogfooding loop dispatcher (Sprint E-1).

    Sub-verbs: capture · list · review · publish · dismiss. Each sub-verb
    has its own argparser; this shim only routes.

    Exit codes are per-verb; see individual `_findings_*` functions.
    """
    if not argv or argv[0] in {"-h", "--help"}:
        return _findings_print_help()
    verb = argv[0]
    rest = argv[1:]
    if verb == "capture":
        return _findings_capture_cli(rest)
    if verb == "list":
        return _findings_list_cli(rest)
    if verb == "review":
        return _findings_review_cli(rest)
    if verb == "publish":
        return _findings_publish_cli(rest)
    if verb == "dismiss":
        return _findings_dismiss_cli(rest)
    print(f"unknown findings verb: {verb!r}", file=sys.stderr)
    _findings_print_help()
    return 2


def _findings_print_help() -> int:
    print("orch findings — dogfooding loop (Sprint E-1)")
    print()
    print("Verbs:")
    print("  orch findings capture --type T --about (orch|project) --summary S ...")
    print("  orch findings list [--status S] [--about A] [--json]")
    print("  orch findings review ID [--json]")
    print("  orch findings publish ID [--repo REPO] [--dry-run] [--yes] [--force]")
    print("  orch findings dismiss ID --reason REASON")
    return 0


def _findings_backend(argv: list[str]) -> tuple[Any, dict[str, Any], Any]:
    """Common bootstrap: parse project flags, load config, resolve backend.

    Returns `(backend, cfg, paths)`. Raises SystemExit via argparse on bad
    args. `argv` is the sub-verb's argv already parsed elsewhere — this only
    reads --project-root/--project-id/--config, so callers stash them in a
    small extra parser instance and pass the residue through.
    """
    from orchestrator.state import get_backend

    p = argparse.ArgumentParser(add_help=False)
    _add_common_project_flags(p)
    args, _rest = p.parse_known_args(argv)
    paths = _resolve_paths_from_argv(args)
    cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    backend = get_backend(paths, cfg)
    return backend, cfg, paths


def _findings_capture_cli(argv: list[str]) -> int:
    """`orch findings capture` — persist a new finding. Exit codes: 0/2/3."""
    from orchestrator import findings as f_mod

    p = argparse.ArgumentParser(
        prog="orch findings capture",
        description="Capture a finding (bug/fix/feature) locally.",
    )
    p.add_argument("--type", dest="ftype", required=True,
                   choices=sorted(f_mod._ALLOWED_TYPES))
    p.add_argument("--about", required=True, choices=sorted(f_mod._ALLOWED_ABOUT))
    p.add_argument("--summary", required=True,
                   help="Single-line human title. Required.")
    p.add_argument("--evidence", default="",
                   help="Multi-line evidence (file:line / logs / repro).")
    p.add_argument("--confidence", default="medium",
                   choices=sorted(f_mod._ALLOWED_CONFIDENCE))
    p.add_argument("--author", default="agent",
                   help="Who captured this. Default: agent")
    p.add_argument("--json", action="store_true",
                   help="Emit the captured finding as JSON.")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        backend, _cfg, _paths = _findings_backend(argv)
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    try:
        finding = f_mod.capture(
            backend,
            finding_type=args.ftype,
            about=args.about,
            summary=args.summary,
            evidence=args.evidence,
            confidence=args.confidence,
            author=args.author,
        )
    except f_mod.DuplicateFindingError as exc:
        existing = exc.existing
        print(
            f"duplicate: same finding already captured as {existing.id} "
            f"(status={existing.status})",
            file=sys.stderr,
        )
        return 2
    except f_mod.FindingValidationError as exc:
        print(f"validation error: {exc}", file=sys.stderr)
        return 3

    if args.json:
        from dataclasses import asdict as _asdict
        print(json.dumps(_asdict(finding), separators=(",", ":")))
    else:
        print(f"captured {finding.id}")
        print(f"  type       {finding.type}")
        print(f"  about      {finding.about}")
        print(f"  confidence {finding.confidence}")
        print(f"  summary    {finding.summary}")
    return 0


def _findings_list_cli(argv: list[str]) -> int:
    """`orch findings list` — human table or JSON array."""
    from orchestrator import findings as f_mod

    p = argparse.ArgumentParser(
        prog="orch findings list",
        description="List findings for this project.",
    )
    p.add_argument("--status", default=None,
                   choices=["pending", "published", "dismissed", "duplicate"])
    p.add_argument("--about", default=None,
                   choices=sorted(f_mod._ALLOWED_ABOUT))
    p.add_argument("--json", action="store_true", help="Emit as JSON array.")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        backend, _cfg, _paths = _findings_backend(argv)
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    rows = f_mod.list_findings(backend, status=args.status, about=args.about)

    if args.json:
        from dataclasses import asdict as _asdict
        print(json.dumps([_asdict(r) for r in rows], separators=(",", ":")))
        return 0

    if not rows:
        print("(no findings)")
        return 0
    header = f"{'ID':<12} {'TYPE':<8} {'ABOUT':<8} {'CONF':<7} {'STATUS':<11} SUMMARY"
    print(header)
    for r in rows:
        short_id = r.id[:8]
        summary = r.summary if len(r.summary) <= 60 else r.summary[:57] + "..."
        print(
            f"{short_id:<12} {r.type:<8} {r.about:<8} {r.confidence:<7} "
            f"{r.status:<11} {summary}"
        )
    return 0


def _findings_review_cli(argv: list[str]) -> int:
    """`orch findings review ID [--json]` — show a finding + GitHub dedup search.

    Exit codes:
      0 — rendered (even when GitHub search returns nothing)
      2 — finding id not found
    """
    from dataclasses import asdict as _asdict

    from orchestrator import findings as f_mod

    p = argparse.ArgumentParser(
        prog="orch findings review",
        description="Show a finding and search the target repo for possible duplicates.",
    )
    p.add_argument("finding_id",
                   help="Finding id (prefix ok — as long as it's unique).")
    p.add_argument("--repo", default=None,
                   help="Override repo for the dedup search (default from config).")
    p.add_argument("--json", action="store_true",
                   help="Emit `{finding, matches}` as a single JSON object.")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        backend, cfg, _paths = _findings_backend(argv)
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    finding = _find_finding_by_prefix(backend, args.finding_id)
    if finding is None:
        print(f"finding not found: {args.finding_id!r}", file=sys.stderr)
        return 2

    repo = args.repo or (cfg.get("findings", {}) or {}).get(
        "publish_repo", f_mod.DEFAULT_REPO
    )
    matches = f_mod.search_github_issues_for_duplicate(finding.summary, repo)

    if args.json:
        print(json.dumps({
            "finding": _asdict(finding),
            "repo": repo,
            "matches": matches,
        }, separators=(",", ":")))
        return 0

    print(f"Finding {finding.id}")
    print(f"  created_at  {finding.created_at}")
    print(f"  type        {finding.type}")
    print(f"  about       {finding.about}")
    print(f"  confidence  {finding.confidence}")
    print(f"  status      {finding.status}")
    if finding.published_url:
        print(f"  published   {finding.published_url}")
    print(f"  author      {finding.author}")
    print(f"  summary     {finding.summary}")
    print()
    print("Evidence:")
    if finding.evidence.strip():
        for ln in finding.evidence.splitlines():
            print(f"  {ln}")
    else:
        print("  (none)")
    print()
    print(f"GitHub dedup search on {repo}:")
    if not matches:
        print("  (no matching open issues)")
    else:
        for m in matches:
            overlap = m.get("overlap", 0.0)
            print(
                f"  #{m.get('number')} · overlap={overlap:.2f} · "
                f"{m.get('html_url')}"
            )
            print(f"      {m.get('title', '')}")
    return 0


def _find_finding_by_prefix(backend: Any, prefix: str) -> Any | None:
    """Lookup helper — exact id match wins; else unique prefix match."""
    exact = backend.get_finding(prefix)
    if exact is not None:
        return exact
    matches = [f for f in backend.iter_findings() if f.id.startswith(prefix)]
    if len(matches) == 1:
        return matches[0]
    return None


def _findings_publish_cli(argv: list[str]) -> int:
    """`orch findings publish ID` — main publish flow with guardrails.

    Exit codes (per Sprint E-1 spec):
      0   published (or dry-run rendered)
      1   rate-limited or a dedup match warned/blocked
      2   refused (about=project OR confidence below floor)
      130 user cancelled at the TTY consent prompt
    """
    from orchestrator import findings as f_mod

    p = argparse.ArgumentParser(
        prog="orch findings publish",
        description=(
            "Publish a finding to a GitHub issue via the gh CLI. Runs every "
            "guardrail (classification, confidence, rate limit, dedup) before "
            "creating the issue."
        ),
    )
    p.add_argument("finding_id", help="Finding id (prefix ok if unique).")
    p.add_argument("--repo", default=None,
                   help="Override target repo (default from config.yaml).")
    p.add_argument("--dry-run", action="store_true",
                   help="Show what would be published without creating an issue.")
    p.add_argument("--yes", action="store_true",
                   help="Skip the final TTY consent prompt (guardrails still run).")
    p.add_argument("--force", action="store_true",
                   help="Override confidence / dedup guardrails (still respects "
                        "about=project and rate limit).")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        backend, cfg, _paths = _findings_backend(argv)
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    finding = _find_finding_by_prefix(backend, args.finding_id)
    if finding is None:
        print(f"finding not found: {args.finding_id!r}", file=sys.stderr)
        return 2

    fcfg = cfg.get("findings", {}) or {}
    repo = args.repo or fcfg.get("publish_repo", f_mod.DEFAULT_REPO)
    label = fcfg.get("label", f_mod.DEFAULT_LABEL)
    rate = int(fcfg.get("publish_rate_limit_per_hour", f_mod.DEFAULT_RATE_LIMIT))
    min_conf = fcfg.get(
        "min_publish_confidence", f_mod.DEFAULT_MIN_PUBLISH_CONFIDENCE
    )

    # Build the consent callable. `--yes` short-circuits to True. Otherwise
    # we ask via stdin when it's a TTY; on a non-TTY we refuse for safety.
    def _consent(f) -> bool:
        if args.yes:
            return True
        if not sys.stdin.isatty():
            print(
                "publish requires TTY confirmation (or --yes on non-interactive "
                "shells)",
                file=sys.stderr,
            )
            return False
        try:
            resp = input(
                f"Publish {f.id[:8]} to {repo}? [y/N] "
            ).strip().lower()
        except (EOFError, KeyboardInterrupt):
            return False
        return resp in {"y", "yes"}

    try:
        result = f_mod.publish(
            backend,
            finding.id,
            repo=repo,
            label=label,
            rate_limit_per_hour=rate,
            min_confidence=min_conf,
            dry_run=args.dry_run,
            force=args.force,
            confirm=None if args.dry_run else _consent,
        )
    except f_mod.RateLimitExceeded as exc:
        print(f"rate limit: {exc}", file=sys.stderr)
        return 1
    except f_mod.DuplicateIssueFound as exc:
        print(f"duplicate: {exc}", file=sys.stderr)
        return 1
    except f_mod.PublishRefusedError as exc:
        msg = str(exc)
        if "cancelled" in msg.lower():
            print(msg, file=sys.stderr)
            return 130
        # about=project or below min confidence.
        if "about=project" in msg or "confidence" in msg:
            print(f"refused: {msg}", file=sys.stderr)
            return 2
        print(f"publish failed: {msg}", file=sys.stderr)
        return 1

    status = result["status"]
    if status == "dry_run":
        print("[dry-run] would publish:")
        print(f"  finding {finding.id}")
        print(f"  repo    {repo}")
        print(f"  label   {label}")
        matches = result.get("dedup_matches") or []
        if matches:
            print(f"  dedup matches ({len(matches)}):")
            for m in matches:
                print(
                    f"    #{m.get('number')} · overlap={m.get('overlap'):.2f} "
                    f"· {m.get('html_url')}"
                )
        rl = result.get("rate_limit", {})
        print(f"  rate    {rl.get('count')}/{rl.get('limit')} in last hour")
        return 0
    if status == "already_published":
        print(f"already published: {result['published_url']}")
        return 0
    print(f"published: {result['published_url']}")
    return 0


def _findings_dismiss_cli(argv: list[str]) -> int:
    """`orch findings dismiss ID --reason REASON`."""
    from orchestrator import findings as f_mod

    p = argparse.ArgumentParser(
        prog="orch findings dismiss",
        description="Mark a finding as dismissed with a required reason.",
    )
    p.add_argument("finding_id", help="Finding id (prefix ok if unique).")
    p.add_argument("--reason", required=True,
                   help="Why this finding is being dismissed. Required.")
    _add_common_project_flags(p)
    args = p.parse_args(argv)

    try:
        backend, _cfg, _paths = _findings_backend(argv)
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    finding = _find_finding_by_prefix(backend, args.finding_id)
    if finding is None:
        print(f"finding not found: {args.finding_id!r}", file=sys.stderr)
        return 2

    try:
        f_mod.dismiss(backend, finding.id, args.reason)
    except f_mod.PublishRefusedError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    print(f"dismissed {finding.id}")
    return 0


def _run_dashboard_subcommand(argv: list[str]) -> int:
    """Handle `orch dashboard [flags]` — separate parser to keep the main
    loop's argparser unchanged.

    We do NOT reuse `_build_argparser()` here on purpose: the dashboard has
    a completely different flag set (--port, --host, --reload) and mixing
    them would leak into the main-loop `--help`. Both parsers accept
    `--project-root`/`--project-id` so path resolution stays identical.

    Sprint E-2 additions:
      --profile   operator|stakeholder|both
      --token     stakeholder shared secret (also settable via
                  ORCH_DASHBOARD_TOKEN env var — flag wins when both set).

    `--profile stakeholder` REQUIRES a token (via flag, env, or config).
    Missing token → CLI errors out BEFORE uvicorn boots so the user sees
    the misconfiguration immediately instead of hitting a live 401 wall.
    """
    p = argparse.ArgumentParser(
        prog="orch dashboard",
        description="Local read-only dashboard over tasks.json / events / spend.",
    )
    # Both default to None on purpose — we can't distinguish "user typed
    # --port 7420" from "user passed nothing" if the default were literal.
    # The resolution ladder below (flag > env > dashboard.yaml server.* >
    # hardcoded default) picks the final values after parsing.
    p.add_argument("--port", type=int, default=None,
                   help="Bind port (default: 7420 or dashboard.yaml server.port)")
    p.add_argument("--host", default=None,
                   help="Bind host (default: 127.0.0.1 or dashboard.yaml server.host)")
    p.add_argument("--project-root", default=None, metavar="PATH",
                   help="Project root; default = cwd. Env fallback: ORCH_PROJECT_ROOT.")
    p.add_argument("--project-id", default=None, metavar="ID",
                   help="Project id override. Env fallback: ORCH_PROJECT_ID.")
    p.add_argument("--config", default=".orchestrator/config.yaml",
                   help="Path to config.yaml (default: .orchestrator/config.yaml)")
    p.add_argument("--reload", action="store_true",
                   help="Enable uvicorn --reload (dev only; watches for code changes).")
    p.add_argument("--profile", default=None,
                   choices=["operator", "stakeholder", "both"],
                   help="Dashboard profile. Default: operator (or value from "
                        "config.yaml's `dashboard.profile`).")
    p.add_argument("--token", default=None, metavar="TOKEN",
                   help="Shared secret for the stakeholder profile. Sets "
                        "ORCH_DASHBOARD_TOKEN in-process. Env fallback: "
                        "ORCH_DASHBOARD_TOKEN.")
    args = p.parse_args(argv)

    # `--token` on the CLI wins over env. Set env before we import the
    # server so `DashboardConfig.load(config_yaml=...)` picks it up when
    # it reads `os.environ`.
    if args.token:
        os.environ["ORCH_DASHBOARD_TOKEN"] = args.token

    # Fail fast if the caller asked for the stakeholder profile without
    # supplying a token via any channel (flag > env > config). We validate
    # BEFORE uvicorn boots so the operator sees the error immediately.
    if args.profile == "stakeholder":
        # Config-provided token is honored later; check env/flag first.
        env_token = os.environ.get("ORCH_DASHBOARD_TOKEN")
        if not env_token:
            # Peek at config.yaml to see if a token is defined there.
            token_in_config = _peek_dashboard_token_in_config(args.config)
            if not token_in_config:
                print(
                    "error: --profile stakeholder requires a token. Provide one via:\n"
                    "  --token TOKEN                   (CLI flag)\n"
                    "  ORCH_DASHBOARD_TOKEN=TOKEN      (env var)\n"
                    "  dashboard.token: TOKEN          (config.yaml)",
                    file=sys.stderr,
                )
                return 2

    try:
        from orchestrator.dashboard.server import run as dashboard_run
    except ImportError as exc:
        print(f"dashboard dependencies missing: {exc}", file=sys.stderr)
        print("Install: fastapi >= 0.115, uvicorn[standard] >= 0.30, jinja2 >= 3.1",
              file=sys.stderr)
        return 1

    # Resolve bind (port, host) precedence: CLI flag > env > dashboard.yaml
    # server.* > hardcoded default. We load the config here (best-effort)
    # so per-project sticky ports work without any flags. Bad `server:`
    # blocks fail loudly, same policy as tunnel config.
    resolved_port, resolved_host = _resolve_dashboard_bind(
        cli_port=args.port,
        cli_host=args.host,
        project_root=args.project_root,
    )

    # One-line resolution summary so operators running multiple orch
    # dashboards in parallel see immediately which port/project they got.
    from pathlib import Path
    _proj_hint = Path(args.project_root).resolve().name if args.project_root else Path.cwd().name
    print(f"Dashboard: http://{resolved_host}:{resolved_port} (project={_proj_hint})")

    return dashboard_run(
        port=resolved_port,
        host=resolved_host,
        project_root=args.project_root,
        project_id=args.project_id,
        config=args.config,
        reload=args.reload,
        profile=args.profile,
        token=args.token,
    )


def _resolve_dashboard_bind(
    *,
    cli_port: int | None,
    cli_host: str | None,
    project_root: str | None,
) -> tuple[int, str]:
    """Apply the bind precedence for `orch dashboard`.

    Order (highest wins):
        1. CLI flag (only when actually supplied — `None` means absent).
        2. Env var (`ORCH_DASHBOARD_PORT` / `ORCH_DASHBOARD_HOST`).
        3. `server.port` / `server.host` in the project `dashboard.yaml`.
        4. Hardcoded defaults: `7420` / `127.0.0.1`.

    Each channel is resolved independently — a CLI `--port` combined with
    a config-only `host` yields (flag_port, config_host).
    """
    from pathlib import Path

    HARD_PORT = 7420
    HARD_HOST = "127.0.0.1"

    # Best-effort dashboard.yaml load. If it blows up with a real
    # ConfigError (bad server block), let it propagate — the operator
    # needs to see that.
    cfg_port: int | None = None
    cfg_host: str | None = None
    root = Path(project_root).resolve() if project_root else Path.cwd()
    try:
        from orchestrator.dashboard.dashboard_config import DashboardConfig
        cfg = DashboardConfig.load(root)
        cfg_port = cfg.server_port
        cfg_host = cfg.server_host
    except ImportError:
        # Dashboard deps not installed; the caller already handles that path.
        pass

    # Port
    if cli_port is not None:
        port = cli_port
    else:
        env_port = os.environ.get("ORCH_DASHBOARD_PORT")
        if env_port:
            try:
                port = int(env_port)
                if port < 1 or port > 65535:
                    raise ValueError
            except ValueError:
                print(
                    f"warning: ORCH_DASHBOARD_PORT={env_port!r} is not a valid "
                    f"port; falling back to config/default.",
                    file=sys.stderr,
                )
                port = cfg_port if cfg_port is not None else HARD_PORT
        elif cfg_port is not None:
            port = cfg_port
        else:
            port = HARD_PORT

    # Host
    if cli_host is not None:
        host = cli_host
    else:
        env_host = os.environ.get("ORCH_DASHBOARD_HOST")
        if env_host and env_host.strip():
            host = env_host.strip()
        elif cfg_host is not None:
            host = cfg_host
        else:
            host = HARD_HOST

    return port, host


def _peek_dashboard_token_in_config(config_path: str) -> str | None:
    """Best-effort lookup of `dashboard.token` from a config.yaml on disk.

    Only used to preflight the stakeholder-mode requirement — never
    fatal if the file is missing or malformed. Returns `None` in that
    case, which triggers the error message with all three options.
    """
    from pathlib import Path
    import yaml

    p = Path(config_path)
    if not p.is_absolute():
        p = Path.cwd() / p
    if not p.exists():
        return None
    try:
        data = yaml.safe_load(p.read_text(encoding="utf-8")) or {}
    except (yaml.YAMLError, OSError):
        return None
    if not isinstance(data, dict):
        return None
    block = data.get("dashboard") or {}
    if not isinstance(block, dict):
        return None
    tok = block.get("token")
    if tok is None:
        return None
    tok = str(tok).strip()
    return tok or None


def _resolve_log_level(
    *,
    verbose: int = 0,
    quiet: bool = False,
    env_var: str | None = None,
) -> str:
    """Sprint C log-level resolution.

    Precedence (highest → lowest):
        1. `--quiet` / `--verbose` (CLI flags — explicit user intent).
        2. `ORCH_LOG_LEVEL` env var when set to a non-empty string.
        3. Code default (`INFO`).

    `verbose=1` → DEBUG (single -v). `verbose>=2` currently caps at DEBUG
    since Python's logging module has no lower level.
    """
    if quiet:
        return "ERROR"
    if verbose > 0:
        return "DEBUG"
    if env_var:
        return env_var
    return "INFO"


def main(argv: list[str] | None = None) -> int:
    """CLI entry point. Return the process exit code (FR-CLI-3)."""
    # Provisional log setup so early subcommand paths still see logging.
    # The main-loop path re-applies with `-v/-q` factored in below.
    logging.basicConfig(
        level=os.environ.get("ORCH_LOG_LEVEL") or "INFO",
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    # Subcommand routing. Every subcommand (`dashboard`, `init`, `atomize`,
    # `list`) peels off BEFORE the main-loop argparser touches argv so the
    # flag namespaces don't collide. Order-insensitive dispatch.
    incoming = sys.argv[1:] if argv is None else argv
    if incoming and incoming[0] == "list":
        return _print_subcommand_list()
    if incoming and incoming[0] == "dashboard":
        return _run_dashboard_subcommand(incoming[1:])
    if incoming and incoming[0] == "init":
        return _run_init_subcommand(incoming[1:])
    if incoming and incoming[0] == "atomize":
        return _run_atomize_subcommand(incoming[1:])
    if incoming and incoming[0] == "task-status":
        return _run_task_status_subcommand(incoming[1:])
    if incoming and incoming[0] == "task":
        return _run_task_subcommand(incoming[1:])
    if incoming and incoming[0] == "migrate":
        from orchestrator.migrate import run_migrate

        return run_migrate(incoming[1:])
    if incoming and incoming[0] == "reset":
        return _run_reset_subcommand(incoming[1:])
    if incoming and incoming[0] == "stop":
        return _run_stop_subcommand(incoming[1:])
    if incoming and incoming[0] == "status":
        return _run_status_subcommand(incoming[1:])
    if incoming and incoming[0] == "tasks":
        return _run_tasks_subcommand(incoming[1:])
    if incoming and incoming[0] == "events":
        return _run_events_subcommand(incoming[1:])
    if incoming and incoming[0] == "logs":
        return _run_logs_subcommand(incoming[1:])
    if incoming and incoming[0] == "graph":
        return _run_graph_subcommand(incoming[1:])
    if incoming and incoming[0] == "doctor":
        return _run_doctor_subcommand(incoming[1:])
    if incoming and incoming[0] == "validate":
        return _run_validate_subcommand(incoming[1:])
    if incoming and incoming[0] == "findings":
        return _run_findings_subcommand(incoming[1:])
    if incoming and incoming[0] == "arch":
        from orchestrator.arch import run_arch_cli

        return run_arch_cli(incoming[1:])
    if incoming and incoming[0] == "upgrade":
        return _run_upgrade_subcommand(incoming[1:])

    parser = _build_argparser()
    args = parser.parse_args(argv)

    # Sprint C: `--json` only means something in --dry-run mode. Enforce the
    # constraint at parse time so operators get a clear error rather than a
    # silent no-op deep in the main loop.
    if getattr(args, "json", False) and not args.dry_run:
        parser.error("--json requires --dry-run")

    # Sprint C: reapply log level now that -v/-q have been parsed. `force=True`
    # replaces the root handler installed by the provisional basicConfig above
    # (Python 3.8+ semantics — noqa on py<3.8 unaffected here).
    logging.basicConfig(
        level=_resolve_log_level(
            verbose=int(getattr(args, "verbose", 0) or 0),
            quiet=bool(getattr(args, "quiet", False)),
            env_var=os.environ.get("ORCH_LOG_LEVEL") or None,
        ),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        force=True,
    )

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

    # Layout validation: tasks.json + scripts/task-*.sh must exist at
    # project_root. The historic "cwd must be named v2/" guard was a rupies
    # remnant — removed in v0.3.1 now that orch is a standalone tool used
    # against arbitrary project names.
    try:
        paths.ensure_valid()
    except CwdViolationError as exc:
        print(str(exc), file=sys.stderr)
        print(
            "\nHint: if this is a fresh directory, run `orch init .` to "
            "scaffold the required layout.",
            file=sys.stderr,
        )
        return 2

    # cwd (variable histórica) se conserva para no reescribir todos los
    # callsites; ahora apunta al `project_root` resuelto.
    cwd = paths.project_root

    # ---- (2) config + router + tasks -------------------------------------
    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
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
    _warn_fallback_routes(router, verbose=bool(int(getattr(args, "verbose", 0) or 0)))

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

    # ---- (5b) state backend selection (Sprint B) -------------------------
    # `get_backend()` picks FileBackend or SqliteBackend based on cfg. For the
    # sqlite backend, `bootstrap(tasks)` idempotently seeds the projects +
    # tasks_runtime rows. For the file backend it's a no-op. Downstream code
    # keeps using RunFile / EventLog / SpendLog surfaces — the adapter
    # factories (`make_runfile` / `make_event_log` / `make_spend_log`) hand
    # back sqlite-backed shims when the backend is sqlite, so callsites don't
    # need to branch.
    from orchestrator.state import (  # noqa: E402
        get_backend,
        make_event_log,
        make_runfile,
        make_spend_log,
    )

    try:
        state_backend = get_backend(paths, cfg)
        state_backend.bootstrap(tasks)
    except Exception as exc:  # noqa: BLE001
        print(f"state backend init failed: {exc}", file=sys.stderr)
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
    # Sprint B: reconcile routes through the active backend so sqlite runs
    # get the same orphan-reap semantics as the file backend.
    # Sprint A / Issue #7: the file backend method internally passes
    # project_root so tasks.json rows also get reverted from in-progress
    # → todo when their PID is dead.
    reap_report = state_backend.reconcile_in_flight()
    if reap_report.get("reconciled"):
        log.info(
            "startup reconcile: reaped %d orphan(s) from %d in-flight entry(ies): %s",
            len(reap_report["reconciled"]),
            reap_report.get("checked", 0),
            reap_report["reconciled"],
        )
    if reap_report.get("reset"):
        log.info(
            "startup reconcile: reverted %d task(s) to todo: %s",
            len(reap_report["reset"]),
            reap_report["reset"],
        )

    # ---- (7) run-file (new or resumed) -----------------------------------
    try:
        if args.resume:
            run_id = args.resume
            run_path = state_dir / f"run-{run_id}.json"
            # For sqlite backend the file may not exist; the adapter loads
            # from the DB instead. Only enforce the file check on file backend.
            from orchestrator.state.sqlite_backend import SqliteBackend

            if not isinstance(state_backend, SqliteBackend) and not run_path.exists():
                print(f"resume: no run file at {run_path}", file=sys.stderr)
                if lock_fd is not None:
                    lock_fd.close()
                return 1
            run_file = make_runfile(
                state_backend, state_dir, run_id=run_id, mode=args.mode, create=False,
            )
        else:
            run_id = str(uuid.uuid4())
            # Sprint A / Issue #12: record our PID so `orch stop` can locate
            # this run later. Routes through the backend-aware factory.
            run_file = make_runfile(
                state_backend,
                state_dir,
                run_id=run_id,
                mode=args.mode,
                create=True,
                parent_pid=os.getpid(),
            )

        if lock_fd is not None:
            write_lock_holder(lock_fd, run_id=run_id, pid=os.getpid())

        event_log = make_event_log(
            state_backend, state_dir, run_id=run_id, project_id=paths.project_id,
        )
        spend_log = make_spend_log(
            state_backend, state_dir, project_id=paths.project_id,
        )

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
            count = _print_plan(
                queue, router, args.max_tasks,
                only=args.only,
                as_json=bool(getattr(args, "json", False)),
            )
            # AS-08: NO run/spend/events files touched — but we already made
            # the run file above (create) which is a side effect. Remove it
            # so dry-run stays clean.
            try:
                run_file.path.unlink(missing_ok=True)
            except OSError:
                pass
            # Also unlink the events file if we created one empty on init.
            # (Only meaningful for the file backend; sqlite backend has no
            # per-run JSONL file — the `.path` attribute is synthetic.)
            try:
                ev_path = getattr(event_log, "path", None)
                if ev_path is not None and ev_path.exists() and ev_path.stat().st_size == 0:
                    ev_path.unlink()
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
        # Sprint A / Issue #11: in-memory map of task_id → defer reason
        # (currently only "blocked-by-budget:<provider>"). Populated by
        # `_spawn_one` when the budget gate blocks; consumed by future
        # `orch status` reporting. Purely runtime — never persisted.
        defer_reasons: dict[str, str] = {}
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
        # Sprint F-2: worktree mode — opt-in via dispatch.worktree_mode in config.yaml
        _dispatch_cfg = cfg.get("dispatch") or {}
        _worktree_mode = bool(_dispatch_cfg.get("worktree_mode", False))
        _base_branch = str(_dispatch_cfg.get("base_branch", "main"))
        if _worktree_mode:
            from orchestrator.worktree import WorktreeManager
            wm: "WorktreeManager | None" = WorktreeManager(paths.project_root)
            log.info("worktree mode enabled; base_branch=%s", _base_branch)
        else:
            wm = None

        # Sprint F-4: VCS provider — only active when worktree_mode AND auto_pr are both on.
        _vcs_cfg = cfg.get("vcs") or {}
        _auto_pr = bool(_vcs_cfg.get("auto_pr", False))
        from orchestrator.state.sqlite_backend import SqliteBackend as _SqliteBackend
        _sqlite_backend: "_SqliteBackend | None" = (
            state_backend if isinstance(state_backend, _SqliteBackend) else None
        )
        if _worktree_mode and _auto_pr and _sqlite_backend is not None:
            from orchestrator.vcs import get_vcs_provider as _get_vcs_provider
            vcs_provider: "VcsProvider | None" = _get_vcs_provider(cfg)
            log.info("VCS auto-PR enabled; provider=%s", _vcs_cfg.get("provider", "github"))
        else:
            vcs_provider = None

        _install_sigint(drain, in_flight, wm=wm)

        dispatched_count = 0
        # Periodic orphan-PID sweep: piggybacks on the existing tick, no
        # new thread. 60 s is coarse enough to be near-free (one `os.kill`
        # per in-flight entry) yet responsive enough that a crashed CLI
        # doesn't linger on the dashboard past a minute.
        _RECONCILE_INTERVAL_SEC = 60.0
        last_reconcile_ts = time.monotonic()
        # Sprint F-4: CI poll timestamp (monotonic); compared against ci_poll_interval_s.
        _last_ci_check_ts: float = 0.0
        while True:
            _reap_once(
                in_flight, queue, run_file, event_log, spend_log, cfg, cwd, gsem, psem,
                retry_queue=retry_queue,
                router=router,
                task_costs=task_costs,
                wm=wm,
                vcs_provider=vcs_provider,
                state_backend=_sqlite_backend,
            )
            # Sprint F-4: CI poller — only when VCS auto-PR is active.
            if vcs_provider is not None and _sqlite_backend is not None and wm is not None:
                _last_ci_check_ts = _check_ci_once(
                    cfg=cfg,
                    state_backend=_sqlite_backend,
                    vcs_provider=vcs_provider,
                    queue=queue,
                    wm=wm,
                    in_flight=in_flight,
                    run_file=run_file,
                    event_log=event_log,
                    spend_log=spend_log,
                    gsem=gsem,
                    psem=psem,
                    retry_queue=retry_queue,
                    router=router,
                    task_costs=task_costs,
                    state_dir=state_dir,
                    cwd=cwd,
                    last_check_ts=_last_ci_check_ts,
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
                    defer_reasons=defer_reasons,
                    wm=wm,
                    base_branch=_base_branch,
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
                tick_report = state_backend.reconcile_in_flight()
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
                wm=wm,
            )
            if wm is not None:
                wm.remove_all()
            return 130

        # Sprint F-2: clean up any remaining worktrees on normal exit
        if wm is not None:
            wm.remove_all()

        # ---- Sprint C end-of-run summary --------------------------------
        # Prints unconditionally after a clean drain (both success and
        # blocked-tasks exit paths). Skipped on:
        #   - `--dry-run` (already returned above)
        #   - SIGINT drain (return 130 above)
        #   - config-error early exits (return 1 before we reach here)
        try:
            _print_run_summary(
                run_id=run_id,
                run_file=run_file,
                task_costs=task_costs,
                deferred=deferred,
                defer_reasons=defer_reasons,
            )
        except Exception as exc:  # noqa: BLE001 — summary must never crash orch
            log.warning("end-of-run summary failed: %s", exc)

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
