"""Prompt template renderer for the Rupies v2 orchestrator.

Contract (FR-P-1..5):
    - Body follows `sdd/orchestrator/explore.md §3` VERBATIM. Do NOT paraphrase
      — the shell-out protocol lines are load-bearing and any rewording risks
      confusing the agents at dispatch time.
    - Specs are referenced BY PATH (`docs/rewrite-plan/<specRef>`), NEVER
      inlined (they average 100-150 KB and would blow the token budget on
      every dispatch — see explore.md §Red flags).
    - Dep comments are the most recent entry from `Task.comments` (populated
      by `scripts/task-finish.sh`), truncated to 500 chars.
    - Every prompt embeds the marker `TASK_ID=<id>` so the orchestrator's
      post-check can detect id-spoofing (AS-10 / C-2).
    - Output lands at `state/prompts/<run-id>/<task-id>.txt` (utf-8), one
      file per dispatch — auditable, replayable.

Delivery to the CLI is via stdin (approved decision, see design.md OPEN /
tasks.md R-013). The temp file is still written so operators can `bat` it
after a failed run.
"""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Iterable

from .models import Task

log = logging.getLogger(__name__)


# Default location for `spec_ref` values, relative to the project root
# (FR-P-2). Retrocompat con rupies. Fase 3: pisable por `config.yaml`
# (`spec_root: <path>`) y propagado como argumento a `render_prompt`.
DEFAULT_SPEC_ROOT = "docs/rewrite-plan"

# Truncation cap for dep comments (FR-P-3).
_DEP_COMMENT_MAX_CHARS = 500


def read_dep_last_comment(dep_task: Task) -> str:
    """Return the most recent comment text on a dep task.

    `Task.comments` is a list of `{"author": ..., "text": ..., "ts": ...}`
    dicts, appended by `scripts/task-finish.sh`. We take the last entry
    (most recent) and its `text` field. Missing / empty → "" (the caller
    renders `(no comment)` in that case).

    Truncation to 500 chars matches FR-P-3.
    """
    comments = dep_task.comments or []
    if not comments:
        return ""
    last = comments[-1]
    text = str(last.get("text", "")) if isinstance(last, dict) else str(last)
    if len(text) > _DEP_COMMENT_MAX_CHARS:
        text = text[:_DEP_COMMENT_MAX_CHARS] + "…"
    return text


def _render_files(files: list[str]) -> str:
    """Render `Task.files` for the prompt.

    Explore.md §3 says the line reads `Files you may write: {files or "[]"}`
    — literal `[]` when empty, otherwise the list repr. That literal is
    important so the agent knows there's no strict-file guard for this task.
    """
    if not files:
        return "[]"
    return str(files)


def _render_spec_ref(spec_ref: str | None, spec_root: str = DEFAULT_SPEC_ROOT) -> str:
    """Render the spec-ref line.

    - Present → `<spec_root>/<spec_ref>` (path only, never inlined).
    - Missing / None / empty → placeholder + WARN log.

    Fase 3: `spec_root` es parámetro (default = `DEFAULT_SPEC_ROOT` para
    retrocompat con rupies). El caller resuelve el valor efectivo desde el
    `config.yaml` del proyecto — cada proyecto puede tener su propio layout
    de specs (`docs/specs/`, `sdd/`, etc.).
    """
    if not spec_ref:
        log.warning("prompt rendered without spec_ref — proceeding with description only")
        return "(no spec ref provided — proceed with the description only)"
    return f"{spec_root}/{spec_ref}"


def _render_deps_block(completed_deps: Iterable[Task]) -> str:
    """Render the 'Completed dependencies (context)' block, or empty string
    when there are no deps (design §6: omit the block entirely).
    """
    deps = list(completed_deps)
    if not deps:
        return ""
    lines = ["Completed dependencies (context):"]
    for dep in deps:
        comment = read_dep_last_comment(dep) or "(no comment)"
        lines.append(f"  - {dep.id}: {comment}")
    return "\n".join(lines) + "\n"


def render_prompt(
    task: Task,
    completed_deps: list[Task],
    spec_ref: str | None,
    run_id: str,
    state_dir: Path,
    project_root: Path | None = None,
    spec_root: str = DEFAULT_SPEC_ROOT,
) -> Path:
    """Write the per-dispatch prompt to disk and return the path.

    Path shape: `{state_dir}/prompts/{run_id}/{task.id}.txt` (FR-P-4, approved
    prompts-path decision). Overwrites on re-render — one prompt per task per
    run.

    The template body is copied verbatim from `explore.md §3`; only the
    substitutions change. If you catch yourself paraphrasing a line, STOP —
    the shell-out protocol wording is contract with the agent.

    Fase 1 multi-proyecto: `project_root` reemplaza el `/Volumes/PortableSSD/
    rupies/v2` que estaba hardcodeado en la línea `Working dir:` del template.
    Cuando llega `None` caemos a `Path.cwd()` — retrocompatible con la
    invocación clásica desde `v2/`.

    Fase 3: `spec_root` se recibe como parámetro (default = rupies histórico
    `docs/rewrite-plan`). El orquestador lo resuelve desde `config.yaml`
    (`spec_root: <path>`) al arrancar y lo pasa acá — cada proyecto puede
    apuntar a su propio layout de specs sin tocar el código.
    """
    out_dir = Path(state_dir) / "prompts" / run_id
    out_dir.mkdir(parents=True, exist_ok=True)
    out_path = out_dir / f"{task.id}.txt"

    working_dir = Path(project_root) if project_root is not None else Path.cwd()

    deps_block = _render_deps_block(completed_deps)
    body = _TEMPLATE.format(
        id=task.id,
        title=task.title,
        description=task.description,
        phase=task.phase,
        estimate_hours=task.estimate_hours,
        reason=task.reason,
        files=_render_files(task.files),
        spec_ref_line=_render_spec_ref(spec_ref, spec_root=spec_root),
        deps_block=deps_block,
        model=task.model,
        working_dir=str(working_dir),
    )

    out_path.write_text(body, encoding="utf-8")
    return out_path


# ---- Template -----------------------------------------------------------
# Verbatim from `sdd/orchestrator/explore.md §3`. The `TASK_ID=<id>` marker
# on line 1 is what powers the id-spoofing check (FR-P-5 / AS-10). Do not
# reorder or reword lines without updating the spec.
_TEMPLATE = """\
TASK_ID={id}
You are executing task {id} for the Rupies v2 rewrite.

Working dir: {working_dir}
Title: {title}
Description: {description}
Phase: {phase}  Estimate: {estimate_hours}h
Model reason: {reason}
Files you may write: {files}
Spec ref (READ FIRST): {spec_ref_line}
{deps_block}
Coordination protocol:
1. Read the spec ref for the exact acceptance criteria for {id}.
2. Do the work. If blocked, run: scripts/task-block.sh {id} "<reason>" "{model}" and STOP.
3. On success, run: scripts/task-finish.sh {id} "<what you did>" "{model}"
   Note: the orchestrator will call scripts/task-start.sh {id} BEFORE launching you.

Constraints:
- Do NOT edit tasks.json directly.
- Do NOT touch files outside {files} unless the spec explicitly requires it.
- Report progress via the scripts above only.
"""
