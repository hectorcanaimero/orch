"""Unit tests for `orchestrator.checkpoints`.

Covers R-016 acceptance:
    - Parametrized `is_critical` matrix covers the four FR-C-1 branches
      (premium / migrations / edge functions / high effort) plus their
      composition (phase 10 + premium).
    - `SemiModeGate.prompt_operator` returns the correct `Decision` for
      every keystroke: `y|Y` → dispatch, `""|n|N` → defer, `s` → skip,
      `q` → quit; unrecognized re-prompts.
    - `KeyboardInterrupt` is decoded as `"quit"` so the main loop drains
      gracefully (AS-04 wording).

The gate NEVER touches `state.call_task_block` — side effects live in the
main loop; this test suite only pins the decision surface.
"""

from __future__ import annotations

import builtins
from typing import Iterable

import pytest

from orchestrator.checkpoints import (
    Decision,
    SemiModeGate,
    gate_reason,
    is_critical,
    is_high_effort,
    is_premium,
    touches_critical_files,
    touches_edge_functions,
    touches_migrations,
    touches_packages_critical,
)
from orchestrator.models import RouteEntry, Task


# ---- Fixtures ------------------------------------------------------------


def _mk_task(
    tid: str = "T-001",
    model: str = "opencode-go/glm-5.1",
    phase: int = 1,
    files: list[str] | None = None,
    hours: float = 1.0,
) -> Task:
    return Task(
        id=tid,
        phase=phase,
        title=f"{tid} title",
        description="",
        model=model,
        reason="",
        status="todo",
        dependencies=[],
        estimate_hours=hours,
        files=list(files or []),
        spec_ref="",
        comments=[],
    )


def _mk_router() -> dict[str, RouteEntry]:
    """Two-entry router: one premium, one standard."""
    return {
        "opencode/claude-opus-4-7": RouteEntry(
            backend="claude",
            cli_model="opus",
            tier="premium",
            is_premium=True,
        ),
        "opencode-go/glm-5.1": RouteEntry(
            backend="opencode",
            cli_model="zhipu/glm-5.1",
            tier="cheap",
            is_premium=False,
        ),
        "opencode/claude-sonnet-4-6": RouteEntry(
            backend="claude",
            cli_model="sonnet",
            tier="standard",
            is_premium=False,
        ),
    }


# ---- Individual predicates ----------------------------------------------


def test_is_premium_true_for_premium_route() -> None:
    router = _mk_router()
    task = _mk_task(model="opencode/claude-opus-4-7")
    assert is_premium(task, router) is True


def test_is_premium_false_for_standard_route() -> None:
    router = _mk_router()
    task = _mk_task(model="opencode-go/glm-5.1")
    assert is_premium(task, router) is False


def test_is_premium_router_miss_returns_false() -> None:
    """Startup validation already caught this — degrade gracefully."""
    task = _mk_task(model="ghost/model")
    assert is_premium(task, {}) is False


def test_touches_migrations() -> None:
    assert touches_migrations(_mk_task(files=["supabase/migrations/001.sql"])) is True
    assert touches_migrations(_mk_task(files=["packages/app/lib/foo.ts"])) is False
    assert touches_migrations(_mk_task(files=[])) is False


def test_touches_edge_functions_only_index_ts() -> None:
    # `index.ts` = edge function entry point → gate.
    assert touches_edge_functions(_mk_task(files=["supabase/functions/asaas/index.ts"])) is True
    # helper file in same dir is NOT an edge function.
    assert touches_edge_functions(_mk_task(files=["supabase/functions/asaas/helpers.ts"])) is False


def test_touches_packages_critical() -> None:
    assert touches_packages_critical(
        _mk_task(files=["packages/app/lib/auth/session.ts"])
    ) is True
    assert touches_packages_critical(
        _mk_task(files=["packages/app/lib/billing/credit.ts"])
    ) is True
    assert touches_packages_critical(
        _mk_task(files=["packages/app/lib/security/rate_limit.ts"])
    ) is True
    # NOT critical: some other lib dir.
    assert touches_packages_critical(
        _mk_task(files=["packages/app/lib/ui/button.ts"])
    ) is False


def test_is_high_effort_threshold() -> None:
    assert is_high_effort(_mk_task(hours=10.0)) is True
    assert is_high_effort(_mk_task(hours=15.5)) is True
    assert is_high_effort(_mk_task(hours=9.99)) is False
    assert is_high_effort(_mk_task(hours=0.0)) is False


# ---- is_critical composition (parametrized truth table) -----------------


@pytest.mark.parametrize(
    "model,phase,files,hours,expected,rationale",
    [
        # (a) premium → True
        ("opencode/claude-opus-4-7", 1, [], 0.5, True, "premium tier alone"),
        # (b) migrations → True
        (
            "opencode-go/glm-5.1",
            3,
            ["supabase/migrations/002_rls.sql"],
            2.0,
            True,
            "migrations",
        ),
        # (b) edge function → True
        (
            "opencode-go/glm-5.1",
            10,
            ["supabase/functions/asaas/index.ts"],
            2.0,
            True,
            "edge function",
        ),
        # (b) packages/lib/auth → True
        (
            "opencode-go/glm-5.1",
            5,
            ["packages/app/lib/auth/token.ts"],
            2.0,
            True,
            "auth package",
        ),
        # (c) phase 10 + standard → False (spec: needs premium for the phase rule)
        (
            "opencode/claude-sonnet-4-6",
            10,
            ["packages/app/lib/ui/button.ts"],
            2.0,
            False,
            "phase 10 alone with standard tier and non-critical file",
        ),
        # (c) phase 10 + premium → True (covered by (a) too — kept for symmetry)
        (
            "opencode/claude-opus-4-7",
            10,
            [],
            2.0,
            True,
            "phase 10 + premium",
        ),
        # (d) estimate hours >= 10 → True
        (
            "opencode-go/glm-5.1",
            2,
            [],
            10.0,
            True,
            "high effort",
        ),
        # None of the above → False
        (
            "opencode-go/glm-5.1",
            2,
            ["docs/foo.md"],
            2.0,
            False,
            "boring cheap task",
        ),
    ],
)
def test_is_critical_matrix(
    model: str, phase: int, files: list[str], hours: float, expected: bool, rationale: str
) -> None:
    router = _mk_router()
    task = _mk_task(model=model, phase=phase, files=files, hours=hours)
    assert is_critical(task, router) is expected, (
        f"rationale: {rationale} — got {is_critical(task, router)}"
    )


def test_gate_reason_reports_first_matching_rule() -> None:
    router = _mk_router()
    # premium overrides files
    t = _mk_task(
        model="opencode/claude-opus-4-7",
        files=["supabase/migrations/001.sql"],
    )
    assert "premium" in gate_reason(t, router).lower()

    # non-premium + migrations → migrations wins
    t = _mk_task(files=["supabase/migrations/001.sql"])
    assert "migrations" in gate_reason(t, router).lower()

    # non-premium + edge function only
    t = _mk_task(files=["supabase/functions/foo/index.ts"])
    assert "functions" in gate_reason(t, router).lower()

    # high-effort only
    t = _mk_task(hours=10.5)
    assert "estimate_hours" in gate_reason(t, router).lower()


# ---- SemiModeGate.prompt_operator ---------------------------------------


def _run_prompt(
    monkeypatch: pytest.MonkeyPatch,
    answers: Iterable[str],
) -> Decision:
    """Drive `prompt_operator` with a scripted list of `input()` responses."""
    router = _mk_router()
    gate = SemiModeGate(router=router)
    task = _mk_task(model="opencode/claude-opus-4-7", phase=10)

    it = iter(answers)

    def _fake_input(_prompt: str) -> str:
        return next(it)

    monkeypatch.setattr(builtins, "input", _fake_input)
    return gate.prompt_operator(task)


def test_prompt_operator_y_dispatches(monkeypatch: pytest.MonkeyPatch) -> None:
    assert _run_prompt(monkeypatch, ["y"]) == "dispatch"


def test_prompt_operator_capital_y_dispatches(monkeypatch: pytest.MonkeyPatch) -> None:
    assert _run_prompt(monkeypatch, ["Y"]) == "dispatch"


def test_prompt_operator_empty_defers(monkeypatch: pytest.MonkeyPatch) -> None:
    # Bare Enter → default is "defer".
    assert _run_prompt(monkeypatch, [""]) == "defer"


def test_prompt_operator_capital_n_defers(monkeypatch: pytest.MonkeyPatch) -> None:
    assert _run_prompt(monkeypatch, ["N"]) == "defer"


def test_prompt_operator_lowercase_n_defers(monkeypatch: pytest.MonkeyPatch) -> None:
    assert _run_prompt(monkeypatch, ["n"]) == "defer"


def test_prompt_operator_s_skips(monkeypatch: pytest.MonkeyPatch) -> None:
    assert _run_prompt(monkeypatch, ["s"]) == "skip"


def test_prompt_operator_q_quits(monkeypatch: pytest.MonkeyPatch) -> None:
    assert _run_prompt(monkeypatch, ["q"]) == "quit"


def test_prompt_operator_unrecognized_reprompts(monkeypatch: pytest.MonkeyPatch) -> None:
    # First keystroke is nonsense, second is `y` — must re-prompt not crash.
    assert _run_prompt(monkeypatch, ["wtf", "y"]) == "dispatch"


def test_prompt_operator_keyboard_interrupt_is_quit(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    router = _mk_router()
    gate = SemiModeGate(router=router)
    task = _mk_task(model="opencode/claude-opus-4-7", phase=10)

    def _boom(_prompt: str) -> str:
        raise KeyboardInterrupt()

    monkeypatch.setattr(builtins, "input", _boom)
    assert gate.prompt_operator(task) == "quit"


def test_prompt_operator_eof_is_quit(monkeypatch: pytest.MonkeyPatch) -> None:
    """EOFError (piped stdin closed) also drains gracefully."""
    router = _mk_router()
    gate = SemiModeGate(router=router)
    task = _mk_task(model="opencode/claude-opus-4-7", phase=10)

    def _eof(_prompt: str) -> str:
        raise EOFError()

    monkeypatch.setattr(builtins, "input", _eof)
    assert gate.prompt_operator(task) == "quit"


def test_prompt_operator_accepts_explicit_reason(monkeypatch: pytest.MonkeyPatch) -> None:
    """Caller can pre-compute the reason and skip the router lookup."""
    gate = SemiModeGate(router={})
    task = _mk_task(model="ghost/model")  # not in router

    monkeypatch.setattr(builtins, "input", lambda _p: "y")
    # Doesn't raise even though router lookup would return no premium info.
    assert gate.prompt_operator(task, reason="manual override") == "dispatch"
