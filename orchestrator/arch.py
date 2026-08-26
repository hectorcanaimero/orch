"""`orch arch generate` — architecture diagram generation via the archify skill.

Delegates the actual diagram authoring to a Claude sub-agent running the
`archify` skill (https://github.com/tt-a1i/archify). The sub-agent inspects
the project's PRDs, specs, and tasks.json, chooses an archify view, and
writes a self-contained HTML file to `docs/architecture/current.html`.

This module owns:
    - Skill detection (global `~/.claude/skills/archify/SKILL.md` or the
      project-local fallback).
    - Source assembly + sha256 hash (deterministic so repeated runs on
      unchanged inputs produce the same hash — dashboard staleness signal).
    - PID lock via `state/arch-generate.lock` (contains PID + started_at;
      dead-PID detection so a crashed run doesn't block indefinitely).
    - Budget-gated dispatch of `claude -p --output-format json …` with the
      same argv shape used by `dispatcher.ClaudeBackend`. Single-shot; no
      reap loop, no per-task semaphores — this is a one-off diagram build.
    - Archiving `current.html` to `docs/architecture/archive/{iso}-{hash7}.html`
      and appending an event to `state/arch-events.jsonl`.

Explicit non-goals:
    - Not wired into the main dispatch loop. Runs synchronously from the
      CLI; the dashboard's `/regenerate` endpoint fires it as a
      background `subprocess.Popen`.
    - No retries. If the sub-agent fails, the operator re-runs.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from orchestrator.procutil import pid_alive as _pid_alive


ARCH_DIR = "docs/architecture"
ARCHIVE_SUBDIR = "archive"
EVENTS_FILENAME = "arch-events.jsonl"
LOCK_FILENAME = "arch-generate.lock"
CURRENT_FILENAME = "current.html"

# 5 min ceiling — archify wants to inspect the whole repo + render HTML.
DEFAULT_TIMEOUT_S = 300.0


# ---- Skill detection ------------------------------------------------------


def resolve_skill_path(project_root: Path) -> Path | None:
    """Locate SKILL.md; None when neither global nor project-local exists.

    Global wins because operators typically install once via `npx skills add`.
    """
    global_path = Path.home() / ".claude" / "skills" / "archify" / "SKILL.md"
    if global_path.exists():
        return global_path
    local_path = project_root / ".claude" / "skills" / "archify" / "SKILL.md"
    if local_path.exists():
        return local_path
    return None


def _install_hint() -> str:
    return (
        "Archify skill is not installed. Install it with:\n"
        "  npx skills add tt-a1i/archify -g\n"
        "Then re-run: orch arch generate"
    )


# ---- Source discovery + hashing ------------------------------------------


def discover_sources(project_root: Path, tasks_json: Path) -> dict[str, Any]:
    """Return the artifact bundle passed to the archify prompt.

    Empty PRD/spec directories are tolerated — a project might not have
    them yet, and archify can still infer structure from tasks.json alone.
    """
    prd_dir = project_root / "docs" / "prd"
    prds: list[Path] = sorted(prd_dir.glob("*.md")) if prd_dir.is_dir() else []

    specs: list[Path] = []
    for candidate in (project_root / "specs", project_root / "docs" / "specs"):
        if candidate.is_dir():
            specs = sorted(candidate.glob("*.md"))
            break

    tasks_path = tasks_json if tasks_json.exists() else None
    return {"prd": prds, "specs": specs, "tasks_json": tasks_path}


def compute_source_hash(sources: dict[str, Any]) -> str:
    """sha256[:7] over the concatenated bytes of every artifact.

    Sort order matters — determinism is the whole point.
    """
    h = hashlib.sha256()
    ordered: list[Path] = []
    ordered.extend(sources.get("prd") or [])
    ordered.extend(sources.get("specs") or [])
    tj = sources.get("tasks_json")
    if tj is not None:
        ordered.append(tj)
    for path in sorted(ordered, key=lambda p: str(p)):
        try:
            h.update(path.read_bytes())
        except OSError:
            continue
    return h.hexdigest()[:7]


# ---- Lock ----------------------------------------------------------------


def _lock_path(state_dir: Path) -> Path:
    return state_dir / LOCK_FILENAME


def read_lock(state_dir: Path) -> dict[str, Any] | None:
    """Return the parsed lock file, or None when absent/corrupt/stale."""
    path = _lock_path(state_dir)
    if not path.exists():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    if not isinstance(data, dict):
        return None
    pid = int(data.get("pid") or 0)
    if pid and not _pid_alive(pid):
        # Dead PID — sweep the stale lock so callers see a clean slate.
        try:
            path.unlink()
        except OSError:
            pass
        return None
    return data


def acquire_lock(state_dir: Path, phase: str = "dispatching") -> dict[str, Any]:
    state_dir.mkdir(parents=True, exist_ok=True)
    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    payload = {
        "pid": os.getpid(),
        "started_at": now,
        "phase": phase,
        "phase_at": now,
    }
    _lock_path(state_dir).write_text(json.dumps(payload), encoding="utf-8")
    return payload


def update_phase(state_dir: Path, phase: str) -> None:
    """Rewrite the lock file with a new phase marker.

    Best-effort — a corrupt/absent lock is treated as "nothing to update"
    so a phase update never crashes the generation flow. Preserves `pid`
    and `started_at` from the existing payload.
    """
    path = _lock_path(state_dir)
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return
    if not isinstance(data, dict):
        return
    data["phase"] = phase
    data["phase_at"] = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    try:
        path.write_text(json.dumps(data), encoding="utf-8")
    except OSError:
        pass


def release_lock(state_dir: Path) -> None:
    try:
        _lock_path(state_dir).unlink()
    except FileNotFoundError:
        pass
    except OSError:
        pass


# ---- Prompt --------------------------------------------------------------


def build_prompt(
    project_root: Path,
    sources: dict[str, Any],
    current_html: Path,
) -> str:
    """Minimal prompt — trust the archify skill to know how to invoke itself.

    Earlier revisions spelled out `node bin/archify.mjs deliver ...` which
    the model interpreted as a path relative to cwd=project_root, where it
    doesn't exist. The result: claude generated a valid JSON IR then stopped
    without producing HTML. The skill's own SKILL.md already documents how
    to invoke archify — we only need to specify the output path.
    """
    prds = sources.get("prd") or []
    specs = sources.get("specs") or []
    tj = sources.get("tasks_json")
    prd_str = "\n  - " + "\n  - ".join(str(p) for p in prds) if prds else " (none)"
    spec_str = "\n  - " + "\n  - ".join(str(p) for p in specs) if specs else " (none)"
    tasks_str = str(tj) if tj else "(none)"
    return (
        f"Use the archify skill to generate an interactive architecture "
        f"diagram for the project at {project_root}.\n\n"
        f"Read these artifacts to ground the diagram in reality:\n"
        f"- PRD:{prd_str}\n"
        f"- Specs:{spec_str}\n"
        f"- Tasks: {tasks_str}\n\n"
        f"Write the final self-contained HTML to exactly this path:\n"
        f"  {current_html}\n\n"
        f"Do not leave intermediate JSON IR files in the project root — put "
        f"any working files under {current_html.parent}.\n"
    )


# ---- Dispatch -------------------------------------------------------------


def _build_claude_argv(model: str) -> list[str]:
    """Same shape as `dispatcher.ClaudeBackend.build_cmd`, minus the Task/Route deps.

    `--permission-mode bypassPermissions`: archify needs to shell out to
    `node bin/archify.mjs render ...` to produce the HTML. `acceptEdits`
    (our old default) auto-accepts Write/Edit but NOT Bash, so archify's
    render step ended up in `permission_denials` and returned an empty
    result after burning ~$0.24 per run. This is a headless subprocess
    we dispatch ourselves — no interactive user to prompt anyway.
    """
    return [
        "claude",
        "-p",
        "--output-format",
        "json",
        "--model",
        model,
        "--session-id",
        str(uuid.uuid4()),
        "--add-dir",
        ".",
        "--permission-mode",
        "bypassPermissions",
    ]


def _run_claude(
    argv: list[str],
    prompt: str,
    cwd: Path,
    timeout_s: float,
) -> subprocess.CompletedProcess:
    """subprocess.run with stdin=prompt bytes. Isolated for test mocking."""
    return subprocess.run(  # noqa: S603 — argv locally constructed
        argv,
        input=prompt.encode("utf-8"),
        cwd=str(cwd),
        capture_output=True,
        timeout=timeout_s,
        check=False,
    )


def parse_claude_result(stdout: str) -> dict[str, Any]:
    """Extract `{cost_usd, model}` from the claude JSON envelope. Best-effort."""
    from orchestrator.dispatcher import _parse_claude_envelope

    env = _parse_claude_envelope(stdout) or {}
    cost = float(env.get("total_cost_usd", 0.0) or 0.0)
    model = str(env.get("model") or env.get("used_model") or "")
    return {"cost_usd": cost, "model": model}


# ---- Events + archive -----------------------------------------------------


def _events_path(state_dir: Path) -> Path:
    return state_dir / EVENTS_FILENAME


def append_event(state_dir: Path, event: dict[str, Any]) -> None:
    state_dir.mkdir(parents=True, exist_ok=True)
    with _events_path(state_dir).open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(event, separators=(",", ":")) + "\n")


def read_events(state_dir: Path) -> list[dict[str, Any]]:
    """Return snapshot events oldest-first; empty list when file missing."""
    path = _events_path(state_dir)
    if not path.exists():
        return []
    out: list[dict[str, Any]] = []
    try:
        for line in path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(obj, dict):
                out.append(obj)
    except OSError:
        return []
    return out


def _now_utc() -> datetime:
    return datetime.now(timezone.utc)


def _iso_filename_token(dt: datetime) -> str:
    """Colons swapped for dashes so the string is safe as a filename/URL segment."""
    return dt.strftime("%Y-%m-%dT%H-%M-%SZ")


def _iso_utc(dt: datetime) -> str:
    return dt.strftime("%Y-%m-%dT%H:%M:%SZ")


def archive_current(
    project_root: Path, source_hash: str, when: datetime | None = None
) -> tuple[Path, str, str]:
    """Copy current.html to archive/{token}-{hash7}.html.

    Returns `(archive_path, url_token, iso_timestamp)` where `url_token` is
    the filename-safe timestamp used in the snapshot URL and `iso_timestamp`
    is the RFC3339 form written to the JSONL event.
    """
    dt = when or _now_utc()
    arch_dir = project_root / ARCH_DIR
    archive_dir = arch_dir / ARCHIVE_SUBDIR
    archive_dir.mkdir(parents=True, exist_ok=True)
    token = _iso_filename_token(dt)
    dest = archive_dir / f"{token}-{source_hash}.html"
    shutil.copyfile(arch_dir / CURRENT_FILENAME, dest)
    return dest, token, _iso_utc(dt)


# ---- CLI -----------------------------------------------------------------


def _resolve_model(cfg: dict[str, Any]) -> str:
    """Prefer explicit `arch.model` in config; fall back to a sane default."""
    arch_cfg = cfg.get("arch") if isinstance(cfg, dict) else None
    if isinstance(arch_cfg, dict):
        model = arch_cfg.get("model")
        if isinstance(model, str) and model.strip():
            return model.strip()
    return "claude-sonnet-4-5"


def _run_generate(argv: list[str]) -> int:
    from orchestrator.orch import _add_common_project_flags, _load_config
    from orchestrator.paths import resolve_project_paths
    from orchestrator.state import CwdViolationError

    p = argparse.ArgumentParser(
        prog="orch arch generate",
        description=(
            "Generate a self-contained HTML architecture diagram via the "
            "archify skill (Claude sub-agent). Archives every run under "
            "docs/architecture/archive/ and records a JSONL event."
        ),
    )
    _add_common_project_flags(p)
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

    skill = resolve_skill_path(paths.project_root)
    if skill is None:
        print(_install_hint(), file=sys.stderr)
        return 1

    try:
        cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
    except Exception as exc:  # noqa: BLE001
        print(f"config load failed: {exc}", file=sys.stderr)
        return 1

    sources = discover_sources(paths.project_root, paths.tasks_json)
    source_hash = compute_source_hash(sources)

    existing = read_lock(paths.state_dir)
    if existing is not None:
        print(
            f"Already in progress (PID {existing.get('pid')}, "
            f"started {existing.get('started_at')})",
            file=sys.stderr,
        )
        return 1

    # Budget gate — respects the sprint 7 provider quota if configured.
    # We check `claude` since that's the backend we dispatch. Disabled
    # gate (no budgets.yaml) is a no-op that returns True.
    try:
        from orchestrator.budget import BudgetGate, load_budget_config

        preset = os.environ.get("ORCH_BUDGETS_PRESET") or cfg.get("budgets_preset") or "conservative"
        budget_path_cfg = cfg.get("budgets_config") or "budgets.yaml"
        budget_path = paths.project_root / budget_path_cfg
        if not budget_path.exists():
            budget_path = paths.config_yaml.parent / budget_path_cfg
        try:
            budget_cfg = load_budget_config(budget_path, preset=preset)
        except ValueError:
            budget_cfg = None
        gate = BudgetGate(state_dir=paths.state_dir, config=budget_cfg)
        ok, reason, _ = gate.can_dispatch("claude")
        if not ok:
            print(f"budget gate blocked dispatch: {reason}", file=sys.stderr)
            return 1
    except ImportError:
        pass

    arch_dir = paths.project_root / ARCH_DIR
    arch_dir.mkdir(parents=True, exist_ok=True)
    current_html = arch_dir / CURRENT_FILENAME

    acquire_lock(paths.state_dir)
    model = _resolve_model(cfg)
    prompt = build_prompt(paths.project_root, sources, current_html)
    argv_claude = _build_claude_argv(model)

    try:
        update_phase(paths.state_dir, "claude_working")
        try:
            proc = _run_claude(argv_claude, prompt, paths.project_root, DEFAULT_TIMEOUT_S)
        except FileNotFoundError:
            print(
                "claude CLI not found on PATH. Install Anthropic's CLI first.",
                file=sys.stderr,
            )
            return 1
        except subprocess.TimeoutExpired:
            print(
                f"archify generation timed out after {DEFAULT_TIMEOUT_S:.0f}s",
                file=sys.stderr,
            )
            return 1

        if proc.returncode != 0:
            stderr_tail = (proc.stderr or b"").decode("utf-8", errors="replace")[-500:]
            print(
                f"claude exited {proc.returncode}: {stderr_tail}",
                file=sys.stderr,
            )
            return 1

        if not current_html.exists():
            stdout_tail = (proc.stdout or b"").decode("utf-8", errors="replace")[-2000:]
            print(
                f"archify did not produce {current_html}\n"
                f"---last 2KB of claude stdout---\n{stdout_tail}",
                file=sys.stderr,
            )
            return 1

        update_phase(paths.state_dir, "finalizing")
        parsed = parse_claude_result(
            (proc.stdout or b"").decode("utf-8", errors="replace")
        )

        archive_path, _token, iso_ts = archive_current(paths.project_root, source_hash)
        rel_archive = archive_path.relative_to(arch_dir).as_posix()
        event = {
            "timestamp": iso_ts,
            "source_hash": source_hash,
            "cost_usd": parsed["cost_usd"],
            "model": parsed["model"] or model,
            "source_artifacts": {
                "prd_count": len(sources.get("prd") or []),
                "spec_count": len(sources.get("specs") or []),
                "task_count": _count_tasks(sources.get("tasks_json")),
            },
            "output_file": rel_archive,
        }
        append_event(paths.state_dir, event)

        print(f"Generated: {current_html}")
        return 0
    finally:
        release_lock(paths.state_dir)


def _count_tasks(tasks_json: Path | None) -> int:
    if tasks_json is None:
        return 0
    try:
        raw = json.loads(tasks_json.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return 0
    rows = raw.get("tasks") if isinstance(raw, dict) else raw
    return len(rows) if isinstance(rows, list) else 0


def run_arch_cli(argv: list[str]) -> int:
    """`orch arch <verb>` router. Only `generate` for now."""
    if not argv or argv[0] in ("-h", "--help"):
        print(
            "usage: orch arch <verb> [flags]\n\n"
            "Verbs:\n"
            "  generate    Dispatch archify to produce docs/architecture/current.html\n"
        )
        return 0 if argv else 1
    verb = argv[0]
    if verb == "generate":
        return _run_generate(argv[1:])
    print(f"unknown verb: {verb!r}", file=sys.stderr)
    return 1
