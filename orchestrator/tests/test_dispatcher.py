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

import os
import signal
import subprocess
import sys
import time
from pathlib import Path

import pytest

from orchestrator.dispatcher import (
    AgyBackend,
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


# ---- Regression: Issue #8 — real opencode 1.18.19 schema ---------------


def test_sum_step_finish_costs_reads_real_opencode_part_wrapped_payload() -> None:
    """Regression for Issue #8.

    Real opencode 1.18.19 wraps `cost`, `tokens`, `reason` under `part`:
        {"type":"step_finish","part":{"cost":0.003,"tokens":{"input":100,"output":20},"reason":"stop"}}
    The old parser looked at TOP-LEVEL `cost`/`tokens` and silently returned 0
    because the real payload was nested. Defends against that regression.
    """
    events = [
        {
            "type": "step_finish",
            "part": {
                "type": "step-finish",
                "reason": "stop",
                "tokens": {"input": 100, "output": 20, "total": 120},
                "cost": 0.003,
            },
        },
        {
            "type": "step_finish",
            "part": {
                "type": "step-finish",
                "reason": "tool_call",
                "tokens": {"input": 50, "output": 5},
                "cost": 0.001,
            },
        },
    ]
    cost, tin, tout = _sum_step_finish_costs(events)
    assert cost == pytest.approx(0.004)
    assert tin == 150
    assert tout == 25


def test_opencode_real_fixture_reports_nonzero_tokens_and_cost() -> None:
    """Regression for Issue #8: the shipped fixture must exercise real schema.

    Guards against the fixture drifting back to the aspirational top-level
    layout (which silently zeroed spend telemetry in production).
    """
    backend = OpencodeBackend()
    text = (FIXTURES / "opencode_success.jsonl").read_text()
    res = backend.parse_result(exit_code=0, log_text=text)
    assert res.success is True, res.error_message
    assert res.tokens_in > 0, "opencode fixture must report input tokens"
    assert res.tokens_out > 0, "opencode fixture must report output tokens"
    assert res.cost_usd > 0.0, "opencode fixture must report cost"
    # Not estimated — usage IS present in the real fixture.
    assert getattr(res, "estimated", False) is False


def test_opencode_marks_estimated_when_usage_is_missing() -> None:
    """When opencode emits step_finish events with NO usage numbers, the
    DispatchResult must be flagged `estimated=True` so downstream reporting
    can distinguish missing telemetry from real zero-cost work.
    """
    # Real-shape step_finish but with empty tokens object → provider not
    # emitting usage.
    log_text = "\n".join([
        '{"type":"step_finish","part":{"type":"step-finish","reason":"stop","tokens":{"input":0,"output":0}}}',
    ])
    backend = OpencodeBackend()
    res = backend.parse_result(
        exit_code=0, log_text=log_text, extra={"cli_model": "some/model"}
    )
    assert res.success is True
    assert res.tokens_in == 0
    assert res.tokens_out == 0
    assert res.estimated is True


def test_opencode_no_events_is_not_marked_estimated() -> None:
    """When there are no step_finish events at all (empty stream, crash before
    first step), the estimated flag stays False — the row is a genuine zero,
    not a telemetry gap."""
    backend = OpencodeBackend()
    res = backend.parse_result(exit_code=0, log_text="")
    # No events → failure (no terminal event), but estimated stays False.
    assert res.estimated is False


# ---- Sprint A / Issue #12: children spawn in new session ---------------


def test_children_spawn_in_new_session(tmp_path: Path) -> None:
    """Every child must be its own session leader (== process group leader)
    so cleanup can `killpg` the whole tree, not just the direct child."""
    task = _mk_task()
    route = _mk_route()
    prompt = _write_prompt(tmp_path, body="hello\n")
    log_path = tmp_path / "state" / "logs" / "B-020.log"

    class _SleepBackend(ClaudeBackend):
        """Runs a sleep so we can inspect the pgid before it exits."""

        def build_cmd(self, task, route):  # type: ignore[override]
            return ["sleep", "1"]

    backend = _SleepBackend()
    dispatch = backend.spawn(
        task=task,
        route=route,
        prompt_path=prompt,
        log_path=log_path,
        cwd=tmp_path,
    )
    try:
        pgid_child = os.getpgid(dispatch.pid)
        pgid_parent = os.getpgid(os.getpid())
        # Child MUST be in its own process group (its pgid == its pid).
        assert pgid_child == dispatch.pid
        # And that group MUST be different from the test-runner's group.
        assert pgid_child != pgid_parent
    finally:
        # Reap.
        popen = dispatch._popen  # type: ignore[attr-defined]
        try:
            popen.terminate()
            popen.wait(timeout=5)
        except Exception:  # noqa: BLE001
            pass
        for fh in dispatch._fhs:  # type: ignore[attr-defined]
            try:
                fh.close()
            except OSError:
                pass


def test_killpg_reaps_grandchildren(tmp_path: Path) -> None:
    """The SIGTERM/SIGKILL path uses `killpg` so children spawned by the
    child (e.g. bash wrappers) also die. Sanity check: spawn `sh -c 'sleep 10'`,
    send SIGTERM to the group, both processes must be gone quickly."""
    task = _mk_task()
    route = _mk_route()
    prompt = _write_prompt(tmp_path, body="\n")
    log_path = tmp_path / "state" / "logs" / "B-021.log"

    class _ShellBackend(ClaudeBackend):
        def build_cmd(self, task, route):  # type: ignore[override]
            # `sh -c 'sleep 10'` → sh is child, sleep is grandchild.
            return ["sh", "-c", "sleep 10"]

    backend = _ShellBackend()
    dispatch = backend.spawn(
        task=task,
        route=route,
        prompt_path=prompt,
        log_path=log_path,
        cwd=tmp_path,
    )
    popen = dispatch._popen  # type: ignore[attr-defined]
    try:
        pgid = os.getpgid(popen.pid)
        # SIGTERM the whole group.
        os.killpg(pgid, signal.SIGTERM)
        # Wait briefly — sh should propagate the signal.
        exit_code = popen.wait(timeout=5)
        # sh dies from SIGTERM (or 143 = 128+15).
        assert exit_code in (-signal.SIGTERM, 143, 0)
    finally:
        for fh in dispatch._fhs:  # type: ignore[attr-defined]
            try:
                fh.close()
            except OSError:
                pass


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


def test_opencode_build_cmd_snapshot(tmp_path: Path) -> None:
    task = _mk_task(tid="P-100", model="opencode/gemini-3.0-pro")
    route = _mk_route(
        backend="opencode", cli_model="google/gemini-3.0-pro", tier="premium"
    )
    cmd = OpencodeBackend().build_cmd(task, route, cwd=tmp_path)
    assert cmd == [
        "opencode",
        "run",
        "--format",
        "json",
        "--model",
        "google/gemini-3.0-pro",
        "--auto",
        "--dir",
        str(tmp_path.resolve()),
    ]


def test_opencode_build_cmd_dir_is_absolute(tmp_path: Path) -> None:
    """--dir must be an absolute path so opencode ignores the parent shell's cwd."""
    task = _mk_task(tid="P-101", model="opencode/gemini-3.0-pro")
    route = _mk_route(
        backend="opencode", cli_model="google/gemini-3.0-pro", tier="premium"
    )
    cmd = OpencodeBackend().build_cmd(task, route, cwd=tmp_path)
    assert "--dir" in cmd
    dir_value = cmd[cmd.index("--dir") + 1]
    assert Path(dir_value).is_absolute()
    assert dir_value == str(tmp_path.resolve())


# ---- get_backend registry ----------------------------------------------


def test_get_backend_dispatch() -> None:
    assert isinstance(get_backend("claude"), ClaudeBackend)
    assert isinstance(get_backend("codex"), CodexBackend)
    assert isinstance(get_backend("opencode"), OpencodeBackend)
    assert isinstance(get_backend("agy"), AgyBackend)


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


# ---- AgyBackend --------------------------------------------------------
#
# agy is a non-interactive JSON-envelope CLI (single JSON blob on stdout, not
# JSONL). Success requires BOTH exit_code == 0 AND payload.status == "SUCCESS".
# Tokens come from `usage.input_tokens` / `usage.output_tokens`; cost stays 0
# (dashboard's pricing.yaml estimates USD from token counts).


import json as _json  # local alias to avoid touching module imports order


def _agy_success_payload(input_tokens: int = 25580, output_tokens: int = 175) -> str:
    return _json.dumps(
        {
            "conversation_id": "conv-abc",
            "status": "SUCCESS",
            "response": "hello world",
            "duration_seconds": 1.9,
            "num_turns": 1,
            "usage": {
                "input_tokens": input_tokens,
                "output_tokens": output_tokens,
                "thinking_tokens": 174,
                "cache_read_tokens": 0,
                "total_tokens": input_tokens + output_tokens,
            },
        }
    )


def test_agy_build_cmd_ordering() -> None:
    """`--print` MUST be the LAST flag; its value is the prompt string.

    The agy CLI parses `--print` positionally — anything after it gets
    swallowed as part of the prompt. build_cmd must lock the exact ordering.
    """
    task = _mk_task(tid="A-001", model="agy/pro")
    route = _mk_route(backend="agy", cli_model="gemini-3.1-pro-high", tier="premium")
    cmd = AgyBackend().build_cmd(task, route, "hello prompt body")
    assert cmd == [
        "agy",
        "--output-format",
        "json",
        "--model",
        "gemini-3.1-pro-high",
        "--print",
        "hello prompt body",
    ]
    # Ordering constraint: --print is last, prompt right after.
    assert cmd[-2] == "--print"
    assert cmd[-1] == "hello prompt body"


def test_agy_parse_result_success() -> None:
    log_text = _agy_success_payload(input_tokens=100, output_tokens=50)
    res = AgyBackend().parse_result(exit_code=0, log_text=log_text)
    assert res.success is True
    assert res.exit_code == 0
    assert res.tokens_in == 100
    assert res.tokens_out == 50
    assert res.cost_usd == 0.0
    assert res.error_message is None


def test_agy_parse_result_non_success_status() -> None:
    payload = _json.dumps(
        {
            "conversation_id": "conv-xyz",
            "status": "TIMEOUT",
            "response": "",
            "usage": {"input_tokens": 10, "output_tokens": 0, "total_tokens": 10},
        }
    )
    res = AgyBackend().parse_result(exit_code=0, log_text=payload)
    assert res.success is False
    assert res.error_message is not None
    assert "TIMEOUT" in res.error_message
    # Token counts still surfaced when present so the budget gate sees them.
    assert res.tokens_in == 10
    assert res.tokens_out == 0


def test_agy_parse_result_malformed_json_exit_zero() -> None:
    """agy always emits JSON in `--output-format json`; garbage output is a failure."""
    res = AgyBackend().parse_result(exit_code=0, log_text="not json at all\n")
    assert res.success is False
    assert res.error_message is not None
    assert res.tokens_in == 0
    assert res.tokens_out == 0


def test_agy_parse_result_exit_nonzero() -> None:
    res = AgyBackend().parse_result(exit_code=1, log_text="fatal: connection refused\n")
    assert res.success is False
    assert res.error_message is not None
    assert res.exit_code == 1


def test_agy_extract_cost_from_usage() -> None:
    log_text = _agy_success_payload(input_tokens=100, output_tokens=50)
    cost, tin, tout = AgyBackend().extract_cost(log_text)
    assert cost == 0.0
    assert tin == 100
    assert tout == 50


def test_agy_extract_cost_missing_usage() -> None:
    payload = _json.dumps({"status": "SUCCESS", "response": "ok"})
    cost, tin, tout = AgyBackend().extract_cost(payload)
    assert (cost, tin, tout) == (0.0, 0, 0)


def test_agy_extract_cost_malformed_json() -> None:
    """Defensive: garbage input must not raise."""
    cost, tin, tout = AgyBackend().extract_cost("not json{{{")
    assert (cost, tin, tout) == (0.0, 0, 0)


def test_agy_registry_lookup() -> None:
    assert isinstance(get_backend("agy"), AgyBackend)
    assert get_backend("agy").name == "agy"
