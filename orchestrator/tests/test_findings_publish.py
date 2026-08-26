"""Tests for `orch findings publish` + `dismiss` — every guardrail.

Sprint E-1 / #17 — commit 4 scope. Each guardrail (classification,
confidence, rate limit, dedup, TTY consent, idempotency, label ensure) has
a targeted test that would catch its removal.
"""

from __future__ import annotations

import io
import json
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

import pytest

from orchestrator import findings as f_mod
from orchestrator.findings import (
    DEFAULT_LABEL,
    DEFAULT_RATE_LIMIT,
    DuplicateIssueFound,
    PublishRefusedError,
    RateLimitExceeded,
    _check_rate_limit,
    _ensure_label,
    _publish_gh_issue,
    capture,
    dismiss,
    publish,
)
from orchestrator.models import Finding
from orchestrator.state import FileBackend
from orchestrator.state.interface import StateBackend


# ---- helpers ------------------------------------------------------------


def _mk_completed(stdout: str = "", stderr: str = "", rc: int = 0) -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(
        args=["gh"], returncode=rc, stdout=stdout, stderr=stderr
    )


@pytest.fixture
def backend(tmp_path: Path) -> StateBackend:
    state_dir = tmp_path / "state"
    state_dir.mkdir()
    return FileBackend(state_dir=state_dir, project_id="pt", project_root=tmp_path)


def _capture(backend: StateBackend, **kw) -> Finding:
    """Convenience — capture with sensible defaults."""
    defaults = dict(
        finding_type="bug",
        about="orch",
        summary="A publishable finding about dispatcher timing",
        confidence="medium",
    )
    defaults.update(kw)
    return capture(backend, **defaults)


def _empty_search_response() -> subprocess.CompletedProcess:
    return _mk_completed(stdout=json.dumps({"items": []}))


def _issue_create_response(url: str = "https://github.com/o/r/issues/42") -> subprocess.CompletedProcess:
    return _mk_completed(stdout=json.dumps({"html_url": url, "number": 42}))


def _fake_runner(
    search_result: subprocess.CompletedProcess | None = None,
    create_result: subprocess.CompletedProcess | None = None,
    label_result: subprocess.CompletedProcess | None = None,
):
    """Build a `subprocess.run` mock that routes on argv shape.

    - `gh api search/issues?...`      → search_result
    - `gh label create ...`           → label_result
    - `gh api repos/.../issues -X POST` → create_result
    """
    calls = []
    search_result = search_result or _empty_search_response()
    create_result = create_result or _issue_create_response()
    label_result = label_result or _mk_completed()

    def runner(cmd, **_kwargs):
        calls.append(cmd)
        if cmd[:3] == ["gh", "label", "create"]:
            return label_result
        if cmd[:2] == ["gh", "api"]:
            path = cmd[2] if len(cmd) > 2 else ""
            if path.startswith("search/issues"):
                return search_result
            return create_result
        return _mk_completed()

    runner.calls = calls  # type: ignore[attr-defined]
    return runner


# ---- publish guardrails -------------------------------------------------


def test_publish_refuses_about_project(backend: StateBackend) -> None:
    f = _capture(backend, about="project", summary="Migrate users table")
    with pytest.raises(PublishRefusedError, match="about=project"):
        publish(
            backend, f.id, repo="o/r",
            runner=_fake_runner(),
            confirm=lambda _f: True,
        )


def test_publish_refuses_low_confidence(backend: StateBackend) -> None:
    f = _capture(backend, confidence="low", summary="Low-confidence guess about a race")
    with pytest.raises(PublishRefusedError, match="confidence"):
        publish(
            backend, f.id, repo="o/r",
            runner=_fake_runner(),
            confirm=lambda _f: True,
        )


def test_publish_low_confidence_with_force_bypasses(backend: StateBackend) -> None:
    f = _capture(backend, confidence="low",
                 summary="Low-confidence guess about a distinct race")
    runner = _fake_runner()
    result = publish(
        backend, f.id, repo="o/r",
        force=True,
        runner=runner,
        confirm=lambda _f: True,
    )
    assert result["status"] == "published"
    assert result["published_url"] == "https://github.com/o/r/issues/42"


def test_publish_rate_limit_trips_at_threshold(backend: StateBackend) -> None:
    # Seed 3 published findings within the last hour.
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    for i in range(3):
        f = _capture(
            backend,
            summary=f"finding number {i} for rate limit seeding",
        )
        backend.update_finding(f.id, status="published",
                               published_url=f"https://x/{i}")
    fresh = _capture(backend, summary="fresh publishable finding tokens")
    with pytest.raises(RateLimitExceeded):
        publish(
            backend, fresh.id, repo="o/r",
            rate_limit_per_hour=3,
            runner=_fake_runner(),
            confirm=lambda _f: True,
        )


def test_publish_rate_limit_disabled_when_zero(backend: StateBackend) -> None:
    for i in range(5):
        f = _capture(backend, summary=f"rate seed edge {i}")
        backend.update_finding(f.id, status="published",
                               published_url=f"https://x/{i}")
    fresh = _capture(backend, summary="brand new publishable subject")
    runner = _fake_runner()
    result = publish(
        backend, fresh.id, repo="o/r",
        rate_limit_per_hour=0,
        runner=runner,
        confirm=lambda _f: True,
    )
    assert result["status"] == "published"


def test_publish_refuses_on_strong_dedup_match(backend: StateBackend) -> None:
    f = _capture(
        backend,
        summary="Dashboard crashes on refresh with sqlite backend",
    )
    search = _mk_completed(stdout=json.dumps({"items": [{
        "number": 55,
        "title": "Dashboard crashes on refresh with sqlite backend",
        "html_url": "https://github.com/o/r/issues/55",
    }]}))
    with pytest.raises(DuplicateIssueFound) as exc:
        publish(
            backend, f.id, repo="o/r",
            runner=_fake_runner(search_result=search),
            confirm=lambda _f: True,
        )
    assert exc.value.match["number"] == 55
    assert exc.value.overlap >= 0.6


def test_publish_dedup_bypassed_by_force(backend: StateBackend) -> None:
    f = _capture(
        backend,
        summary="Dashboard crashes on refresh with sqlite backend variant B",
    )
    search = _mk_completed(stdout=json.dumps({"items": [{
        "number": 55,
        "title": "Dashboard crashes on refresh with sqlite backend variant B",
        "html_url": "https://github.com/o/r/issues/55",
    }]}))
    runner = _fake_runner(search_result=search)
    result = publish(
        backend, f.id, repo="o/r",
        force=True,
        runner=runner,
        confirm=lambda _f: True,
    )
    assert result["status"] == "published"


def test_publish_idempotent_when_already_published(backend: StateBackend) -> None:
    f = _capture(backend, summary="Already there")
    backend.update_finding(f.id, status="published",
                           published_url="https://existing/1")
    runner = _fake_runner()
    result = publish(
        backend, f.id, repo="o/r",
        runner=runner,
        confirm=lambda _f: True,
    )
    assert result["status"] == "already_published"
    assert result["published_url"] == "https://existing/1"
    # No gh calls should have happened.
    assert runner.calls == []  # type: ignore[attr-defined]


def test_publish_dry_run_makes_no_writes(backend: StateBackend) -> None:
    f = _capture(backend, summary="Nice dry run candidate summary here")
    runner = _fake_runner()
    result = publish(
        backend, f.id, repo="o/r",
        dry_run=True,
        runner=runner,
        confirm=lambda _f: True,
    )
    assert result["status"] == "dry_run"
    # The stored finding must still be pending.
    stored = backend.get_finding(f.id)
    assert stored is not None
    assert stored.status == "pending"
    # No POST /issues call. Search may happen, but no label create + no create.
    verbs = [c[:3] for c in runner.calls]  # type: ignore[attr-defined]
    assert ["gh", "label", "create"] not in verbs


def test_publish_user_cancels_consent_raises_refused(backend: StateBackend) -> None:
    f = _capture(backend, summary="Consent will be denied here today")
    with pytest.raises(PublishRefusedError, match="cancelled"):
        publish(
            backend, f.id, repo="o/r",
            runner=_fake_runner(),
            confirm=lambda _f: False,
        )


def test_publish_ensures_label(backend: StateBackend) -> None:
    f = _capture(backend, summary="Label ensure test finding subject")
    runner = _fake_runner()
    publish(
        backend, f.id, repo="o/r",
        runner=runner,
        confirm=lambda _f: True,
    )
    assert any(
        c[:3] == ["gh", "label", "create"]
        for c in runner.calls  # type: ignore[attr-defined]
    )


def test_publish_updates_finding_to_published(backend: StateBackend) -> None:
    f = _capture(backend, summary="Persisted after publish subject line")
    publish(
        backend, f.id, repo="o/r",
        runner=_fake_runner(),
        confirm=lambda _f: True,
    )
    stored = backend.get_finding(f.id)
    assert stored is not None
    assert stored.status == "published"
    assert stored.published_url == "https://github.com/o/r/issues/42"


def test_publish_not_found_raises(backend: StateBackend) -> None:
    with pytest.raises(PublishRefusedError, match="not found"):
        publish(backend, "no-such-id", repo="o/r",
                runner=_fake_runner(), confirm=lambda _f: True)


# ---- rate limit helper -------------------------------------------------


def test_check_rate_limit_disabled_returns_zero(backend: StateBackend) -> None:
    assert _check_rate_limit(backend, 0) == (0, 0)


def test_check_rate_limit_counts_only_last_hour(backend: StateBackend) -> None:
    # Two publishes within the hour, one two hours ago (should not count).
    old_ts = (datetime.now(timezone.utc) - timedelta(hours=2)).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )
    old = _capture(backend, summary="Old finding not counted")
    # Rewrite the created_at directly for deterministic aging.
    backend.update_finding(old.id, status="published",
                           published_url="https://x/old")
    # File backend stores created_at inside the row → tweak on disk.
    p = tmp_p = Path(backend.state_dir) / "findings.jsonl"  # type: ignore[union-attr]
    rows = [json.loads(ln) for ln in p.read_text().splitlines() if ln.strip()]
    for r in rows:
        if r["id"] == old.id:
            r["created_at"] = old_ts
    p.write_text("\n".join(json.dumps(r) for r in rows) + "\n")

    for i in range(2):
        f = _capture(backend, summary=f"recent finding {i}")
        backend.update_finding(f.id, status="published",
                               published_url=f"https://x/{i}")
    count, limit = _check_rate_limit(backend, 5)
    assert count == 2
    assert limit == 5


# ---- ensure_label -------------------------------------------------------


def test_ensure_label_swallows_already_exists() -> None:
    runner_calls: list[list[str]] = []

    def runner(cmd, **_kwargs):
        runner_calls.append(cmd)
        return _mk_completed(rc=1, stderr="Label already exists (HTTP 422)")

    # Should NOT raise.
    _ensure_label("o/r", DEFAULT_LABEL, runner=runner)
    assert runner_calls, "gh label create must be attempted"


def test_ensure_label_survives_gh_missing() -> None:
    def runner(cmd, **_kwargs):
        raise FileNotFoundError("gh not installed")

    _ensure_label("o/r", DEFAULT_LABEL, runner=runner)  # no raise


# ---- dismiss ------------------------------------------------------------


def test_dismiss_updates_status_and_reason(backend: StateBackend) -> None:
    f = _capture(backend, summary="To be dismissed as noise")
    dismiss(backend, f.id, "not actionable")
    stored = backend.get_finding(f.id)
    assert stored is not None
    assert stored.status == "dismissed"
    assert stored.dismissed_reason == "not actionable"


def test_dismiss_missing_raises(backend: StateBackend) -> None:
    with pytest.raises(PublishRefusedError):
        dismiss(backend, "nope", "reason")


# ---- CLI wiring for publish + dismiss ----------------------------------


def _bootstrap_project(tmp_path: Path) -> None:
    (tmp_path / ".orchestrator").mkdir(exist_ok=True)
    (tmp_path / ".orchestrator" / "config.yaml").write_text(
        "concurrency: {global_max: 1}\n"
        "state: {backend: file}\n"
        "findings: {publish_repo: 'ownr/reporepo', publish_rate_limit_per_hour: 5}\n",
        encoding="utf-8",
    )
    (tmp_path / "tasks.json").write_text('{"tasks": []}', encoding="utf-8")
    (tmp_path / "scripts").mkdir(exist_ok=True)
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (tmp_path / "scripts" / name).write_text("#!/bin/sh\nexit 0\n")
        (tmp_path / "scripts" / name).chmod(0o755)


def _run_cli(argv: list[str], tmp_path: Path) -> tuple[int, str, str]:
    from orchestrator.orch import main

    stdout, stderr = sys.stdout, sys.stderr
    sio_out, sio_err = io.StringIO(), io.StringIO()
    sys.stdout, sys.stderr = sio_out, sio_err
    try:
        rc = main(["findings", *argv, "--project-root", str(tmp_path)])
    except SystemExit as exc:
        rc = int(exc.code) if isinstance(exc.code, int) else 2
    finally:
        sys.stdout, sys.stderr = stdout, stderr
    return rc, sio_out.getvalue(), sio_err.getvalue()


def test_cli_publish_project_findings_exit_2(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(
        ["capture", "--type", "bug", "--about", "project",
         "--summary", "user tracker only", "--confidence", "high"],
        tmp_path,
    )
    _, out, _ = _run_cli(["list", "--json"], tmp_path)
    fid = json.loads(out.strip())[0]["id"]

    rc, _out, err = _run_cli(["publish", fid, "--yes"], tmp_path)
    assert rc == 2
    assert "about=project" in err


def test_cli_publish_low_confidence_exit_2(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(
        ["capture", "--type", "bug", "--about", "orch",
         "--summary", "very fuzzy race idea", "--confidence", "low"],
        tmp_path,
    )
    _, out, _ = _run_cli(["list", "--json"], tmp_path)
    fid = json.loads(out.strip())[0]["id"]
    with patch("orchestrator.findings.subprocess.run",
               return_value=_empty_search_response()):
        rc, _out, err = _run_cli(["publish", fid, "--yes"], tmp_path)
    assert rc == 2
    assert "confidence" in err.lower()


def test_cli_publish_dry_run_ok(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(
        ["capture", "--type", "bug", "--about", "orch",
         "--summary", "clean publishable subject"],
        tmp_path,
    )
    _, out, _ = _run_cli(["list", "--json"], tmp_path)
    fid = json.loads(out.strip())[0]["id"]
    with patch("orchestrator.findings.subprocess.run",
               return_value=_empty_search_response()):
        rc, out, err = _run_cli(["publish", fid, "--dry-run"], tmp_path)
    assert rc == 0, err
    assert "dry-run" in out


def test_cli_dismiss_happy_path(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(
        ["capture", "--type", "bug", "--about", "orch",
         "--summary", "will be dismissed soon"],
        tmp_path,
    )
    _, out, _ = _run_cli(["list", "--json"], tmp_path)
    fid = json.loads(out.strip())[0]["id"]
    rc, out, err = _run_cli(
        ["dismiss", fid, "--reason", "not relevant"], tmp_path
    )
    assert rc == 0, err
    assert "dismissed" in out
    # Verify status changed.
    _, out, _ = _run_cli(["list", "--json"], tmp_path)
    row = json.loads(out.strip())[0]
    assert row["status"] == "dismissed"
    assert row["dismissed_reason"] == "not relevant"
