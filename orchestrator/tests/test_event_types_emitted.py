"""Every event_type literal the code emits must be in EVENT_TYPES.

Bug 9 of the Go port: `orch.py` emitted seven PR/CI event types that
`EventLog.emit` rejected with ValueError. The tests of that path used a fake
emit that did not validate, so nothing noticed. This test walks the real
emitters with `ast` so a new event type cannot be added without being
declared, and checks the real `EventLog.emit` accepts each declared type.
"""

from __future__ import annotations

import ast
from pathlib import Path

import pytest

from orchestrator.state.file_backend import EVENT_TYPES, EventLog

_PKG = Path(__file__).resolve().parents[1]
_SOURCES = sorted(
    p for p in _PKG.rglob("*.py") if "tests" not in p.parts and p.name != "__init__.py"
)


def _literal_event_types(node: ast.AST) -> set[str]:
    """Event-type literals passed as the first argument of `.emit(...)`."""
    out: set[str] = set()
    for call in ast.walk(node):
        if not isinstance(call, ast.Call) or not call.args:
            continue
        func = call.func
        if not (isinstance(func, ast.Attribute) and func.attr == "emit"):
            continue
        first = call.args[0]
        branches = [first]
        if isinstance(first, ast.IfExp):
            branches = [first.body, first.orelse]
        for b in branches:
            if isinstance(b, ast.Constant) and isinstance(b.value, str):
                out.add(b.value)
    return out


def _emitted_by_source() -> dict[Path, set[str]]:
    found: dict[Path, set[str]] = {}
    for path in _SOURCES:
        types = _literal_event_types(ast.parse(path.read_text(encoding="utf-8")))
        if types:
            found[path] = types
    return found


def test_scan_finds_the_real_emitters() -> None:
    """Guard against the scan silently matching nothing."""
    emitted = _emitted_by_source()
    all_types = set().union(*emitted.values())
    assert {"dispatch", "success", "pr_created", "ci_blocked"} <= all_types


def test_every_emitted_event_type_is_declared() -> None:
    undeclared = {
        (path.relative_to(_PKG).as_posix(), t)
        for path, types in _emitted_by_source().items()
        for t in types
        if t not in EVENT_TYPES
    }
    assert not undeclared, f"emitted but not in EVENT_TYPES: {sorted(undeclared)}"


@pytest.mark.parametrize("event_type", sorted(EVENT_TYPES))
def test_event_log_accepts_every_declared_type(tmp_path: Path, event_type: str) -> None:
    log = EventLog(tmp_path / "events.jsonl", project_id="p1")
    entry = log.emit(event_type, "T-1", backend="claude")
    assert entry.event_type == event_type


def test_ci_path_event_types_are_declared() -> None:
    """The seven that raised before the fix."""
    for t in (
        "pr_created",
        "ci_redispatch",
        "ci_success",
        "pr_auto_merged",
        "pr_auto_merge_failed",
        "ci_failure_retry",
        "ci_blocked",
    ):
        assert t in EVENT_TYPES, t
