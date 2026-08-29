"""Backend dispatcher for the Rupies v2 orchestrator.

Contracts respected here (from `orchestrator/spec.md` §1.3 and
`orchestrator/design.md` §5):

- Five concrete adapters (`ClaudeBackend`, `CodexBackend`, `OpencodeBackend`,
  `GeminiBackend`, `AgyBackend`) implement a common `Backend` `Protocol`. The main loop
  (R-017/R-018) never imports the concrete classes — it dispatches on
  `route.backend`.
- **Prompt delivery**: stdin for claude/codex/opencode; `-p <text>` arg for gemini (approved decision, see
  `design.md OPEN`). `spawn` opens the prompt file and pipes its bytes to
  `subprocess.Popen(..., stdin=PIPE)`. We never embed the 1-2 KB prompt in
  the argv or via `@file` — that route hit escaping edge-cases in practice.
- The subprocess's stdout AND stderr are captured to
  `state/logs/<task-id>.log` so:
    (a) the main loop can grep for the `task-finish.sh <id>` marker to
        defend against id-spoofing (AS-10 / C-2), and
    (b) `parse_result` has the raw text to extract cost / tokens without
        having to keep the subprocess alive.
- Cost extraction table (design §5):
    | Backend  | Source                                                       |
    |----------|--------------------------------------------------------------|
    | claude   | stdout JSON envelope: `total_cost_usd` + `usage.*_tokens`    |
    | codex    | JSONL in `-o <out>` file: Σ `step_finish.cost` + Σ `tokens`  |
    | opencode | JSONL stdout stream: Σ `step_finish.cost` + Σ `tokens`       |

What this module does NOT do (kept for R-017/R-018):
    - The main loop (`orch.py`).
    - `os.waitpid(-1, WNOHANG)` reap loop.
    - `threading.Semaphore` concurrency gates.
    - Post-run `git diff` computation to populate `DispatchResult.files_touched`
      — the dispatcher leaves that field empty; the main loop fills it.
    - The strict-files revert decision (`cfg.strict_files_phases`).
"""

from __future__ import annotations

import json
import logging
import os
import re
import signal
import subprocess
import time
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import Enum
from pathlib import Path
from typing import Any, Iterator, Protocol, runtime_checkable

from .models import Dispatch, RouteEntry, Task

log = logging.getLogger(__name__)


# ---- Constants -----------------------------------------------------------


# Grace period between SIGTERM and SIGKILL on timeout (FR-D-3, AS-03).
_TERM_TO_KILL_GRACE_S = 10.0

# Where per-dispatch stdout/stderr logs live, relative to `state_dir`.
_LOGS_SUBDIR = "logs"


# ---- Result type ---------------------------------------------------------


@dataclass
class DispatchResult:
    """Terminal state of one subprocess dispatch (success or failure).

    Every field is populated by `parse_result` / `wait_result`, EXCEPT:
      - `files_touched`: left `[]` by the dispatcher; the main loop fills it
        from `git diff --name-only` after the child reaps. Doing it here
        would force the dispatcher to know the v2 root — which it doesn't.
    """

    exit_code: int
    success: bool
    cost_usd: float = 0.0
    tokens_in: int = 0
    tokens_out: int = 0
    stdout: str = ""
    stderr: str = ""
    error_message: str | None = None
    files_touched: list[str] = field(default_factory=list)
    # FR-D-7: set by parse_result / post-run detection when the failure looks
    # like a version-drift issue (backend rejected the cli_model). The main
    # loop reads this flag to decide whether to swap in `route.fallback_cli_model`
    # before retrying instead of blocking.
    should_retry_with_fallback: bool = False
    # Sprint A / Issue #8: True when the backend produced step_finish events
    # but never populated usage (tokens_in/out both 0). The main loop
    # propagates this to the spend row so downstream reporting/dashboards can
    # distinguish real zero-cost dispatches from missing telemetry.
    estimated: bool = False


# ---- Utilities -----------------------------------------------------------


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


# ---- Failure classification (Fase B) ------------------------------------


class FailureClass(str, Enum):
    """Coarse category of a failed dispatch, driving the reap loop's retry policy.

    Values are lower-snake strings so they serialize into event logs / dashboards
    without a custom encoder. Order of definition matches the order the
    `classify_failure` matcher checks (specificity first).
    """

    ID_SPOOF = "id_spoof"           # agent invoked task-finish.sh with wrong id
    TIMEOUT = "timeout"             # subprocess exceeded estimateHours * multiplier
    RATE_LIMIT = "rate_limit"       # 429 / "rate limit" / "too many requests"
    PERMISSION = "permission"       # auth/permission denied — retrying won't help
    BUDGET = "budget"               # spend cap hit — retrying wastes more budget
    VERSION_DRIFT = "version_drift" # backend rejected the requested cli_model
    TRANSIENT = "transient"         # 5xx / UnknownError / err_xxxxxxxx server hiccup
    PARSER = "parser"               # our own parse_result couldn't read the output
    OTHER = "other"                 # unknown — conservative default, retry once


# Substrings we treat as evidence the backend rejected the requested cli_model
# (e.g. version drifted, model was renamed, or the local CLI's registry doesn't
# know it yet). Kept broad — false positives just trigger a single fallback
# retry, which is cheap; false negatives leave the task blocked, which is loud.
_VERSION_DRIFT_MARKERS: tuple[str, ...] = (
    "model not found",
    "unknown model",
    "no such model",
    "invalid model",
    "model does not exist",
    "unsupported model",
)

# Substrings set explicitly by orch/wait_result — these live in `error_message`
# only, NOT in CLI stdout/stderr, so we anchor on the field.
_ID_SPOOF_MARKER = "id spoofing detected"
_TIMEOUT_MARKER = "orchestrator timeout"

# Rate-limit markers (specific — checked before generic TRANSIENT 5xx).
_RATE_LIMIT_MARKERS: tuple[str, ...] = (
    "429",
    "rate limit",
    "too many requests",
)

# Permission/auth markers.
_PERMISSION_MARKERS: tuple[str, ...] = (
    "permission denied",
    "not authorized",
    "unauthorized",
    "auth error",
    "authentication failed",
    "401",
    "403",
)

# Budget markers — the orchestrator's per-dispatch budget cap, or a backend's
# own budget rejection.
_BUDGET_MARKERS: tuple[str, ...] = (
    "max budget",
    "budget exceeded",
    "over budget",
)

# Transient markers — server-side hiccups where a simple retry usually clears.
# `_ERR_CODE_RE` catches opencode/opencodego's `err_xxxxxxxx` correlation ids.
_TRANSIENT_MARKERS: tuple[str, ...] = (
    "500",
    "502",
    "503",
    "504",
    "internal server error",
    "bad gateway",
    "service unavailable",
    "gateway timeout",
    "unknownerror",
    "connection reset",
    "connection dropped",
    "econnreset",
)
_ERR_CODE_RE = re.compile(r"\berr_[0-9a-f]{6,}\b", re.IGNORECASE)

# Parser markers — our own error_message strings when parse_result gives up.
_PARSER_MARKERS: tuple[str, ...] = (
    "could not parse",
    "produced no jsonl events",
    "produced no events",
    "parse_result raised",
)


def _classify_haystack(result: "DispatchResult") -> str:
    """Concatenate error_message + stderr + tail of stdout, lowercased.

    Kept as one string (not a list) so substring / regex scans are one pass.
    stdout is tailed to 2 KB — enough to catch a trailing error line without
    turning classification into an O(MB) scan on large JSONL streams.
    """
    parts: list[str] = []
    if result.error_message:
        parts.append(result.error_message)
    if result.stderr:
        parts.append(result.stderr)
    if result.stdout:
        parts.append(result.stdout[-2048:])
    return "\n".join(parts).lower()


def classify_failure(result: "DispatchResult") -> FailureClass:
    """Map a failed `DispatchResult` to a `FailureClass`. PURE (no I/O).

    Order matters — checks that key on orch-set markers (`error_message`) run
    first, then specific CLI-output markers, then generic ones, then the
    conservative `OTHER` default. Callers pass any DispatchResult; success
    results are NOT special-cased here (the reap loop only calls this on
    failure).

    See module-level `_*_MARKERS` tuples for the concrete substrings.
    """
    # 1) ID_SPOOF — set explicitly by _post_run_checks; wins over any CLI output.
    if result.error_message and _ID_SPOOF_MARKER in result.error_message.lower():
        return FailureClass.ID_SPOOF

    # 2) TIMEOUT — set explicitly by _wait_with_timeout / reap loop.
    if result.error_message and _TIMEOUT_MARKER in result.error_message.lower():
        return FailureClass.TIMEOUT

    blob = _classify_haystack(result)

    # 3) RATE_LIMIT — specific keywords, checked before generic 5xx TRANSIENT.
    if any(m in blob for m in _RATE_LIMIT_MARKERS):
        return FailureClass.RATE_LIMIT

    # 4) PERMISSION — auth errors are terminal; no retry.
    if any(m in blob for m in _PERMISSION_MARKERS):
        return FailureClass.PERMISSION

    # 5) BUDGET — spend cap hit; no retry.
    if any(m in blob for m in _BUDGET_MARKERS):
        return FailureClass.BUDGET

    # 6) VERSION_DRIFT — reuses the historical marker list; triggers fallback.
    if any(m in blob for m in _VERSION_DRIFT_MARKERS):
        return FailureClass.VERSION_DRIFT

    # 7) TRANSIENT — 5xx / UnknownError / err_xxxxxxxx correlation ids.
    if any(m in blob for m in _TRANSIENT_MARKERS) or _ERR_CODE_RE.search(blob):
        return FailureClass.TRANSIENT

    # 8) PARSER — our own error_message strings when parse_result gives up.
    #    Checked after TRANSIENT so a "parse failed after 500" leans transient.
    if any(m in blob for m in _PARSER_MARKERS):
        return FailureClass.PARSER

    # 9) Conservative default — retry once, same model, standard backoff.
    return FailureClass.OTHER


def is_version_drift_error(result: "DispatchResult") -> bool:
    """Return True if `result` looks like the backend rejected the cli_model.

    Backwards-compat wrapper: kept so existing callers (`orch.py::_reap_once`)
    don't have to import `FailureClass` just to run the drift check. New code
    should call `classify_failure(result) == FailureClass.VERSION_DRIFT`
    directly.
    """
    return classify_failure(result) is FailureClass.VERSION_DRIFT


def _ensure_logs_dir(state_dir: Path) -> Path:
    """Create `state/logs/` on first spawn and return the directory."""
    logs = Path(state_dir) / _LOGS_SUBDIR
    logs.mkdir(parents=True, exist_ok=True)
    return logs


def _parse_claude_envelope(text: str) -> dict[str, Any] | None:
    """Extract the top-level JSON envelope from claude's `--output-format json`.

    claude emits ONE JSON object on stdout. If the process is killed mid-run
    or stderr leaks into stdout, the last non-empty line that parses as JSON
    is the safe candidate — try trailing-line first, fall back to whole text.
    Returns None if nothing parses.
    """
    stripped = text.strip()
    if not stripped:
        return None
    # Fast path: whole stdout is the envelope.
    try:
        obj = json.loads(stripped)
        if isinstance(obj, dict):
            return obj
    except json.JSONDecodeError:
        pass
    # Fallback: last non-empty line.
    for line in reversed(stripped.splitlines()):
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict):
            return obj
    return None


def _iter_jsonl_events(source: str | Path) -> Iterator[dict[str, Any]]:
    """Yield every parseable JSON object from a JSONL blob or file path.

    Silently skips lines that don't parse — real codex/opencode output can
    include partial trailing lines when the CLI is SIGKILLed. Matches the
    dashboard's tolerance in NFR-OBS-2.
    """
    if isinstance(source, Path):
        try:
            text = source.read_text(encoding="utf-8")
        except OSError as exc:
            log.warning("could not read jsonl source %s: %s", source, exc)
            return
    else:
        text = source
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict):
            yield obj


# ---- Backend Protocol ----------------------------------------------------


@runtime_checkable
class Backend(Protocol):
    """Adapter contract for a CLI backend.

    Concrete implementations MUST set a class-level `name` attribute matching
    the `RouteEntry.backend` literal (`"claude" | "codex" | "opencode"`).
    """

    name: str

    def build_cmd(self, task: Task, route: RouteEntry) -> list[str]:
        """Return the argv list for `subprocess.Popen`. NO shell metacharacters."""
        ...

    def spawn(
        self,
        task: Task,
        route: RouteEntry,
        prompt_path: Path,
        log_path: Path,
        cwd: Path,
    ) -> Dispatch:
        """Fork the CLI, wired up per the backend's stdin/stdout contract.

        `prompt_path` is piped in via stdin. `log_path` captures stdout + stderr
        (merged). Returns a populated `Dispatch` with the child PID and the
        Popen stashed on `.` `_popen` for `wait_result`.
        """
        ...

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:
        """Block until the child terminates or `timeout_s` elapses.

        Timeout path: SIGTERM, wait `_TERM_TO_KILL_GRACE_S`, then SIGKILL.
        Always returns a `DispatchResult`; on timeout, `error_message` starts
        with `"orchestrator timeout"` (FR-D-3 reason string).
        """
        ...

    def parse_result(
        self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None
    ) -> DispatchResult:
        """Pure: given exit code + captured stdout/stderr, build a DispatchResult.

        Kept pure so tests can pin the parsing contract against fixture text
        without spawning any subprocess. `extra` carries backend-specific
        artifact paths (e.g. codex's `-o` file).
        """
        ...

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:
        """Return `(cost_usd, tokens_in, tokens_out)` from backend output."""
        ...


# ---- Base spawn/wait helpers (shared) -----------------------------------


def _open_prompt_and_log(
    prompt_path: Path, log_path: Path
) -> tuple[Any, Any]:
    """Open prompt (read) + log (append) file handles for a Popen call.

    Returns `(prompt_fh, log_fh)`. Callers MUST close both after the child
    exits (or on error) — Popen keeps refs but doesn't own them.
    """
    log_path.parent.mkdir(parents=True, exist_ok=True)
    prompt_fh = open(prompt_path, "rb")
    log_fh = open(log_path, "ab")
    return prompt_fh, log_fh


def _spawn_generic(
    backend_name: str,
    task: Task,
    cmd: list[str],
    prompt_path: Path,
    log_path: Path,
    cwd: Path,
    session_id: str,
    output_path: str,
    extra: dict[str, Any] | None = None,
) -> Dispatch:
    """Shared Popen wiring for all three backends.

    stdin = the prompt file (bytes); stdout & stderr merged into `log_path`.
    We stash the Popen handle on `dispatch._popen` and the file handles on
    `dispatch._fhs` so `wait_result` can close them.
    """
    prompt_fh, log_fh = _open_prompt_and_log(prompt_path, log_path)
    # Scrub PWD so opencode doesn't inherit the parent shell's cwd (it resolves
    # file writes against $PWD, ignoring Popen(cwd=...)). Harmless for claude
    # and codex — they both take an explicit dir flag already.
    env = {**os.environ, "PWD": str(cwd)}
    try:
        popen = subprocess.Popen(  # noqa: S603 — argv is locally constructed
            cmd,
            stdin=prompt_fh,
            stdout=log_fh,
            stderr=subprocess.STDOUT,
            cwd=str(cwd),
            env=env,
            close_fds=True,
            # Sprint A / Issue #12: each child becomes its own session leader
            # (and process-group leader). Cleanup on SIGINT / SIGTERM / timeout
            # can then use `os.killpg(os.getpgid(pid), sig)` to catch not just
            # the CLI but any bash wrappers or sub-agents it forked. Without
            # this, killing the CLI could leave grandchildren orphaned in the
            # user's login session.
            start_new_session=True,
        )
    except (FileNotFoundError, OSError):
        # Clean up handles before re-raising so we don't leak.
        prompt_fh.close()
        log_fh.close()
        raise

    dispatch = Dispatch(
        task_id=task.id,
        backend=backend_name,  # type: ignore[arg-type]
        pid=popen.pid,
        session_id=session_id,
        started_at=_utc_now_iso(),
        prompt_path=str(prompt_path),
        log_path=str(log_path),
        output_path=output_path,
    )
    # Attach non-schema runtime handles. `Dispatch` isn't slotted so this is
    # safe; the sidecar avoids polluting `models.py` (frozen scope).
    dispatch._popen = popen  # type: ignore[attr-defined]
    dispatch._fhs = (prompt_fh, log_fh)  # type: ignore[attr-defined]
    dispatch.extra = dict(extra or {})  # type: ignore[attr-defined]
    return dispatch


def _signal_child_group(popen: subprocess.Popen, sig: int) -> None:
    """Send `sig` to the child's process group; fall back to the direct pid.

    Sprint A / Issue #12: mirrors `orch._killpg_or_pid` but scoped to a
    single Popen. Kept in dispatcher.py so `_wait_with_timeout` doesn't
    have to import from orch.py (would create a circular import).
    """
    pid = popen.pid
    try:
        pgid = os.getpgid(pid)
    except (ProcessLookupError, PermissionError, OSError):
        try:
            popen.send_signal(sig)
        except (ProcessLookupError, OSError):
            pass
        return
    try:
        os.killpg(pgid, sig)
    except (ProcessLookupError, PermissionError, OSError):
        try:
            popen.send_signal(sig)
        except (ProcessLookupError, OSError):
            pass


def _wait_with_timeout(dispatch: Dispatch, timeout_s: float) -> tuple[int, str, bool]:
    """Wait for the child, enforcing SIGTERM → grace → SIGKILL on timeout.

    Returns `(exit_code, error_message_or_empty, timed_out)`.

    Sprint A / Issue #12: uses process-group signals (killpg) so bash
    wrappers or sub-agents forked by the CLI get reaped too, not just
    the direct child.
    """
    popen: subprocess.Popen = dispatch._popen  # type: ignore[attr-defined]
    timed_out = False
    err_msg = ""
    try:
        exit_code = popen.wait(timeout=timeout_s)
    except subprocess.TimeoutExpired:
        timed_out = True
        err_msg = f"orchestrator timeout after {timeout_s:.0f}s"
        # SIGTERM to the whole process group first — polite request.
        _signal_child_group(popen, signal.SIGTERM)
        try:
            exit_code = popen.wait(timeout=_TERM_TO_KILL_GRACE_S)
        except subprocess.TimeoutExpired:
            # Grace expired — SIGKILL the group and reap unconditionally.
            _signal_child_group(popen, signal.SIGKILL)
            try:
                exit_code = popen.wait(timeout=5.0)
            except subprocess.TimeoutExpired:
                # Truly zombied. Return -9 as a marker.
                exit_code = -9

    # Close the file handles we opened in spawn.
    fhs = getattr(dispatch, "_fhs", None)
    if fhs is not None:
        for fh in fhs:
            try:
                fh.close()
            except OSError:
                pass
    return exit_code, err_msg, timed_out


def _read_log(log_path: str | Path) -> str:
    """Read the captured stdout/stderr log; return '' on missing file."""
    try:
        return Path(log_path).read_text(encoding="utf-8", errors="replace")
    except (OSError, FileNotFoundError):
        return ""


# ---- ClaudeBackend -------------------------------------------------------


class ClaudeBackend:
    """Adapter for the `claude` CLI (Anthropic-native).

    Contract source: `explore.md §1` (tested end-to-end at exploration time).
    Success: JSON envelope `is_error==false && subtype=="success"`.
    """

    name = "claude"

    def __init__(self, cfg: dict[str, Any] | None = None):
        # `cfg` is optional — we only reach into it for the optional
        # `claude_max_budget_usd` knob. Default None → the flag is omitted.
        self._cfg = cfg or {}

    def build_cmd(self, task: Task, route: RouteEntry) -> list[str]:
        """Return the argv for `claude -p --output-format json ...`.

        Prompt is delivered via stdin (see module docstring). The cwd is
        supplied at spawn time; `--add-dir` grants the CLI explicit write
        access to that directory (defense-in-depth against escapes).
        """
        # We use a per-dispatch session id so retries stay correlatable in
        # the claude session store; caller can override via `route.session_id`
        # if it ever grows that field.
        session_id = str(uuid.uuid4())
        cmd: list[str] = [
            "claude",
            "-p",
            "--output-format",
            "json",
            "--model",
            route.cli_model,
            "--session-id",
            session_id,
            "--add-dir",
            ".",  # cwd — spawn passes the actual v2 root via `cwd=`
            "--permission-mode",
            "acceptEdits",
        ]
        # Optional budget cap — only add if the operator wired it up.
        budget = self._cfg.get("claude_max_budget_usd")
        if budget is None:
            budget = self._cfg.get("budget", {}).get("per_dispatch_usd") \
                if isinstance(self._cfg.get("budget"), dict) else None
        if budget is not None:
            cmd.extend(["--max-budget-usd", str(budget)])
        return cmd

    def spawn(
        self,
        task: Task,
        route: RouteEntry,
        prompt_path: Path,
        log_path: Path,
        cwd: Path,
    ) -> Dispatch:
        _ensure_logs_dir(log_path.parent.parent)
        cmd = self.build_cmd(task, route)
        session_id = _extract_session_id(cmd)
        return _spawn_generic(
            self.name,
            task,
            cmd,
            prompt_path,
            log_path,
            cwd,
            session_id=session_id,
            output_path="",  # claude has no separate output file
        )

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:
        exit_code, err_msg, timed_out = _wait_with_timeout(dispatch, timeout_s)
        log_text = _read_log(dispatch.log_path)
        result = self.parse_result(exit_code, log_text)
        if timed_out:
            result.success = False
            result.error_message = err_msg
        return result

    def parse_result(
        self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None
    ) -> DispatchResult:
        env = _parse_claude_envelope(log_text)
        if env is None:
            return DispatchResult(
                exit_code=exit_code,
                success=False,
                stdout=log_text,
                error_message="could not parse claude JSON envelope",
            )
        is_error = bool(env.get("is_error"))
        subtype = env.get("subtype", "")
        success = (exit_code == 0) and (not is_error) and (subtype == "success")
        cost_usd, tokens_in, tokens_out = self.extract_cost(log_text)
        error_message: str | None = None
        if not success:
            err = env.get("api_error_status") or env.get("terminal_reason") or ""
            if isinstance(err, dict):
                error_message = err.get("message") or str(err)
            else:
                error_message = str(err) or f"claude reported is_error={is_error}"
        return DispatchResult(
            exit_code=exit_code,
            success=success,
            cost_usd=cost_usd,
            tokens_in=tokens_in,
            tokens_out=tokens_out,
            stdout=log_text,
            stderr="",
            error_message=error_message,
        )

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:
        env = _parse_claude_envelope(log_text)
        if env is None:
            return 0.0, 0, 0
        cost = float(env.get("total_cost_usd", 0.0) or 0.0)
        usage = env.get("usage") or {}
        tokens_in = int(usage.get("input_tokens", 0) or 0)
        tokens_out = int(usage.get("output_tokens", 0) or 0)
        return cost, tokens_in, tokens_out


def _extract_session_id(cmd: list[str]) -> str:
    """Recover the `--session-id` flag value from a built argv."""
    try:
        i = cmd.index("--session-id")
        return cmd[i + 1]
    except (ValueError, IndexError):
        return ""


# ---- CodexBackend --------------------------------------------------------


class CodexBackend:
    """Adapter for the `codex` CLI (OpenAI-native).

    Success signal (from `explore.md §1`): the `-o` JSONL file ends with a
    non-error terminal event AND no event has `type:"error"`. Cost is the sum
    of `step_finish.cost` over every step in the `-o` file.
    """

    name = "codex"

    def build_cmd(self, task: Task, route: RouteEntry) -> list[str]:
        """Return the argv for `codex exec --skip-git-repo-check --json ...`.

        The `-o` path is a per-task file under `state/logs/`; we know the task
        id here but not the state dir, so `spawn` overwrites `output_path` in
        the argv just before Popen.
        """
        # Placeholder `-o` value — `spawn` replaces it with the resolved path.
        return [
            "codex",
            "exec",
            "--skip-git-repo-check",
            "--json",
            "-o",
            f"__OUTPUT__{task.id}.codex.json",
            "-C",
            ".",  # cwd — spawn passes actual v2 root via `cwd=`
            # `--approve-for-me` ya implica el sandbox workspace-write; pasar
            # `-s workspace-write` explícito choca en codex >=0.148 ("cannot be
            # used with --approve-for-me"). Dejamos solo --approve-for-me.
            "--approve-for-me",
            "-m",
            route.cli_model,
        ]

    def spawn(
        self,
        task: Task,
        route: RouteEntry,
        prompt_path: Path,
        log_path: Path,
        cwd: Path,
    ) -> Dispatch:
        logs_dir = _ensure_logs_dir(log_path.parent.parent)
        # Concrete `-o` path lives beside the log — one file per dispatch.
        output_path = logs_dir / f"{task.id}.codex.json"

        cmd = self.build_cmd(task, route)
        # Replace the placeholder -o value with the concrete path.
        for i, arg in enumerate(cmd):
            if arg.startswith("__OUTPUT__"):
                cmd[i] = str(output_path)
                break

        return _spawn_generic(
            self.name,
            task,
            cmd,
            prompt_path,
            log_path,
            cwd,
            session_id="",  # codex issues its own session id in the JSONL
            output_path=str(output_path),
            extra={"codex_output_path": str(output_path)},
        )

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:
        exit_code, err_msg, timed_out = _wait_with_timeout(dispatch, timeout_s)
        # El JSONL de codex (`--json`) va a STDOUT, capturado en `log_path`.
        # El archivo `-o` (`--output-last-message`) solo tiene el último mensaje
        # en markdown, no eventos — por eso parseamos el log, no el `-o`.
        log_text = _read_log(dispatch.log_path)
        result = self.parse_result(exit_code, log_text)
        result.stdout = log_text
        if timed_out:
            result.success = False
            result.error_message = err_msg
        return result

    def parse_result(
        self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None
    ) -> DispatchResult:
        events = list(_iter_jsonl_events(log_text))
        has_error = any(_is_codex_error(e) for e in events)
        # Terminal de éxito en codex >=0.148: el último evento `turn.*` es
        # `turn.completed` (y no `turn.failed`). El esquema viejo (`task_complete`
        # / `step_finish`) ya no se emite.
        terminal_ok = False
        for ev in reversed(events):
            t = ev.get("type")
            if t == "turn.completed":
                terminal_ok = True
                break
            if t == "turn.failed":
                terminal_ok = False
                break
        success = (exit_code == 0) and (not has_error) and terminal_ok
        cost_usd, tokens_in, tokens_out = _sum_codex_usage(events)
        error_message: str | None = None
        if not success:
            err_ev = next((e for e in events if _is_codex_error(e)), None)
            if err_ev is not None:
                error_message = _codex_error_message(err_ev)
            elif not events:
                error_message = "codex produced no JSONL events"
            else:
                error_message = f"codex terminal event was not success (exit={exit_code})"
        return DispatchResult(
            exit_code=exit_code,
            success=success,
            cost_usd=cost_usd,
            tokens_in=tokens_in,
            tokens_out=tokens_out,
            stdout=log_text,
            stderr="",
            error_message=error_message,
        )

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:
        return _sum_codex_usage(list(_iter_jsonl_events(log_text)))


# ---- OpencodeBackend -----------------------------------------------------


# opencode reasons other than these still indicate a completed run
_OPENCODE_FAILURE_REASONS: frozenset[str] = frozenset(
    {"error", "cancelled", "aborted", "canceled"}
)


class OpencodeBackend:
    """Adapter for the `opencode` CLI (pass-through providers).

    Success: terminal `step_finish` whose `reason` is NOT in
    ``_OPENCODE_FAILURE_REASONS`` AND no `type:"error"` event in the stream.
    Any other terminal reason (`"stop"`, `"unknown"`, `"length"`,
    provider-specific values) counts as a completed run. Cost sums
    `step_finish.cost` (same shape as codex).
    """

    name = "opencode"

    def build_cmd(self, task: Task, route: RouteEntry, cwd: Path) -> list[str]:
        """Return the argv for `opencode run --format json --auto --dir <abs> ...`.

        `RouteEntry` has no `backend_provider` field — router entries already
        embed the provider prefix in `cli_model` (see `model_router.yaml`,
        e.g. `google/gemini-3.0-pro`). Use `cli_model` verbatim.

        `--dir` is required because opencode resolves file writes against $PWD
        (or its own project detection), ignoring Popen(cwd=...). The value must
        be absolute so it works regardless of what shell the orch was launched
        from.
        """
        return [
            "opencode",
            "run",
            "--format",
            "json",
            "--model",
            route.cli_model,
            "--auto",
            "--dir",
            str(Path(cwd).resolve()),
        ]

    def spawn(
        self,
        task: Task,
        route: RouteEntry,
        prompt_path: Path,
        log_path: Path,
        cwd: Path,
    ) -> Dispatch:
        _ensure_logs_dir(log_path.parent.parent)
        cmd = self.build_cmd(task, route, cwd)
        return _spawn_generic(
            self.name,
            task,
            cmd,
            prompt_path,
            log_path,
            cwd,
            session_id="",  # opencode issues its own session id in the JSONL
            output_path="",  # JSONL is streamed to stdout → captured in log_path
            # Stash cli_model on the Dispatch so parse_result can key the
            # once-per-model "no usage reported" warning by (backend, model).
            extra={"cli_model": route.cli_model},
        )

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:
        exit_code, err_msg, timed_out = _wait_with_timeout(dispatch, timeout_s)
        log_text = _read_log(dispatch.log_path)
        # Pass along the cli_model (stashed in `Dispatch.extra` during spawn) so
        # the "no usage reported" warning fires once per (backend, model).
        extra = getattr(dispatch, "extra", None) or {}
        result = self.parse_result(exit_code, log_text, extra=extra)
        if timed_out:
            result.success = False
            result.error_message = err_msg
        return result

    def parse_result(
        self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None
    ) -> DispatchResult:
        events = list(_iter_jsonl_events(log_text))
        has_error = any(e.get("type") == "error" for e in events)
        # Terminal predicate: last step_finish must NOT have a failure reason.
        # Real opencode 1.18.19 wraps `reason` inside `part.reason`; the shared
        # `_step_finish_payload` helper tolerates both layouts.
        terminal_ok = False
        terminal_reason: str | None = None
        for ev in reversed(events):
            if ev.get("type") == "step_finish":
                payload = _step_finish_payload(ev)
                terminal_reason = payload.get("reason") or ""
                terminal_ok = terminal_reason not in _OPENCODE_FAILURE_REASONS
                break
        success = (exit_code == 0) and (not has_error) and terminal_ok
        cost_usd, tokens_in, tokens_out = _sum_step_finish_costs(events)
        # Sprint A / Issue #8: if opencode produced step_finish events but never
        # populated usage (both token counts zero), warn once and mark the row
        # estimated so downstream code can distinguish "no work" from "no
        # telemetry".
        estimated = False
        if _has_any_step_finish(events) and tokens_in == 0 and tokens_out == 0:
            estimated = True
            model = ""
            if extra and isinstance(extra, dict):
                model = str(extra.get("cli_model") or "")
            _warn_usage_missing_once(self.name, model)
        error_message: str | None = None
        if not success:
            err_ev = next((e for e in events if e.get("type") == "error"), None)
            if err_ev:
                err = err_ev.get("error") or {}
                if isinstance(err, dict):
                    error_message = err.get("message") or str(err)
                else:
                    error_message = str(err)
            elif not events:
                error_message = "opencode produced no JSONL events"
            elif terminal_reason is None:
                error_message = (
                    f"opencode did not emit a terminal step_finish event "
                    f"(exit={exit_code})"
                )
            else:
                error_message = (
                    f"opencode step_finish reason={terminal_reason!r} is in "
                    f"failure set {sorted(_OPENCODE_FAILURE_REASONS)} "
                    f"(exit={exit_code})"
                )
        return DispatchResult(
            exit_code=exit_code,
            success=success,
            cost_usd=cost_usd,
            tokens_in=tokens_in,
            tokens_out=tokens_out,
            stdout=log_text,
            stderr="",
            error_message=error_message,
            estimated=estimated,
        )

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:
        return _sum_step_finish_costs(list(_iter_jsonl_events(log_text)))


# ---- Shared helpers ------------------------------------------------------


# Tracks (backend_name, cli_model) pairs we've already warned about not
# reporting usage. Prevents log spam: one warning per (backend, model) per
# process run. Cleared on module reload / process restart.
_USAGE_MISSING_WARNED: set[tuple[str, str]] = set()


def _step_finish_payload(ev: dict[str, Any]) -> dict[str, Any]:
    """Return the payload dict for a step_finish event, tolerating schema drift.

    Real opencode 1.18.19 wraps the fields (`cost`, `tokens`, `reason`) inside
    an inner `part` dict:
        {"type":"step_finish", "part": {"cost": 0.003, "tokens": {...}, "reason": "stop"}}

    Older/aspirational fixtures put them at the top level:
        {"type":"step_finish", "cost": 0.003, "tokens": {...}, "reason": "stop"}

    This helper returns whichever level has the payload keys, preferring `part`
    (real opencode) when both exist. Falls back to the event itself so the
    legacy shape keeps working.
    """
    part = ev.get("part")
    if isinstance(part, dict) and (
        "cost" in part or "tokens" in part or "reason" in part
    ):
        return part
    return ev


def _sum_step_finish_costs(events: list[dict[str, Any]]) -> tuple[float, int, int]:
    """Sum `step_finish.cost` and token counts across every step_finish event.

    Used by both codex and opencode. Real opencode 1.18.19 wraps the payload
    under `part` (`part.cost`, `part.tokens.{input,output}`); older/test
    fixtures put those fields at the top level. `_step_finish_payload` picks
    the right layer so we never silently return 0.

    Some model providers via opencode may not emit token usage at all — in
    that case this returns 0s and the caller should treat the spend as
    estimated (see `OpencodeBackend.parse_result` which sets the `estimated`
    flag on the `DispatchResult`).
    """
    cost = 0.0
    tokens_in = 0
    tokens_out = 0
    for ev in events:
        if ev.get("type") != "step_finish":
            continue
        payload = _step_finish_payload(ev)
        try:
            cost += float(payload.get("cost", 0.0) or 0.0)
        except (TypeError, ValueError):
            pass
        tokens = payload.get("tokens") or {}
        if isinstance(tokens, dict):
            try:
                tokens_in += int(tokens.get("input", 0) or 0)
            except (TypeError, ValueError):
                pass
            try:
                tokens_out += int(tokens.get("output", 0) or 0)
            except (TypeError, ValueError):
                pass
    return cost, tokens_in, tokens_out


def _has_any_step_finish(events: list[dict[str, Any]]) -> bool:
    """True when at least one `step_finish` event exists in the stream."""
    return any(ev.get("type") == "step_finish" for ev in events)


def _warn_usage_missing_once(backend_name: str, cli_model: str) -> None:
    """Emit one warning per (backend, model) when usage numbers are all zero.

    Some opencode providers (older models, some free-tier passthroughs) don't
    populate `tokens.{input,output}` on `step_finish`. Silently accepting
    that as `0 tokens` breaks budget guardrails and hides real spend. We warn
    the operator and mark the spend row `estimated: true` upstream.
    """
    key = (backend_name, cli_model or "unknown")
    if key in _USAGE_MISSING_WARNED:
        return
    _USAGE_MISSING_WARNED.add(key)
    log.warning(
        "WARN: %s/%s does not report usage — spend will be estimated (tokens=0)",
        backend_name,
        cli_model or "unknown",
    )


# Codex a veces emite item.type=="error" para warnings no-fatales (ej. contexto
# de skills recortado). Estos NO deben treatearse como fallo — el modelo sigue
# ejecutando normal después.
_CODEX_NON_FATAL_WARNINGS = (
    "Skill descriptions were shortened",
)


def _is_codex_non_fatal_warning(message: str) -> bool:
    return any(w in message for w in _CODEX_NON_FATAL_WARNINGS)


def _is_codex_error(ev: dict[str, Any]) -> bool:
    """Un evento de error de codex >=0.148: `type=error` a nivel top, o un
    `item.completed` cuyo `item.type == "error"` (p. ej. model_not_found).

    Filtra warnings no-fatales conocidos (ver `_CODEX_NON_FATAL_WARNINGS`)."""
    if ev.get("type") == "error":
        msg = str(ev.get("message") or "")
        return not _is_codex_non_fatal_warning(msg)
    if ev.get("type") == "item.completed":
        item = ev.get("item")
        if isinstance(item, dict) and item.get("type") == "error":
            msg = str(item.get("message") or "")
            return not _is_codex_non_fatal_warning(msg)
    return False


def _codex_error_message(ev: dict[str, Any]) -> str:
    """Mensaje legible de un evento de error de codex (ver `_is_codex_error`)."""
    item = ev.get("item")
    if isinstance(item, dict) and item.get("message"):
        return str(item["message"])
    if ev.get("message"):
        return str(ev["message"])
    err = ev.get("error")
    if isinstance(err, dict):
        return str(err.get("message") or err)
    return str(err or ev)


def _sum_codex_usage(events: list[dict[str, Any]]) -> tuple[float, int, int]:
    """Suma tokens de los eventos `turn.completed.usage` de codex >=0.148.

    El JSONL de codex NO reporta costo en USD, solo tokens, así que cost_usd
    queda en 0.0.
    ponytail: sin costo USD, el gate de budget por-dispatch no aplica a codex;
    si hace falta, estimar cost = tokens * precio-del-modelo aquí.
    """
    tokens_in = 0
    tokens_out = 0
    for ev in events:
        if ev.get("type") != "turn.completed":
            continue
        usage = ev.get("usage") or {}
        if isinstance(usage, dict):
            try:
                tokens_in += int(usage.get("input_tokens", 0) or 0)
            except (TypeError, ValueError):
                pass
            try:
                tokens_out += int(usage.get("output_tokens", 0) or 0)
            except (TypeError, ValueError):
                pass
    return 0.0, tokens_in, tokens_out


# ---- GeminiBackend -------------------------------------------------------


class GeminiBackend:
    """Adapter for the `gemini` CLI (Google Gemini native).

    Non-interactive mode: ``gemini -p <prompt> --model <cli_model>``.
    Unlike the other backends, the prompt is passed as a CLI argument rather
    than via stdin (the gemini CLI does not read prompts from a pipe).

    Success: exit_code == 0. The gemini CLI outputs plain text; there is no
    structured cost envelope so cost/token fields are always zero.
    """

    name = "gemini"

    def build_cmd(self, task: Task, route: RouteEntry, prompt_text: str) -> list[str]:
        return [
            "gemini",
            "-p",
            prompt_text,
            "--model",
            route.cli_model,
        ]

    def spawn(
        self,
        task: Task,
        route: RouteEntry,
        prompt_path: Path,
        log_path: Path,
        cwd: Path,
    ) -> Dispatch:
        _ensure_logs_dir(log_path.parent.parent)
        prompt_text = prompt_path.read_text(encoding="utf-8", errors="replace")
        cmd = self.build_cmd(task, route, prompt_text)
        log_fh = log_path.open("ab")
        env = {**os.environ, "PWD": str(cwd)}
        try:
            popen = subprocess.Popen(  # noqa: S603
                cmd,
                stdin=subprocess.DEVNULL,
                stdout=log_fh,
                stderr=subprocess.STDOUT,
                cwd=str(cwd),
                env=env,
                close_fds=True,
                start_new_session=True,
            )
        except (FileNotFoundError, OSError):
            log_fh.close()
            raise

        dispatch = Dispatch(
            task_id=task.id,
            backend=self.name,  # type: ignore[arg-type]
            pid=popen.pid,
            session_id="",
            started_at=_utc_now_iso(),
            prompt_path=str(prompt_path),
            log_path=str(log_path),
            output_path="",
        )
        dispatch._popen = popen  # type: ignore[attr-defined]
        dispatch._fhs = (log_fh,)  # type: ignore[attr-defined]
        dispatch.extra = {}  # type: ignore[attr-defined]
        return dispatch

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:
        exit_code, err_msg, timed_out = _wait_with_timeout(dispatch, timeout_s)
        log_text = _read_log(dispatch.log_path)
        result = self.parse_result(exit_code, log_text)
        if timed_out:
            result.success = False
            result.error_message = err_msg
        return result

    def parse_result(
        self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None
    ) -> DispatchResult:
        success = exit_code == 0
        error_message: str | None = None
        if not success:
            lines = [ln.strip() for ln in log_text.splitlines() if ln.strip()]
            error_message = lines[-1] if lines else f"gemini exited with code {exit_code}"
        return DispatchResult(
            exit_code=exit_code,
            success=success,
            cost_usd=0.0,
            tokens_in=0,
            tokens_out=0,
            stdout=log_text,
            stderr="",
            error_message=error_message,
        )

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:
        return 0.0, 0, 0


# ---- AgyBackend ---------------------------------------------------------


class AgyBackend:
    """Adapter for the `agy` CLI (Antigravity — Google multi-model gateway).

    Non-interactive mode:
        ``agy --output-format json --model <cli_model> --print <prompt>``

    Like `gemini`, the prompt is passed as a CLI arg (not stdin). The `--print`
    flag MUST be LAST because its value is the prompt string and the CLI parses
    it positionally — any flag ordered after `--print` gets swallowed as part
    of the prompt.

    Output shape (single JSON blob on stdout, NOT JSONL):
        {"conversation_id": ..., "status": "SUCCESS", "response": ...,
         "duration_seconds": ..., "num_turns": 1,
         "usage": {"input_tokens": ..., "output_tokens": ..., "thinking_tokens":
                   ..., "cache_read_tokens": ..., "total_tokens": ...}}

    Success requires BOTH `exit_code == 0` AND `status == "SUCCESS"`. There is
    no `cost_usd` field — the dashboard's `pricing.yaml` handles USD estimation
    from the extracted token counts, so `cost_usd` is always 0.0 here.
    """

    name = "agy"

    def build_cmd(self, task: Task, route: RouteEntry, prompt_text: str) -> list[str]:
        # `--print` MUST be the last flag; its value is the prompt string.
        # `--print-timeout` is intentionally omitted — the orchestrator's own
        # `_wait_with_timeout` supervises via SIGTERM/SIGKILL (dispatcher
        # convention: no per-backend timeout knobs).
        return [
            "agy",
            "--output-format",
            "json",
            "--model",
            route.cli_model,
            "--print",
            prompt_text,
        ]

    def spawn(
        self,
        task: Task,
        route: RouteEntry,
        prompt_path: Path,
        log_path: Path,
        cwd: Path,
    ) -> Dispatch:
        _ensure_logs_dir(log_path.parent.parent)
        prompt_text = prompt_path.read_text(encoding="utf-8", errors="replace")
        cmd = self.build_cmd(task, route, prompt_text)
        log_fh = log_path.open("ab")
        env = {**os.environ, "PWD": str(cwd)}
        try:
            popen = subprocess.Popen(  # noqa: S603
                cmd,
                stdin=subprocess.DEVNULL,
                stdout=log_fh,
                stderr=subprocess.STDOUT,
                cwd=str(cwd),
                env=env,
                close_fds=True,
                start_new_session=True,
            )
        except (FileNotFoundError, OSError):
            log_fh.close()
            raise

        dispatch = Dispatch(
            task_id=task.id,
            backend=self.name,  # type: ignore[arg-type]
            pid=popen.pid,
            session_id="",
            started_at=_utc_now_iso(),
            prompt_path=str(prompt_path),
            log_path=str(log_path),
            output_path="",
        )
        dispatch._popen = popen  # type: ignore[attr-defined]
        dispatch._fhs = (log_fh,)  # type: ignore[attr-defined]
        dispatch.extra = {}  # type: ignore[attr-defined]
        return dispatch

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:
        exit_code, err_msg, timed_out = _wait_with_timeout(dispatch, timeout_s)
        log_text = _read_log(dispatch.log_path)
        result = self.parse_result(exit_code, log_text)
        if timed_out:
            result.success = False
            result.error_message = err_msg
        return result

    def parse_result(
        self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None
    ) -> DispatchResult:
        tokens_in = 0
        tokens_out = 0
        status_field: str | None = None
        payload: dict[str, Any] | None = None

        stripped = log_text.strip()
        if stripped:
            try:
                loaded = json.loads(stripped)
            except (json.JSONDecodeError, ValueError):
                loaded = None
            if isinstance(loaded, dict):
                payload = loaded
                raw_status = payload.get("status")
                if isinstance(raw_status, str):
                    status_field = raw_status
                usage = payload.get("usage")
                if isinstance(usage, dict):
                    tokens_in = int(usage.get("input_tokens") or 0)
                    tokens_out = int(usage.get("output_tokens") or 0)

        # agy always emits JSON in --output-format json mode; if we couldn't
        # parse it, that's a failure regardless of exit_code.
        success = exit_code == 0 and status_field == "SUCCESS"

        error_message: str | None = None
        if not success:
            if status_field and status_field != "SUCCESS":
                error_message = f"agy status={status_field}"
            elif payload is None and exit_code == 0:
                # Exit code 0 but body wasn't parseable JSON — treat as failure.
                lines = [ln.strip() for ln in log_text.splitlines() if ln.strip()]
                error_message = (
                    lines[-1]
                    if lines
                    else "agy produced no parseable JSON output"
                )
            else:
                lines = [ln.strip() for ln in log_text.splitlines() if ln.strip()]
                error_message = (
                    lines[-1] if lines else f"agy exited with code {exit_code}"
                )

        return DispatchResult(
            exit_code=exit_code,
            success=success,
            cost_usd=0.0,
            tokens_in=tokens_in,
            tokens_out=tokens_out,
            stdout=log_text,
            stderr="",
            error_message=error_message,
        )

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:
        try:
            payload = json.loads(log_text.strip())
        except (json.JSONDecodeError, ValueError, AttributeError):
            return 0.0, 0, 0
        if not isinstance(payload, dict):
            return 0.0, 0, 0
        usage = payload.get("usage")
        if not isinstance(usage, dict):
            return 0.0, 0, 0
        try:
            tokens_in = int(usage.get("input_tokens") or 0)
            tokens_out = int(usage.get("output_tokens") or 0)
        except (TypeError, ValueError):
            return 0.0, 0, 0
        return 0.0, tokens_in, tokens_out


# ---- Backend registry ----------------------------------------------------


_BACKENDS: dict[str, type[Backend]] = {
    "claude": ClaudeBackend,
    "codex": CodexBackend,
    "opencode": OpencodeBackend,
    "gemini": GeminiBackend,
    "agy": AgyBackend,
}


def get_backend(name: str, cfg: dict[str, Any] | None = None) -> Backend:
    """Instantiate the concrete backend for a router entry.

    Only `ClaudeBackend` currently takes `cfg`; the others ignore it. Keeps
    the main loop's dispatcher-lookup path uniform.
    """
    try:
        cls = _BACKENDS[name]
    except KeyError as exc:
        raise ValueError(f"unknown backend {name!r}; expected one of {list(_BACKENDS)}") from exc
    if cls is ClaudeBackend:
        return cls(cfg=cfg)  # type: ignore[call-arg]
    return cls()  # type: ignore[call-arg]


__all__ = [
    "Backend",
    "DispatchResult",
    "FailureClass",
    "ClaudeBackend",
    "CodexBackend",
    "OpencodeBackend",
    "GeminiBackend",
    "AgyBackend",
    "classify_failure",
    "get_backend",
    "is_version_drift_error",
]
