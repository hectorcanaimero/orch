"""Config loading with deep-merge override support (Sprint F-3).

Resolution order (last wins):
  1. Defaults set by _apply_defaults()
  2. config.yaml
  3. Override files (budgets.yaml, model_router.yaml, dashboard/dashboard.yaml)
     loaded from project_root if present.
"""
from __future__ import annotations

import copy
from pathlib import Path
from typing import Any

import yaml


def deep_merge(base: dict, override: dict) -> dict:
    """Recursively merge *override* into a copy of *base*. Override wins on conflict."""
    result = dict(base)
    for key, val in override.items():
        if key in result and isinstance(result[key], dict) and isinstance(val, dict):
            result[key] = deep_merge(result[key], val)
        else:
            result[key] = copy.deepcopy(val)
    return result


def _try_load_override(path: Path) -> dict[str, Any]:
    """Load a YAML override file. Returns {} if the file doesn't exist or is unparseable.

    Errors are silently swallowed so a malformed override (e.g. model_router.yaml
    with bad syntax) does not abort config loading. The dedicated preflight checks
    (``orch doctor`` / ``orch validate``) surface parse errors with proper
    diagnostics — we must not swallow them here and convert them into a generic
    ``config.parse`` failure.
    """
    if not path.exists():
        return {}
    try:
        with open(path, encoding="utf-8") as fh:
            return yaml.safe_load(fh) or {}
    except Exception:  # noqa: BLE001 — yaml.YAMLError or IO errors
        return {}


def _apply_defaults(cfg: dict[str, Any]) -> dict[str, Any]:
    """Fill in sane defaults so downstream code doesn't need .get() everywhere."""
    from orchestrator.prompt_builder import DEFAULT_SPEC_ROOT

    cfg.setdefault("concurrency", {})
    cfg["concurrency"].setdefault("global_max", 6)
    cfg["concurrency"].setdefault(
        "per_provider", {"claude": 3, "codex": 2, "opencode": 3}
    )
    cfg.setdefault("strict_files_phases", [])
    cfg.setdefault("default_timeout_multiplier", 1.5)
    cfg.setdefault("budget", {})
    cfg["budget"].setdefault("per_dispatch_usd", 5.0)
    cfg.setdefault("retry", {})
    cfg["retry"].setdefault("backoff_seconds", 5.0)
    cfg["retry"].setdefault("rate_limit_backoff_seconds", 60.0)
    cfg["retry"].setdefault("max_attempts", 2)
    cfg.setdefault("spec_root", DEFAULT_SPEC_ROOT)
    cfg.setdefault("budgets_config", "budgets.yaml")
    cfg.setdefault("budgets_preset", "conservative")
    cfg.setdefault("typical_dispatch_tokens", 200_000)
    cfg.setdefault("findings", {})
    cfg["findings"].setdefault("publish_repo", "hectorcanaimero/orch")
    cfg["findings"].setdefault("publish_rate_limit_per_hour", 3)
    cfg["findings"].setdefault("label", "auto-reported")
    cfg["findings"].setdefault("min_publish_confidence", "medium")
    cfg.setdefault("dispatch", {})
    cfg["dispatch"].setdefault("worktree_mode", False)
    cfg["dispatch"].setdefault("base_branch", "main")
    cfg.setdefault("presentation", {})
    cfg["presentation"].setdefault("status_labels", {
        "backlog":     "Planificado",
        "in_progress": "En progreso",
        "in-progress": "En progreso",
        "done":        "Entregado",
        "blocked":     "Bloqueado",
        "todo":        "Por hacer",
        "skipped":     "Omitido",
    })
    return cfg


def load_config(
    path: str | Path,
    project_root: str | Path | None = None,
) -> dict[str, Any]:
    """Load config.yaml and apply optional override files.

    Args:
        path: Path to config.yaml. Raises FileNotFoundError if missing.
        project_root: Directory to look for override files. Defaults to
                      the directory containing config.yaml.
    """
    p = Path(path)
    if not p.exists():
        raise FileNotFoundError(f"config file not found: {p}")

    root = Path(project_root) if project_root else p.parent

    with open(p, encoding="utf-8") as fh:
        cfg: dict[str, Any] = yaml.safe_load(fh) or {}

    cfg = _apply_defaults(cfg)

    # Apply override files in priority order — last file wins within a key.
    for override_path in [
        root / "budgets.yaml",
        root / "model_router.yaml",
        root / "dashboard" / "dashboard.yaml",
    ]:
        override = _try_load_override(override_path)
        if override:
            cfg = deep_merge(cfg, override)

    return cfg
