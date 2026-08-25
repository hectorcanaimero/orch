"""Unit tests for `orchestrator.router`.

Covers R-006 acceptance:
    - `load_router` parses YAML → `dict[str, RouteEntry]` and computes
      `is_premium` from `tier`.
    - `validate_all_models` raises `UnroutedModelError` listing every
      offending `(task_id, model)` pair (FR-D-6 / AS-06).
    - Format errors (bad backend, bad tier) surface as `RouterFormatError`.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.models import Task
from orchestrator.router import (
    RouterFormatError,
    UnroutedModelError,
    load_router,
    validate_all_models,
)


ROUTER_PATH = Path(__file__).parent.parent / "model_router.yaml"


def _mk_task(tid: str, model: str) -> Task:
    return Task(
        id=tid,
        phase=1,
        title=tid,
        description="",
        model=model,
        reason="",
        status="todo",
        dependencies=[],
        estimate_hours=0.1,
        files=[],
        spec_ref="",
        comments=[],
    )


# ---- load_router --------------------------------------------------------


def test_load_router_shipping_file() -> None:
    router = load_router(ROUTER_PATH)
    # tasks.json references 16 unique models; the router is a SUPERSET —
    # after the 2026-08-20 accepted-model audit it also holds routes for
    # new models not yet referenced in tasks.json. Assert the floor, not
    # equality, so future adds don't break this test.
    assert len(router) >= 16
    # Spot-check a premium entry.
    opus = router["claude/claude-opus-4-7"]
    assert opus.backend == "claude"
    assert opus.tier == "premium"
    assert opus.is_premium is True
    # Cheap entry — is_premium must be False.
    glm = router["opencode-go/glm-5.1"]
    assert glm.backend == "opencode"
    assert glm.tier == "cheap"
    assert glm.is_premium is False


def test_load_router_computes_is_premium_even_if_yaml_omits(tmp_path: Path) -> None:
    """YAML flag is denormalized; loader owns the truth."""
    yaml_path = tmp_path / "r.yaml"
    yaml_path.write_text(
        "some/model:\n"
        "  backend: claude\n"
        "  cli_model: foo\n"
        "  tier: premium\n"  # no is_premium key
    )
    router = load_router(yaml_path)
    assert router["some/model"].is_premium is True


def test_load_router_rejects_bad_backend(tmp_path: Path) -> None:
    p = tmp_path / "bad.yaml"
    p.write_text(
        "m:\n  backend: nope\n  cli_model: x\n  tier: cheap\n  is_premium: false\n"
    )
    with pytest.raises(RouterFormatError, match="invalid backend"):
        load_router(p)


def test_load_router_rejects_bad_tier(tmp_path: Path) -> None:
    p = tmp_path / "bad.yaml"
    p.write_text(
        "m:\n  backend: claude\n  cli_model: x\n  tier: gold\n  is_premium: false\n"
    )
    with pytest.raises(RouterFormatError, match="invalid tier"):
        load_router(p)


def test_load_router_rejects_missing_cli_model(tmp_path: Path) -> None:
    p = tmp_path / "bad.yaml"
    p.write_text(
        "m:\n  backend: claude\n  cli_model: ''\n  tier: cheap\n  is_premium: false\n"
    )
    with pytest.raises(RouterFormatError, match="cli_model"):
        load_router(p)


# ---- validate_all_models -----------------------------------------------


def test_validate_all_models_passes_on_shipping_router() -> None:
    """Real project data must validate against the shipped router — no drift.

    Skipped when there's no `tasks.json` at the project root: this repo
    can be checked out standalone (orch as a tool) or under a project like
    rupies/v2. The drift guard is only meaningful when a real tasks.json
    lives alongside the router.
    """
    import pytest

    from orchestrator.task_queue import load_tasks

    tasks_json = Path(__file__).parent.parent.parent / "tasks.json"
    if not tasks_json.exists():
        pytest.skip(f"no tasks.json at {tasks_json} (standalone orch checkout)")
    tasks = load_tasks(tasks_json)
    router = load_router(ROUTER_PATH)
    # Must not raise.
    validate_all_models(tasks, router)


def test_validate_all_models_raises_on_missing_model() -> None:
    router = load_router(ROUTER_PATH)
    tasks = [
        _mk_task("R-XYZ", "unknown/model-9000"),
        _mk_task("R-ABC", "opencode-go/glm-5.1"),  # good
    ]
    with pytest.raises(UnroutedModelError) as exc:
        validate_all_models(tasks, router)
    msg = str(exc.value)
    # Offender id + model both present in the message.
    assert "R-XYZ" in msg
    assert "unknown/model-9000" in msg
    # Good one is not mentioned.
    assert "R-ABC" not in msg
    # Structured access for programmatic handling.
    assert exc.value.offenders == [("R-XYZ", "unknown/model-9000")]


def test_validate_all_models_lists_every_offender() -> None:
    """Operator gets ALL offenders in one shot — not incremental."""
    router = load_router(ROUTER_PATH)
    tasks = [
        _mk_task("R-002", "ghost/one"),
        _mk_task("R-001", "ghost/two"),
        _mk_task("R-003", "opencode-go/glm-5.1"),  # good
    ]
    with pytest.raises(UnroutedModelError) as exc:
        validate_all_models(tasks, router)
    ids = [tid for tid, _ in exc.value.offenders]
    # Sorted for stable output.
    assert ids == ["R-001", "R-002"]


# ---- escalation_model validation (FR-D-8) --------------------------------


def test_load_router_parses_escalation_model(tmp_path: Path) -> None:
    """FR-D-8: `escalation_model` is an optional per-route field. It carries
    another route KEY (not a raw cli_model) that the retry logic promotes to
    on attempt 3.
    """
    p = tmp_path / "r.yaml"
    p.write_text(
        "primary/key:\n"
        "  backend: opencode\n"
        "  cli_model: foo/bar\n"
        "  tier: cheap\n"
        "  is_premium: false\n"
        "  escalation_model: escalation/key\n"
        "escalation/key:\n"
        "  backend: opencode\n"
        "  cli_model: baz/qux\n"
        "  tier: cheap\n"
        "  is_premium: false\n",
        encoding="utf-8",
    )
    router = load_router(p)
    assert router["primary/key"].escalation_model == "escalation/key"
    assert router["escalation/key"].escalation_model is None


def test_load_router_rejects_escalation_pointing_to_missing_route(
    tmp_path: Path,
) -> None:
    """FR-D-8: escalation_model must point to an existing route key. Fail
    startup with a clean error listing the offender — mirrors the fallback
    validation pattern (fail-fast, no dispatch on drift).
    """
    p = tmp_path / "r.yaml"
    p.write_text(
        "primary/key:\n"
        "  backend: opencode\n"
        "  cli_model: foo/bar\n"
        "  tier: cheap\n"
        "  is_premium: false\n"
        "  escalation_model: nowhere/missing\n",
        encoding="utf-8",
    )
    with pytest.raises(RouterFormatError, match="escalation_model"):
        load_router(p)


def test_load_router_shipping_file_has_escalation_on_seed_routes() -> None:
    """The shipping router.yaml gets escalation_model on 3 seed routes
    (approved: glm-5.1, gemini-3.0-pro, mimo-v2.5). Guard so future edits
    don't accidentally drop them.
    """
    router = load_router(ROUTER_PATH)
    assert router["opencode-go/glm-5.1"].escalation_model == "opencode-go/kimi-k2.6"
    assert router["opencode/gemini-3.0-pro"].escalation_model == "opencode/deepseek-v4-pro"
    assert router["opencode-go/mimo-v2.5"].escalation_model == "opencode-go/qwen3.7-plus"
