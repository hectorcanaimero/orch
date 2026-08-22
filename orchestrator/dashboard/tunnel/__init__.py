"""Tunnel supervisor package (Sprint E-5).

Exposes the public surface consumed by `orchestrator.dashboard.server` in
Batch 3 — the manager, its config surface, gate dependencies, and the
provider allowlist. Nothing here has side effects at import time.
"""

from __future__ import annotations

from orchestrator.dashboard.tunnel.deps import (
    REASON_AUTOSSH_MISSING,
    REASON_CONFIG_DISABLED,
    REASON_HOST_GATE,
    REASON_OK,
    REASON_PROFILE_GATE,
    evaluate_capabilities,
    require_loopback_host,
    require_operator_profile,
    require_tunnel_enabled,
)
from orchestrator.dashboard.tunnel.manager import (
    STATE_ERROR,
    STATE_IDLE,
    STATE_RUNNING,
    STATE_STARTING,
    STATE_STOPPING,
    TunnelManager,
    TunnelManagerConfig,
    read_state,
)
from orchestrator.dashboard.tunnel.providers import (
    PROVIDERS,
    ProviderSpec,
    compile_reconnect_regex,
    compile_url_regex,
    known_providers,
    resolve,
)


__all__ = [
    "PROVIDERS",
    "ProviderSpec",
    "REASON_AUTOSSH_MISSING",
    "REASON_CONFIG_DISABLED",
    "REASON_HOST_GATE",
    "REASON_OK",
    "REASON_PROFILE_GATE",
    "STATE_ERROR",
    "STATE_IDLE",
    "STATE_RUNNING",
    "STATE_STARTING",
    "STATE_STOPPING",
    "TunnelManager",
    "TunnelManagerConfig",
    "compile_reconnect_regex",
    "compile_url_regex",
    "evaluate_capabilities",
    "known_providers",
    "read_state",
    "require_loopback_host",
    "require_operator_profile",
    "require_tunnel_enabled",
    "resolve",
]
