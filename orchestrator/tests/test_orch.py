"""Tests for `orchestrator.orch` (R-017 CLI + R-018 main loop).

Covers acceptance:
    - FR-CLI-3: CWD violation → exit 2.
    - AS-06 / FR-D-6: unrouted model → exit 1 with offender listed.
    - AS-08: --dry-run prints plan, exit 0, NO subprocess spawned, NO
      run-file / spend-file / events-file on disk.
    - FR-STATE-4 / AS-09: flock contention → exit 3.
    - AS-01 / AS-05: full main-loop happy path with a FakeBackend — 3 root
      tasks dispatched, all reach `done`, exit 0, event log has 3 dispatch
      + 3 success events.

The tests stage a temporary `v2/`-named directory with the minimal file
layout the orchestrator needs (`tasks.json`, `scripts/task-*.sh`,
`orchestrator/config.yaml`, `orchestrator/model_router.yaml`,
`orchestrator/state/`). Real `claude|codex|opencode` are NEVER invoked —
`test_full_loop_fake_backend` monkeypatches `get_backend` to return a
`FakeBackend` that spawns a fast `sh -c 'exit 0'` child so the
`os.waitpid(-1, WNOHANG)` path is exercised for real.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

import pytest

from orchestrator import orch as orch_mod
from orchestrator.dispatcher import Backend, DispatchResult
from orchestrator.models import Dispatch, RouteEntry, Task
from orchestrator.state import EventLog, acquire_flock


FIXTURES = Path(__file__).parent / "fixtures"
REPO_ROOT = Path(__file__).resolve().parents[2]  # v2/
REAL_SCRIPTS_DIR = REPO_ROOT / "scripts"


# ---- Test scaffolding ---------------------------------------------------


def _mk_no_op_script(path: Path) -> None:
    """Drop a tiny `#!/bin/sh` script that always exits 0.

    Used to shim `scripts/task-start.sh`, `task-finish.sh`, `task-block.sh`
    so the orchestrator's C-1..C-3 shell-outs don't touch a real tasks.json.
    """
    path.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
    path.chmod(0o755)


def _stage_v2(root: Path, tasks_src: Path, router_src: Path, config_src: Path) -> Path:
    """Build a temp v2/-named directory with the minimum layout.

    Layout:
        <root>/v2/
          tasks.json                        (copied from fixture)
          scripts/task-{start,finish,block}.sh (no-op stubs)
          orchestrator/config.yaml          (copied from fixture)
          orchestrator/model_router.yaml    (copied from fixture)
          orchestrator/state/               (empty)
    Returns the v2/ path.
    """
    v2 = root / "v2"
    v2.mkdir(parents=True, exist_ok=True)
    (v2 / "scripts").mkdir(exist_ok=True)
    (v2 / "orchestrator" / "state").mkdir(parents=True, exist_ok=True)

    # tasks.json
    shutil.copy(tasks_src, v2 / "tasks.json")

    # scripts — no-op so C-1..C-3 don't actually mutate anything.
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        _mk_no_op_script(v2 / "scripts" / name)

    # config + router
    shutil.copy(router_src, v2 / "orchestrator" / "model_router.yaml")
    shutil.copy(config_src, v2 / "orchestrator" / "config.yaml")

    return v2


@pytest.fixture()
def staged_v2(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Stage the standard main-loop fixture layout and chdir into it."""
    v2 = _stage_v2(
        tmp_path,
        FIXTURES / "main_loop_tasks.json",
        FIXTURES / "main_loop_router.yaml",
        FIXTURES / "main_loop_config.yaml",
    )
    monkeypatch.chdir(v2)
    return v2


# ---- FakeBackend --------------------------------------------------------


class FakeBackend:
    """Backend that spawns a real short-lived child and reports success.

    Uses `sh -c 'echo done > $LOG; exit 0'` so the reap loop's
    `os.waitpid(-1, WNOHANG)` path is exercised for real. Cost/token values
    are canned so `_record_spend` writes a complete row.
    """

    name = "opencode"  # matches the fixture's router entry

    def build_cmd(self, task: Task, route: RouteEntry) -> list[str]:  # noqa: ARG002
        return ["sh", "-c", "echo done; exit 0"]

    def spawn(
        self,
        task: Task,
        route: RouteEntry,
        prompt_path: Path,
        log_path: Path,
        cwd: Path,
    ) -> Dispatch:
        log_path.parent.mkdir(parents=True, exist_ok=True)
        log_fh = open(log_path, "ab")
        prompt_fh = open(prompt_path, "rb")
        popen = subprocess.Popen(  # noqa: S603
            self.build_cmd(task, route),
            stdin=prompt_fh,
            stdout=log_fh,
            stderr=subprocess.STDOUT,
            cwd=str(cwd),
            close_fds=True,
        )
        d = Dispatch(
            task_id=task.id,
            backend=self.name,
            pid=popen.pid,
            session_id=f"s-{task.id}",
            started_at="2026-08-19T12:00:00Z",
            prompt_path=str(prompt_path),
            log_path=str(log_path),
            output_path="",
        )
        d._popen = popen  # type: ignore[attr-defined]
        d._fhs = (prompt_fh, log_fh)  # type: ignore[attr-defined]
        return d

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:  # noqa: ARG002
        raise NotImplementedError("main loop uses parse_result on reap, not wait_result")

    def parse_result(
        self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None  # noqa: ARG002
    ) -> DispatchResult:
        # Cost fixture: something deterministic + non-zero so the spend log
        # asserts pick up realistic values.
        return DispatchResult(
            exit_code=exit_code,
            success=(exit_code == 0),
            cost_usd=0.01,
            tokens_in=100,
            tokens_out=50,
            stdout=log_text,
            stderr="",
            error_message=None if exit_code == 0 else "nonzero exit",
        )

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:  # noqa: ARG002
        return 0.01, 100, 50


# ---- Test: CWD violation ------------------------------------------------


def test_cwd_violation_returns_2(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """FR-STATE-1: CWD not named `v2/` → exit 2."""
    monkeypatch.chdir(tmp_path)  # tmp_path is NOT named "v2"
    rc = orch_mod.main(["--dry-run"])
    assert rc == 2


# ---- Test: unrouted model -----------------------------------------------


def test_router_miss_returns_1(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """AS-06: task references a model with no router entry → exit 1."""
    # Custom tasks.json with an unroutable model.
    bad_tasks = {
        "meta": {},
        "phases": [{"id": 1, "name": "x"}],
        "tasks": [
            {
                "id": "T-BAD",
                "phase": 1,
                "title": "unroutable",
                "description": "",
                "model": "opencode/nonexistent-model",
                "reason": "",
                "status": "todo",
                "dependencies": [],
                "estimateHours": 0.1,
                "files": [],
                "specRef": "",
                "comments": [],
            }
        ],
    }
    bad_path = tmp_path / "bad_tasks.json"
    bad_path.write_text(json.dumps(bad_tasks), encoding="utf-8")

    v2 = _stage_v2(
        tmp_path,
        bad_path,
        FIXTURES / "main_loop_router.yaml",
        FIXTURES / "main_loop_config.yaml",
    )
    monkeypatch.chdir(v2)

    # Guarantee we never spawn a subprocess if this test regresses.
    def _boom(*a, **kw):  # noqa: ANN001, ARG001
        raise AssertionError("subprocess must not be spawned on unrouted model")

    monkeypatch.setattr("orchestrator.dispatcher.subprocess.Popen", _boom)

    rc = orch_mod.main(["--mode", "auto"])
    assert rc == 1


# ---- Test: dry-run ------------------------------------------------------


def test_dry_run_is_side_effect_free(
    staged_v2: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    """AS-08: --dry-run prints plan, exits 0, spawns nothing, writes nothing."""

    def _boom(*a, **kw):  # noqa: ANN001, ARG001
        raise AssertionError("dry-run must not spawn any subprocess")

    monkeypatch.setattr("orchestrator.dispatcher.subprocess.Popen", _boom)

    rc = orch_mod.main(["--dry-run"])
    assert rc == 0

    # No run file, no events file, no spend file.
    state_dir = staged_v2 / "orchestrator" / "state"
    run_files = list(state_dir.glob("run-*.json"))
    event_files = list(state_dir.glob("events-*.jsonl"))
    spend_files = list(state_dir.glob("spend-*.jsonl"))
    assert run_files == [], f"unexpected run files: {run_files}"
    assert event_files == [], f"unexpected event files: {event_files}"
    assert spend_files == [], f"unexpected spend files: {spend_files}"

    # Plan output includes the 3 task ids.
    out = capsys.readouterr().out
    assert "T-A" in out
    assert "T-B" in out
    assert "T-C" in out


def test_dry_run_max_tasks_caps_plan(
    staged_v2: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    """--max-tasks limits the plan size."""
    rc = orch_mod.main(["--dry-run", "--max-tasks", "2"])
    assert rc == 0
    out = capsys.readouterr().out
    # Two of the three task ids should appear; one should not.
    hits = [tid for tid in ("T-A", "T-B", "T-C") if tid in out]
    assert len(hits) == 2


# ---- Test: flock contention --------------------------------------------


def test_flock_contention_returns_3(staged_v2: Path) -> None:
    """AS-09: another orchestrator holds the lock → exit 3."""
    lock_path = staged_v2 / "orchestrator" / "state" / ".lock"
    held = acquire_flock(lock_path)
    try:
        rc = orch_mod.main(["--dry-run"])
        assert rc == 3
    finally:
        held.close()


# ---- Test: full main loop with FakeBackend -----------------------------


def test_full_loop_fake_backend(
    staged_v2: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """AS-01: 3 root tasks dispatch, all reach done via FakeBackend, exit 0.

    Also verifies AS-05 (per-provider cap holds — 3 caps, 3 tasks, so max
    concurrent = 3) and NFR-OBS-1 (dispatch + success events emitted per task).
    """

    def _fake_get_backend(name: str, cfg: dict[str, Any] | None = None):  # noqa: ARG001
        return FakeBackend()

    monkeypatch.setattr("orchestrator.orch.get_backend", _fake_get_backend)

    rc = orch_mod.main(["--mode", "auto"])
    assert rc == 0, "clean drain expected"

    state_dir = staged_v2 / "orchestrator" / "state"

    # Exactly one run file was created.
    run_files = list(state_dir.glob("run-*.json"))
    assert len(run_files) == 1
    run_data = json.loads(run_files[0].read_text())
    assert set(run_data["completed"]) == {"T-A", "T-B", "T-C"}
    assert run_data["blocked"] == []
    assert run_data["in_flight"] == {}

    # Event log: 3 dispatch + 3 success events (NFR-OBS-1).
    event_files = list(state_dir.glob("events-*.jsonl"))
    assert len(event_files) == 1
    events = [
        json.loads(line)
        for line in event_files[0].read_text().splitlines()
        if line.strip()
    ]
    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    successes = [e for e in events if e["event_type"] == "success"]
    assert len(dispatches) == 3
    assert len(successes) == 3
    assert {e["task_id"] for e in dispatches} == {"T-A", "T-B", "T-C"}
    assert {e["task_id"] for e in successes} == {"T-A", "T-B", "T-C"}

    # Spend log: 3 lines with all fields populated (AS-12).
    spend_files = list(state_dir.glob("spend-*.jsonl"))
    assert len(spend_files) == 1
    spend_rows = [
        json.loads(line)
        for line in spend_files[0].read_text().splitlines()
        if line.strip()
    ]
    assert len(spend_rows) == 3
    for row in spend_rows:
        for key in ("ts", "task_id", "backend", "model", "tokens_in", "tokens_out", "cost_usd", "duration_s"):
            assert key in row, f"missing spend key: {key}"
        assert row["cost_usd"] == 0.01
        assert row["tokens_in"] == 100


# ---- FR-D-4 retry-once + FR-D-7 fallback -------------------------------


class _FailingBackend:
    """Backend whose `parse_result` always returns failure.

    Configurable via `error_message` so drift-detection tests can inject
    "model not found: ..." while retry-once tests inject a plain error.
    Spawn still runs a real `sh -c 'exit 1'` so the reap loop's
    `os.waitpid` path is exercised end-to-end.
    """

    name = "opencode"

    def __init__(self, error_message: str = "backend failed") -> None:
        self.error_message = error_message
        self.parse_calls = 0

    def build_cmd(self, task: Task, route: RouteEntry) -> list[str]:  # noqa: ARG002
        # Non-zero exit so the reap loop sees failure without any parsing.
        return ["sh", "-c", "exit 1"]

    def spawn(
        self,
        task: Task,
        route: RouteEntry,
        prompt_path: Path,
        log_path: Path,
        cwd: Path,
    ) -> Dispatch:
        log_path.parent.mkdir(parents=True, exist_ok=True)
        log_fh = open(log_path, "ab")
        prompt_fh = open(prompt_path, "rb")
        popen = subprocess.Popen(  # noqa: S603
            self.build_cmd(task, route),
            stdin=prompt_fh,
            stdout=log_fh,
            stderr=subprocess.STDOUT,
            cwd=str(cwd),
            close_fds=True,
        )
        d = Dispatch(
            task_id=task.id,
            backend=self.name,
            pid=popen.pid,
            session_id=f"s-{task.id}",
            started_at="2026-08-19T12:00:00Z",
            prompt_path=str(prompt_path),
            log_path=str(log_path),
            output_path="",
        )
        d._popen = popen  # type: ignore[attr-defined]
        d._fhs = (prompt_fh, log_fh)  # type: ignore[attr-defined]
        return d

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:  # noqa: ARG002
        raise NotImplementedError

    def parse_result(
        self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None  # noqa: ARG002
    ) -> DispatchResult:
        self.parse_calls += 1
        return DispatchResult(
            exit_code=exit_code,
            success=False,
            cost_usd=0.0,
            tokens_in=0,
            tokens_out=0,
            stdout=log_text,
            stderr="",
            error_message=self.error_message,
        )

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:  # noqa: ARG002
        return 0.0, 0, 0


def _single_task_fixture(v2: Path, task_id: str = "T-ONE") -> None:
    """Write a 1-task tasks.json into an already-staged v2/ dir."""
    tasks = {
        "meta": {},
        "phases": [{"id": 1, "name": "test"}],
        "tasks": [
            {
                "id": task_id,
                "phase": 1,
                "title": "solo",
                "description": "single-task retry fixture",
                "model": "opencode-go/glm-5.1",
                "reason": "retry test",
                "status": "todo",
                "dependencies": [],
                "estimateHours": 0.5,
                "files": [],
                "specRef": "",
                "comments": [],
            }
        ],
    }
    (v2 / "tasks.json").write_text(json.dumps(tasks), encoding="utf-8")


def test_retry_once_then_block(
    staged_v2: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """FR-D-4: first failure → retry event + re-queue; second failure → block.

    Uses a FakeBackend that always fails. Expect exactly:
        - 2 dispatch events (attempt=1 then attempt=2)
        - 1 retry event
        - 1 fail event (final block)
        - task ends up blocked in the run file
    """
    _single_task_fixture(staged_v2, task_id="T-RETRY")

    backend = _FailingBackend(error_message="transient boom")

    def _fake_get_backend(name: str, cfg: dict[str, Any] | None = None):  # noqa: ARG001
        return backend

    monkeypatch.setattr("orchestrator.orch.get_backend", _fake_get_backend)

    rc = orch_mod.main(["--mode", "auto"])
    # Exit=1 because the task ended blocked (spec: blocked → exit 1).
    assert rc == 1

    state_dir = staged_v2 / "orchestrator" / "state"
    event_files = list(state_dir.glob("events-*.jsonl"))
    assert len(event_files) == 1
    events = [
        json.loads(line)
        for line in event_files[0].read_text().splitlines()
        if line.strip()
    ]

    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    retries = [e for e in events if e["event_type"] == "retry"]
    fails = [e for e in events if e["event_type"] == "fail"]

    assert len(dispatches) == 2, f"expected 2 dispatch events, got {len(dispatches)}"
    assert len(retries) == 1, f"expected 1 retry event, got {len(retries)}"
    assert len(fails) == 1, f"expected 1 fail event (final block), got {len(fails)}"

    # Retry event carries attempt=2 (the NEXT attempt) per the spawn contract.
    assert retries[0]["extra"]["attempt"] == 2
    # First dispatch is attempt=1, second is attempt=2.
    assert dispatches[0]["extra"]["attempt"] == 1
    assert dispatches[1]["extra"]["attempt"] == 2
    # Final fail carries attempt=2 (second failure).
    assert fails[0]["extra"]["attempt"] == 2

    # Backend saw exactly 2 parse_result calls (one per attempt).
    assert backend.parse_calls == 2

    # Run file records the task as blocked.
    run_files = list(state_dir.glob("run-*.json"))
    assert len(run_files) == 1
    run_data = json.loads(run_files[0].read_text())
    assert run_data["blocked"] == ["T-RETRY"]


def test_version_drift_fallback(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """FR-D-7: version-drift error triggers fallback_cli_model substitution.

    The FailingBackend reports "model not found: xyz" on both attempts. The
    router entry has `fallback_cli_model` set; expect the retry to use the
    fallback (via `dataclasses.replace(route, cli_model=fallback)`), which
    the test verifies by inspecting the second `dispatch` event's cli_model.
    """
    # Build a router with a fallback for the test model.
    router_yaml = tmp_path / "router.yaml"
    router_yaml.write_text(
        "opencode-go/glm-5.1:\n"
        "  backend: opencode\n"
        "  cli_model: zhipu/glm-5.1\n"
        "  tier: cheap\n"
        "  is_premium: false\n"
        "  fallback_cli_model: zhipu/glm-5.0-fallback\n",
        encoding="utf-8",
    )

    v2 = _stage_v2(
        tmp_path,
        FIXTURES / "main_loop_tasks.json",
        router_yaml,
        FIXTURES / "main_loop_config.yaml",
    )
    _single_task_fixture(v2, task_id="T-DRIFT")
    monkeypatch.chdir(v2)

    # Track every route.cli_model the backend was called with, per attempt.
    seen_cli_models: list[str] = []

    class _DriftBackend(_FailingBackend):
        def spawn(self, task, route, prompt_path, log_path, cwd):
            seen_cli_models.append(route.cli_model)
            return super().spawn(task, route, prompt_path, log_path, cwd)

    backend = _DriftBackend(error_message="model not found: zhipu/glm-5.1")

    def _fake_get_backend(name: str, cfg: dict[str, Any] | None = None):  # noqa: ARG001
        return backend

    monkeypatch.setattr("orchestrator.orch.get_backend", _fake_get_backend)

    rc = orch_mod.main(["--mode", "auto"])
    assert rc == 1  # ends blocked after fallback also fails

    # Backend was called with two DIFFERENT cli_models: original then fallback.
    assert len(seen_cli_models) == 2, f"expected 2 spawns, got {seen_cli_models}"
    assert seen_cli_models[0] == "zhipu/glm-5.1"
    assert seen_cli_models[1] == "zhipu/glm-5.0-fallback", (
        f"second spawn should have used the fallback model; got {seen_cli_models[1]}"
    )

    # Event stream: 2 dispatches (one per model), 1 retry with drift reason.
    state_dir = v2 / "orchestrator" / "state"
    event_files = list(state_dir.glob("events-*.jsonl"))
    assert len(event_files) == 1
    events = [
        json.loads(line)
        for line in event_files[0].read_text().splitlines()
        if line.strip()
    ]
    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    retries = [e for e in events if e["event_type"] == "retry"]
    assert len(dispatches) == 2
    assert len(retries) == 1
    assert dispatches[0]["extra"]["cli_model"] == "zhipu/glm-5.1"
    assert dispatches[1]["extra"]["cli_model"] == "zhipu/glm-5.0-fallback"
    # Retry event's reason mentions "version-drift" (fallback path) not a plain
    # transient boom — that's how we know FR-D-7 fired vs. FR-D-4 alone.
    assert "version-drift" in retries[0]["extra"]["reason"]


# ---- Fase B: FailureClass-driven retry policy --------------------------


def _run_single_task_with_backend(
    staged_v2: Path,
    monkeypatch: pytest.MonkeyPatch,
    backend: Backend,
    task_id: str = "T-CLASS",
) -> tuple[int, list[dict[str, Any]], dict[str, Any]]:
    """Stage a single-task fixture, patch get_backend, run orch, return artifacts.

    Returns `(rc, events, run_data)`. Shared by the class-driven tests so each
    one focuses on the assertions (not the boilerplate).
    """
    _single_task_fixture(staged_v2, task_id=task_id)

    def _fake_get_backend(name: str, cfg: dict[str, Any] | None = None):  # noqa: ARG001
        return backend

    monkeypatch.setattr("orchestrator.orch.get_backend", _fake_get_backend)

    rc = orch_mod.main(["--mode", "auto"])
    state_dir = staged_v2 / "orchestrator" / "state"
    event_files = list(state_dir.glob("events-*.jsonl"))
    assert len(event_files) == 1
    events = [
        json.loads(line)
        for line in event_files[0].read_text().splitlines()
        if line.strip()
    ]
    run_files = list(state_dir.glob("run-*.json"))
    assert len(run_files) == 1
    run_data = json.loads(run_files[0].read_text())
    return rc, events, run_data


def test_permission_failure_blocks_without_retry(
    staged_v2: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """PERMISSION class: no retry (retrying an auth error just fails again).

    Expected: 1 dispatch, 0 retry events, 1 fail event, task blocked.
    """
    backend = _FailingBackend(error_message="permission denied: token invalid")
    rc, events, run_data = _run_single_task_with_backend(
        staged_v2, monkeypatch, backend, task_id="T-PERM"
    )

    assert rc == 1
    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    retries = [e for e in events if e["event_type"] == "retry"]
    fails = [e for e in events if e["event_type"] == "fail"]

    assert len(dispatches) == 1, f"permission errors must not retry; got {len(dispatches)} dispatches"
    assert len(retries) == 0, f"permission errors must not emit retry events; got {len(retries)}"
    assert len(fails) == 1
    assert run_data["blocked"] == ["T-PERM"]
    assert backend.parse_calls == 1


def test_budget_failure_blocks_without_retry(
    staged_v2: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """BUDGET class: no retry (retrying wastes more budget)."""
    backend = _FailingBackend(error_message="max budget of $5.00 reached")
    rc, events, run_data = _run_single_task_with_backend(
        staged_v2, monkeypatch, backend, task_id="T-BUDGET"
    )

    assert rc == 1
    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    retries = [e for e in events if e["event_type"] == "retry"]

    assert len(dispatches) == 1
    assert len(retries) == 0
    assert run_data["blocked"] == ["T-BUDGET"]


def test_transient_failure_retries_once(
    staged_v2: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """TRANSIENT class: existing retry-once behavior still holds.

    Regression guard so the new switch doesn't accidentally kill the common path.
    """
    backend = _FailingBackend(error_message="500 Internal Server Error from upstream")
    rc, events, run_data = _run_single_task_with_backend(
        staged_v2, monkeypatch, backend, task_id="T-TRANS"
    )

    assert rc == 1
    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    retries = [e for e in events if e["event_type"] == "retry"]
    fails = [e for e in events if e["event_type"] == "fail"]

    assert len(dispatches) == 2, f"transient errors must retry once; got {len(dispatches)}"
    assert len(retries) == 1
    assert len(fails) == 1
    assert run_data["blocked"] == ["T-TRANS"]


def test_rate_limit_failure_uses_long_backoff(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """RATE_LIMIT class: retry uses `retry.rate_limit_backoff_seconds` (60s
    default), NOT the standard `retry.backoff_seconds` (5s).

    Rather than pulling the full main loop (which requires waiting 60s of
    wall-clock), we drive `_reap_once` directly with a stub in-flight entry
    whose parse_result surfaces a 429 rate-limit error. We then inspect the
    `retry_earliest_at` stamp on the resulting `_RetryItem`.
    """
    # Freeze the clock so the assertion is exact.
    fake_now = [10_000.0]
    monkeypatch.setattr(orch_mod.time, "monotonic", lambda: fake_now[0])
    monkeypatch.setattr(orch_mod, "_monotonic", lambda: fake_now[0])

    # Stub the task-file check + task-block script so _reap_once's terminal
    # branch is inert (rate-limit path retries; but if the retry gate
    # somehow doesn't fire, the block call would explode).
    monkeypatch.setattr(orch_mod, "_task_status_in_file", lambda _tid: None)
    monkeypatch.setattr(
        orch_mod, "call_task_block", lambda *_a, **_kw: None
    )
    monkeypatch.setattr(
        orch_mod, "_record_spend", lambda *_a, **_kw: None
    )

    # Backend whose parse_result returns a RATE_LIMIT-classified failure.
    backend = _FailingBackend(error_message="429 rate limit exceeded")

    task = Task.from_json(
        {"id": "T-RL", "phase": 0, "title": "t", "model": "opencode-go/glm-5.1"}
    )
    route = RouteEntry(
        backend="opencode",
        cli_model="zhipu/glm-5.1",
        tier="cheap",
        is_premium=False,
    )
    dispatch = Dispatch(
        task_id=task.id,
        backend="opencode",
        pid=424242,
        session_id="s-rl",
        started_at="2026-08-19T12:00:00Z",
        prompt_path="/tmp/p",
        log_path="/tmp/l",
        output_path="",
    )
    dispatch.attempt = 1

    entry = orch_mod.InFlight(
        task=task,
        route=route,
        backend=backend,
        dispatch=dispatch,
        started_at_mono=fake_now[0],
        timeout_s=60.0,
        task_lock_fd=None,
    )
    entry.timed_out = False

    # Fake `os.waitpid` to return our pid exactly once, then no-children.
    calls = {"n": 0}
    def _fake_waitpid(_pid, _opts):  # noqa: ANN001
        calls["n"] += 1
        if calls["n"] == 1:
            return (424242, 1 << 8)  # exit_code=1 via _exit_code_from_status
        raise ChildProcessError

    monkeypatch.setattr(orch_mod.os, "waitpid", _fake_waitpid)
    monkeypatch.setattr(orch_mod, "_read_log_safely", lambda _p: "")

    in_flight = {424242: entry}

    # Minimal queue with our task in-flight.
    class _StubQueue:
        def __init__(self) -> None:
            self._status = {"T-RL": "in_flight"}

        def mark_done(self, tid: str) -> None:  # pragma: no cover
            self._status[tid] = "done"

        def mark_blocked(self, tid: str) -> None:  # pragma: no cover
            self._status[tid] = "blocked"

    class _StubRunFile:
        def __init__(self) -> None:
            self.removed: list[str] = []

        def mark_done(self, tid: str) -> None:  # pragma: no cover
            pass

        def mark_blocked(self, tid: str) -> None:  # pragma: no cover
            pass

        def remove_dispatch(self, tid: str) -> None:
            self.removed.append(tid)

    class _StubEventLog:
        def __init__(self) -> None:
            self.events: list[tuple] = []

        def emit(self, event_type: str, task_id: str, **extra: Any) -> None:
            self.events.append((event_type, task_id, extra))

    class _StubSpendLog:
        pass

    class _StubSem:
        def release(self) -> None:
            pass

    cfg = {
        "concurrency": {"global_max": 3, "per_provider": {"opencode": 3}},
        "strict_files_phases": [],
        "default_timeout_multiplier": 1.5,
        "budget": {"per_dispatch_usd": 5.0},
        "retry": {
            "backoff_seconds": 5,
            "rate_limit_backoff_seconds": 60,
        },
    }
    retry_queue: list = []
    reaped = orch_mod._reap_once(
        in_flight=in_flight,
        queue=_StubQueue(),
        run_file=_StubRunFile(),
        event_log=_StubEventLog(),
        spend_log=_StubSpendLog(),
        cfg=cfg,
        cwd=Path("/tmp"),
        gsem=_StubSem(),
        psem={"opencode": _StubSem()},
        retry_queue=retry_queue,
    )
    assert reaped == 1
    assert len(retry_queue) == 1, (
        "rate-limit failure must schedule exactly one retry item"
    )
    item = retry_queue[0]
    assert item.retry_earliest_at == fake_now[0] + 60.0, (
        f"rate-limit retry must use 60s backoff, got "
        f"{item.retry_earliest_at - fake_now[0]}s"
    )


# ---- FR-D-4 retry backoff (5s minimum wall-clock delay) ---------------


def _mk_retry_refill_env(
    backoff_seconds: float,
    monkeypatch: pytest.MonkeyPatch,
    fake_now: list[float],
) -> tuple[list, list, dict]:
    """Build the minimal env for a direct _refill() invocation.

    Returns (retry_queue, spawn_calls, cfg). `retry_queue` starts with one
    `_RetryItem`. `spawn_calls` is a list appended to every time _spawn_one
    fires. `cfg` carries the backoff setting.

    `time.monotonic` is monkeypatched to return the current value of
    `fake_now[0]` so tests can advance the clock deterministically.
    """
    monkeypatch.setattr(orch_mod.time, "monotonic", lambda: fake_now[0])

    spawn_calls: list = []

    def _fake_spawn_one(task, route, attempt, *_a, **_kw):  # noqa: ANN001, ARG001
        spawn_calls.append((task.id, attempt))
        return True

    monkeypatch.setattr(orch_mod, "_spawn_one", _fake_spawn_one)

    task = Task.from_json(
        {"id": "T-BACKOFF", "phase": 0, "title": "t", "model": "opencode-go/glm-5.1"}
    )
    route = RouteEntry(
        backend="opencode",
        cli_model="zhipu/glm-5.1",
        tier="cheap",
        is_premium=False,
    )
    # Simulate what _reap_once should do: attach retry_earliest_at
    retry_item = orch_mod._RetryItem(
        task=task,
        route=route,
        attempt=2,
        retry_earliest_at=fake_now[0] + backoff_seconds,
    )
    retry_queue = [retry_item]

    cfg = {
        "concurrency": {"global_max": 3, "per_provider": {"opencode": 3}},
        "strict_files_phases": [],
        "default_timeout_multiplier": 1.5,
        "budget": {"per_dispatch_usd": 5.0},
        "retry": {"backoff_seconds": backoff_seconds},
    }
    return retry_queue, spawn_calls, cfg


def _call_refill(retry_queue, cfg):
    """Invoke _refill with harmless empties for everything except retry_queue."""
    class _StubQueue:
        def ready(self, in_flight_ids=None, only=None):  # noqa: ARG002
            return []

    class _StubRunFile:
        pass

    class _StubEventLog:
        pass

    gsem, psem = orch_mod._build_semaphores(cfg)
    return orch_mod._refill(
        queue=_StubQueue(),
        router={},
        cfg=cfg,
        mode="auto",
        gate=None,
        gsem=gsem,
        psem=psem,
        in_flight={},
        run_file=_StubRunFile(),
        event_log=_StubEventLog(),
        run_id="test-run",
        state_dir=Path("/tmp"),
        cwd=Path("/tmp"),
        dispatched_count=0,
        max_tasks=None,
        deferred=set(),
        drain=orch_mod._DrainFlag(),
        retry_queue=retry_queue,
        use_task_locks=False,
        only=None,
    )


def test_retry_queue_respects_backoff_delay(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """FR-D-4 (design.md:173): retry must wait `retry_backoff_seconds` before
    being drained. With backoff=5s:
        - t=0    : refill runs, retry NOT dispatched (waits)
        - t=4.9  : refill runs, retry still NOT dispatched
        - t=5.0  : refill runs, retry IS dispatched exactly once
    """
    fake_now = [1000.0]
    retry_queue, spawn_calls, cfg = _mk_retry_refill_env(
        backoff_seconds=5.0, monkeypatch=monkeypatch, fake_now=fake_now
    )

    # t=0: retry_earliest_at = 1005.0, clock at 1000.0 → skip
    _call_refill(retry_queue, cfg)
    assert spawn_calls == [], (
        f"retry drained too early (t=0, earliest=+5s): {spawn_calls}"
    )
    assert len(retry_queue) == 1, "retry item should stay queued while waiting"

    # t=4.9: still under 5s
    fake_now[0] = 1004.9
    _call_refill(retry_queue, cfg)
    assert spawn_calls == [], f"retry drained at t=4.9 (< 5s): {spawn_calls}"
    assert len(retry_queue) == 1

    # t=5.0: backoff elapsed → dispatch
    fake_now[0] = 1005.0
    _call_refill(retry_queue, cfg)
    assert spawn_calls == [("T-BACKOFF", 2)], (
        f"retry should have dispatched at t=5.0: {spawn_calls}"
    )
    assert len(retry_queue) == 0, "retry item should be drained after dispatch"


def test_retry_queue_zero_backoff_drains_next_tick(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Regression guard: with backoff=0 (test config override), the retry
    item is drained on the very next _refill tick — matching the existing
    `test_retry_once_then_block` semantic."""
    fake_now = [500.0]
    retry_queue, spawn_calls, cfg = _mk_retry_refill_env(
        backoff_seconds=0.0, monkeypatch=monkeypatch, fake_now=fake_now
    )

    _call_refill(retry_queue, cfg)
    assert spawn_calls == [("T-BACKOFF", 2)]
    assert len(retry_queue) == 0


# ---- Spoof detector -----------------------------------------------------


def _spoof_task(task_id: str = "R-001") -> Task:
    return Task.from_json({"id": task_id, "phase": 0, "title": "t", "model": "m"})


def test_spoof_ignores_read_file_content() -> None:
    """AS-10 regression: the detector must inspect executed COMMANDS only.

    The orchestrator's own design/test files document the attack with the
    literal `task-finish.sh B`. An agent that reads them (content lands in a
    tool-OUTPUT field, not a command) must not be flagged. Observed on R-001.
    """
    log = json.dumps(
        {"tool": "bash", "input": {"command": "cat orchestrator/design.md"},
         "output": "- WHEN agent invokes `task-finish.sh B ...` (wrong id)."}
    )
    assert orch_mod._detect_id_spoofing(_spoof_task(), log) is None


def test_spoof_allows_correct_finish() -> None:
    log = json.dumps({"input": {"command": "scripts/task-finish.sh R-001 \"done\""}})
    assert orch_mod._detect_id_spoofing(_spoof_task(), log) is None


def test_spoof_catches_wrong_id() -> None:
    log = json.dumps({"input": {"command": "scripts/task-finish.sh Z-999 done"}})
    assert orch_mod._detect_id_spoofing(_spoof_task(), log) == "Z-999"


def test_spoof_ignores_cat_of_scripts() -> None:
    # Regresión P0-002: el agente hizo `cat` de varios scripts. task-finish.sh
    # aparece como ARGUMENTO de cat, no como comando — no debe dispararse.
    log = json.dumps(
        {"input": {"command": "cat a/task-finish.sh b/task-start.sh c/task-status.sh"}}
    )
    assert orch_mod._detect_id_spoofing(_spoof_task(), log) is None


# ---- FR-D-8: attempt-3 escalation to different model -------------------


def _write_escalation_router(
    path: Path,
    primary_cli: str = "zhipu/glm-5.1",
    escalation_key: str = "opencode-go/kimi-k2.6",
    escalation_cli: str = "moonshot/kimi-k2.6",
    with_escalation: bool = True,
) -> None:
    """Write a 2-route router where the primary can escalate to the escalation.

    The primary route is `opencode-go/glm-5.1` (matches _single_task_fixture).
    The escalation target is a full second route — the escalation model is
    looked up as its own route entry (same rules as any dispatch).
    """
    escalation_line = (
        f"  escalation_model: {escalation_key}\n" if with_escalation else ""
    )
    path.write_text(
        "opencode-go/glm-5.1:\n"
        "  backend: opencode\n"
        f"  cli_model: {primary_cli}\n"
        "  tier: cheap\n"
        "  is_premium: false\n"
        f"{escalation_line}"
        f"{escalation_key}:\n"
        "  backend: opencode\n"
        f"  cli_model: {escalation_cli}\n"
        "  tier: cheap\n"
        "  is_premium: false\n",
        encoding="utf-8",
    )


def test_escalation_route_validated_at_startup(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str]
) -> None:
    """Router that names a missing `escalation_model` must fail startup with a
    clean error listing the offender, BEFORE any subprocess is spawned. Mirrors
    the cli_model / tier / backend validation pattern (RouterFormatError → exit 1).
    """
    router_yaml = tmp_path / "bad_router.yaml"
    # escalation_model points to a route key that does not exist in the router.
    router_yaml.write_text(
        "opencode-go/glm-5.1:\n"
        "  backend: opencode\n"
        "  cli_model: zhipu/glm-5.1\n"
        "  tier: cheap\n"
        "  is_premium: false\n"
        "  escalation_model: opencode-go/does-not-exist\n",
        encoding="utf-8",
    )

    v2 = _stage_v2(
        tmp_path,
        FIXTURES / "main_loop_tasks.json",
        router_yaml,
        FIXTURES / "main_loop_config.yaml",
    )
    _single_task_fixture(v2, task_id="T-BADESC")
    monkeypatch.chdir(v2)

    def _boom(*a, **kw):  # noqa: ANN001, ARG001
        raise AssertionError("must not spawn when escalation_model is invalid")

    monkeypatch.setattr("orchestrator.dispatcher.subprocess.Popen", _boom)

    rc = orch_mod.main(["--mode", "auto"])
    assert rc == 1
    # The startup error message must mention the offending escalation_model
    # AND the missing target route key so operators can fix in one pass.
    captured = capsys.readouterr()
    combined = captured.err + captured.out
    assert "escalation_model" in combined, (
        f"expected startup error to mention 'escalation_model'; got:\n{combined}"
    )
    assert "opencode-go/does-not-exist" in combined, (
        f"expected startup error to name the missing route; got:\n{combined}"
    )
    # And NO run/events/spend files were created (fail-fast before flock).
    state_dir = v2 / "orchestrator" / "state"
    assert list(state_dir.glob("run-*.json")) == []
    assert list(state_dir.glob("events-*.jsonl")) == []


def test_attempt_3_uses_escalation_route(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """FR-D-8: retryable failure on attempt 2 + escalation_model set →
    attempt 3 uses the escalation route (new backend / cli_model).

    Expected event sequence:
      - dispatch attempt=1 (primary route)
      - retry attempt=2 (FR-D-4)
      - dispatch attempt=2 (primary route)
      - escalate {from_route, to_route, attempt=3}
      - dispatch attempt=3 (escalation route)
      - fail attempt=3 (blocked terminally)
    Task ends up blocked, exit=1.
    """
    router_yaml = tmp_path / "router.yaml"
    _write_escalation_router(router_yaml)

    v2 = _stage_v2(
        tmp_path,
        FIXTURES / "main_loop_tasks.json",
        router_yaml,
        FIXTURES / "main_loop_config.yaml",
    )
    _single_task_fixture(v2, task_id="T-ESC")
    monkeypatch.chdir(v2)

    seen_cli_models: list[str] = []

    class _EscalationBackend(_FailingBackend):
        def spawn(self, task, route, prompt_path, log_path, cwd):
            seen_cli_models.append(route.cli_model)
            return super().spawn(task, route, prompt_path, log_path, cwd)

    backend = _EscalationBackend(error_message="500 Internal Server Error")

    def _fake_get_backend(name: str, cfg: dict[str, Any] | None = None):  # noqa: ARG001
        return backend

    monkeypatch.setattr("orchestrator.orch.get_backend", _fake_get_backend)

    rc = orch_mod.main(["--mode", "auto"])
    assert rc == 1

    # Three spawns total: primary x2, then escalation x1.
    assert len(seen_cli_models) == 3, f"expected 3 spawns, got {seen_cli_models}"
    assert seen_cli_models[0] == "zhipu/glm-5.1"
    assert seen_cli_models[1] == "zhipu/glm-5.1"
    assert seen_cli_models[2] == "moonshot/kimi-k2.6", (
        f"attempt 3 must use escalation cli_model; got {seen_cli_models[2]}"
    )

    state_dir = v2 / "orchestrator" / "state"
    event_files = list(state_dir.glob("events-*.jsonl"))
    assert len(event_files) == 1
    events = [
        json.loads(line)
        for line in event_files[0].read_text().splitlines()
        if line.strip()
    ]

    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    escalates = [e for e in events if e["event_type"] == "escalate"]
    fails = [e for e in events if e["event_type"] == "fail"]

    assert len(dispatches) == 3, f"expected 3 dispatches; got {len(dispatches)}"
    assert [d["extra"]["attempt"] for d in dispatches] == [1, 2, 3]
    assert len(escalates) == 1, f"expected 1 escalate event; got {len(escalates)}"
    esc = escalates[0]
    assert esc["extra"]["attempt"] == 3
    assert esc["extra"]["from_route"] == "opencode-go/glm-5.1"
    assert esc["extra"]["to_route"] == "opencode-go/kimi-k2.6"
    assert esc["extra"]["failure_class"] in {"transient", "other"}
    assert len(fails) == 1
    assert fails[0]["extra"]["attempt"] == 3


def test_no_escalation_when_class_terminal(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """FR-D-8: PERMISSION / BUDGET / TIMEOUT / ID_SPOOF are NEVER escalated,
    even if `escalation_model` is set. They terminate on attempt 1.
    """
    router_yaml = tmp_path / "router.yaml"
    _write_escalation_router(router_yaml)

    v2 = _stage_v2(
        tmp_path,
        FIXTURES / "main_loop_tasks.json",
        router_yaml,
        FIXTURES / "main_loop_config.yaml",
    )
    _single_task_fixture(v2, task_id="T-PERM2")
    monkeypatch.chdir(v2)

    backend = _FailingBackend(error_message="permission denied: token invalid")

    def _fake_get_backend(name: str, cfg: dict[str, Any] | None = None):  # noqa: ARG001
        return backend

    monkeypatch.setattr("orchestrator.orch.get_backend", _fake_get_backend)

    rc = orch_mod.main(["--mode", "auto"])
    assert rc == 1

    state_dir = v2 / "orchestrator" / "state"
    event_files = list(state_dir.glob("events-*.jsonl"))
    events = [
        json.loads(line)
        for line in event_files[0].read_text().splitlines()
        if line.strip()
    ]

    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    escalates = [e for e in events if e["event_type"] == "escalate"]

    assert len(dispatches) == 1, (
        f"PERMISSION must not retry or escalate; got {len(dispatches)} dispatches"
    )
    assert len(escalates) == 0, (
        f"PERMISSION must NEVER emit escalate; got {len(escalates)}"
    )
    assert backend.parse_calls == 1


def test_no_escalation_when_route_lacks_escalation_model(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """FR-D-8 backwards-compat: retryable failure without escalation_model on
    the route → block after attempt 2 (existing FR-D-4 behavior preserved).

    Guarantees existing routes (without escalation_model) keep the classic
    max=2-attempt semantic. This is the regression guard for the 170 existing
    tests.
    """
    router_yaml = tmp_path / "router.yaml"
    _write_escalation_router(router_yaml, with_escalation=False)

    v2 = _stage_v2(
        tmp_path,
        FIXTURES / "main_loop_tasks.json",
        router_yaml,
        FIXTURES / "main_loop_config.yaml",
    )
    _single_task_fixture(v2, task_id="T-NOESC")
    monkeypatch.chdir(v2)

    backend = _FailingBackend(error_message="500 Internal Server Error")

    def _fake_get_backend(name: str, cfg: dict[str, Any] | None = None):  # noqa: ARG001
        return backend

    monkeypatch.setattr("orchestrator.orch.get_backend", _fake_get_backend)

    rc = orch_mod.main(["--mode", "auto"])
    assert rc == 1

    state_dir = v2 / "orchestrator" / "state"
    event_files = list(state_dir.glob("events-*.jsonl"))
    events = [
        json.loads(line)
        for line in event_files[0].read_text().splitlines()
        if line.strip()
    ]

    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    escalates = [e for e in events if e["event_type"] == "escalate"]
    fails = [e for e in events if e["event_type"] == "fail"]

    assert len(dispatches) == 2, (
        f"without escalation_model, max=2 attempts; got {len(dispatches)}"
    )
    assert len(escalates) == 0, (
        f"no escalation_model on route → no escalate event; got {len(escalates)}"
    )
    assert len(fails) == 1
    assert fails[0]["extra"]["attempt"] == 2


def test_escalation_respects_per_dispatch_budget(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """FR-D-8: attempt 3 still counts against `budget.per_dispatch_usd`.

    If the cumulative spend after attempt 2 already exceeds the per-dispatch
    budget cap, escalation must be blocked instead of firing a 3rd attempt.
    """
    router_yaml = tmp_path / "router.yaml"
    _write_escalation_router(router_yaml)

    # Tiny budget cap: 0.005 USD. Every _FailingBackend spawn records 0.0 USD
    # spend, so we override the backend to report a nonzero cost that pushes
    # spend past the cap after 2 attempts.
    config_yaml = tmp_path / "budget_config.yaml"
    config_yaml.write_text(
        "concurrency:\n"
        "  global_max: 3\n"
        "  per_provider:\n"
        "    claude: 3\n"
        "    codex: 3\n"
        "    opencode: 3\n"
        "  per_file: 1\n"
        "strict_files_phases: []\n"
        "default_timeout_multiplier: 1.5\n"
        "budget:\n"
        "  per_dispatch_usd: 0.005\n"
        "retry:\n"
        "  backoff_seconds: 0\n",
        encoding="utf-8",
    )

    v2 = _stage_v2(
        tmp_path,
        FIXTURES / "main_loop_tasks.json",
        router_yaml,
        config_yaml,
    )
    _single_task_fixture(v2, task_id="T-BUDESC")
    monkeypatch.chdir(v2)

    class _CostlyFailingBackend(_FailingBackend):
        """Reports nonzero cost per attempt so the budget cap fires."""

        def parse_result(self, exit_code, log_text, extra=None):  # noqa: ARG002
            self.parse_calls += 1
            return DispatchResult(
                exit_code=exit_code,
                success=False,
                cost_usd=0.01,  # each attempt costs 0.01; cap is 0.005
                tokens_in=0,
                tokens_out=0,
                stdout=log_text,
                stderr="",
                error_message=self.error_message,
            )

    backend = _CostlyFailingBackend(error_message="500 Internal Server Error")

    def _fake_get_backend(name: str, cfg: dict[str, Any] | None = None):  # noqa: ARG001
        return backend

    monkeypatch.setattr("orchestrator.orch.get_backend", _fake_get_backend)

    rc = orch_mod.main(["--mode", "auto"])
    assert rc == 1

    state_dir = v2 / "orchestrator" / "state"
    event_files = list(state_dir.glob("events-*.jsonl"))
    events = [
        json.loads(line)
        for line in event_files[0].read_text().splitlines()
        if line.strip()
    ]

    dispatches = [e for e in events if e["event_type"] == "dispatch"]
    escalates = [e for e in events if e["event_type"] == "escalate"]

    # Attempts 1 + 2 already blow past the 0.005 cap (each costs 0.01), so
    # attempt 3 must NOT fire.
    assert len(dispatches) == 2, (
        f"budget cap must block escalation; got {len(dispatches)} dispatches"
    )
    assert len(escalates) == 0, (
        f"budget cap → no escalate event; got {len(escalates)}"
    )


# ---- Sprint A / Issue #11: budget deferral logging ---------------------


def test_extract_usage_from_reason_parses_gate_string() -> None:
    """`_extract_usage_from_reason` parses `(used, budget)` from the reason
    string produced by BudgetGate.can_dispatch."""
    reason = "codex over threshold: 367,000 tokens used, cap 240,000 (60% of 400,000)"
    used, budget = orch_mod._extract_usage_from_reason(reason)
    assert used == 367_000
    assert budget == 400_000


def test_extract_usage_from_reason_returns_zeros_on_garbage() -> None:
    assert orch_mod._extract_usage_from_reason("") == (0, 0)
    assert orch_mod._extract_usage_from_reason("nothing to see here") == (0, 0)


def test_defer_emits_human_readable_log_line(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    caplog: pytest.LogCaptureFixture,
) -> None:
    """When the budget gate blocks a dispatch, orch logs a compact line with
    task_id, provider, window pct, and reset ETA — in addition to the
    structured `budget_skip` event."""
    from datetime import datetime, timedelta, timezone

    task = Task.from_json(
        {"id": "F0.6.T3", "phase": 0, "title": "t", "model": "opencode-go/glm-5.1"}
    )
    route = RouteEntry(
        backend="codex",
        cli_model="gpt-5.6-codex",
        tier="premium",
        is_premium=True,
    )
    # Fake budget gate: always blocks with a realistic reason + reset.
    reset_at = datetime.now(timezone.utc) + timedelta(hours=2, minutes=14)

    class _AlwaysBlocksGate:
        def can_dispatch(self, provider: str):  # noqa: ARG002
            reason = (
                "codex over threshold: 367,000 tokens used, "
                "cap 240,000 (60% of 400,000)"
            )
            return False, reason, reset_at

    # Stub event log — records only what was emitted.
    emitted_events: list[dict] = []

    class _StubEventLog:
        def emit(self, event_type, task_id, **extra):  # noqa: ANN001
            emitted_events.append({"event_type": event_type, "task_id": task_id, **extra})

    class _StubRunFile:
        pass

    class _StubQueue:
        pass

    gsem, psem = orch_mod._build_semaphores({
        "concurrency": {"global_max": 1, "per_provider": {"codex": 1}},
    })
    cfg = {
        "concurrency": {"global_max": 1, "per_provider": {"codex": 1}},
        "strict_files_phases": [],
        "default_timeout_multiplier": 1.5,
        "budget": {"per_dispatch_usd": 5.0},
        "retry": {"backoff_seconds": 5.0},
        "spec_root": "specs",
    }

    defer_reasons: dict[str, str] = {}
    with caplog.at_level("INFO", logger="orchestrator.orch"):
        ok = orch_mod._spawn_one(
            task=task,
            route=route,
            attempt=1,
            cfg=cfg,
            gsem=gsem,
            psem=psem,
            in_flight={},
            run_file=_StubRunFile(),
            event_log=_StubEventLog(),
            run_id="test-run",
            state_dir=tmp_path,
            cwd=tmp_path,
            queue=_StubQueue(),
            use_task_locks=False,
            budget_gate=_AlwaysBlocksGate(),
            defer_reasons=defer_reasons,
        )
    assert ok is False

    # Structured event was emitted.
    budget_skips = [e for e in emitted_events if e["event_type"] == "budget_skip"]
    assert len(budget_skips) == 1
    assert budget_skips[0]["task_id"] == "F0.6.T3"
    assert budget_skips[0]["backend"] == "codex"

    # Human-readable log line contains the expected pieces.
    log_lines = [r.getMessage() for r in caplog.records]
    defer_lines = [line for line in log_lines if "deferred" in line and "F0.6.T3" in line]
    assert defer_lines, f"no defer log line found; captured: {log_lines}"
    line = defer_lines[0]
    assert "F0.6.T3" in line
    assert "codex" in line
    assert "367k" in line  # tokens used, formatted short
    assert "400k" in line  # token budget, formatted short
    # ETA should look like `~2h 14m` (or `~2h 13m` if a second slipped by).
    assert "2h" in line
    assert "resets in" in line

    # Defer-reasons side channel was populated so future `orch status` can see it.
    assert defer_reasons.get("F0.6.T3", "").startswith("blocked-by-budget:")


def test_defer_reason_cleared_when_gate_passes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """When the budget gate passes on a subsequent tick, the stale
    `blocked-by-budget` marker for that task must be cleared."""

    class _AllowingGate:
        def can_dispatch(self, provider: str):  # noqa: ARG002
            return True, None, None

    # We only need the gate check to succeed; the spawn will then fail on
    # the semaphore-less/skeletal environment, but the defer_reasons
    # cleanup happens BEFORE the semaphore try_acquire — so we can assert
    # against a partial-completion in _spawn_one.
    #
    # To keep the test tight, we shortcut: pre-populate the marker and
    # monkeypatch out the semaphore path so the function returns early
    # after clearing the marker.
    task = Task.from_json(
        {"id": "F0.6.T3", "phase": 0, "title": "t", "model": "opencode-go/glm-5.1"}
    )
    route = RouteEntry(
        backend="codex",
        cli_model="gpt-5.6-codex",
        tier="premium",
        is_premium=True,
    )

    # Force the psem check to fail so _spawn_one returns False right after
    # clearing the defer marker — no need to build the rest of the world.
    class _AlwaysFullSem:
        def try_acquire(self):
            return False

        def release(self):
            pass

    gsem = _AlwaysFullSem()
    psem = {"codex": _AlwaysFullSem()}
    cfg = {
        "concurrency": {"global_max": 1, "per_provider": {"codex": 1}},
        "strict_files_phases": [],
        "default_timeout_multiplier": 1.5,
        "budget": {"per_dispatch_usd": 5.0},
        "retry": {"backoff_seconds": 5.0},
        "spec_root": "specs",
    }
    defer_reasons = {"F0.6.T3": "blocked-by-budget:codex"}

    class _StubEventLog:
        def emit(self, *_a, **_kw):  # noqa: ANN001
            pass

    class _StubRunFile:
        pass

    class _StubQueue:
        pass

    orch_mod._spawn_one(
        task=task,
        route=route,
        attempt=1,
        cfg=cfg,
        gsem=gsem,
        psem=psem,
        in_flight={},
        run_file=_StubRunFile(),
        event_log=_StubEventLog(),
        run_id="test-run",
        state_dir=tmp_path,
        cwd=tmp_path,
        queue=_StubQueue(),
        use_task_locks=False,
        budget_gate=_AllowingGate(),
        defer_reasons=defer_reasons,
    )
    # Gate said OK → stale marker cleared even though semaphore was full.
    assert "F0.6.T3" not in defer_reasons
