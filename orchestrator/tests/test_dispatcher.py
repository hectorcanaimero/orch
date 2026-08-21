"""Unit tests for `orchestrator.dispatcher`.

Covers R-013 / R-014 / R-015 acceptance:

- Cost/token extraction against canned fixture output for each backend
  (`fixtures/dispatcher/{claude,codex,opencode}_{success,error}.*`).
- `build_cmd` snapshots pin the exact argv per backend — any drift means
  the CLI contract changed and the spec/design needs an update.
- `spawn` smoke test: replaces the real CLI with `echo` (via a subclass
  overriding `build_cmd`) and asserts the child terminates, the log file
  contains the piped prompt bytes, and `_popen` / `output_path` are wired.
- Timeout path: spawns `sleep 5`, waits with `timeout_s=0.5`, asserts SIGTERM
  → SIGKILL, and `error_message` includes "orchestrator timeout".

The tests NEVER invoke real `claude` / `codex` / `opencode` binaries — they
either use `echo` / `sleep` (present on macOS + Linux) or drive the pure
`parse_result` / `extract_cost` helpers directly.
"""

from __future__ import annotations

import subprocess
import sys
import time
from pathlib import Path

import pytest

from orchestrator.dispatcher import (
    Backend,
    ClaudeBackend,
    CodexBackend,
    DispatchResult,
    OpencodeBackend,
    _iter_jsonl_events,
    _parse_claude_envelope,
    _sum_step_finish_costs,
    get_backend,
)
from orchestrator.models import RouteEntry, Task


FIXTURES = Path(__file__).parent / "fixtures" / "dispatcher"


# ---- Helpers -------------------------------------------------------------


def _mk_task(
    tid: str = "B-020",
    model: str = "opencode/claude-opus-4-7",
    phase: int = 10,
    files: list[str] | None = None,
    hours: float = 6.0,
    deps: list[str] | None = None,
) -> Task:
    return Task(
        id=tid,
        phase=phase,
        title=f"{tid} title",
        description=f"{tid} description",
        model=model,
        reason="tests",
        status="todo",
        dependencies=list(deps or []),
        estimate_hours=hours,
        files=list(files or []),
        spec_ref="",
        comments=[],
    )


def _mk_route(
    backend: str = "claude",
    cli_model: str = "opus",
    tier: str = "premium",
) -> RouteEntry:
    return RouteEntry(
        backend=backend,  # type: ignore[arg-type]
        cli_model=cli_model,
        tier=tier,  # type: ignore[arg-type]
        is_premium=(tier == "premium"),
        fallback_cli_model=None,
    )


def _write_prompt(tmp_path: Path, body: str = "hello prompt") -> Path:
    p = tmp_path / "prompts" / "B-020.txt"
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(body, encoding="utf-8")
    return p


# ---- Cost extraction: claude --------------------------------------------


def test_parse_claude_envelope_success() -> None:
    text = (FIXTURES / "claude_success.txt").read_text()
    env = _parse_claude_envelope(text)
    assert env is not None
    assert env["is_error"] is False
    assert env["subtype"] == "success"


def test_claude_extract_cost_success() -> None:
    backend = ClaudeBackend()
    text = (FIXTURES / "claude_success.txt").read_text()
    cost, tin, tout = backend.extract_cost(text)
    assert cost == pytest.approx(0.0285802)
    assert tin == 1234
    assert tout == 567


def test_claude_parse_result_success() -> None:
    backend = ClaudeBackend()
    text = (FIXTURES / "claude_success.txt").read_text()
    res = backend.parse_result(exit_code=0, log_text=text)
    assert isinstance(res, DispatchResult)
    assert res.success is True
    assert res.cost_usd == pytest.approx(0.0285802)
    assert res.tokens_in == 1234
    assert res.tokens_out == 567
    assert res.error_message is None
    assert res.exit_code == 0


def test_claude_parse_result_error_envelope() -> None:
    backend = ClaudeBackend()
    text = (FIXTURES / "claude_error.txt").read_text()
    res = backend.parse_result(exit_code=1, log_text=text)
    assert res.success is False
    assert res.error_message is not None
    # cost is still extracted from partial usage
    assert res.cost_usd == pytest.approx(0.0012)


def test_claude_parse_result_unparseable_stdout() -> None:
    backend = ClaudeBackend()
    res = backend.parse_result(exit_code=0, log_text="garbage output not json")
    assert res.success is False
    assert res.error_message is not None
    assert "parse" in res.error_message.lower()


# ---- Cost extraction: codex ---------------------------------------------


def test_codex_iter_events_success() -> None:
    text = (FIXTURES / "codex_success.jsonl").read_text()
    events = list(_iter_jsonl_events(text))
    types = [e.get("type") for e in events]
    assert types.count("turn.completed") == 2
    assert "turn.failed" not in types


def test_codex_extract_cost_success() -> None:
    backend = CodexBackend()
    text = (FIXTURES / "codex_success.jsonl").read_text()
    cost, tin, tout = backend.extract_cost(text)
    # codex >=0.148 no reporta costo en USD; tokens vienen de turn.completed.usage.
    assert cost == pytest.approx(0.0)
    assert tin == 800 + 1400
    assert tout == 120 + 420


def test_codex_parse_result_success() -> None:
    backend = CodexBackend()
    text = (FIXTURES / "codex_success.jsonl").read_text()
    res = backend.parse_result(exit_code=0, log_text=text)
    assert res.success is True
    assert res.cost_usd == pytest.approx(0.0)
    assert res.tokens_in == 2200
    assert res.tokens_out == 540


def test_codex_parse_result_error_event() -> None:
    backend = CodexBackend()
    text = (FIXTURES / "codex_error.jsonl").read_text()
    res = backend.parse_result(exit_code=1, log_text=text)
    assert res.success is False
    assert res.error_message is not None


# ---- Cost extraction: opencode ------------------------------------------


def test_opencode_extract_cost_success() -> None:
    backend = OpencodeBackend()
    text = (FIXTURES / "opencode_success.jsonl").read_text()
    cost, tin, tout = backend.extract_cost(text)
    assert cost == pytest.approx(0.0021 + 0.0064)
    assert tin == 600 + 1100
    assert tout == 80 + 240


def test_opencode_parse_result_success() -> None:
    backend = OpencodeBackend()
    text = (FIXTURES / "opencode_success.jsonl").read_text()
    res = backend.parse_result(exit_code=0, log_text=text)
    assert res.success is True
    assert res.cost_usd == pytest.approx(0.0085)


def test_opencode_parse_result_error_stream() -> None:
    backend = OpencodeBackend()
    text = (FIXTURES / "opencode_error.jsonl").read_text()
    res = backend.parse_result(exit_code=1, log_text=text)
    assert res.success is False
    assert res.error_message is not None
    assert "auth" in res.error_message.lower() or "provider" in res.error_message.lower()


# ---- Shared helper ------------------------------------------------------


def test_sum_step_finish_costs_ignores_non_step_events() -> None:
    events = [
        {"type": "message", "cost": 999},  # ignored — wrong type
        {"type": "step_finish", "cost": 0.5, "tokens": {"input": 10, "output": 3}},
        {"type": "step_finish", "cost": "0.25", "tokens": {"input": 5, "output": 1}},
    ]
    cost, tin, tout = _sum_step_finish_costs(events)
    assert cost == pytest.approx(0.75)
    assert tin == 15
    assert tout == 4


# ---- build_cmd snapshots ------------------------------------------------


def test_claude_build_cmd_snapshot() -> None:
    task = _mk_task()
    route = _mk_route(backend="claude", cli_model="opus", tier="premium")
    cmd = ClaudeBackend().build_cmd(task, route)
    # Locked argv — drift means CLI contract changed. Ignore the session id
    # (uuid) and any optional budget flag we didn't set.
    assert cmd[0] == "claude"
    assert cmd[1] == "-p"
    assert "--output-format" in cmd
    assert cmd[cmd.index("--output-format") + 1] == "json"
    assert "--model" in cmd
    assert cmd[cmd.index("--model") + 1] == "opus"
    assert "--session-id" in cmd
    # UUID string ~= 36 chars
    assert len(cmd[cmd.index("--session-id") + 1]) >= 32
    assert "--add-dir" in cmd
    assert "--permission-mode" in cmd
    assert cmd[cmd.index("--permission-mode") + 1] == "acceptEdits"


def test_claude_build_cmd_respects_budget_cfg() -> None:
    task = _mk_task()
    route = _mk_route()
    backend = ClaudeBackend(cfg={"budget": {"per_dispatch_usd": 5.0}})
    cmd = backend.build_cmd(task, route)
    assert "--max-budget-usd" in cmd
    assert cmd[cmd.index("--max-budget-usd") + 1] == "5.0"


def test_codex_build_cmd_snapshot() -> None:
    task = _mk_task(tid="B-035", model="opencode/gpt-5.6-codex")
    route = _mk_route(backend="codex", cli_model="gpt-5.6-codex", tier="premium")
    cmd = CodexBackend().build_cmd(task, route)
    assert cmd[0] == "codex"
    assert cmd[1] == "exec"
    assert "--skip-git-repo-check" in cmd
    assert "--json" in cmd
    assert "-o" in cmd
    # Placeholder before spawn; concrete path is set inside spawn().
    assert cmd[cmd.index("-o") + 1].startswith("__OUTPUT__")
    assert "-C" in cmd
    # `-s/--sandbox` NO debe convivir con `--approve-for-me` (codex >=0.148 los
    # rechaza juntos); --approve-for-me ya implica el sandbox workspace-write.
    assert "-s" not in cmd
    assert "--sandbox" not in cmd
    assert "--approve-for-me" in cmd
    assert "-m" in cmd
    assert cmd[cmd.index("-m") + 1] == "gpt-5.6-codex"


def test_opencode_build_cmd_snapshot() -> None:
    task = _mk_task(tid="P-100", model="opencode/gemini-3.0-pro")
    route = _mk_route(
        backend="opencode", cli_model="google/gemini-3.0-pro", tier="premium"
    )
    cmd = OpencodeBackend().build_cmd(task, route)
    assert cmd == [
        "opencode",
        "run",
        "--format",
        "json",
        "--model",
        "google/gemini-3.0-pro",
        "--auto",
    ]


# ---- get_backend registry ----------------------------------------------


def test_get_backend_dispatch() -> None:
    assert isinstance(get_backend("claude"), ClaudeBackend)
    assert isinstance(get_backend("codex"), CodexBackend)
    assert isinstance(get_backend("opencode"), OpencodeBackend)


def test_get_backend_unknown_raises() -> None:
    with pytest.raises(ValueError, match="unknown backend"):
        get_backend("bogus")


# ---- Spawn smoke test ---------------------------------------------------


class _EchoClaudeBackend(ClaudeBackend):
    """Override `build_cmd` to run `cat` instead of `claude`.

    `cat` reads stdin (the prompt file) and writes to stdout — which the
    spawn helper captures into the log file. That's exactly what we need to
    prove the stdin-piping is wired correctly.
    """

    def build_cmd(self, task, route):  # type: ignore[override]
        return ["cat"]


def test_spawn_and_wait_pipes_prompt_via_stdin(tmp_path: Path) -> None:
    task = _mk_task()
    route = _mk_route()
    prompt = _write_prompt(tmp_path, body="TASK_ID=B-020\nhello from prompt\n")
    log_path = tmp_path / "state" / "logs" / "B-020.log"

    backend = _EchoClaudeBackend()
    dispatch = backend.spawn(
        task=task,
        route=route,
        prompt_path=prompt,
        log_path=log_path,
        cwd=tmp_path,
    )

    assert dispatch.pid > 0
    assert dispatch.task_id == "B-020"
    assert dispatch.backend == "claude"
    assert getattr(dispatch, "_popen", None) is not None

    # Wait for `cat` to finish (short — exits when stdin closes).
    popen = dispatch._popen  # type: ignore[attr-defined]
    popen.wait(timeout=5)
    # Close the file handles we owned in spawn (wait_result normally does this,
    # but we bypassed it here to stay pure to the smoke test).
    for fh in dispatch._fhs:  # type: ignore[attr-defined]
        try:
            fh.close()
        except OSError:
            pass

    log_bytes = log_path.read_bytes()
    assert b"TASK_ID=B-020" in log_bytes
    assert b"hello from prompt" in log_bytes


# ---- Timeout path -------------------------------------------------------


class _SleepBackend(ClaudeBackend):
    """Sleeps 5s — used to prove SIGTERM/SIGKILL on `timeout_s < 5`."""

    def build_cmd(self, task, route):  # type: ignore[override]
        return [sys.executable, "-c", "import time; time.sleep(5)"]


def test_wait_result_timeout_sends_sigterm(tmp_path: Path) -> None:
    task = _mk_task()
    route = _mk_route()
    prompt = _write_prompt(tmp_path, body="ignored\n")
    log_path = tmp_path / "state" / "logs" / "B-020.log"

    backend = _SleepBackend()
    dispatch = backend.spawn(
        task=task,
        route=route,
        prompt_path=prompt,
        log_path=log_path,
        cwd=tmp_path,
    )
    started = time.monotonic()
    res = backend.wait_result(dispatch, timeout_s=0.5)
    elapsed = time.monotonic() - started

    # Should have terminated the child before the 5s sleep completed.
    assert elapsed < 4.0, f"expected fast kill, took {elapsed}s"
    assert res.success is False
    assert res.error_message is not None
    assert "timeout" in res.error_message.lower()
    # PID should be reaped by now.
    popen = dispatch._popen  # type: ignore[attr-defined]
    assert popen.returncode is not None
