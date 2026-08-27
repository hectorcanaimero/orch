"""Tests for `orch router add-missing` core logic (issue #55).

Covers the pure helpers in orchestrator.router that infer RouteEntry rows from
`backend/cli_model` keys, dedupe missing models, and append them to the YAML
without clobbering existing content or comments.
"""

from __future__ import annotations

from pathlib import Path

from orchestrator.models import Task
from orchestrator.router import (
    RouteEntry,
    append_router_entries,
    infer_route_entry,
    load_router,
    missing_models,
    validate_all_models,
)


# ---- infer_route_entry ------------------------------------------------------


def test_infer_splits_backend_and_cli_model():
    entry = infer_route_entry("claude/claude-haiku-4-5")
    assert entry is not None
    assert entry.backend == "claude"
    assert entry.cli_model == "claude-haiku-4-5"
    assert entry.tier == "standard"      # safe default
    assert entry.is_premium is False


def test_infer_respects_default_tier_override():
    entry = infer_route_entry("codex/gpt-5.6", default_tier="premium")
    assert entry is not None
    assert entry.tier == "premium"
    assert entry.is_premium is True


def test_infer_keeps_nested_slashes_in_cli_model():
    # opencode pass-through keys can carry a vendor path after the backend.
    entry = infer_route_entry("opencode/deepseek/v4-pro")
    assert entry is not None
    assert entry.backend == "opencode"
    assert entry.cli_model == "deepseek/v4-pro"


def test_infer_returns_none_for_bare_model_without_slash():
    assert infer_route_entry("claude-haiku-4-5") is None


def test_infer_returns_none_for_unknown_backend():
    assert infer_route_entry("openai/gpt-5.6") is None


def test_infer_returns_none_for_empty_cli_model():
    assert infer_route_entry("claude/") is None


# ---- missing_models ---------------------------------------------------------


def test_missing_models_dedupes_and_sorts():
    router = {"claude/opus": _entry()}
    tasks = [
        _mk_task("T3", "claude/haiku"),
        _mk_task("T1", "claude/haiku"),
        _mk_task("T2", "codex/gpt"),
        _mk_task("T4", "claude/opus"),  # present
    ]
    assert missing_models(tasks, router) == ["claude/haiku", "codex/gpt"]


def test_missing_models_empty_when_all_present():
    router = {"claude/opus": _entry()}
    tasks = [_mk_task("T1", "claude/opus")]
    assert missing_models(tasks, router) == []


# ---- append_router_entries --------------------------------------------------


def test_append_adds_new_entries_and_preserves_existing(tmp_path: Path):
    path = tmp_path / "model_router.yaml"
    path.write_text(
        "# my header comment\n"
        "claude/opus:\n"
        "  backend: claude\n"
        "  cli_model: claude-opus-4-7\n"
        "  tier: premium\n",
        encoding="utf-8",
    )
    added = append_router_entries(
        path,
        {"claude/haiku": infer_route_entry("claude/claude-haiku-4-5")},
    )
    assert added == ["claude/haiku"]

    text = path.read_text(encoding="utf-8")
    assert "# my header comment" in text          # comment preserved
    assert "claude/opus:" in text                 # existing entry preserved
    assert "claude/haiku:" in text                # new entry appended

    # Re-parse: the file must still be a valid, loadable router.
    router = load_router(path)
    assert "claude/haiku" in router
    assert router["claude/haiku"].cli_model == "claude-haiku-4-5"
    assert router["claude/opus"].tier == "premium"  # untouched


def test_append_skips_keys_already_present(tmp_path: Path):
    path = tmp_path / "model_router.yaml"
    path.write_text(
        "claude/opus:\n  backend: claude\n  cli_model: claude-opus-4-7\n  tier: premium\n",
        encoding="utf-8",
    )
    added = append_router_entries(path, {"claude/opus": _entry()})
    assert added == []                            # nothing appended
    # File must not have grown a duplicate key.
    assert path.read_text(encoding="utf-8").count("claude/opus:") == 1


def test_appended_entries_make_validate_pass(tmp_path: Path):
    path = tmp_path / "model_router.yaml"
    path.write_text(
        "claude/opus:\n  backend: claude\n  cli_model: claude-opus-4-7\n  tier: premium\n",
        encoding="utf-8",
    )
    tasks = [_mk_task("T1", "claude/haiku")]
    router = load_router(path)
    missing = missing_models(tasks, router)
    append_router_entries(path, {m: infer_route_entry(m) for m in missing})
    # Reload and validate — must no longer raise.
    validate_all_models(tasks, load_router(path))  # no exception == pass


def _entry() -> RouteEntry:
    return RouteEntry(
        backend="claude", cli_model="claude-opus-4-7", tier="premium", is_premium=True
    )


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
