"""Tests for `orchestrator.dashboard.tunnel.providers` — Sprint E-5 (TUN-2, TUN-7, D2)."""

from __future__ import annotations

import pytest

from orchestrator.dashboard.tunnel import providers
from orchestrator.dashboard.tunnel.providers import (
    PROVIDERS,
    ProviderSpec,
    compile_reconnect_regex,
    compile_url_regex,
    known_providers,
    resolve,
)


# ---- Allowlist (TUN-2) ---------------------------------------------------


def test_resolve_autossh_returns_pinned_binary() -> None:
    spec = resolve("autossh")
    assert isinstance(spec, ProviderSpec)
    assert spec.command == "autossh"
    assert spec.name == "autossh"


def test_resolve_unknown_provider_raises_keyerror() -> None:
    with pytest.raises(KeyError):
        resolve("ngrok")


def test_known_providers_contains_autossh_and_bore() -> None:
    # Order is registry-insertion order; autossh first (v1), bore second (v2).
    assert known_providers() == ("autossh", "bore")


def test_provider_registry_is_frozen_dataclass() -> None:
    spec = PROVIDERS["autossh"]
    with pytest.raises((AttributeError, TypeError)):
        spec.command = "hacked"  # type: ignore[misc]


# ---- URL regex (TUN-7) ---------------------------------------------------


@pytest.mark.parametrize(
    "line",
    [
        "https://abc123.a.pinggy.link",
        "Forwarded: https://xyz-9.a.pinggy.link is now available",
        "prefix noise https://a-b-c.a.pinggy.link suffix noise",
    ],
)
def test_url_regex_captures_valid_pinggy_urls(line: str) -> None:
    pattern = compile_url_regex(resolve("autossh"))
    m = pattern.search(line)
    assert m is not None
    assert m.group(0).startswith("https://")
    assert m.group(0).endswith(".a.pinggy.link")


@pytest.mark.parametrize(
    "line",
    [
        "http://abc123.a.pinggy.link",  # http scheme not accepted
        "https://abc123.example.com",  # wrong domain
        "no url here at all",
        "https://ABC.A.PINGGY.LINK",  # uppercase — regex is lowercase-only
    ],
)
def test_url_regex_rejects_malformed(line: str) -> None:
    pattern = compile_url_regex(resolve("autossh"))
    assert pattern.search(line) is None


def test_compile_url_regex_accepts_raw_pattern_string() -> None:
    pat = compile_url_regex(r"https://\d+\.example\.test")
    assert pat.search("https://42.example.test") is not None


# ---- Reconnect regex (D2 — autossh_reconnects) ---------------------------


@pytest.mark.parametrize(
    "line",
    [
        "autossh[123]: starting ssh (count 4)",
        "autossh[123]: ssh exited prematurely; restarting",
        "ssh reconnecting to remote host",
        "connection lost, retry",
        "port forwarding restarted successfully",
        "STARTING SSH",  # case-insensitive
    ],
)
def test_reconnect_regex_matches_autossh_lifecycle_events(line: str) -> None:
    pattern = compile_reconnect_regex(resolve("autossh"))
    assert pattern.search(line) is not None


@pytest.mark.parametrize(
    "line",
    [
        "https://abc123.a.pinggy.link",
        "everything is fine",
        "handshake success",
    ],
)
def test_reconnect_regex_does_not_match_normal_stdout(line: str) -> None:
    pattern = compile_reconnect_regex(resolve("autossh"))
    assert pattern.search(line) is None


# ---- Module surface ------------------------------------------------------


def test_providers_module_exposes_expected_symbols() -> None:
    for name in (
        "PROVIDERS",
        "ProviderSpec",
        "compile_url_regex",
        "compile_reconnect_regex",
        "resolve",
        "known_providers",
    ):
        assert hasattr(providers, name), f"missing symbol: {name}"


# ---- bore provider (v2) --------------------------------------------------


def test_bore_provider_registered() -> None:
    spec = resolve("bore")
    assert isinstance(spec, ProviderSpec)
    assert spec.name == "bore"
    assert spec.command == "bore"
    # bore uses url_template because its stdout emits only "bore.pub:PORT",
    # not a full URL. The template assembles the http:// scheme.
    assert spec.url_template == "http://bore.pub:{port}"
    # Default args target the free public bore.pub server on local :7420.
    assert spec.default_args == ("local", "7420", "--to", "bore.pub")
    # bore has no supervisor loop → no reconnect regex.
    assert spec.autossh_reconnect_regex == ""


def test_bore_url_extraction_produces_full_http_url() -> None:
    spec = resolve("bore")
    pattern = compile_url_regex(spec)
    line = (
        "2024-01-15T12:00:00.000000Z  INFO bore_cli::client: "
        "listening at bore.pub:12345"
    )
    m = pattern.search(line)
    assert m is not None
    assert m.groupdict() == {"port": "12345"}
    # This mirrors the manager: partial match + template → full URL.
    assembled = spec.url_template.format_map(m.groupdict())
    assert assembled == "http://bore.pub:12345"


@pytest.mark.parametrize(
    "line",
    [
        "listening at bore.pub:1",
        "prefix noise listening at bore.pub:65535 suffix",
        "INFO bore_cli::client: listening at bore.pub:42",
    ],
)
def test_bore_url_regex_captures_various_ports(line: str) -> None:
    pattern = compile_url_regex(resolve("bore"))
    m = pattern.search(line)
    assert m is not None
    assert m.group("port").isdigit()


@pytest.mark.parametrize(
    "line",
    [
        "listening at example.com:12345",  # wrong host
        "connecting to bore.pub:12345",     # wrong verb
        "bore.pub:12345",                    # missing "listening at" prefix
        "no port info here",
    ],
)
def test_bore_url_regex_rejects_non_matching_lines(line: str) -> None:
    pattern = compile_url_regex(resolve("bore"))
    assert pattern.search(line) is None


def test_url_template_defaults_to_none_for_legacy_providers() -> None:
    # autossh emits a full URL in stdout — no template needed. The manager
    # falls back to match.group(0) when this is None (backwards compat).
    spec = resolve("autossh")
    assert spec.url_template is None
