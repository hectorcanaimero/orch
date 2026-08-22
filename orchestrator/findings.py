"""Business logic for the dogfooding-loop findings system (Sprint E-1 / #17).

This module is intentionally backend-agnostic: every operation takes a
`StateBackend` and delegates persistence to it. The CLI wrappers in
`orch.py` resolve the backend once per subcommand invocation.

Design constraints (from the sprint spec, non-negotiable):
    - NO new runtime deps. Standard library only + `gh` CLI shell-outs.
    - `about=project` findings can NEVER be published (they belong on the
      user's own tracker). `orch findings publish` refuses hard.
    - `confidence=low` cannot publish unless `--force`.
    - Publishing is IDEMPOTENT: once a finding has `status=published` and a
      `published_url`, a repeat publish is a no-op with a helpful message.
    - Rate-limited to N publishes/hour (configurable). Enforced pre-flight.
    - Dedup at capture (local hash) AND at publish (GitHub search).
    - Publishing requires TTY confirmation; `--yes` skips only the FINAL
      "are you sure" prompt, never the guardrail checks.
    - Label `auto-reported` is created on demand on the target repo.

Public surface (used by the CLI + tests):
    capture               — create + persist a finding, refuse duplicates.
    list_findings         — filter passthrough to the backend.
    publish               — main flow with every guardrail.
    dismiss               — mark as dismissed with a reason.
    search_github_issues_for_duplicate  — dedup against remote issues.

Private helpers exposed for tests:
    _normalize_summary, _dedup_hash, _word_overlap_ratio,
    _check_rate_limit, _ensure_label, _gh_api, _publish_gh_issue.
"""

from __future__ import annotations

import hashlib
import json
import logging
import re
import subprocess
import uuid
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from typing import Any, Iterable

from .models import Finding
from .state.interface import StateBackend

log = logging.getLogger(__name__)


# ---- constants ----------------------------------------------------------

DEFAULT_REPO = "hectorcanaimero/orch"
DEFAULT_LABEL = "auto-reported"
DEFAULT_RATE_LIMIT = 3  # publishes per rolling hour
DEFAULT_MIN_PUBLISH_CONFIDENCE = "medium"
DEDUP_OVERLAP_THRESHOLD = 0.6

_ALLOWED_TYPES = frozenset({"bug", "fix", "feature"})
_ALLOWED_ABOUT = frozenset({"orch", "project"})
_ALLOWED_CONFIDENCE = frozenset({"low", "medium", "high"})
# Ordinal used by min_publish_confidence gate; low < medium < high.
_CONFIDENCE_RANK: dict[str, int] = {"low": 0, "medium": 1, "high": 2}

# Words that add zero dedup signal — drop them before computing overlap so
# "orch prints a stack trace on init" and "the stack trace on init" line up.
_STOPWORDS = frozenset({
    "the", "a", "an", "of", "on", "in", "to", "for", "with", "and", "or",
    "is", "are", "be", "at", "by", "from", "as", "it", "this", "that",
    "when", "then", "if", "into", "onto", "up", "down", "out",
})

# Title cap for GitHub issues — longer titles hurt scanning in the issue list.
_ISSUE_TITLE_MAX = 120


# ---- error types --------------------------------------------------------


class FindingValidationError(ValueError):
    """Raised when capture inputs violate the schema (bad type, empty summary…)."""


class DuplicateFindingError(ValueError):
    """Raised when capture detects an existing finding with the same dedup hash."""

    def __init__(self, message: str, existing: Finding):
        super().__init__(message)
        self.existing = existing


class PublishRefusedError(RuntimeError):
    """Raised by `publish` when a guardrail blocks the operation."""


class RateLimitExceeded(PublishRefusedError):
    """Raised when the publish rate limit trips."""


class DuplicateIssueFound(PublishRefusedError):
    """Raised when GitHub already has an issue overlapping the finding summary."""

    def __init__(self, message: str, match: dict[str, Any], overlap: float):
        super().__init__(message)
        self.match = match
        self.overlap = overlap


# ---- pure helpers -------------------------------------------------------


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _normalize_summary(summary: str) -> str:
    """Lowercase, collapse whitespace, strip punctuation for dedup hashing."""
    s = (summary or "").lower()
    # Replace punctuation with a space; retain word characters and spaces.
    s = re.sub(r"[^\w\s]+", " ", s, flags=re.UNICODE)
    s = re.sub(r"\s+", " ", s).strip()
    return s


def _dedup_hash(finding_type: str, about: str, summary: str) -> str:
    """Stable hash used for local (per-project) duplicate detection."""
    key = f"{finding_type}|{about}|{_normalize_summary(summary)}"
    return hashlib.sha256(key.encode("utf-8")).hexdigest()


def _tokenize(text: str) -> set[str]:
    """Lowercased word set with stopwords removed. Empty tokens dropped."""
    words = re.findall(r"\w+", (text or "").lower(), flags=re.UNICODE)
    return {w for w in words if w and w not in _STOPWORDS and len(w) > 1}


def _word_overlap_ratio(a: str, b: str) -> float:
    """Jaccard similarity on lowercased, stopword-filtered word sets.

    Returns 0.0 when both inputs are empty or share no tokens; 1.0 when the
    token sets are identical. Not a percentage — a `float` in `[0, 1]`.
    """
    sa = _tokenize(a)
    sb = _tokenize(b)
    if not sa or not sb:
        return 0.0
    inter = sa & sb
    union = sa | sb
    if not union:
        return 0.0
    return len(inter) / len(union)


def _issue_keywords(summary: str, limit: int = 6) -> list[str]:
    """Pick the strongest tokens from a summary for GitHub's `q=` search.

    We drop stopwords + very short words, then keep the first `limit` in
    the order they appear (order matters less than presence — GitHub does
    the actual scoring). Deduplicated while preserving order.
    """
    seen: set[str] = set()
    out: list[str] = []
    for word in re.findall(r"\w+", (summary or "").lower(), flags=re.UNICODE):
        if len(word) <= 2 or word in _STOPWORDS:
            continue
        if word in seen:
            continue
        seen.add(word)
        out.append(word)
        if len(out) >= limit:
            break
    return out


# ---- capture / list -----------------------------------------------------


def _validate_capture(
    finding_type: str,
    about: str,
    summary: str,
    confidence: str,
) -> None:
    if finding_type not in _ALLOWED_TYPES:
        raise FindingValidationError(
            f"invalid type {finding_type!r}; expected one of {sorted(_ALLOWED_TYPES)}"
        )
    if about not in _ALLOWED_ABOUT:
        raise FindingValidationError(
            f"invalid about {about!r}; expected one of {sorted(_ALLOWED_ABOUT)}"
        )
    if confidence not in _ALLOWED_CONFIDENCE:
        raise FindingValidationError(
            f"invalid confidence {confidence!r}; "
            f"expected one of {sorted(_ALLOWED_CONFIDENCE)}"
        )
    if not summary or not summary.strip():
        raise FindingValidationError("summary is required and cannot be blank")
    if "\n" in summary:
        raise FindingValidationError("summary must be a single line (no newlines)")


def capture(
    backend: StateBackend,
    *,
    finding_type: str,
    about: str,
    summary: str,
    evidence: str = "",
    confidence: str = "medium",
    author: str = "agent",
    project_id: str = "",
    now: str | None = None,
    finding_id: str | None = None,
) -> Finding:
    """Create and persist a finding. Raises on validation or dedup collision.

    `project_id` defaults to the backend's project (populated by the CLI).
    `finding_id`/`now` are injection seams for deterministic tests — normal
    callers omit them.
    """
    _validate_capture(finding_type, about, summary, confidence)
    dedup = _dedup_hash(finding_type, about, summary)
    existing = backend.find_finding_by_dedup_hash(dedup)
    if existing is not None:
        raise DuplicateFindingError(
            f"duplicate finding: already captured as {existing.id} "
            f"(status={existing.status})",
            existing,
        )
    finding = Finding(
        id=finding_id or uuid.uuid4().hex,
        created_at=now or _utc_now_iso(),
        type=finding_type,  # type: ignore[arg-type]
        about=about,  # type: ignore[arg-type]
        summary=summary.strip(),
        evidence=evidence or "",
        confidence=confidence,  # type: ignore[arg-type]
        dedup_hash=dedup,
        project_id=project_id or getattr(backend, "project_id", "") or "",
        author=author or "agent",
    )
    backend.append_finding(finding)
    return finding


def list_findings(
    backend: StateBackend,
    *,
    status: str | None = None,
    about: str | None = None,
) -> list[Finding]:
    """Materialize `backend.iter_findings` — same filter args, list result."""
    return list(backend.iter_findings(status=status, about=about))


# ---- GitHub `gh` CLI shell-outs -----------------------------------------


def _gh_api(
    args: list[str],
    *,
    check: bool = True,
    runner: Any = None,
) -> subprocess.CompletedProcess:
    """Wrapper around `gh api <args...>` that captures stdout/stderr.

    `runner` is a test seam — defaults to `subprocess.run`. Callers that
    mock `subprocess.run(["gh", ...])` will still work through the default.
    """
    cmd = ["gh", "api", *args]
    log.debug("gh api: %s", " ".join(cmd))
    fn = runner or subprocess.run
    return fn(  # noqa: S603 — argv list, no shell
        cmd,
        check=check,
        capture_output=True,
        text=True,
    )


def search_github_issues_for_duplicate(
    summary: str,
    repo: str,
    *,
    limit: int = 5,
    runner: Any = None,
) -> list[dict[str, Any]]:
    """Query `gh api search/issues` for OPEN issues matching the summary tokens.

    Returns up to `limit` shallow dicts with at minimum `number`, `title`,
    `html_url`, `overlap` (computed here — Jaccard vs the input summary).

    Never raises on network / auth errors: those are logged and the caller
    gets an empty list back. The point of the search is to WARN, not to gate
    on the network being up.
    """
    kws = _issue_keywords(summary)
    if not kws:
        return []
    query = f"repo:{repo} is:issue is:open " + " ".join(kws)
    try:
        proc = _gh_api(
            [f"search/issues?q={_shell_quote(query)}&per_page={int(limit)}"],
            check=False,
            runner=runner,
        )
    except FileNotFoundError:
        log.warning("gh CLI not found; skipping GitHub dedup search")
        return []
    if proc.returncode != 0:
        log.warning("gh api search/issues failed (%s): %s", proc.returncode, proc.stderr.strip())
        return []
    try:
        payload = json.loads(proc.stdout or "{}")
    except json.JSONDecodeError as exc:
        log.warning("gh api returned invalid JSON: %s", exc)
        return []
    items = payload.get("items") if isinstance(payload, dict) else None
    if not isinstance(items, list):
        return []
    out: list[dict[str, Any]] = []
    for item in items[:limit]:
        if not isinstance(item, dict):
            continue
        title = str(item.get("title") or "")
        out.append({
            "number": item.get("number"),
            "title": title,
            "html_url": item.get("html_url") or "",
            "overlap": round(_word_overlap_ratio(summary, title), 3),
        })
    # Highest overlap first — makes the "worst" duplicate the first shown.
    out.sort(key=lambda row: row["overlap"], reverse=True)
    return out


def _shell_quote(text: str) -> str:
    """URL-safe encode of a GitHub search query.

    `gh api "search/issues?q=..."` requires percent-encoded spaces. We do the
    encoding inline rather than pull in `urllib` because we already limit
    tokens to `\\w+`, so a naïve replace is correct here.
    """
    from urllib.parse import quote

    return quote(text, safe=":/+-_.~")


# ---- rate limiting / label ensure --------------------------------------


def _check_rate_limit(
    backend: StateBackend,
    limit_per_hour: int,
    *,
    now: datetime | None = None,
) -> tuple[int, int]:
    """Raise `RateLimitExceeded` when publishes-in-last-hour >= limit.

    Returns `(count_in_window, limit)` for observability. Limit <= 0 disables
    the check (returns `(0, limit)`).
    """
    if limit_per_hour is None or limit_per_hour <= 0:
        return (0, limit_per_hour or 0)
    now_dt = now or datetime.now(timezone.utc)
    cutoff = now_dt - timedelta(hours=1)
    count = 0
    for f in backend.iter_findings(status="published"):
        # Publish timestamp: we don't persist a separate `published_at`, so
        # rely on `created_at` shifted by the update. This under-estimates
        # publish age (created before publish), but the direction is safe:
        # older creations fall out of the window sooner, i.e. we may permit
        # slightly MORE publishes than a strict window would. Acceptable
        # for a soft rate limit whose only purpose is to keep the tracker
        # from being flooded by a runaway agent.
        try:
            ts = datetime.strptime(f.created_at, "%Y-%m-%dT%H:%M:%SZ").replace(
                tzinfo=timezone.utc
            )
        except (ValueError, TypeError):
            continue
        if ts >= cutoff:
            count += 1
    if count >= limit_per_hour:
        raise RateLimitExceeded(
            f"rate limit hit: {count} publishes in the last hour "
            f"(limit={limit_per_hour})"
        )
    return (count, limit_per_hour)


def _ensure_label(repo: str, label: str, *, runner: Any = None) -> None:
    """Idempotently create `label` on `repo` — swallows "already exists" errors.

    Uses `gh label create` rather than the REST endpoint because it accepts a
    friendlier CLI. Any other error is logged but not raised: a missing label
    at publish time will surface as a proper API error from `issues.create`.
    """
    cmd = [
        "gh", "label", "create", label,
        "--repo", repo,
        "--color", "fbca04",
        "--description", "Auto-reported by an orch agent",
    ]
    fn = runner or subprocess.run
    try:
        proc = fn(  # noqa: S603
            cmd,
            check=False,
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        log.warning("gh CLI not found; skipping label ensure")
        return
    if proc.returncode == 0:
        return
    stderr = (proc.stderr or "").lower()
    if "already exists" in stderr or "http 422" in stderr:
        return
    log.warning("gh label create failed (%s): %s", proc.returncode, proc.stderr.strip())


# ---- publish / dismiss --------------------------------------------------


def _build_issue_body(finding: Finding) -> str:
    """Render the finding as a GitHub issue body — Markdown, agent-friendly."""
    lines = [
        f"**Type**: {finding.type}",
        f"**About**: {finding.about}",
        f"**Confidence**: {finding.confidence}",
        f"**Author**: {finding.author}",
        f"**Captured at**: {finding.created_at}",
        f"**Local id**: `{finding.id}`",
        "",
        "## Evidence",
        "",
        finding.evidence.strip() if finding.evidence else "_(none provided)_",
        "",
        "---",
        "_Reported automatically by an orch agent — see `orch findings`._",
    ]
    return "\n".join(lines)


def _truncate_title(summary: str) -> str:
    """Cap the issue title at `_ISSUE_TITLE_MAX` chars with a `…` suffix."""
    s = (summary or "").strip()
    if len(s) <= _ISSUE_TITLE_MAX:
        return s
    return s[: _ISSUE_TITLE_MAX - 1].rstrip() + "…"


def _publish_gh_issue(
    finding: Finding,
    repo: str,
    label: str,
    *,
    runner: Any = None,
) -> str:
    """Create a GitHub issue via `gh api` and return the html_url.

    Uses `POST /repos/{repo}/issues` for a stable JSON contract.
    """
    body = _build_issue_body(finding)
    payload = {
        "title": _truncate_title(finding.summary),
        "body": body,
        "labels": [label],
    }
    # `gh api` reads --input from stdin when passed `-f field=value` is too
    # limited (no arrays). Use --input - with a JSON stdin payload.
    cmd = [
        "gh", "api",
        f"repos/{repo}/issues",
        "--method", "POST",
        "--input", "-",
    ]
    fn = runner or subprocess.run
    proc = fn(  # noqa: S603
        cmd,
        input=json.dumps(payload),
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise PublishRefusedError(
            f"gh api POST issues failed ({proc.returncode}): {proc.stderr.strip()}"
        )
    try:
        data = json.loads(proc.stdout or "{}")
    except json.JSONDecodeError as exc:
        raise PublishRefusedError(f"gh api returned invalid JSON: {exc}") from exc
    url = data.get("html_url") if isinstance(data, dict) else None
    if not url:
        raise PublishRefusedError(
            "gh api POST issues did not return html_url; refusing to mark published"
        )
    return str(url)


def publish(
    backend: StateBackend,
    finding_id: str,
    *,
    repo: str,
    label: str = DEFAULT_LABEL,
    rate_limit_per_hour: int = DEFAULT_RATE_LIMIT,
    min_confidence: str = DEFAULT_MIN_PUBLISH_CONFIDENCE,
    dedup_threshold: float = DEDUP_OVERLAP_THRESHOLD,
    dry_run: bool = False,
    yes: bool = False,  # noqa: ARG001 — TTY consent is CLI-layer's job
    force: bool = False,
    confirm: Any = None,
    runner: Any = None,
    now: datetime | None = None,
) -> dict[str, Any]:
    """Publish a finding to a GitHub issue. Returns a report dict.

    Layout of the returned dict:
        {
          "status": "published" | "already_published" | "dry_run",
          "finding_id": <id>,
          "published_url": <url or None>,
          "rate_limit": {"count": N, "limit": L},
          "dedup_matches": [{"number", "title", "html_url", "overlap"}, ...],
        }

    Every guardrail raises `PublishRefusedError` (or a subclass) — the CLI
    layer catches and maps to an exit code. `confirm` is a callable that
    returns True/False; when None, defaults to always True (CLI passes a
    real TTY prompter).
    """
    finding = backend.get_finding(finding_id)
    if finding is None:
        raise PublishRefusedError(f"finding not found: {finding_id!r}")

    # Idempotency — do nothing if already published.
    if finding.status == "published" and finding.published_url:
        return {
            "status": "already_published",
            "finding_id": finding.id,
            "published_url": finding.published_url,
            "rate_limit": {"count": 0, "limit": rate_limit_per_hour},
            "dedup_matches": [],
        }

    # Hard gate: about=project is user's tracker, never orch's.
    if finding.about == "project":
        raise PublishRefusedError(
            "cannot publish findings with about=project — those belong on your "
            "own tracker, not on the orch repo"
        )

    # Confidence gate.
    if _CONFIDENCE_RANK.get(finding.confidence, 0) < _CONFIDENCE_RANK.get(
        min_confidence, 1
    ):
        if not force:
            raise PublishRefusedError(
                f"confidence {finding.confidence!r} is below minimum "
                f"{min_confidence!r} — pass --force to override"
            )

    # Rate limit.
    count, limit = _check_rate_limit(
        backend, rate_limit_per_hour, now=now
    )

    # GitHub dedup search.
    matches = search_github_issues_for_duplicate(
        finding.summary, repo, runner=runner
    )
    strong = [m for m in matches if m.get("overlap", 0.0) >= dedup_threshold]
    if strong and not force:
        top = strong[0]
        raise DuplicateIssueFound(
            f"possible duplicate of #{top.get('number')} ({top.get('html_url')}) "
            f"— overlap={top.get('overlap')}. Pass --force to publish anyway",
            top,
            float(top.get("overlap", 0.0)),
        )

    if dry_run:
        return {
            "status": "dry_run",
            "finding_id": finding.id,
            "published_url": None,
            "rate_limit": {"count": count, "limit": limit},
            "dedup_matches": matches,
        }

    # Final consent — CLI passes a prompt; tests pass a lambda.
    if confirm is not None:
        ok = bool(confirm(finding))
        if not ok:
            raise PublishRefusedError("user cancelled at consent prompt")

    # Ensure label exists (idempotent) then create the issue.
    _ensure_label(repo, label, runner=runner)
    url = _publish_gh_issue(finding, repo, label, runner=runner)
    backend.update_finding(finding.id, status="published", published_url=url)
    return {
        "status": "published",
        "finding_id": finding.id,
        "published_url": url,
        "rate_limit": {"count": count + 1, "limit": limit},
        "dedup_matches": matches,
    }


def dismiss(backend: StateBackend, finding_id: str, reason: str) -> Finding:
    """Mark a finding as dismissed with a reason. Idempotent for already-dismissed."""
    finding = backend.get_finding(finding_id)
    if finding is None:
        raise PublishRefusedError(f"finding not found: {finding_id!r}")
    backend.update_finding(
        finding_id, status="dismissed", dismissed_reason=reason or ""
    )
    return replace(finding, status="dismissed", dismissed_reason=reason or "")
