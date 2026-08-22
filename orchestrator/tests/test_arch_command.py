"""Tests for `orch arch generate` (Sprint E-4).

Covers:
    * Missing archify SKILL.md → exit 1 with install hint on stderr.
    * Deterministic source_hash for the same inputs.
    * Lock lifecycle: created at start, removed on success.

The actual `claude` subprocess is mocked — we never dispatch a real agent
in tests (would cost real dollars per plan constraint).
"""

from __future__ import annotations

import json
from pathlib import Path
from types import SimpleNamespace

from orchestrator import arch as arch_mod


def _scaffold(tmp_path: Path) -> Path:
    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )
    (root / "orchestrator" / "config.yaml").write_text("", encoding="utf-8")
    return root


# ---- Skill detection -----------------------------------------------------


def test_generate_exits_1_when_skill_missing(tmp_path: Path, capsys, monkeypatch) -> None:
    root = _scaffold(tmp_path)
    # Point HOME + resolve_skill_path elsewhere so both global and local misses.
    monkeypatch.setattr(arch_mod, "resolve_skill_path", lambda _pr: None)
    rc = arch_mod.run_arch_cli(["generate", "--project-root", str(root)])
    assert rc == 1
    err = capsys.readouterr().err
    assert "Archify skill is not installed" in err
    assert "npx skills add tt-a1i/archify -g" in err


# ---- Source hash determinism --------------------------------------------


def test_source_hash_is_deterministic(tmp_path: Path) -> None:
    root = _scaffold(tmp_path)
    prd_dir = root / "docs" / "prd"
    prd_dir.mkdir(parents=True)
    (prd_dir / "001.md").write_text("prd body A", encoding="utf-8")
    (prd_dir / "002.md").write_text("prd body B", encoding="utf-8")
    specs_dir = root / "specs"
    specs_dir.mkdir()
    (specs_dir / "F1.pkg.T1.md").write_text("spec body", encoding="utf-8")

    tasks_json = root / "tasks.json"
    sources_a = arch_mod.discover_sources(root, tasks_json)
    sources_b = arch_mod.discover_sources(root, tasks_json)
    hash_a = arch_mod.compute_source_hash(sources_a)
    hash_b = arch_mod.compute_source_hash(sources_b)
    assert hash_a == hash_b
    assert len(hash_a) == 7

    # Mutating a PRD flips the hash.
    (prd_dir / "001.md").write_text("prd body A CHANGED", encoding="utf-8")
    hash_c = arch_mod.compute_source_hash(arch_mod.discover_sources(root, tasks_json))
    assert hash_c != hash_a


# ---- Lock lifecycle ------------------------------------------------------


def test_lock_created_at_start_and_removed_on_success(
    tmp_path: Path, monkeypatch
) -> None:
    root = _scaffold(tmp_path)
    # Fake skill presence so generate proceeds past the detection gate.
    fake_skill = tmp_path / "SKILL.md"
    fake_skill.write_text("archify", encoding="utf-8")
    monkeypatch.setattr(arch_mod, "resolve_skill_path", lambda _pr: fake_skill)

    lock_seen: dict[str, bool] = {"present_during_dispatch": False}
    # `--project-root` triggers namespaced state layout — state lives under
    # <root>/orchestrator/state/<project_id>/ (project_id defaults to basename).
    state_dir = root / "orchestrator" / "state" / "proj"

    def _fake_claude(argv, prompt, cwd, timeout_s):
        # Lock must exist right when the sub-agent is about to run.
        lock_seen["present_during_dispatch"] = (
            (state_dir / "arch-generate.lock").exists()
        )
        # Materialize the diagram the way the real sub-agent would.
        arch_dir = Path(cwd) / "docs" / "architecture"
        arch_dir.mkdir(parents=True, exist_ok=True)
        (arch_dir / "current.html").write_text("<html>diagram</html>", encoding="utf-8")
        # Return a stdout envelope with a cost the parser can lift.
        stdout = json.dumps({
            "is_error": False,
            "subtype": "success",
            "total_cost_usd": 0.42,
            "model": "claude-sonnet-4-5",
            "usage": {"input_tokens": 100, "output_tokens": 200},
        }).encode("utf-8")
        return SimpleNamespace(returncode=0, stdout=stdout, stderr=b"")

    monkeypatch.setattr(arch_mod, "_run_claude", _fake_claude)

    rc = arch_mod.run_arch_cli(["generate", "--project-root", str(root)])
    assert rc == 0
    assert lock_seen["present_during_dispatch"] is True

    # Lock is swept post-success; archive + event landed.
    assert not (state_dir / "arch-generate.lock").exists(), (
        f"lock still present in {state_dir}"
    )
    events = arch_mod.read_events(state_dir)
    assert len(events) == 1
    assert events[0]["cost_usd"] == 0.42
    assert events[0]["model"] == "claude-sonnet-4-5"
    archive_dir = root / "docs" / "architecture" / "archive"
    assert list(archive_dir.glob("*.html")), "archive copy missing"


def test_lock_removed_when_dispatch_fails(tmp_path: Path, monkeypatch, capsys) -> None:
    """Even a failing sub-agent must release the lock so the next run isn't stuck."""
    root = _scaffold(tmp_path)
    fake_skill = tmp_path / "SKILL.md"
    fake_skill.write_text("archify", encoding="utf-8")
    monkeypatch.setattr(arch_mod, "resolve_skill_path", lambda _pr: fake_skill)

    def _boom(argv, prompt, cwd, timeout_s):
        return SimpleNamespace(returncode=1, stdout=b"", stderr=b"boom")

    monkeypatch.setattr(arch_mod, "_run_claude", _boom)
    rc = arch_mod.run_arch_cli(["generate", "--project-root", str(root)])
    assert rc == 1
    # namespaced state layout under --project-root — lock lives beside project id.
    assert not (root / "orchestrator" / "state" / "proj" / "arch-generate.lock").exists()


def test_second_generate_refuses_when_live_lock_exists(
    tmp_path: Path, monkeypatch, capsys
) -> None:
    root = _scaffold(tmp_path)
    fake_skill = tmp_path / "SKILL.md"
    fake_skill.write_text("archify", encoding="utf-8")
    monkeypatch.setattr(arch_mod, "resolve_skill_path", lambda _pr: fake_skill)

    import os
    state_dir = root / "orchestrator" / "state" / "proj"
    state_dir.mkdir(parents=True, exist_ok=True)
    (state_dir / "arch-generate.lock").write_text(
        json.dumps({"pid": os.getpid(), "started_at": "2026-08-22T00:00:00Z"})
    )

    rc = arch_mod.run_arch_cli(["generate", "--project-root", str(root)])
    assert rc == 1
    assert "Already in progress" in capsys.readouterr().err


def test_stale_lock_is_ignored(tmp_path: Path, monkeypatch) -> None:
    """A lock whose PID is dead must not block a fresh run."""
    root = _scaffold(tmp_path)
    fake_skill = tmp_path / "SKILL.md"
    fake_skill.write_text("archify", encoding="utf-8")
    monkeypatch.setattr(arch_mod, "resolve_skill_path", lambda _pr: fake_skill)

    state_dir = root / "orchestrator" / "state" / "proj"
    state_dir.mkdir(parents=True, exist_ok=True)
    # PID 999999 is virtually guaranteed to not exist.
    (state_dir / "arch-generate.lock").write_text(
        json.dumps({"pid": 999999, "started_at": "2026-08-22T00:00:00Z"})
    )

    def _fake_claude(argv, prompt, cwd, timeout_s):
        arch_dir = Path(cwd) / "docs" / "architecture"
        arch_dir.mkdir(parents=True, exist_ok=True)
        (arch_dir / "current.html").write_text("<html>x</html>", encoding="utf-8")
        return SimpleNamespace(
            returncode=0,
            stdout=json.dumps({
                "is_error": False, "subtype": "success",
                "total_cost_usd": 0.0, "model": "m",
            }).encode("utf-8"),
            stderr=b"",
        )

    monkeypatch.setattr(arch_mod, "_run_claude", _fake_claude)
    rc = arch_mod.run_arch_cli(["generate", "--project-root", str(root)])
    assert rc == 0


def test_acquire_lock_includes_initial_phase(tmp_path: Path) -> None:
    state_dir = tmp_path / "state"
    payload = arch_mod.acquire_lock(state_dir)
    assert payload["phase"] == "dispatching"
    assert payload["phase_at"] == payload["started_at"]
    on_disk = json.loads((state_dir / "arch-generate.lock").read_text())
    assert on_disk["phase"] == "dispatching"


def test_update_phase_preserves_pid_and_started_at(tmp_path: Path) -> None:
    state_dir = tmp_path / "state"
    original = arch_mod.acquire_lock(state_dir)
    arch_mod.update_phase(state_dir, "claude_working")
    updated = json.loads((state_dir / "arch-generate.lock").read_text())
    assert updated["phase"] == "claude_working"
    assert updated["pid"] == original["pid"]
    assert updated["started_at"] == original["started_at"]
    # phase_at may equal started_at if the clock hasn't advanced past 1s
    # resolution — we only assert it's a valid ISO string.
    assert isinstance(updated["phase_at"], str) and updated["phase_at"]


def test_update_phase_is_noop_when_lock_missing(tmp_path: Path) -> None:
    # Must not raise when there's nothing to update — protects the
    # generation flow from crashing on a transient FS race.
    arch_mod.update_phase(tmp_path / "state", "claude_working")
    assert not (tmp_path / "state" / "arch-generate.lock").exists()
