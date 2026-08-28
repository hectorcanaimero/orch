"""Slack/Discord webhook notifier for Sprint G-6.

Zero new deps — POSTs via stdlib ``urllib.request`` with a short timeout,
silent-fail: a broken webhook must NEVER surface an exception to the
orchestrator's dispatch loop. The goal is "the dev doesn't have to babysit
the dashboard", not "add another crash path to the run loop".

Usage:
    notifier = Notifier.from_config(cfg)
    notifier.notify_blocked("F1.T3", reason="spec ambiguous")
    notifier.notify_ci_blocked("F1.T3", pr_url="https://...", attempts=3)
    text = notifier.digest_text(summary, milestones)  # for `orch notify digest`

Config schema (see config_loader._apply_defaults):
    notifications:
      slack_webhook: ""      # https://hooks.slack.com/services/...
      discord_webhook: ""    # https://discord.com/api/webhooks/...
      timeout_s: 5

Slack accepts ``{"text": "..."}``. Discord accepts ``{"content": "..."}``.
Both are treated as best-effort side channels; the JSONL event log remains
the source of truth for what happened.
"""
from __future__ import annotations

import json
import logging
import urllib.error
import urllib.request
from typing import Any, Iterable

log = logging.getLogger(__name__)

_DEFAULT_TIMEOUT_S = 5.0


class Notifier:
    """POST short notifications to Slack/Discord webhooks.

    A webhook URL of ``""`` (the default) disables that channel. A notifier
    with all channels disabled is a no-op — safe to instantiate and call
    unconditionally from the dispatch loop.
    """

    def __init__(
        self,
        slack_webhook: str = "",
        discord_webhook: str = "",
        timeout_s: float = _DEFAULT_TIMEOUT_S,
    ) -> None:
        self.slack_webhook = slack_webhook or ""
        self.discord_webhook = discord_webhook or ""
        self.timeout_s = float(timeout_s)

    # ---- construction ------------------------------------------------------

    @classmethod
    def from_config(cls, cfg: dict[str, Any]) -> "Notifier":
        """Build a Notifier from the top-level ``cfg`` dict.

        Reads ``cfg["notifications"]``. Missing/empty section → all channels
        disabled (safe no-op).
        """
        n = (cfg or {}).get("notifications") or {}
        return cls(
            slack_webhook=str(n.get("slack_webhook") or ""),
            discord_webhook=str(n.get("discord_webhook") or ""),
            timeout_s=float(n.get("timeout_s") or _DEFAULT_TIMEOUT_S),
        )

    # ---- introspection -----------------------------------------------------

    @property
    def enabled(self) -> bool:
        """True when at least one webhook is configured."""
        return bool(self.slack_webhook) or bool(self.discord_webhook)

    # ---- public notification API ------------------------------------------

    def notify_blocked(self, task_id: str, *, reason: str = "") -> None:
        """Task went to `blocked` in the dispatch loop."""
        reason_snip = (reason or "unknown").strip().splitlines()[0][:200]
        self._send(f":no_entry: orch: task `{task_id}` blocked — {reason_snip}")

    def notify_ci_blocked(
        self, task_id: str, *, pr_url: str = "", attempts: int = 0
    ) -> None:
        """CI kept failing past `vcs.ci_max_retries` — task marked blocked."""
        tail = f" ({pr_url})" if pr_url else ""
        self._send(
            f":warning: orch: task `{task_id}` blocked after {attempts} CI attempt(s){tail}"
        )

    def notify_test(self, message: str = "orch: notifier test") -> bool:
        """Send a one-off message to verify webhook config.

        Unlike the notify_* helpers this returns whether any channel accepted
        the message — used by ``orch notify test`` so the operator gets a real
        exit code and stderr line when the config is wrong.
        """
        return self._send(message, raise_on_error=False, report=True)

    def digest_text(
        self,
        summary: dict[str, Any],
        milestones: Iterable[dict[str, Any]] | None = None,
    ) -> str:
        """Compose the plain-text digest body.

        Reuses ``executive_summary`` output verbatim (``summary["text"]``) and
        appends a compact milestone table when milestones are supplied. Pure —
        no I/O, no clock, safe to snapshot in tests.
        """
        head = str(summary.get("text") or "").strip()
        lines = [head] if head else []
        rows = list(milestones or [])
        if rows:
            lines.append("")
            lines.append("Milestones:")
            for m in rows:
                name = str(m.get("name") or m.get("id") or "?")
                prog = m.get("progress") or {}
                done = int(prog.get("done", 0))
                total = int(prog.get("total", 0))
                eta = (m.get("eta") or {}).get("eta_date") or "—"
                lines.append(f"  - {name}: {done}/{total} — ETA {eta}")
        return "\n".join(lines).strip() + "\n"

    # ---- transport --------------------------------------------------------

    def _send(
        self, text: str, *, raise_on_error: bool = False, report: bool = False
    ) -> bool:
        """POST *text* to every configured channel.

        Returns True when at least one channel accepted the payload. Silent on
        failure by default (best-effort) — set ``raise_on_error=True`` only for
        the ``notify_test`` path where the operator wants a real signal.
        """
        any_ok = False
        for url, payload in self._payloads(text):
            ok = self._post(url, payload, raise_on_error=raise_on_error)
            any_ok = any_ok or ok
            if report:
                where = "slack" if "slack" in url else "discord"
                if ok:
                    log.info("notifier: %s OK", where)
                else:
                    log.warning("notifier: %s FAILED", where)
        return any_ok

    def _payloads(self, text: str) -> list[tuple[str, dict[str, str]]]:
        out: list[tuple[str, dict[str, str]]] = []
        if self.slack_webhook:
            out.append((self.slack_webhook, {"text": text}))
        if self.discord_webhook:
            out.append((self.discord_webhook, {"content": text}))
        return out

    def _post(
        self, url: str, payload: dict[str, str], *, raise_on_error: bool
    ) -> bool:
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            url,
            data=data,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout_s) as resp:
                # Slack returns 200 with body "ok"; Discord returns 204.
                return 200 <= resp.status < 300
        except (urllib.error.URLError, OSError, ValueError) as exc:
            if raise_on_error:
                raise
            log.warning("notifier POST failed (%s): %s", url[:60], exc)
            return False
