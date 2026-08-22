"""Provider allowlist for the dashboard tunnel (Sprint E-5, TUN-2).

v1 ships a single provider — `autossh` fronting a Pinggy SSH tunnel. The
registry pins each entry to a canonical binary name and a URL-extraction
regex so an operator cannot swap the command out from under the config gate.

Everything here is pure data + regex — no I/O, no subprocess. `TunnelManager`
consumes the compiled patterns in its stdout reader thread (D2: the
`autossh_reconnect_regex` is separate from the URL regex so we can count
supervisor-internal reconnects independently of manual restarts).
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field


PROVIDER_AUTOSSH = "autossh"


@dataclass(frozen=True, slots=True)
class ProviderSpec:
    """Frozen provider entry — name, pinned binary, argv defaults, regexes."""

    name: str
    command: str
    default_args: tuple[str, ...] = field(default_factory=tuple)
    url_regex: str = ""
    autossh_reconnect_regex: str = ""


# Pinggy free tier assigns a random subdomain under `a.pinggy.link`. autossh
# writes the assigned URL to stdout once the ssh handshake succeeds.
_AUTOSSH_URL_PATTERN = r"https://[a-z0-9-]+\.a\.pinggy\.link"

# autossh's own monitoring loop logs when it decides to restart the child
# ssh — we count those distinctly from operator-driven /stop→/start cycles.
# The exact wording is stable across autossh 1.4c: "starting ssh"/"ssh exited"
# both fire on a reconnect. Match either so we don't miss the event.
_AUTOSSH_RECONNECT_PATTERN = (
    r"(?:starting ssh|ssh exited|ssh reconnecting|connection lost|"
    r"port forwarding restarted)"
)

_AUTOSSH_DEFAULT_ARGS: tuple[str, ...] = (
    "-M", "0",
    "-o", "StrictHostKeyChecking=no",
    "-o", "ServerAliveInterval=30",
    "-o", "ExitOnForwardFailure=yes",
    "-p", "443",
    "-R", "0:localhost:7420",
    "a.pinggy.io",
)


PROVIDERS: dict[str, ProviderSpec] = {
    PROVIDER_AUTOSSH: ProviderSpec(
        name=PROVIDER_AUTOSSH,
        command="autossh",
        default_args=_AUTOSSH_DEFAULT_ARGS,
        url_regex=_AUTOSSH_URL_PATTERN,
        autossh_reconnect_regex=_AUTOSSH_RECONNECT_PATTERN,
    ),
}


def resolve(provider_name: str) -> ProviderSpec:
    """Look up a provider by name. Raises `KeyError` on miss (config gate)."""
    if provider_name not in PROVIDERS:
        raise KeyError(provider_name)
    return PROVIDERS[provider_name]


def compile_url_regex(spec_or_pattern: ProviderSpec | str) -> re.Pattern[str]:
    """Compile the URL regex — accepts a spec (uses its `url_regex`) or a raw pattern."""
    pattern = (
        spec_or_pattern.url_regex
        if isinstance(spec_or_pattern, ProviderSpec)
        else spec_or_pattern
    )
    return re.compile(pattern)


def compile_reconnect_regex(spec: ProviderSpec) -> re.Pattern[str]:
    return re.compile(spec.autossh_reconnect_regex, re.IGNORECASE)


def known_providers() -> tuple[str, ...]:
    return tuple(PROVIDERS.keys())
