"""Semi-mode gate for the Rupies v2 orchestrator.

Contracts (from `orchestrator/spec.md` §1.6 and `orchestrator/design.md` §8):

- Predicate `is_critical(task, router)` composes the four rules from FR-C-1:
    (a) `route.is_premium` (opus, gpt-5.6, gemini-3.0-pro, deepseek-v4-pro).
    (b) `files[]` intersects `supabase/migrations/**`,
        `supabase/functions/*/index.ts`, or
        `packages/*/lib/{auth,billing,security}/**`.
    (c) `phase == 10 AND route.is_premium` (backend crit).
    (d) `estimate_hours >= 10`.

- `SemiModeGate.prompt_operator(task)` is the ONLY place the module reads
  stdin. In `--mode auto` the main loop never constructs `SemiModeGate`, so
  the whole module stays inert (FR-C-3).

- Answer decoding (AS-04):
    y / Y            → "dispatch"
    "" (Enter) / n   → "defer"    (re-prompt at end of run)
    s                → "skip"     (permanent block via task-block.sh)
    q                → "quit"     (drain in-flight, exit 0)
  Any other key re-prompts.

This module deliberately does NOT invoke `task-block.sh` — that lives in
`state.call_task_block`. `prompt_operator` just returns a decision string;
the main loop wires the side effect.
"""

from __future__ import annotations

import logging
from typing import Iterable, Literal

from .models import RouteEntry, Task

log = logging.getLogger(__name__)


# ---- Rich detection (optional dep) --------------------------------------


try:  # pragma: no cover — optional runtime dep
    from rich import print as _rich_print  # type: ignore[import-not-found]
    _HAVE_RICH = True
except ImportError:  # pragma: no cover
    _rich_print = print  # type: ignore[assignment]
    _HAVE_RICH = False


# ---- Predicate constants (locked to FR-C-1) -----------------------------


# Path fragments that flag a task as touching critical infrastructure.
# Kept literal (not regex) so the tests can trace exactly which fragment
# matched.
_CRITICAL_PATH_FRAGMENTS: tuple[str, ...] = (
    "supabase/migrations/",
    "packages/",  # narrowed further by `_touches_packages_critical`
)


_PACKAGES_CRITICAL_SUFFIXES: tuple[str, ...] = (
    "/lib/auth/",
    "/lib/billing/",
    "/lib/security/",
)


_HIGH_EFFORT_HOURS_THRESHOLD = 10.0


# Decision surface (Literal so `main loop` gets a discriminated union).
Decision = Literal["dispatch", "defer", "skip", "quit"]


# ---- Predicates ----------------------------------------------------------


def is_premium(task: Task, router: dict[str, RouteEntry]) -> bool:
    """True iff the task's router entry is `tier: premium`.

    Router-miss returns False rather than raising — the whole run has
    already failed startup validation (FR-D-6) if a task's model has no
    route, so hitting this branch here means the caller passed an out-of-
    band Task; we degrade to False.
    """
    route = router.get(task.model)
    if route is None:
        return False
    return bool(route.is_premium)


def touches_migrations(task: Task) -> bool:
    """True iff any declared file lives under `supabase/migrations/`."""
    for f in task.files or ():
        if "supabase/migrations/" in f:
            return True
    return False


def touches_edge_functions(task: Task) -> bool:
    """True iff any declared file is `supabase/functions/*/index.ts`."""
    for f in task.files or ():
        # Match the SPEC pattern exactly: only `index.ts` inside a function
        # directory counts (other files in the same dir are helpers).
        if "supabase/functions/" in f and f.endswith("/index.ts"):
            return True
    return False


def touches_packages_critical(task: Task) -> bool:
    """True iff a file lives under `packages/*/lib/{auth,billing,security}/`."""
    for f in task.files or ():
        if not f.startswith("packages/"):
            continue
        # Any of the critical suffixes must appear AFTER a `packages/<pkg>/`
        # prefix. Substring match is sufficient because `_PACKAGES_CRITICAL_SUFFIXES`
        # includes the leading `/lib/`.
        for suffix in _PACKAGES_CRITICAL_SUFFIXES:
            if suffix in f:
                return True
    return False


def touches_critical_files(task: Task) -> bool:
    """Union of the three file-based predicates from FR-C-1(b)."""
    return (
        touches_migrations(task)
        or touches_edge_functions(task)
        or touches_packages_critical(task)
    )


def is_high_effort(task: Task) -> bool:
    """True iff `estimate_hours >= 10` (FR-C-1d)."""
    return (task.estimate_hours or 0.0) >= _HIGH_EFFORT_HOURS_THRESHOLD


def is_critical(task: Task, router: dict[str, RouteEntry]) -> bool:
    """Compose the four FR-C-1 rules into a single gate predicate.

    Returns True iff ANY of:
      (a) router entry is `tier: premium`.
      (b) task touches migrations / edge functions / critical packages.
      (c) `phase == 10 AND route.is_premium` (subsumed by (a) — kept for
          symmetry with the spec).
      (d) `estimate_hours >= 10`.

    Kept as a single call so the main loop stays a one-liner:
        if mode == "semi" and is_critical(task, router): gate.prompt_operator(task)
    """
    return (
        is_premium(task, router)
        or touches_critical_files(task)
        or (task.phase == 10 and is_premium(task, router))
        or is_high_effort(task)
    )


def gate_reason(task: Task, router: dict[str, RouteEntry]) -> str:
    """Human-readable reason string for the operator prompt (AS-04).

    Only the first matching rule is reported — enough for the operator to
    know why the gate fired. Order matches FR-C-1.
    """
    if is_premium(task, router):
        return f"premium tier ({router[task.model].tier})"
    if touches_migrations(task):
        return "touches supabase/migrations/**"
    if touches_edge_functions(task):
        return "touches supabase/functions/**/index.ts"
    if touches_packages_critical(task):
        return "touches packages/*/lib/{auth,billing,security}/**"
    if is_high_effort(task):
        return f"estimate_hours={task.estimate_hours} >= {_HIGH_EFFORT_HOURS_THRESHOLD}"
    return "critical"


# ---- Gate ---------------------------------------------------------------


class SemiModeGate:
    """Blocks the main loop on stdin for critical tasks (AS-04 / FR-C-2).

    The gate is stateless w.r.t. the queue — decision handling (defer, skip,
    quit) is the main loop's job. `prompt_operator` just decodes the
    keystroke into a `Decision` string.
    """

    def __init__(self, router: dict[str, RouteEntry] | None = None):
        # `router` is optional — only used to render the "premium" reason
        # in the prompt. `prompt_operator` accepts a pre-computed reason
        # for tests / callers that already know it.
        self._router = router or {}

    # ---- rendering ------------------------------------------------------

    def _summary_lines(self, task: Task, reason: str) -> list[str]:
        """Build the header lines shown before the y/N/s/q prompt."""
        model_line = f"  model: {task.model}"
        files = list(task.files or [])
        files_line = "  files: " + (", ".join(files) if files else "[]")
        return [
            "",
            f"=== SEMI GATE — {task.id} [phase {task.phase}] ===",
            f"  {task.title}",
            model_line,
            files_line,
            f"  est: {task.estimate_hours}h",
            f"  reason: {reason}",
        ]

    # ---- prompt ---------------------------------------------------------

    def prompt_operator(self, task: Task, reason: str | None = None) -> Decision:
        """Show the task summary and block on one keystroke.

        Returns one of the four `Decision` literals. Any unrecognized input
        re-prompts (defensive: an operator might fat-finger `x`). A
        `KeyboardInterrupt` from Ctrl-C is treated as `"quit"` so the main
        loop can drain cleanly (AS-04 wording).
        """
        gate_reason_str = reason or gate_reason(task, self._router)
        for line in self._summary_lines(task, gate_reason_str):
            _rich_print(line)

        while True:
            try:
                raw = input(
                    "[y]dispatch / [N]defer / [s]skip-permanently / [q]uit > "
                )
            except (EOFError, KeyboardInterrupt):
                # SIGINT / EOF → treat as "quit" so main loop drains gracefully.
                _rich_print("")  # newline so the shell prompt starts on a fresh line
                return "quit"

            answer = (raw or "").strip().lower()
            if answer == "y":
                return "dispatch"
            if answer in ("", "n"):
                return "defer"
            if answer == "s":
                return "skip"
            if answer == "q":
                return "quit"
            _rich_print("  (unrecognized — please answer y / n / s / q)")


__all__ = [
    "Decision",
    "SemiModeGate",
    "gate_reason",
    "is_critical",
    "is_high_effort",
    "is_premium",
    "touches_critical_files",
    "touches_edge_functions",
    "touches_migrations",
    "touches_packages_critical",
]
