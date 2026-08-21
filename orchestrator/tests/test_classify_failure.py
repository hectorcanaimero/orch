"""Tests for `orchestrator.dispatcher.classify_failure` (Fase B).

`classify_failure` is a PURE helper that maps a `DispatchResult` to a
`FailureClass` enum so the reap loop in orch.py can pick the right retry
policy (skip retry, retry same, retry with fallback, longer backoff).

Design (from Fase B scope):

| Class          | Trigger                                                                 |
|----------------|-------------------------------------------------------------------------|
| ID_SPOOF       | error_message starts with "id spoofing detected"                        |
| TIMEOUT        | error_message starts with "orchestrator timeout"                        |
| RATE_LIMIT     | 429 / "rate limit" / "too many requests" markers in stderr/stdout       |
| PERMISSION     | "permission denied" / "not authorized" / "auth" markers                 |
| BUDGET         | "max budget" / "budget exceeded" markers                                |
| VERSION_DRIFT  | Existing `_VERSION_DRIFT_MARKERS`                                        |
| TRANSIENT      | 5xx / "UnknownError" / "err_xxxxxxxx" opencode/opencodego server hiccup |
| PARSER         | error_message like "could not parse" / "produced no ... events"          |
| OTHER          | conservative default                                                    |

Order matters: ID_SPOOF and TIMEOUT are checked first (set by orch, not CLI),
then the rest by specificity. Every check is case-insensitive.
"""

from __future__ import annotations

import pytest

from orchestrator.dispatcher import (
    DispatchResult,
    FailureClass,
    classify_failure,
    is_version_drift_error,
)


def _fail(
    *,
    error_message: str | None = None,
    stderr: str = "",
    stdout: str = "",
    exit_code: int = 1,
) -> DispatchResult:
    """Build a failing DispatchResult; `success` is always False for these tests."""
    return DispatchResult(
        exit_code=exit_code,
        success=False,
        stdout=stdout,
        stderr=stderr,
        error_message=error_message,
    )


# ---- ID_SPOOF (checked FIRST — set explicitly by orch) ------------------


def test_id_spoof_marker() -> None:
    result = _fail(error_message="id spoofing detected: agent called task-finish.sh 'B-999'")
    assert classify_failure(result) is FailureClass.ID_SPOOF


def test_id_spoof_wins_over_other_markers_in_stderr() -> None:
    # Even if stderr also contains a rate-limit hint, id_spoof (set by orch)
    # must dominate — the agent violated the contract, no retry.
    result = _fail(
        error_message="id spoofing detected: task-finish.sh 'X'",
        stderr="429 rate limit exceeded",
    )
    assert classify_failure(result) is FailureClass.ID_SPOOF


# ---- TIMEOUT (checked SECOND — set explicitly by orch/wait) -------------


def test_timeout_marker_from_wait_result() -> None:
    result = _fail(error_message="orchestrator timeout after 300s")
    assert classify_failure(result) is FailureClass.TIMEOUT


def test_timeout_marker_from_reap_loop() -> None:
    # `_reap_once` sets exactly "orchestrator timeout" (no suffix).
    result = _fail(error_message="orchestrator timeout")
    assert classify_failure(result) is FailureClass.TIMEOUT


# ---- RATE_LIMIT ---------------------------------------------------------


def test_rate_limit_429_in_stderr() -> None:
    result = _fail(stderr="HTTP 429: too many requests to the API")
    assert classify_failure(result) is FailureClass.RATE_LIMIT


def test_rate_limit_phrase_in_stdout() -> None:
    result = _fail(stdout="Rate limit exceeded, please retry later")
    assert classify_failure(result) is FailureClass.RATE_LIMIT


def test_rate_limit_too_many_requests() -> None:
    result = _fail(stderr="429 Too Many Requests")
    assert classify_failure(result) is FailureClass.RATE_LIMIT


def test_rate_limit_case_insensitive() -> None:
    result = _fail(stderr="RATE LIMIT reached")
    assert classify_failure(result) is FailureClass.RATE_LIMIT


# ---- PERMISSION ---------------------------------------------------------


def test_permission_denied() -> None:
    result = _fail(stderr="permission denied: cannot access /secret")
    assert classify_failure(result) is FailureClass.PERMISSION


def test_permission_not_authorized() -> None:
    result = _fail(error_message="Not authorized to use this endpoint")
    assert classify_failure(result) is FailureClass.PERMISSION


def test_permission_auth_error() -> None:
    result = _fail(stderr="401 auth error: token invalid")
    assert classify_failure(result) is FailureClass.PERMISSION


# ---- BUDGET -------------------------------------------------------------


def test_budget_max_budget() -> None:
    result = _fail(stderr="Reached max budget of $5.00")
    assert classify_failure(result) is FailureClass.BUDGET


def test_budget_exceeded() -> None:
    result = _fail(error_message="budget exceeded during execution")
    assert classify_failure(result) is FailureClass.BUDGET


# ---- VERSION_DRIFT (reuses existing markers) ----------------------------


def test_version_drift_model_not_found() -> None:
    result = _fail(error_message="model not found: zhipu/glm-9.9")
    assert classify_failure(result) is FailureClass.VERSION_DRIFT


def test_version_drift_unknown_model() -> None:
    result = _fail(stderr="unknown model: foo")
    assert classify_failure(result) is FailureClass.VERSION_DRIFT


def test_is_version_drift_error_wrapper_still_works() -> None:
    # Backwards compat: `is_version_drift_error` MUST return True for the
    # same inputs it used to accept.
    result = _fail(error_message="model does not exist")
    assert is_version_drift_error(result) is True

    result2 = _fail(error_message="model not found: xyz")
    assert is_version_drift_error(result2) is True


def test_is_version_drift_error_false_for_other_classes() -> None:
    # A transient 500 must NOT trigger the drift-fallback path.
    result = _fail(stderr="500 Internal Server Error")
    assert is_version_drift_error(result) is False


# ---- TRANSIENT ----------------------------------------------------------


def test_transient_500_error() -> None:
    result = _fail(stderr="500 Internal Server Error from upstream")
    assert classify_failure(result) is FailureClass.TRANSIENT


def test_transient_502_error() -> None:
    result = _fail(stderr="502 Bad Gateway")
    assert classify_failure(result) is FailureClass.TRANSIENT


def test_transient_unknown_error() -> None:
    result = _fail(stdout="UnknownError: connection dropped")
    assert classify_failure(result) is FailureClass.TRANSIENT


def test_transient_opencode_err_code() -> None:
    # opencode/opencodego emit `err_xxxxxxxx` for transient server errors.
    result = _fail(stderr="opencode failed: err_a1b2c3d4")
    assert classify_failure(result) is FailureClass.TRANSIENT


# ---- PARSER -------------------------------------------------------------


def test_parser_could_not_parse_claude_envelope() -> None:
    result = _fail(error_message="could not parse claude JSON envelope")
    assert classify_failure(result) is FailureClass.PARSER


def test_parser_no_jsonl_events() -> None:
    result = _fail(error_message="opencode produced no JSONL events")
    assert classify_failure(result) is FailureClass.PARSER


def test_parser_codex_no_events() -> None:
    result = _fail(error_message="codex produced no JSONL events")
    assert classify_failure(result) is FailureClass.PARSER


# ---- OTHER (conservative default) ---------------------------------------


def test_other_when_nothing_matches() -> None:
    result = _fail(error_message="something weird happened", stderr="", stdout="")
    assert classify_failure(result) is FailureClass.OTHER


def test_other_empty_result() -> None:
    result = _fail(error_message=None, stderr="", stdout="")
    assert classify_failure(result) is FailureClass.OTHER


# ---- Ordering / precedence ----------------------------------------------


def test_id_spoof_beats_timeout() -> None:
    # If somehow both markers appear, ID_SPOOF wins (checked first).
    result = _fail(error_message="id spoofing detected: orchestrator timeout")
    assert classify_failure(result) is FailureClass.ID_SPOOF


def test_timeout_beats_rate_limit() -> None:
    # A subprocess that got killed for timing out AND its log tail contains
    # a stale "429 rate limit" line should still be classified TIMEOUT.
    result = _fail(
        error_message="orchestrator timeout after 300s",
        stderr="429 rate limit exceeded",
    )
    assert classify_failure(result) is FailureClass.TIMEOUT


def test_rate_limit_beats_transient_when_both_markers_present() -> None:
    # RATE_LIMIT is more specific than TRANSIENT — pick the specific one.
    result = _fail(stderr="429 Too Many Requests (upstream returned 500)")
    assert classify_failure(result) is FailureClass.RATE_LIMIT


def test_permission_beats_transient() -> None:
    result = _fail(stderr="permission denied (500 upstream)")
    assert classify_failure(result) is FailureClass.PERMISSION


def test_version_drift_beats_transient() -> None:
    result = _fail(stderr="model not found (500 upstream)")
    assert classify_failure(result) is FailureClass.VERSION_DRIFT


# ---- Purity contract ----------------------------------------------------


def test_classify_failure_is_pure() -> None:
    """No mutation of the input, no I/O — same inputs → same output twice."""
    result = _fail(stderr="429 rate limit")
    cls1 = classify_failure(result)
    cls2 = classify_failure(result)
    assert cls1 is cls2
    # Result untouched.
    assert result.error_message is None
    assert result.stderr == "429 rate limit"


def test_failure_class_is_str_enum() -> None:
    """FailureClass values are lower-snake strings so they serialize cleanly
    into event logs / dashboards without a custom encoder."""
    assert FailureClass.VERSION_DRIFT.value == "version_drift"
    assert FailureClass.RATE_LIMIT.value == "rate_limit"
    assert FailureClass.TIMEOUT.value == "timeout"
    assert FailureClass.PERMISSION.value == "permission"
    assert FailureClass.BUDGET.value == "budget"
    assert FailureClass.ID_SPOOF.value == "id_spoof"
    assert FailureClass.PARSER.value == "parser"
    assert FailureClass.TRANSIENT.value == "transient"
    assert FailureClass.OTHER.value == "other"
