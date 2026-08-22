# Sprint E-1 — Dogfooding Loop

Branch: `sprint-e/dogfooding-loop` · Closes: #17 (`orch findings`)

Every agent running on top of orch — dispatchers, atomizers, dashboards —
discovers bugs, missing features, and quirks of both orch itself AND the
user's project. Sprint E-1 gives those agents a first-class capture surface
(`orch findings capture`), a human review point (`orch findings review`),
and a guarded publish path to GitHub issues on the orch repo
(`orch findings publish`).

## Guiding principles

- **Backend-agnostic**: findings live in SQLite (when the state backend is
  sqlite) or a `findings.jsonl` file (when the state backend is file). The
  `StateBackend` Protocol grows five methods; every existing test keeps
  passing.
- **No new runtime deps**. GitHub calls shell out to the `gh` CLI (already
  a documented prerequisite of the Sprint A/B/C/D workflows).
- **Human-in-the-loop by default**. Publishing is an active decision, not a
  background action. `--yes` skips only the FINAL consent prompt, never
  the guardrail checks.
- **Zero foot-guns**: `about: project` findings can never be published to
  the orch repo. `low` confidence cannot publish. Dedup runs both locally
  (hash) and against GitHub (issue search). Rate limit of 3/hour.

## Contract summary

| Command | Purpose | Exit codes |
|---|---|---|
| `orch findings capture` | Persist a new finding | 0 ok · 2 duplicate · 3 validation |
| `orch findings list` | Show findings (table or `--json`) | 0 |
| `orch findings review ID` | Show one finding + GitHub dedup search | 0 · 2 not found |
| `orch findings publish ID` | Guarded publish → GitHub issue | 0 · 1 rate/dedup · 2 refused · 130 cancelled |
| `orch findings dismiss ID --reason R` | Mark dismissed with a reason | 0 · 2 not found |

## Data model

`orchestrator/models.py::Finding`

```python
@dataclass
class Finding:
    id: str                          # uuid4 hex
    created_at: str                  # ISO 8601 UTC
    type: Literal["bug", "fix", "feature"]
    about: Literal["orch", "project"]
    summary: str                     # required, single line
    evidence: str                    # multiline ok
    confidence: Literal["low", "medium", "high"]
    status: Literal["pending", "published", "dismissed", "duplicate"] = "pending"
    published_url: str | None = None
    duplicate_of: str | None = None
    dedup_hash: str = ""             # sha256(type|about|normalized_summary)
    project_id: str = ""
    author: str = "agent"
    dismissed_reason: str | None = None
```

`dedup_hash` is computed via `_normalize_summary` (lowercase, strip
punctuation, collapse whitespace) so cosmetic differences in wording never
create duplicates. The hash is UNIQUE at the storage layer on both
backends.

## Storage

### SQLite backend
Migration `sqlite_migrations/002_findings.sql`:

```sql
PRAGMA user_version = 2;
CREATE TABLE findings (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('bug','fix','feature')),
    about TEXT NOT NULL CHECK (about IN ('orch','project')),
    summary TEXT NOT NULL,
    evidence TEXT NOT NULL DEFAULT '',
    confidence TEXT NOT NULL CHECK (confidence IN ('low','medium','high')),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','published','dismissed','duplicate')),
    published_url TEXT,
    duplicate_of TEXT,
    dedup_hash TEXT NOT NULL UNIQUE,
    author TEXT NOT NULL DEFAULT 'agent',
    dismissed_reason TEXT
);
CREATE INDEX idx_findings_project_status ON findings(project_id, status);
CREATE INDEX idx_findings_project_about  ON findings(project_id, about);
```

Applied lazily on first `SqliteBackend.__init__`. Idempotent —
`PRAGMA user_version` guards re-application.

### File backend
Append-only `<state_dir>/findings.jsonl`. Updates rewrite the file atomically
(via `_atomic_write`) — findings are dozens of rows per project, so a full
rewrite on update is well below the file-backend performance budget.

### StateBackend Protocol additions
Five new methods (see `orchestrator/state/interface.py`):

- `append_finding(finding)` — persist; raises on hash collision
- `iter_findings(status=None, about=None)` — filtered iterator
- `get_finding(id)` — exact fetch
- `update_finding(id, **updates)` — partial mutation (whitelist of fields)
- `find_finding_by_dedup_hash(hash)` — dedup lookup

All parametrized in `test_backend_parity.py` — file and sqlite prove the
same observable contract.

## Business logic — `orchestrator/findings.py`

Pure Python, backend-agnostic. Takes a `StateBackend` and delegates I/O.

Public API:
- `capture(backend, ...)` — validate + hash + persist. Raises
  `DuplicateFindingError` if the hash matches an existing row,
  `FindingValidationError` on schema violations.
- `list_findings(backend, status=?, about=?)` — thin passthrough.
- `search_github_issues_for_duplicate(summary, repo)` — shells
  `gh api search/issues?q=repo:<repo>+is:open+<keywords>`. Returns matches
  sorted by Jaccard word-overlap ratio (highest first). Tolerates
  `FileNotFoundError`, non-zero rc, and invalid JSON — always returns a
  list (never raises).
- `publish(backend, id, repo=..., ...)` — the main flow (see below).
- `dismiss(backend, id, reason)` — mark dismissed.

Private helpers (exposed for tests):
- `_normalize_summary(s)` — dedup normalization.
- `_dedup_hash(type, about, summary)` — sha256 of pipe-joined normalized
  triple.
- `_word_overlap_ratio(a, b)` — Jaccard on lowercased, stopword-filtered
  token sets.
- `_check_rate_limit(backend, limit)` — publishes-in-last-hour probe.
- `_ensure_label(repo, label)` — idempotent `gh label create`.

## Publish flow (`publish()`)

1. **Fetch** finding by id → 2/not-found if missing.
2. **Idempotency**: if `status=published` and `published_url` present, return
   `already_published` with the existing URL. NO gh calls.
3. **Classification gate**: `about=project` → refuse hard (belongs on the
   user's tracker). Exit 2.
4. **Confidence gate**: `low < min_publish_confidence` → refuse unless
   `--force`. Exit 2.
5. **Rate limit**: count publishes in last rolling hour; ≥ limit → raise
   `RateLimitExceeded`. Exit 1.
6. **GitHub dedup search**: `search_github_issues_for_duplicate(...)`. If
   any match has `overlap ≥ 0.6`, raise `DuplicateIssueFound` unless
   `--force`. Exit 1.
7. **Dry run short-circuit**: if `--dry-run`, return the report without
   any writes. NO label create, NO issue POST.
8. **Consent**: if `confirm` callable is set (CLI passes a TTY prompter),
   invoke it. False → raise (cancelled). Exit 130.
9. **Label ensure**: `_ensure_label(repo, label)`. Idempotent; "already
   exists" swallowed.
10. **Create issue**: `_publish_gh_issue(...)` — `gh api repos/<repo>/issues
    -X POST --input -`. Returns `html_url` on success.
11. **Persist**: `backend.update_finding(id, status="published",
    published_url=url)`.

Return dict:
```python
{
  "status": "published" | "already_published" | "dry_run",
  "finding_id": <id>,
  "published_url": <url or None>,
  "rate_limit": {"count": N, "limit": L},
  "dedup_matches": [{"number", "title", "html_url", "overlap"}, ...],
}
```

## Config additions

`orchestrator/config.yaml`:

```yaml
findings:
  publish_repo: "hectorcanaimero/orch"
  publish_rate_limit_per_hour: 3
  label: "auto-reported"
  min_publish_confidence: "medium"
```

Any missing key falls back to the module defaults in
`orchestrator.findings` (`DEFAULT_REPO`, `DEFAULT_RATE_LIMIT`,
`DEFAULT_LABEL`, `DEFAULT_MIN_PUBLISH_CONFIDENCE`), so operators who forgot
to update their config still get sane behavior.

## Tests

| File | Focus |
|---|---|
| `test_findings_store.py` | Sqlite migration (v2), file layout, multitenant isolation |
| `test_backend_parity.py` (+7 tests) | Both backends agree on the 5 Protocol methods |
| `test_findings_capture.py` | Dedup helpers, capture validation, list filters, CLI |
| `test_findings_review.py` | GitHub search mocked, review CLI (human + `--json`) |
| `test_findings_publish.py` | Every guardrail has a targeted test |
| `test_findings_integration.py` | Cross-backend E2E, edge cases, rate-limit boundary |

Total added: ~90 tests across 6 files. Both backend gates
(`ORCH_TEST_BACKEND=file` and `ORCH_TEST_BACKEND=sqlite`) stay green.

## Non-goals for E-1

- No dashboard integration for findings — reserved for a later sprint.
- No auto-capture from failed dispatches — the agent decides when to
  `orch findings capture`.
- No cross-project federation — findings stay scoped to their `project_id`.
