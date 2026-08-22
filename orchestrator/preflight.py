"""Shared preflight primitives used by `orch doctor` and `orch validate`.

Two families of pure functions live here:

    * ``CheckResult`` producers — probe the environment at runtime (backends
      installed, scripts executable, state dir writable, sqlite DB opens).
      Used by ``orch doctor``.

    * ``ValidationError`` producers — static analysis of the config/graph
      (schema, missing deps, cycles, unresolved routes, undersized budget
      presets). Used by ``orch validate``.

Design:
    - No stdout, no logging, no argparse — every function returns a list of
      immutable results the CLI layer renders. That makes them trivially
      unit-testable (see ``test_preflight.py``).
    - No new dependencies — stdlib + already-imported ``yaml``.
    - Backend probes execute in a ``ThreadPoolExecutor`` with ``max_workers=4``
      and a per-probe 5-second timeout so a hung ``opencode auth list`` can't
      wedge the whole doctor run.
    - Cycle detection uses DFS 3-coloring (white/gray/black) so it reports
      the ACTUAL cycle path, not just "there is a cycle". Downstream users
      need the trace to know which task to break.
"""

from __future__ import annotations

import concurrent.futures
import json
import os
import shutil
import stat
import subprocess
import sqlite3
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable, Literal, Sequence

import yaml

from .models import Task


CheckStatus = Literal["ok", "warn", "error", "skip"]
Severity = Literal["error", "warn"]

# Backends known to orch — probes iterate this set (plus whatever the tasks
# actually reference, for defensive coverage).
_KNOWN_BACKENDS: tuple[str, ...] = ("claude", "codex", "opencode")

# 5s cap per probe — well above cold-start for `--version` invocations but
# short enough that `orch doctor` completes in seconds even when every CLI
# is missing.
_PROBE_TIMEOUT_S = 5.0


# ---------------------------------------------------------------------------
# Result types
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class CheckResult:
    """Outcome of one runtime check.

    ``status`` semantics:
        - ``ok``   : check passed, nothing to do.
        - ``warn`` : check passed but something's off (fallback active,
                     legacy version, best-effort skip).
        - ``error``: check failed — orch will misbehave until fixed.
        - ``skip`` : check intentionally not run (feature disabled, N/A
                     for the active backend).
    """

    name: str
    status: CheckStatus
    detail: str = ""
    remediation: str | None = None

    def as_json(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "status": self.status,
            "detail": self.detail,
            "remediation": self.remediation,
        }


@dataclass(frozen=True, slots=True)
class ValidationError:
    """Outcome of one static validator.

    ``kind`` is a machine-stable tag callers can filter on (e.g.
    ``dep.cycle``, ``route.unresolved``); ``message`` is the human string.
    """

    task_id: str | None
    field: str
    kind: str
    message: str
    remediation: str | None = None
    severity: Severity = "error"

    def as_json(self) -> dict[str, Any]:
        return {
            "task_id": self.task_id,
            "field": self.field,
            "kind": self.kind,
            "message": self.message,
            "remediation": self.remediation,
            "severity": self.severity,
        }


# ---------------------------------------------------------------------------
# Runtime probes (doctor)
# ---------------------------------------------------------------------------


def _probe_version(cli: str) -> tuple[bool, str]:
    """Run ``<cli> --version`` and return ``(ok, detail)``.

    ``ok`` is True when the process exits 0 (regardless of what it printed);
    ``detail`` is the first line of stdout/stderr, trimmed.
    """
    try:
        result = subprocess.run(
            [cli, "--version"],
            capture_output=True,
            text=True,
            timeout=_PROBE_TIMEOUT_S,
        )
    except FileNotFoundError:
        return False, f"{cli}: not on PATH"
    except subprocess.TimeoutExpired:
        return False, f"{cli} --version timed out (> {_PROBE_TIMEOUT_S:.0f}s)"
    except OSError as exc:  # permissions, exec format errors, etc.
        return False, f"{cli}: {exc}"

    line = (result.stdout or result.stderr or "").splitlines()
    text = line[0].strip() if line else "(no output)"
    return result.returncode == 0, text


def _probe_opencode_auth() -> CheckResult:
    """Cheap, read-only auth check for opencode (``opencode auth list``)."""
    which = shutil.which("opencode")
    if not which:
        return CheckResult(
            name="backend.opencode.auth",
            status="skip",
            detail="opencode CLI not installed — auth probe skipped",
        )
    try:
        result = subprocess.run(
            ["opencode", "auth", "list"],
            capture_output=True,
            text=True,
            timeout=_PROBE_TIMEOUT_S,
        )
    except subprocess.TimeoutExpired:
        return CheckResult(
            name="backend.opencode.auth",
            status="warn",
            detail=f"opencode auth list timed out (> {_PROBE_TIMEOUT_S:.0f}s)",
        )
    except OSError as exc:  # noqa: BLE001
        return CheckResult(
            name="backend.opencode.auth",
            status="warn",
            detail=f"opencode auth list failed: {exc}",
        )
    if result.returncode != 0:
        return CheckResult(
            name="backend.opencode.auth",
            status="error",
            detail=(result.stderr or result.stdout or "auth list nonzero exit").strip().splitlines()[0][:200],
            remediation="Run `opencode auth login` to authenticate.",
        )
    return CheckResult(
        name="backend.opencode.auth",
        status="ok",
        detail="opencode auth list ok",
    )


def _probe_claude_auth() -> CheckResult:
    """Cheap auth check for the Claude CLI.

    No fully-cheap auth probe exists (``claude auth`` requires interaction);
    we surface the state as ``skip`` with an explanation, per product
    decision #9.
    """
    which = shutil.which("claude")
    if not which:
        return CheckResult(
            name="backend.claude.auth",
            status="skip",
            detail="claude CLI not installed — auth probe skipped",
        )
    return CheckResult(
        name="backend.claude.auth",
        status="skip",
        detail="no cheap auth probe for claude CLI — assumed ok (run `claude` interactively to verify)",
    )


def _probe_codex_auth() -> CheckResult:
    """Cheap auth check for the Codex CLI (same limitation as claude)."""
    which = shutil.which("codex")
    if not which:
        return CheckResult(
            name="backend.codex.auth",
            status="skip",
            detail="codex CLI not installed — auth probe skipped",
        )
    return CheckResult(
        name="backend.codex.auth",
        status="skip",
        detail="no cheap auth probe for codex CLI — assumed ok (run `codex` interactively to verify)",
    )


_AUTH_PROBES = {
    "opencode": _probe_opencode_auth,
    "claude": _probe_claude_auth,
    "codex": _probe_codex_auth,
}


def _referenced_backends(tasks: Iterable[Task], router: dict[str, Any] | None) -> set[str]:
    """Set of backend names actually referenced by the current tasks.json.

    Falls back to the union of ``_KNOWN_BACKENDS`` when tasks reference
    unroutable models — doctor should still probe the standard CLIs so the
    operator sees the environment picture.
    """
    used: set[str] = set()
    if router:
        for task in tasks:
            route = router.get(task.model)
            if route is not None:
                # RouteEntry or plain dict — both expose `backend`.
                backend = getattr(route, "backend", None) or route.get("backend")  # type: ignore[union-attr]
                if backend:
                    used.add(backend)
    if not used:
        return set(_KNOWN_BACKENDS)
    return used


def check_backends(
    tasks: Iterable[Task],
    router: dict[str, Any] | None,
    *,
    max_workers: int = 4,
) -> list[CheckResult]:
    """Probe every backend CLI referenced by tasks.json (or all known ones).

    Runs ``shutil.which`` + ``<cli> --version`` for each backend, plus the
    cheap read-only auth probe. Uses a ``ThreadPoolExecutor`` so a slow
    ``--version`` for one backend doesn't block the others.
    """
    tasks_list = list(tasks)
    backends = sorted(_referenced_backends(tasks_list, router))

    results: list[CheckResult] = []
    version_futures: dict[str, concurrent.futures.Future[tuple[bool, str]]] = {}
    auth_futures: dict[str, concurrent.futures.Future[CheckResult]] = {}

    with concurrent.futures.ThreadPoolExecutor(max_workers=max_workers) as pool:
        for backend in backends:
            cli = backend
            which = shutil.which(cli)
            if not which:
                results.append(
                    CheckResult(
                        name=f"backend.{backend}",
                        status="error",
                        detail=f"{cli} not on PATH",
                        remediation=f"Install {cli} and ensure it's on your $PATH.",
                    )
                )
                # Skip auth probe if the CLI itself isn't installed — the
                # per-backend probe still runs so we get a uniform `skip`
                # entry in the report.
                auth_futures[backend] = pool.submit(_AUTH_PROBES[backend])
                continue
            version_futures[backend] = pool.submit(_probe_version, cli)
            auth_futures[backend] = pool.submit(_AUTH_PROBES[backend])

        for backend, fut in version_futures.items():
            cli = backend
            try:
                ok, detail = fut.result(timeout=_PROBE_TIMEOUT_S + 1)
            except concurrent.futures.TimeoutError:
                results.append(
                    CheckResult(
                        name=f"backend.{backend}",
                        status="warn",
                        detail=f"{cli} --version hung past timeout",
                    )
                )
                continue
            path = shutil.which(cli) or "?"
            if ok:
                results.append(
                    CheckResult(
                        name=f"backend.{backend}",
                        status="ok",
                        detail=f"{detail} at {path}",
                    )
                )
            else:
                results.append(
                    CheckResult(
                        name=f"backend.{backend}",
                        status="error",
                        detail=detail,
                        remediation=f"Verify {cli} is installed correctly and on PATH.",
                    )
                )

        for backend, fut in auth_futures.items():
            try:
                results.append(fut.result(timeout=_PROBE_TIMEOUT_S + 1))
            except concurrent.futures.TimeoutError:
                results.append(
                    CheckResult(
                        name=f"backend.{backend}.auth",
                        status="warn",
                        detail="auth probe timed out",
                    )
                )

    return sorted(results, key=lambda r: r.name)


def check_config_files(
    *,
    config_yaml: Path,
    budgets_yaml: Path | None,
    router_yaml: Path,
    tasks_json: Path,
    budgets_preset: str | None = None,
) -> list[CheckResult]:
    """Parse every config file and report per-file status.

    The parsed dicts aren't returned — dedicated validators use their own
    typed loaders. This function's job is to catch YAML syntax and shape
    errors before the CLI tries to load them for real.
    """
    results: list[CheckResult] = []

    # config.yaml
    results.append(_check_yaml(config_yaml, "config.parse", top_level="mapping"))

    # budgets.yaml (optional file; when present must be a mapping with `presets`).
    if budgets_yaml is None or not budgets_yaml.exists():
        results.append(
            CheckResult(
                name="budgets.parse",
                status="skip",
                detail=f"budgets.yaml not present at {budgets_yaml} — budget gate disabled",
            )
        )
    else:
        base = _check_yaml(budgets_yaml, "budgets.parse", top_level="mapping")
        results.append(base)
        if base.status == "ok" and budgets_preset:
            try:
                data = yaml.safe_load(budgets_yaml.read_text(encoding="utf-8")) or {}
            except Exception as exc:  # noqa: BLE001 — already covered above
                data = {}
            presets = (data.get("presets") if isinstance(data, dict) else None) or {}
            if budgets_preset not in presets:
                available = ", ".join(sorted(presets)) or "<none>"
                results.append(
                    CheckResult(
                        name="budgets.preset",
                        status="error",
                        detail=(
                            f"active preset {budgets_preset!r} not found in {budgets_yaml.name}. "
                            f"Available: {available}"
                        ),
                        remediation=(
                            "Fix `budgets_preset` in config.yaml or add the missing preset to budgets.yaml."
                        ),
                    )
                )
            else:
                results.append(
                    CheckResult(
                        name="budgets.preset",
                        status="ok",
                        detail=f"preset {budgets_preset!r} present",
                    )
                )

    # router.yaml
    results.append(_check_yaml(router_yaml, "router.parse", top_level="mapping"))

    # tasks.json
    results.append(_check_json(tasks_json, "tasks.parse"))

    return results


def _check_yaml(path: Path, name: str, *, top_level: str) -> CheckResult:
    if not path.exists():
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path} does not exist",
            remediation=f"Create {path.name} (run `orch init` for a template).",
        )
    try:
        with path.open("r", encoding="utf-8") as fh:
            data = yaml.safe_load(fh)
    except yaml.YAMLError as exc:
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path.name}: YAML parse error: {exc}",
        )
    except OSError as exc:
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path.name}: cannot read: {exc}",
        )
    if top_level == "mapping" and not isinstance(data, (dict, type(None))):
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path.name}: expected top-level mapping, got {type(data).__name__}",
        )
    return CheckResult(name=name, status="ok", detail=f"{path.name} parses ok")


def _check_json(path: Path, name: str) -> CheckResult:
    if not path.exists():
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path} does not exist",
            remediation=f"Create {path.name} (run `orch init` for a template).",
        )
    try:
        with path.open("r", encoding="utf-8") as fh:
            data = json.load(fh)
    except json.JSONDecodeError as exc:
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path.name}: JSON parse error: {exc}",
        )
    except OSError as exc:
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path.name}: cannot read: {exc}",
        )
    if isinstance(data, dict):
        rows = data.get("tasks", [])
    elif isinstance(data, list):
        rows = data
    else:
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path.name}: expected dict or list, got {type(data).__name__}",
        )
    if not isinstance(rows, list):
        return CheckResult(
            name=name,
            status="error",
            detail=f"{path.name}: `tasks` is not a list ({type(rows).__name__})",
        )
    return CheckResult(
        name=name,
        status="ok",
        detail=f"{path.name} parses ok ({len(rows)} task rows)",
    )


def check_scripts(scripts_dir: Path) -> list[CheckResult]:
    """Verify `scripts/task-*.sh` are present and executable + `jq` on PATH.

    Missing scripts are ``error`` (orch cannot dispatch without them);
    missing execute bit is ``warn`` (kernel might still exec via bash).
    """
    results: list[CheckResult] = []
    required = ("task-start.sh", "task-finish.sh", "task-block.sh")
    for name in required:
        path = scripts_dir / name
        check_name = f"scripts.{name}"
        if not path.exists():
            results.append(
                CheckResult(
                    name=check_name,
                    status="error",
                    detail=f"{path} missing",
                    remediation=f"Re-scaffold with `orch init --force`, or copy from the orch package templates.",
                )
            )
            continue
        mode = path.stat().st_mode
        if not (mode & (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)):
            results.append(
                CheckResult(
                    name=check_name,
                    status="warn",
                    detail=f"{path} present but not executable",
                    remediation=f"chmod +x {path}",
                )
            )
        else:
            results.append(
                CheckResult(
                    name=check_name,
                    status="ok",
                    detail=f"{path.name} present + executable",
                )
            )

    jq_path = shutil.which("jq")
    if jq_path:
        results.append(
            CheckResult(
                name="jq.present",
                status="ok",
                detail=f"jq at {jq_path}",
            )
        )
    else:
        results.append(
            CheckResult(
                name="jq.present",
                status="error",
                detail="jq not on PATH — task-*.sh scripts require it",
                remediation="Install jq (`brew install jq` / `apt install jq`).",
            )
        )
    return results


def check_state_backend(
    *,
    state_dir: Path,
    backend: str,
    sqlite_path: Path | None = None,
    expected_schema_version: int | None = None,
) -> list[CheckResult]:
    """Verify the state directory is writable and (sqlite only) the DB opens."""
    results: list[CheckResult] = []

    # Directory writability. `mkdir(parents=True, exist_ok=True)` is safe: the
    # doctor is idempotent by design.
    try:
        state_dir.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        results.append(
            CheckResult(
                name="state.dir.writable",
                status="error",
                detail=f"cannot create {state_dir}: {exc}",
                remediation=f"chmod the parent so orch can create {state_dir}.",
            )
        )
        return results

    if not os.access(state_dir, os.W_OK):
        results.append(
            CheckResult(
                name="state.dir.writable",
                status="error",
                detail=f"{state_dir} is not writable",
                remediation=f"chmod u+w {state_dir}",
            )
        )
    else:
        results.append(
            CheckResult(
                name="state.dir.writable",
                status="ok",
                detail=f"{state_dir} writable",
            )
        )

    if backend != "sqlite":
        results.append(
            CheckResult(
                name="state.db.accessible",
                status="skip",
                detail=f"backend={backend} — sqlite DB check not applicable",
            )
        )
        return results

    db_path = sqlite_path or (state_dir / "orch.db")
    try:
        conn = sqlite3.connect(str(db_path))
    except sqlite3.Error as exc:
        results.append(
            CheckResult(
                name="state.db.accessible",
                status="error",
                detail=f"cannot open {db_path}: {exc}",
                remediation=f"Delete {db_path} or run `orch migrate` to re-create.",
            )
        )
        return results
    try:
        try:
            cur = conn.execute("PRAGMA user_version")
            row = cur.fetchone()
            version = int(row[0]) if row else 0
        except sqlite3.Error as exc:
            results.append(
                CheckResult(
                    name="state.db.accessible",
                    status="error",
                    detail=f"{db_path}: PRAGMA user_version failed: {exc}",
                )
            )
            return results
    finally:
        conn.close()

    if expected_schema_version is not None and version != expected_schema_version:
        results.append(
            CheckResult(
                name="state.db.accessible",
                status="warn",
                detail=(
                    f"{db_path}: schema_version={version} (expected {expected_schema_version})"
                ),
                remediation="Run `orch migrate` to bring the DB up to the current schema.",
            )
        )
    else:
        results.append(
            CheckResult(
                name="state.db.accessible",
                status="ok",
                detail=f"{db_path}: opens ok (schema_version={version})",
            )
        )
    return results


# ---------------------------------------------------------------------------
# Tunnel checks (Sprint E-5, TUN-11)
# ---------------------------------------------------------------------------


def check_tunnel(dashboard_yaml: Path) -> list[CheckResult]:
    """Two checks: `tunnel.config` (parse dashboard.yaml → tunnel section)
    and `tunnel.binary` (canonical provider binary on PATH when enabled).

    Absent `dashboard.yaml` OR absent `tunnel:` block → both checks PASS
    (feature disabled by default). Parse errors from `parse_tunnel_section`
    surface as `tunnel.config` FAIL. Missing binary with `enabled:true`
    surfaces as `tunnel.binary` FAIL; missing binary while disabled is a
    WARN (users may be prepping config before install).
    """
    results: list[CheckResult] = []

    # tunnel.config — parse the section (or observe its absence).
    if not dashboard_yaml.exists():
        results.append(
            CheckResult(
                name="tunnel.config",
                status="ok",
                detail=f"{dashboard_yaml.name} not present — tunnel disabled",
            )
        )
        results.append(
            CheckResult(
                name="tunnel.binary",
                status="ok",
                detail="tunnel disabled — provider binary not required",
            )
        )
        return results

    try:
        raw_doc = yaml.safe_load(dashboard_yaml.read_text(encoding="utf-8")) or {}
    except (yaml.YAMLError, OSError) as exc:
        results.append(
            CheckResult(
                name="tunnel.config",
                status="error",
                detail=f"{dashboard_yaml.name}: cannot read: {exc}",
                remediation=f"Fix YAML syntax in {dashboard_yaml}.",
            )
        )
        results.append(
            CheckResult(
                name="tunnel.binary",
                status="skip",
                detail="tunnel.config failed — binary check skipped",
            )
        )
        return results

    raw_section = raw_doc.get("tunnel") if isinstance(raw_doc, dict) else None

    # Lazy import — the tunnel package is only pulled in when needed so
    # environments without the feature stay lean.
    from orchestrator.dashboard.dashboard_config import (
        ConfigError,
        parse_tunnel_section,
    )
    from orchestrator.dashboard.tunnel.providers import resolve

    try:
        tcfg = parse_tunnel_section(raw_section)
    except ConfigError as exc:
        results.append(
            CheckResult(
                name="tunnel.config",
                status="error",
                detail=str(exc),
                remediation=(
                    f"Fix the offending key in {dashboard_yaml.name} tunnel section."
                ),
            )
        )
        results.append(
            CheckResult(
                name="tunnel.binary",
                status="skip",
                detail="tunnel.config failed — binary check skipped",
            )
        )
        return results

    if raw_section is None:
        results.append(
            CheckResult(
                name="tunnel.config",
                status="ok",
                detail=f"{dashboard_yaml.name}: no tunnel section — feature disabled",
            )
        )
    else:
        results.append(
            CheckResult(
                name="tunnel.config",
                status="ok",
                detail=(
                    f"tunnel section valid (provider={tcfg.provider}, "
                    f"enabled={tcfg.enabled})"
                ),
            )
        )

    # tunnel.binary — resolve the canonical command from provider allowlist,
    # not the config field, so an operator can't hide a missing binary by
    # overriding `command` (the allowlist forbids that anyway, but keep the
    # doctor consistent with the runtime gate).
    spec = resolve(tcfg.provider)
    binary = spec.command
    binary_path = shutil.which(binary)
    if binary_path:
        results.append(
            CheckResult(
                name="tunnel.binary",
                status="ok",
                detail=f"{binary} at {binary_path}",
            )
        )
    elif tcfg.enabled:
        results.append(
            CheckResult(
                name="tunnel.binary",
                status="error",
                detail=f"{binary} not on PATH; tunnel.enabled is true",
                remediation=(
                    f"Install {binary} (`brew install {binary}` / "
                    f"`apt install {binary}`)."
                ),
            )
        )
    else:
        results.append(
            CheckResult(
                name="tunnel.binary",
                status="warn",
                detail=(
                    f"{binary} not on PATH; tunnel.enabled is false so orch "
                    f"still boots, but /start would fail if you enable it"
                ),
                remediation=(
                    f"Install {binary} before flipping tunnel.enabled to true."
                ),
            )
        )
    return results


# ---------------------------------------------------------------------------
# Static validators (validate)
# ---------------------------------------------------------------------------


def validate_schema(tasks: Iterable[Task]) -> list[ValidationError]:
    """Verify every task has the required fields with sane types.

    ``Task.from_json`` already asserts the minimum shape at load time, so by
    the time we get here we mostly check semantic constraints (non-empty id,
    non-negative phase, positive estimate).
    """
    errors: list[ValidationError] = []
    for task in tasks:
        if not task.id or not isinstance(task.id, str):
            errors.append(
                ValidationError(
                    task_id=None,
                    field="id",
                    kind="schema.tasks",
                    message="task is missing a non-empty string id",
                )
            )
            continue
        if not isinstance(task.phase, int) or task.phase < 0:
            errors.append(
                ValidationError(
                    task_id=task.id,
                    field="phase",
                    kind="schema.tasks",
                    message=f"phase must be a non-negative int, got {task.phase!r}",
                )
            )
        if not task.model or not isinstance(task.model, str):
            errors.append(
                ValidationError(
                    task_id=task.id,
                    field="model",
                    kind="schema.tasks",
                    message="model must be a non-empty string",
                )
            )
        if not isinstance(task.dependencies, list):
            errors.append(
                ValidationError(
                    task_id=task.id,
                    field="dependencies",
                    kind="schema.tasks",
                    message=f"dependencies must be a list, got {type(task.dependencies).__name__}",
                )
            )
    return errors


def validate_routes(
    tasks: Iterable[Task],
    router_keys: Iterable[str],
) -> list[ValidationError]:
    """Every ``task.model`` must appear as a key in ``model_router.yaml``."""
    keys = set(router_keys)
    errors: list[ValidationError] = []
    for task in tasks:
        if task.model and task.model not in keys:
            errors.append(
                ValidationError(
                    task_id=task.id,
                    field="model",
                    kind="route.unresolved",
                    message=(
                        f"model {task.model!r} has no entry in model_router.yaml"
                    ),
                    remediation="Add a route for this model or fix the task.model value.",
                )
            )
    return errors


def validate_dependencies(tasks: Iterable[Task]) -> list[ValidationError]:
    """Every id in ``dependencies`` must exist in ``tasks``."""
    tasks_list = list(tasks)
    known_ids = {t.id for t in tasks_list}
    errors: list[ValidationError] = []
    for task in tasks_list:
        for dep in task.dependencies:
            if dep == task.id:
                errors.append(
                    ValidationError(
                        task_id=task.id,
                        field="dependencies",
                        kind="dep.cycle",
                        message=f"task {task.id!r} depends on itself",
                    )
                )
                continue
            if dep not in known_ids:
                errors.append(
                    ValidationError(
                        task_id=task.id,
                        field="dependencies",
                        kind="dep.missing",
                        message=f"depends on unknown task {dep!r}",
                    )
                )
    return errors


def find_cycles(tasks: Iterable[Task]) -> list[list[str]]:
    """Return each cycle in the dep graph as a list of task ids (path form).

    DFS 3-coloring:
        WHITE (0)  — not visited
        GRAY  (1)  — currently on the DFS stack
        BLACK (2)  — fully explored

    A back-edge to a GRAY node closes a cycle. We reconstruct the specific
    path from the parent chain so downstream consumers can print the actual
    offending edges, e.g. ``A -> B -> C -> A``.

    Self-loops are excluded here (``validate_dependencies`` catches them as
    ``dep.cycle`` already).
    """
    tasks_list = list(tasks)
    adj: dict[str, list[str]] = {
        t.id: [d for d in t.dependencies if d != t.id]
        for t in tasks_list
    }
    color: dict[str, int] = {t.id: 0 for t in tasks_list}
    parent: dict[str, str | None] = {t.id: None for t in tasks_list}
    cycles: list[list[str]] = []
    seen_cycle_keys: set[tuple[str, ...]] = set()

    def _record_cycle(closing_from: str, back_edge_target: str) -> None:
        # Walk parent chain from closing_from back to back_edge_target.
        path: list[str] = []
        node: str | None = closing_from
        # Bound the traversal to avoid pathological loops in edge cases.
        while node is not None and node != back_edge_target and len(path) <= len(tasks_list):
            path.append(node)
            node = parent.get(node)
        path.append(back_edge_target)
        path.reverse()
        path.append(back_edge_target)  # close the loop visually
        # Canonicalize so we don't record the same cycle twice from different
        # entry points: rotate so the smallest id is first (ignore last dup).
        loop = path[:-1]
        if not loop:
            return
        smallest_at = loop.index(min(loop))
        rotated = tuple(loop[smallest_at:] + loop[:smallest_at])
        if rotated in seen_cycle_keys:
            return
        seen_cycle_keys.add(rotated)
        cycles.append(list(rotated) + [rotated[0]])

    def _visit(node: str) -> None:
        # Iterative DFS to avoid stack overflow on very deep graphs.
        stack: list[tuple[str, int]] = [(node, 0)]
        while stack:
            n, idx = stack.pop()
            if idx == 0:
                if color[n] != 0:
                    continue
                color[n] = 1
            children = adj.get(n, [])
            if idx < len(children):
                stack.append((n, idx + 1))
                child = children[idx]
                if child not in color:
                    # Unknown child — validated elsewhere as dep.missing.
                    continue
                if color[child] == 1:
                    _record_cycle(n, child)
                elif color[child] == 0:
                    parent[child] = n
                    stack.append((child, 0))
            else:
                color[n] = 2

    for tid in list(color.keys()):
        if color[tid] == 0:
            _visit(tid)
    return cycles


def validate_cycles(tasks: Iterable[Task]) -> list[ValidationError]:
    """Wrap ``find_cycles`` into ``ValidationError`` rows."""
    errors: list[ValidationError] = []
    for cycle in find_cycles(tasks):
        pretty = " -> ".join(cycle)
        errors.append(
            ValidationError(
                task_id=cycle[0],
                field="dependencies",
                kind="dep.cycle",
                message=f"dependency cycle: {pretty}",
                remediation="Break the cycle by removing one of the edges above.",
            )
        )
    return errors


def validate_files_writable(
    tasks: Iterable[Task],
    project_root: Path,
) -> list[ValidationError]:
    """Verify parent dirs of each ``files[]`` entry exist and are writable.

    Opt-in (``orch validate --files``): file trees on developer laptops
    frequently move around, so this would false-positive on every branch.
    """
    errors: list[ValidationError] = []
    for task in tasks:
        for rel in task.files:
            try:
                target = (project_root / rel).resolve()
            except Exception:  # noqa: BLE001
                errors.append(
                    ValidationError(
                        task_id=task.id,
                        field="files",
                        kind="files.writable",
                        message=f"cannot resolve path {rel!r}",
                    )
                )
                continue
            parent = target.parent
            if not parent.exists():
                errors.append(
                    ValidationError(
                        task_id=task.id,
                        field="files",
                        kind="files.writable",
                        message=f"parent dir {parent} does not exist for {rel!r}",
                        severity="warn",
                    )
                )
                continue
            if not os.access(parent, os.W_OK):
                errors.append(
                    ValidationError(
                        task_id=task.id,
                        field="files",
                        kind="files.writable",
                        message=f"parent dir {parent} is not writable for {rel!r}",
                    )
                )
    return errors


def validate_preset_sanity(
    budgets_yaml: Path | None,
    preset: str | None,
    typical_dispatch_tokens: int,
) -> list[ValidationError]:
    """Warn when a provider's window can barely fit two typical dispatches.

    Reuses the Sprint A logic (``budget.warn_undersized_presets``) but wraps
    the output into ``ValidationError(severity='warn')`` rows.
    """
    from .budget import load_budget_config, warn_undersized_presets

    errors: list[ValidationError] = []
    if budgets_yaml is None or not budgets_yaml.exists() or not preset:
        return errors
    try:
        cfg = load_budget_config(budgets_yaml, preset)
    except ValueError as exc:
        errors.append(
            ValidationError(
                task_id=None,
                field="budgets_preset",
                kind="preset.sanity",
                message=str(exc),
            )
        )
        return errors
    warnings = warn_undersized_presets(cfg, preset, typical_dispatch_tokens)
    for msg in warnings:
        errors.append(
            ValidationError(
                task_id=None,
                field="budgets_preset",
                kind="preset.sanity",
                message=msg,
                severity="warn",
            )
        )
    return errors


def validate_config_shape(config_yaml: Path) -> list[ValidationError]:
    """Superficial config.yaml shape check — enough to catch dangerous typos.

    We don't lock down the full schema (that'd break on every new knob).
    We do assert the fields orch actively depends on are the right type.
    """
    errors: list[ValidationError] = []
    if not config_yaml.exists():
        errors.append(
            ValidationError(
                task_id=None,
                field="config",
                kind="schema.config",
                message=f"{config_yaml} does not exist",
            )
        )
        return errors
    try:
        with config_yaml.open("r", encoding="utf-8") as fh:
            data = yaml.safe_load(fh) or {}
    except (yaml.YAMLError, OSError) as exc:
        errors.append(
            ValidationError(
                task_id=None,
                field="config",
                kind="schema.config",
                message=f"cannot parse: {exc}",
            )
        )
        return errors
    if not isinstance(data, dict):
        errors.append(
            ValidationError(
                task_id=None,
                field="config",
                kind="schema.config",
                message=f"config.yaml top-level must be a mapping, got {type(data).__name__}",
            )
        )
        return errors

    # Concurrency block (optional but if present must be a mapping).
    if "concurrency" in data and not isinstance(data["concurrency"], dict):
        errors.append(
            ValidationError(
                task_id=None,
                field="concurrency",
                kind="schema.config",
                message="`concurrency` must be a mapping",
            )
        )
    state_cfg = data.get("state")
    if state_cfg is not None:
        if not isinstance(state_cfg, dict):
            errors.append(
                ValidationError(
                    task_id=None,
                    field="state",
                    kind="schema.config",
                    message="`state` must be a mapping",
                )
            )
        else:
            backend_value = state_cfg.get("backend", "file")
            if backend_value not in ("file", "sqlite"):
                errors.append(
                    ValidationError(
                        task_id=None,
                        field="state.backend",
                        kind="schema.config",
                        message=f"state.backend must be 'file' or 'sqlite', got {backend_value!r}",
                    )
                )
    return errors


def validate_graph(
    tasks: Sequence[Task],
    *,
    router_keys: Iterable[str] | None = None,
) -> list[ValidationError]:
    """Run the whole-graph static validators and return every error.

    ``files.writable`` and ``preset.sanity`` are NOT included here — they
    depend on I/O the caller may not want to run. Compose them via
    ``validate_files_writable`` / ``validate_preset_sanity`` explicitly.
    """
    out: list[ValidationError] = []
    out.extend(validate_schema(tasks))
    out.extend(validate_dependencies(tasks))
    out.extend(validate_cycles(tasks))
    if router_keys is not None:
        out.extend(validate_routes(tasks, router_keys))
    return out


# ---------------------------------------------------------------------------
# Aggregation helpers used by the CLI wrappers
# ---------------------------------------------------------------------------


def summarize_checks(results: Iterable[CheckResult]) -> dict[str, int]:
    """Count check statuses for the summary line + JSON payload."""
    out = {"ok": 0, "warn": 0, "error": 0, "skip": 0}
    for r in results:
        out[r.status] = out.get(r.status, 0) + 1
    return out


def exit_code_for_checks(results: Iterable[CheckResult]) -> int:
    """0 all ok, 1 warnings only, 2 any error — per Sprint D contract."""
    saw_warn = False
    for r in results:
        if r.status == "error":
            return 2
        if r.status == "warn":
            saw_warn = True
    return 1 if saw_warn else 0


def exit_code_for_errors(errors: Iterable[ValidationError]) -> int:
    """Same convention as doctor: 0 clean, 1 warn-only, 2 any error."""
    saw_warn = False
    for e in errors:
        if e.severity == "error":
            return 2
        if e.severity == "warn":
            saw_warn = True
    return 1 if saw_warn else 0
