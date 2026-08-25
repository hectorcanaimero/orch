"""Regression tests for `model_router.yaml` — enforces that every route
references a model accepted by the current opencode-go registry (2026-08-20
audit) and that fallbacks are self-consistent.

Context: an audit found ~11% of tasks routed to stale/fake models
(e.g. `gpt-5.6-codex` doesn't exist, `xai/grok-4.6` was renamed to `grok-4.5`,
`google/gemini-3.0-flash` isn't in the accepted list). This test locks the
cleanup so future edits can't reintroduce drift silently.

Native routes (backend: claude, backend: codex) are exempt from the
accepted-model check — those CLIs have their own model registries.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.router import load_router


ROUTER_PATH = Path(__file__).parent.parent / "model_router.yaml"


# Authoritative list of `cli_model` strings accepted by opencode-go as of
# 2026-08-20 (from user-provided audit). Provider-prefixed form matches how
# they appear in `model_router.yaml`.
ACCEPTED_OPENCODE_MODELS: set[str] = {
    # xAI (normalized to opencode-go/ prefix)
    "opencode-go/grok-4.5",
    # OpenAI (via opencode-go pass-through — openai/ prefix works locally)
    "openai/gpt-5.6-luna",
    # Z.ai GLM family (normalized to opencode-go/ prefix)
    "opencode-go/glm-5.3",
    "opencode-go/glm-5.2",
    "opencode-go/glm-5.1",  # legacy key, still accepted
    # Moonshot / Kimi (normalized to opencode-go/ prefix)
    "opencode-go/kimi-k3",
    "opencode-go/kimi-k2.7-code",
    "opencode-go/kimi-k2.6",
    # Xiaomi MiMo (normalized to opencode-go/ prefix)
    "opencode-go/mimo-v2.5-pro",
    "opencode-go/mimo-v2.5",
    # Alibaba Qwen (normalized to opencode-go/ prefix)
    "opencode-go/qwen3.8-max",
    "opencode-go/qwen3.7-max",
    "opencode-go/qwen3.7-plus",
    "opencode-go/qwen3.6-plus",
    # MiniMax (normalized to opencode-go/ prefix)
    "opencode-go/minimax-m3",
    "opencode-go/minimax-m2.7",
    # DeepSeek (deepseek/ prefix works locally — no change needed)
    "deepseek/deepseek-v4-pro",
    "deepseek/deepseek-v4-flash",
    # (Muse Spark, Hy3 intentionally skipped — TODO block at end of yaml)
}


# ---- Rename regressions -------------------------------------------------

# For each stale route key, expected new `cli_model` value.
EXPECTED_CLI_MODEL_RENAMES: dict[str, str] = {
    "opencode-go/minimax-m2.5": "opencode-go/minimax-m2.7",
    "opencode-go/grok-4.6": "opencode-go/grok-4.5",
    "opencode-go/gemini-3.0-flash": "deepseek/deepseek-v4-flash",
    "opencode/gemini-3.0-pro": "opencode-go/grok-4.5",
    "opencode-go/mimo-v2.5": "opencode-go/mimo-v2.5",
}


EXPECTED_FALLBACK_RENAMES: dict[str, str | None] = {
    # opencode/gemini-3.0-pro rerouted to opencode-go/grok-4.5 → fallback = deepseek-v4-pro
    "opencode/gemini-3.0-pro": "deepseek/deepseek-v4-pro",
    # opencode-go/mimo-v2.5 restored → fallback = opencode-go/mimo-v2.5-pro
    "opencode-go/mimo-v2.5": "opencode-go/mimo-v2.5-pro",
    # codex/gpt-5.6 fallback typo fix (WARN log said gpt-5.4); key renamed from opencode/gpt-5.6-codex
    "codex/gpt-5.6": "gpt-5.4",
}


# Newly-added route keys we expect to exist after the cleanup.
EXPECTED_NEW_ROUTES: set[str] = {
    # Premium tier
    "opencode/gpt-5.6-luna",
    "opencode/glm-5.3",
    "opencode/kimi-k3",
    "opencode/minimax-m3",
    "opencode/mimo-v2.5-pro",
    # Standard tier
    "opencode/glm-5.2",
    "opencode/kimi-k2.7-code",
    "opencode/qwen3.7-max",
    "opencode/minimax-m2.7",
    # Cheap tier
    "opencode-go/kimi-k2.6",
    "opencode-go/qwen3.7-plus",
    "opencode-go/qwen3.6-plus",
}


# Route keys that use a NATIVE CLI (claude/codex/gemini) and therefore reference
# a model outside opencode-go's accepted list — exempt from the check.
NATIVE_BACKENDS: set[str] = {"claude", "codex", "gemini"}


def test_rename_targets_present_with_expected_cli_model() -> None:
    """Every stale route MUST be renamed to the audit-approved cli_model."""
    router = load_router(ROUTER_PATH)
    for route_key, expected in EXPECTED_CLI_MODEL_RENAMES.items():
        assert route_key in router, f"route {route_key!r} missing from router"
        actual = router[route_key].cli_model
        assert actual == expected, (
            f"route {route_key!r}: cli_model {actual!r} != expected {expected!r}"
        )


def test_fallback_renames_applied() -> None:
    """Fallback cli_model must match the audit fix (typo + rerouted routes)."""
    router = load_router(ROUTER_PATH)
    for route_key, expected in EXPECTED_FALLBACK_RENAMES.items():
        assert route_key in router, f"route {route_key!r} missing"
        actual = router[route_key].fallback_cli_model
        assert actual == expected, (
            f"route {route_key!r}: fallback_cli_model {actual!r} != expected {expected!r}"
        )


def test_mimo_route_restored_to_opencode_backend() -> None:
    """The temp claude patch on opencode-go/mimo-v2.5 must be reverted."""
    router = load_router(ROUTER_PATH)
    entry = router["opencode-go/mimo-v2.5"]
    assert entry.backend == "opencode", (
        f"opencode-go/mimo-v2.5 backend {entry.backend!r} — expected 'opencode' "
        "(temp claude patch was supposed to be removed)"
    )


def test_new_routes_added() -> None:
    """All accepted models missing from the old router must exist as routes."""
    router = load_router(ROUTER_PATH)
    missing = EXPECTED_NEW_ROUTES - router.keys()
    assert not missing, f"expected new routes missing: {sorted(missing)}"


def test_no_route_uses_disallowed_opencode_model() -> None:
    """opencode/opencode-go routes may only reference accepted models.

    Native routes (backend: claude, backend: codex) are exempt — they use
    their own CLI's model registry.
    """
    router = load_router(ROUTER_PATH)
    offenders: list[tuple[str, str]] = []
    for route_key, entry in router.items():
        if entry.backend in NATIVE_BACKENDS:
            continue
        if entry.cli_model not in ACCEPTED_OPENCODE_MODELS:
            offenders.append((route_key, entry.cli_model))
    assert not offenders, (
        "routes referencing models NOT in the accepted opencode-go list:\n"
        + "\n".join(f"  - {k}: {m!r}" for k, m in offenders)
    )


def test_fallbacks_are_resolvable() -> None:
    """Every non-null fallback_cli_model must be resolvable.

    Resolution: EITHER the string appears as `cli_model` on some other route
    in the file, OR it's in the accepted opencode list, OR (for native
    backends) it's a plausible native model string.
    """
    router = load_router(ROUTER_PATH)
    all_cli_models = {e.cli_model for e in router.values()}
    offenders: list[tuple[str, str]] = []
    for route_key, entry in router.items():
        fb = entry.fallback_cli_model
        if fb is None:
            continue
        if fb in all_cli_models:
            continue
        if fb in ACCEPTED_OPENCODE_MODELS:
            continue
        # Native-backend fallback: must live in the same family as cli_model.
        if entry.backend in NATIVE_BACKENDS:
            # Cheap heuristic: fallback shares the family prefix.
            fam = entry.cli_model.split("-")[0]
            if fb.startswith(fam[:6]):
                continue
        offenders.append((route_key, fb))
    assert not offenders, (
        "fallback_cli_model values that don't resolve to any known model:\n"
        + "\n".join(f"  - {k}: fallback={m!r}" for k, m in offenders)
    )


def test_codex_gpt56_routes_use_verified_luna_model() -> None:
    """After the 2026-08-20 live verification (`codex exec -m gpt-5.6-luna`
    returned turn.completed with valid JSONL), the two codex-backend routes
    that were pinned to `gpt-5.4` as a TODO must now point at `gpt-5.6-luna`.

    Fallback stays at `gpt-5.4` for version-drift safety.
    """
    router = load_router(ROUTER_PATH)
    for route_key in ("codex/gpt-5.5", "codex/gpt-5.6"):
        assert route_key in router, f"route {route_key!r} missing"
        entry = router[route_key]
        assert entry.cli_model == "gpt-5.6-luna", (
            f"{route_key!r}: cli_model {entry.cli_model!r} != 'gpt-5.6-luna'"
        )
        assert entry.fallback_cli_model == "gpt-5.4", (
            f"{route_key!r}: fallback_cli_model {entry.fallback_cli_model!r} "
            "!= 'gpt-5.4' (sensible fallback for version drift)"
        )


def test_router_entry_count_meets_expected_floor() -> None:
    """Cleanup should ADD routes, not remove existing ones.

    Old count was 16 (one per tasks.json model). After adding the
    accepted-model routes, count should be strictly higher.
    """
    router = load_router(ROUTER_PATH)
    assert len(router) >= 16 + len(EXPECTED_NEW_ROUTES), (
        f"router has {len(router)} entries; expected at least "
        f"{16 + len(EXPECTED_NEW_ROUTES)} after cleanup"
    )
