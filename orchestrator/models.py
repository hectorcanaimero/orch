"""Core dataclasses for the Rupies v2 task orchestrator.

Contracts:
    - Immutable value objects (`Task`, `RouteEntry`, `SpendEntry`, `EventEntry`)
      use `frozen=True, slots=True` so misuse (accidental mutation) raises.
    - Runtime aggregates (`Dispatch`, `RunState`) are mutable — the main loop
      mutates them per tick.

Fields mirror `design.md §2` — do not drift without an SDD change.

No I/O lives here. No YAML parsing, no filesystem. Just types.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Literal

Status = Literal["backlog", "todo", "in-progress", "done", "blocked"]
Backend = Literal["claude", "codex", "opencode", "gemini", "agy"]
Tier = Literal["premium", "standard", "cheap"]
Mode = Literal["auto", "semi"]


@dataclass(frozen=True, slots=True)
class Task:
    """A single unit of work from `tasks.json`.

    Frozen because the resolver keeps a canonical `dict[id, Task]`; status
    changes live in a separate `dict[id, Status]` map (see `task_queue.py`).
    """

    id: str
    phase: int
    title: str
    description: str
    model: str
    reason: str
    status: Status
    dependencies: list[str]
    estimate_hours: float
    files: list[str]
    spec_ref: str
    comments: list[dict]

    @classmethod
    def from_json(cls, raw: dict[str, Any]) -> "Task":
        """Build a `Task` from a `tasks.json` row.

        `tasks.json` uses camelCase (`estimateHours`, `specRef`); the dataclass
        uses snake_case. Optional list fields default to `[]` when missing.
        """
        return cls(
            id=raw["id"],
            phase=int(raw["phase"]),
            title=raw["title"],
            description=raw.get("description", ""),
            model=raw["model"],
            reason=raw.get("reason", ""),
            status=raw.get("status", "todo"),
            dependencies=list(raw.get("dependencies", []) or []),
            estimate_hours=float(raw.get("estimateHours", 0) or 0),
            files=list(raw.get("files", []) or []),
            spec_ref=raw.get("specRef", ""),
            comments=list(raw.get("comments", []) or []),
        )


@dataclass(frozen=True, slots=True)
class RouteEntry:
    """One row from `model_router.yaml`.

    `is_premium` is precomputed at load-time (see `router.load_router`) so the
    hot path doesn't string-compare `tier` on every check.

    `escalation_model` (FR-D-8, added 2026-08): OPTIONAL, per-route metadata.
    When set, names another ROUTE KEY (not a raw cli_model) that the reap
    loop promotes to on attempt 3 after a retryable failure on attempt 2.
    Validated at startup by `router.load_router` — a non-null value that
    doesn't correspond to a real route entry is a fail-fast startup error.
    None → this route keeps the classic FR-D-4 max=2-attempt semantic.
    """

    backend: Backend
    cli_model: str
    tier: Tier
    is_premium: bool
    fallback_cli_model: str | None = None
    escalation_model: str | None = None


@dataclass
class Dispatch:
    """An in-flight subprocess. Mutable — the reap loop updates `attempt`."""

    task_id: str
    backend: Backend
    pid: int
    session_id: str
    started_at: str
    prompt_path: str
    log_path: str
    output_path: str
    attempt: int = 1


@dataclass
class RunState:
    """Per-run operational state, persisted to `state/run-<uuid>.json`.

    Sprint A / Issue #12: `parent_pid` records the orch process PID at run
    start so `orch stop` (subcommand) can locate the running orch and send
    it SIGTERM. 0 = not recorded (backwards-compat with pre-Sprint-A rows).
    """

    run_id: str
    started_at: str
    mode: Mode
    in_flight: dict[str, Dispatch] = field(default_factory=dict)
    completed: list[str] = field(default_factory=list)
    blocked: list[str] = field(default_factory=list)
    deferred: list[str] = field(default_factory=list)
    parent_pid: int = 0


@dataclass(frozen=True, slots=True)
class SpendEntry:
    """One row appended to `state/spend-<YYYY-MM-DD>.jsonl` per completed dispatch.

    Fase 2: `project_id` opcional. Default None por retrocompatibilidad con
    filas escritas antes del refactor multi-proyecto — tolerado al leer.
    Sitios nuevos que construyen SpendEntry deben pasar `paths.project_id`.

    Sprint A / Issue #8: `estimated` is True when the backend produced work
    events but never reported usage (tokens_in/out both 0). Downstream
    consumers (dashboard, budget report) can flag these rows so operators
    know the number is not real telemetry. Default False keeps rows written
    before the field existed backwards-compatible.
    """

    ts: str
    task_id: str
    backend: Backend
    model: str
    tokens_in: int
    tokens_out: int
    cost_usd: float
    duration_s: float
    project_id: str | None = None
    estimated: bool = False


@dataclass
class Finding:
    """A single dogfooding finding captured by an agent (Sprint E-1 / #17).

    Findings are the seed of the orch → GitHub feedback loop: an agent working
    on a project notices a bug/fix/feature idea (about orch itself OR about
    the user's project), captures it via `orch findings capture`, and — after
    a human review — publishes to a GitHub issue tracker via `orch findings
    publish`.

    Fields:
        id            uuid4 hex — assigned at capture time.
        created_at    ISO-8601 UTC.
        type          bug | fix | feature.
        about         orch (report upstream) | project (user's tracker only).
        summary       Single-line human title. Required.
        evidence      Multi-line: file:line refs, log excerpts, repro. Optional.
        confidence    low | medium | high. `low` cannot publish (list-only).
        status        pending | published | dismissed | duplicate.
        published_url Populated on successful publish.
        duplicate_of  URL of the matched issue (only when status=duplicate).
        dedup_hash    sha256(type|about|normalized_summary) — enforces local
                      dedup at capture time.
        project_id    Multitenant scoping (matches backend project_id).
        author        Who captured it (agent hint or "human").
        dismissed_reason  Free-form note when status transitioned to dismissed.
    """

    id: str
    created_at: str
    type: Literal["bug", "fix", "feature"]
    about: Literal["orch", "project"]
    summary: str
    evidence: str
    confidence: Literal["low", "medium", "high"]
    status: Literal["pending", "published", "dismissed", "duplicate"] = "pending"
    published_url: str | None = None
    duplicate_of: str | None = None
    dedup_hash: str = ""
    project_id: str = ""
    author: str = "agent"
    dismissed_reason: str | None = None


@dataclass(frozen=True, slots=True)
class EventEntry:
    """One row appended to `state/events-<run-id>.jsonl` per state transition.

    `event_type` values are locked by FR-STATE-7:
        dispatch | exit_ok | exit_err | timeout | retry | block
        | resume_adopt | resume_reset | dry_run_planned
    Extending them requires a spec update.

    Fase 2: `project_id` opcional (default None). Retrocompatibilidad total
    con filas viejas de rupies escritas antes del refactor. Sitios nuevos
    que construyen EventEntry deben pasar `paths.project_id`.
    """

    event_type: str
    task_id: str
    backend: str
    ts: str
    extra: dict[str, Any] = field(default_factory=dict)
    project_id: str | None = None
