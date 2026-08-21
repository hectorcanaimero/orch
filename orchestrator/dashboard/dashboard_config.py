"""Dashboard UX config loader.

Loads `dashboard.yaml` — first from the project root (operator override),
then falls back to the packaged default shipped alongside `server.py`.

Keys mirror the query param names on `/kanban` so the resolver in
`server.py` can do a simple `query_val or config.kanban.wip_default`.

Design mirrors `PricingTable.load()` — no side effects, tolerant of a
missing project file, tolerant of a malformed YAML (falls back to defaults
with a warning printed to stderr).
"""

from __future__ import annotations

import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml


@dataclass(frozen=True, slots=True)
class KanbanDefaults:
    wip_default: int | None = None
    sort_default: str | None = None
    group_default: str | None = None
    refresh_interval_s: int = 10


@dataclass(frozen=True, slots=True)
class DashboardConfig:
    kanban: KanbanDefaults

    @classmethod
    def load(cls, project_root: Path | None = None) -> "DashboardConfig":
        """Merge packaged defaults with the optional project override.

        Precedence: project `dashboard.yaml` > packaged `dashboard.yaml`
        > hard-coded dataclass defaults. Missing files are silently OK.
        """
        packaged = Path(__file__).parent / "dashboard.yaml"
        merged: dict[str, Any] = {}
        for src in (packaged, (project_root / "dashboard.yaml") if project_root else None):
            if src is None or not src.exists():
                continue
            try:
                data = yaml.safe_load(src.read_text(encoding="utf-8")) or {}
            except (yaml.YAMLError, OSError) as exc:
                print(f"[dashboard] ignoring {src}: {exc}", file=sys.stderr)
                continue
            if not isinstance(data, dict):
                continue
            _deep_merge(merged, data)

        kanban_raw = merged.get("kanban") or {}
        return cls(
            kanban=KanbanDefaults(
                wip_default=_coerce_int_or_none(kanban_raw.get("wip_default")),
                sort_default=_coerce_str_or_none(kanban_raw.get("sort_default")),
                group_default=_coerce_str_or_none(kanban_raw.get("group_default")),
                refresh_interval_s=int(kanban_raw.get("refresh_interval_s") or 10),
            )
        )


def _deep_merge(base: dict, override: dict) -> None:
    """In-place recursive merge — later wins, dicts merge, scalars replace."""
    for k, v in override.items():
        if isinstance(v, dict) and isinstance(base.get(k), dict):
            _deep_merge(base[k], v)
        else:
            base[k] = v


def _coerce_int_or_none(v: Any) -> int | None:
    if v is None or v == "":
        return None
    try:
        return int(v)
    except (TypeError, ValueError):
        return None


def _coerce_str_or_none(v: Any) -> str | None:
    if v is None or v == "":
        return None
    return str(v)
