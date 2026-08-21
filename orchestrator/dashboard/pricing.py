"""Pricing table loader + fallback cost calculator.

Contract (from the MVP brief):
    - The dashboard trusts `SpendEntry.cost_usd` when it's a real positive
      number. That's what the orchestrator recorded at dispatch time and
      it's the source of truth (claude/anthropic return real billing).
    - When `cost_usd` is 0 (or None / missing / malformed), we fall back
      to this table to *estimate* a cost from tokens. This matters for
      opencode and codex where there's no billing API and the recorded
      cost is always 0.
    - The table lives at `dashboard/pricing.yaml` (defaults) and can be
      overridden per project with a `pricing.yaml` at `<project_root>/`.

The loader is intentionally forgiving: missing YAML, missing keys, missing
`default` — none of them crash. Worst case we return 0.0 and the dashboard
shows "$0.00" for that row.

Prices are USD per 1M tokens. Formula:
    cost = tokens_in * input / 1_000_000 + tokens_out * output / 1_000_000
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any


_DEFAULT_TABLE_PATH = Path(__file__).parent / "pricing.yaml"

# Absolute floor when nothing else resolves. We prefer to under-estimate than
# to invent a $99/1M price out of thin air.
_HARDCODED_FALLBACK: dict[str, float] = {"input": 1.00, "output": 4.00}


@dataclass(frozen=True, slots=True)
class ModelPrice:
    """Per-1M-tokens price for a single model."""

    input: float
    output: float

    def cost(self, tokens_in: int, tokens_out: int) -> float:
        """Compute USD cost for a dispatch. Negative tokens → 0."""
        ti = max(0, int(tokens_in or 0))
        to = max(0, int(tokens_out or 0))
        return (ti * self.input + to * self.output) / 1_000_000.0


class PricingTable:
    """Layered price table: project overrides on top of dashboard defaults.

    Layering rules:
        - Both files are optional. Missing files are silently ignored.
        - Project keys REPLACE (not merge with) default keys of the same
          model name — you can't tweak just `input` and keep `output`. This
          keeps the mental model simple: one row = one full price.
        - Model lookup order: exact key → `default` key → hardcoded floor.
    """

    def __init__(self, prices: dict[str, ModelPrice]):
        self._prices = prices

    # ---- constructors ---------------------------------------------------

    @classmethod
    def load(cls, project_root: Path | None = None) -> "PricingTable":
        """Load defaults from the packaged YAML, then layer project overrides.

        `project_root` is optional — when None (or when no `pricing.yaml`
        exists there), only defaults are used.
        """
        merged: dict[str, dict[str, float]] = {}
        merged.update(_read_models(_DEFAULT_TABLE_PATH))
        if project_root is not None:
            override = project_root / "pricing.yaml"
            merged.update(_read_models(override))

        # Guarantee a `default` row so `for_model` always finds SOMETHING
        # short of the hardcoded floor. Cheap safety net for typos in YAML.
        merged.setdefault("default", dict(_HARDCODED_FALLBACK))

        prices = {
            name: ModelPrice(
                input=float(row.get("input", 0.0) or 0.0),
                output=float(row.get("output", 0.0) or 0.0),
            )
            for name, row in merged.items()
        }
        return cls(prices)

    # ---- lookup ---------------------------------------------------------

    def for_model(self, model: str) -> ModelPrice:
        """Return the price for `model`, falling back to `default` → floor."""
        if model in self._prices:
            return self._prices[model]
        if "default" in self._prices:
            return self._prices["default"]
        return ModelPrice(**_HARDCODED_FALLBACK)

    def has(self, model: str) -> bool:
        """True if `model` has an explicit row (not just the default fallback)."""
        return model in self._prices

    def estimate_cost(self, model: str, tokens_in: int, tokens_out: int) -> float:
        """Convenience: look up + compute in one call."""
        return self.for_model(model).cost(tokens_in, tokens_out)

    def resolve_cost(
        self,
        recorded_cost: float | None,
        model: str,
        tokens_in: int,
        tokens_out: int,
    ) -> float:
        """Return the cost we should display for a spend row.

        Rules:
            - `recorded_cost` > 0                → use it (source of truth)
            - `recorded_cost` in {0, None, NaN}  → estimate from pricing table
            - malformed model                    → fall back to `default` row
        """
        try:
            rc = float(recorded_cost) if recorded_cost is not None else 0.0
        except (TypeError, ValueError):
            rc = 0.0
        if rc > 0:
            return rc
        return self.estimate_cost(model or "default", tokens_in, tokens_out)

    # ---- introspection --------------------------------------------------

    def models(self) -> list[str]:
        """All model keys currently in the table, sorted for stable UIs."""
        return sorted(self._prices.keys())


# ---- helpers ----------------------------------------------------------------


def _read_models(path: Path) -> dict[str, dict[str, float]]:
    """Read a `pricing.yaml` file and return its `models:` map.

    Missing file → `{}`. Malformed YAML → `{}` with no crash. `models:` is
    the only accepted top-level shape; anything else is treated as empty.
    """
    if not path.exists():
        return {}
    try:
        import yaml
    except ImportError:  # pragma: no cover — pyyaml is a hard dep of orch
        return {}
    try:
        with path.open("r", encoding="utf-8") as fh:
            data: Any = yaml.safe_load(fh) or {}
    except Exception:  # noqa: BLE001 — corrupt YAML shouldn't kill the dashboard
        return {}
    if not isinstance(data, dict):
        return {}
    models = data.get("models") or {}
    if not isinstance(models, dict):
        return {}
    # Filter: keep only rows that look like `{input: float, output: float}`.
    out: dict[str, dict[str, float]] = {}
    for name, row in models.items():
        if not isinstance(row, dict):
            continue
        out[str(name)] = {
            "input": float(row.get("input", 0.0) or 0.0),
            "output": float(row.get("output", 0.0) or 0.0),
        }
    return out
