# Go migration notes

Bugs found in the Python `orch` while the Go rewrite (see CLAUDE.md → "Migración a Go (en curso)") is in progress get logged here instead of fixed with a new feature or a large refactor in Python. Small, targeted fixes are still fine — this file is for anything that would otherwise tempt scope creep in the version being replaced.

## Open notes

- ~~Stale `jinja2` mentions~~ — **RESOLVED** (g0/sonnet-cleanup): `orchestrator/orch.py`'s
  dashboard-missing-deps hint no longer tells the user to install `jinja2 >= 3.1`, and
  `orchestrator/dashboard/__init__.py`'s module docstring now describes FastAPI serving
  the embedded React SPA instead of calling out `jinja2` as a "heavy import".

- ~~**Frontend leftovers of `board_url`**~~ — **RESOLVED** (g0/sonnet-cleanup, 2026-09-11):
  `frontend/src/lib/types.ts:202`'s `board_url?` field and `frontend/env.example`'s
  `dashboard.board_url` comment are both removed.

- ~~**Project templates wrote snake_case task keys `Task.from_json` doesn't
  read**~~ — **RESOLVED** (g0/sonnet-templates, 2026-09-11): found by opus during
  G0.3 (PR #95). All four `orchestrator/templates/projects/*/tasks.json.tmpl`
  used `estimate_hours` / `spec_ref`, but `Task.from_json` (orchestrator/models.py)
  only reads `estimateHours` / `specRef` — every task scaffolded from a template
  silently got `estimate_hours=0.0` and `spec_ref=""`, so the dashboard ETA showed
  "—" and the dispatch prompt dropped the "Spec ref (READ FIRST)" line for every
  templated project. Renamed the 32 occurrences across the 4 templates; added a
  parametrized regression test (`test_template_tasks_are_readable_by_task_from_json`
  in `orchestrator/tests/test_init.py`) that scaffolds each template and asserts
  every task has `estimate_hours > 0` and a non-empty `spec_ref`.

- ~~**P0: the stakeholder URL was unusable in a browser**~~ — **RESOLVED**
  (g0/opus-stakeholder-url, 2026-09-11): `TokenAuthMiddleware` returned 401 for
  `index.html` and `/assets/*` when no token was present, so a stakeholder URL
  with `?token=` loaded the SPA shell but the JS/CSS asset requests (which don't
  carry the query param) 401'd and the page stayed blank — the token-entry form
  inside the SPA was unreachable because the SPA itself never rendered. Fixed in
  two halves. Server: `_is_public_shell` in `orchestrator/dashboard/middleware.py`
  exempts requests that resolve to the SPA StaticFiles mount, plus the
  `/assets/` prefix; `/api/` is a hard never-public prefix on top of route
  resolution so a method-fuzz or an unknown `/api/*` path can't fall through to
  the mount. SPA: `adoptTokenFromQuery()` (`frontend/src/hooks/useAuth.ts`, called
  from `main.tsx` before the first render) moves `?token=` into `localStorage`
  and scrubs it out of the address bar with `history.replaceState`.
  `orchestrator/tests/test_dashboard_security_public_shell.py` sweeps the live
  route table and fails if any data route ever escapes the token gate.

## Notes for the Go rewrite

- **`internal/dashboard` — where the auth gate belongs.** Put the token check on
  the **data** routes (`/api/*`, `/stakeholder/summary`, `/snapshot`,
  `/logs/stream`), never on the static surface. The embedded SPA's `index.html`
  and its `/assets/*` bundle must be served **without** a token: a browser
  cannot attach a token to the `<script src>` requests the shell makes, so
  gating them means the shared URL renders a blank page and the token form is
  unreachable. Carry over the two invariants the Python fix pins: an unknown
  path under `/api/` is never public (don't let a catch-all static handler win
  it), and a wrong method on a data route resolves to that route, not to the
  static fallback. The equivalent of the route-table sweep test is cheap in Go
  and worth keeping.
