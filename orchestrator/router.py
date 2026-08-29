"""Model router loader + fail-fast validator.

Contract (FR-D-6, AS-06):
    Every `task.model` string appearing in `tasks.json` MUST be a key in
    `model_router.yaml`. `validate_all_models` raises `UnroutedModelError`
    listing every offender BEFORE any subprocess is spawned — the router is
    the last line of defense against silent misroutes.

The loader normalizes YAML rows into `RouteEntry` instances (see
`orchestrator/models.py`) and computes `is_premium` if the YAML omits it
(defensive: config authors sometimes forget the denormalized flag).
"""

from __future__ import annotations

from pathlib import Path
from typing import Iterable

import yaml

from .models import RouteEntry, Task


class UnroutedModelError(Exception):
    """Raised when one or more tasks reference a model not in the router.

    The exception message lists every offending `(task_id, model)` pair so the
    operator can fix them in one pass (proposal Q1: router absorbs drift, we
    do NOT edit `tasks.json`).
    """

    def __init__(self, offenders: list[tuple[str, str]]):
        self.offenders = offenders
        lines = [f"  - {tid}: {model!r}" for tid, model in offenders]
        msg = (
            f"model_router.yaml is missing entries for {len(offenders)} task(s):\n"
            + "\n".join(lines)
            + "\nRun `orch router add-missing` to auto-add them (backend/cli_model "
            "inferred, tier defaults to standard), or edit model_router.yaml by hand."
        )
        super().__init__(msg)


class RouterFormatError(Exception):
    """YAML shape doesn't match RouteEntry (missing keys, wrong types)."""


_VALID_BACKENDS = {"claude", "codex", "opencode", "gemini", "agy"}
_VALID_TIERS = {"premium", "standard", "cheap"}


def load_router(path: str | Path) -> dict[str, RouteEntry]:
    """Load `model_router.yaml` and return `{tasks-json-model: RouteEntry}`.

    Validates each row: `backend` and `tier` must be enum-valid; `cli_model`
    non-empty. `is_premium` is recomputed from `tier` (YAML flag is
    denormalized cache — we own the truth here).
    """
    with open(path, encoding="utf-8") as fh:
        raw = yaml.safe_load(fh) or {}

    if not isinstance(raw, dict):
        raise RouterFormatError(
            f"{path}: expected top-level mapping, got {type(raw).__name__}"
        )

    router: dict[str, RouteEntry] = {}
    for key, row in raw.items():
        if not isinstance(row, dict):
            raise RouterFormatError(f"{path}: entry {key!r} is not a mapping")
        backend = row.get("backend")
        cli_model = row.get("cli_model")
        tier = row.get("tier")
        if backend not in _VALID_BACKENDS:
            raise RouterFormatError(
                f"{path}: entry {key!r} has invalid backend {backend!r} "
                f"(expected one of {sorted(_VALID_BACKENDS)})"
            )
        if tier not in _VALID_TIERS:
            raise RouterFormatError(
                f"{path}: entry {key!r} has invalid tier {tier!r} "
                f"(expected one of {sorted(_VALID_TIERS)})"
            )
        if not isinstance(cli_model, str) or not cli_model:
            raise RouterFormatError(
                f"{path}: entry {key!r} missing non-empty cli_model"
            )
        # Issues #87 / #88: agy-specific extras. Validate light-touch —
        # both are strings when present, otherwise router load stays
        # backward-compat for every non-agy row.
        agent = row.get("agent")
        effort = row.get("effort")
        if agent is not None and not isinstance(agent, str):
            raise RouterFormatError(
                f"{path}: entry {key!r} field `agent` must be a string, got "
                f"{type(agent).__name__}"
            )
        if effort is not None:
            if not isinstance(effort, str) or effort not in {"low", "medium", "high"}:
                raise RouterFormatError(
                    f"{path}: entry {key!r} field `effort` must be one of "
                    f"'low' | 'medium' | 'high', got {effort!r}"
                )
        router[key] = RouteEntry(
            backend=backend,
            cli_model=cli_model,
            tier=tier,
            is_premium=(tier == "premium"),
            fallback_cli_model=row.get("fallback_cli_model"),
            escalation_model=row.get("escalation_model"),
            agent=agent,
            effort=effort,
        )

    # FR-D-8: `escalation_model` must be a valid route KEY in this same file.
    # Fail-fast here so no dispatch ever picks up a dangling escalation reference.
    # (Mirrors AS-06 semantics for `task.model` → router key.)
    for key, entry in router.items():
        if entry.escalation_model is None:
            continue
        if entry.escalation_model not in router:
            raise RouterFormatError(
                f"{path}: entry {key!r} has escalation_model "
                f"{entry.escalation_model!r} that is not a route in this file "
                f"(add it, or remove the escalation_model field)"
            )
    return router


def validate_all_models(
    tasks: Iterable[Task], router: dict[str, RouteEntry]
) -> None:
    """Fail-fast: every task.model must be routable (FR-D-6 / AS-06).

    Raises `UnroutedModelError` with every `(task_id, model)` offender listed,
    so the operator fixes them all in a single edit pass — no incremental
    trial-and-error.
    """
    offenders = [(t.id, t.model) for t in tasks if t.model not in router]
    if offenders:
        # Sort by task id for stable error output (test assertions rely on it).
        offenders.sort(key=lambda pair: pair[0])
        raise UnroutedModelError(offenders)


# ---- issue #55: infer + append missing routes -------------------------------


def infer_route_entry(
    model_key: str, *, default_tier: str = "standard"
) -> RouteEntry | None:
    """Infer a `RouteEntry` from a `backend/cli_model` router key.

    The router key convention is `<backend>/<cli_model>` (see the header of
    `model_router.yaml`). We can safely infer `backend` (prefix) and
    `cli_model` (remainder), but `tier` is a business decision that a model
    name can't reveal — so it defaults to `standard` (non-premium, does not
    trip the semi gate). Callers must tell the user to review tiers.

    Returns None when the backend prefix is unknown or absent (a bare model
    string with no `/`, or `openai/...`): those need a human, not a guess.
    """
    backend, sep, cli_model = model_key.partition("/")
    if not sep or backend not in _VALID_BACKENDS or not cli_model:
        return None
    return RouteEntry(
        backend=backend,
        cli_model=cli_model,
        tier=default_tier,
        is_premium=(default_tier == "premium"),
    )


def missing_models(
    tasks: Iterable[Task], router: dict[str, RouteEntry]
) -> list[str]:
    """Unique `task.model` keys referenced by tasks but absent from the router.

    Deduplicated and sorted — an `UnroutedModelError` lists one line per task,
    but N tasks usually share a handful of models. The caller adds *models*,
    not tasks.
    """
    return sorted({t.model for t in tasks if t.model not in router})


def _format_router_entry(key: str, entry: RouteEntry) -> str:
    """Render one router row as YAML text (comment-safe append, no full dump)."""
    return (
        f"{key}:\n"
        f"  backend: {entry.backend}\n"
        f"  cli_model: {entry.cli_model}\n"
        f"  tier: {entry.tier}\n"
    )


def append_router_entries(
    path: str | Path, entries: dict[str, RouteEntry]
) -> list[str]:
    """Append inferred entries to `model_router.yaml`, preserving the file.

    We append raw YAML text rather than re-dumping the parsed mapping so the
    header comments and hand-authored ordering survive untouched. Keys already
    present in the file are skipped (idempotent). Returns the list of keys
    actually appended, sorted.
    """
    p = Path(path)
    existing = load_router(p)  # also validates the current file is well-formed
    to_add = {k: v for k, v in entries.items() if k not in existing and v is not None}
    if not to_add:
        return []

    blocks = [_format_router_entry(k, to_add[k]) for k in sorted(to_add)]
    body = p.read_text(encoding="utf-8")
    sep = "" if body.endswith("\n") else "\n"
    addition = (
        f"{sep}\n# ---- auto-added by `orch router add-missing` "
        f"(review tiers) ----\n" + "\n".join(blocks)
    )
    p.write_text(body + addition, encoding="utf-8")
    return sorted(to_add)
