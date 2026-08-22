# Sprint E-2 — Dashboard Profiles + Public URL

Branch: `sprint-e2/dashboard-profiles` · Closes: #15

The read-only dashboard (Sprint 5) was designed for the operator running orch
on their own machine. Sprint E-2 extends it with two new profiles so the same
FastAPI app can safely serve a stakeholder-facing view over a public URL —
without leaking per-model cost, raw logs, or the internal control surface.

## Guiding principles

- **Operator UX unchanged**. Default profile is `operator`. Existing
  deployments boot without config changes and see no behavioral difference.
- **Defense in depth**. Every operator-only route is gated by BOTH the ASGI
  middleware (403 in stakeholder mode) AND a template feature flag that hides
  sensitive fields. Break one, the other still holds.
- **Info-hiding rejections**. 401/403 bodies are single-line plain text with
  no route names, no stack traces, no `WWW-Authenticate` header. Nothing a
  scanner can enumerate.
- **No new runtime deps**. Everything is FastAPI + Starlette + Jinja2 (already
  present since Sprint 5). Cloudflare Tunnel is a docs-only recommendation.
- **Backend-agnostic**. Curated payload reads through the same
  `read_all_events` / `read_all_spends` helpers that Sprint 5 uses — file and
  sqlite backends produce identical stakeholder JSON.

## Profile matrix

| Profile        | Auth               | Routes served                                                                 |
|----------------|--------------------|-------------------------------------------------------------------------------|
| `operator`     | none (default)     | Everything, exactly like pre-Sprint-E-2.                                      |
| `stakeholder`  | Bearer token       | `/stakeholder`, `/stakeholder/summary`, `/static/*`. Every other route 403s.  |
| `both`         | Bearer on `/stakeholder/*` | Under `/stakeholder/*` → stakeholder rules. Everything else → operator (no auth). |

The token is the string in `ORCH_DASHBOARD_TOKEN` (env), `--token TOKEN`
(CLI flag), or `dashboard.token` in `config.yaml`. Precedence: flag > env >
config. It must be presented as `Authorization: Bearer <token>` OR
`?token=<token>` (query param variant for browser bookmarks).

## Curated payload

`GET /stakeholder/summary` returns exactly these fields:

```json
{
  "project_id": "my-project",
  "summary": { "total": 42, "done": 18, "in_progress": 4, "blocked": 1, "backlog": 19, "percent_done": 42, "estimate_hours_total": 210.0 },
  "milestones": [
    { "phase": 1, "total_count": 12, "done_count": 12, "done": true },
    { "phase": 2, "total_count": 30, "done_count": 6, "done": false }
  ],
  "spend_rounded_usd": 4.5,
  "eta_hours": 88.4,
  "refresh_interval_s": 30
}
```

What's NOT in it (and never will be, without a spec change):

- Per-model spend breakdown or provider identifiers.
- Per-task IDs, models, exit codes, or raw log content.
- Per-task or per-day cost.
- Stack traces or error messages.
- Any URL that streams (`/logs/stream`, `/api/events/stream`).

## Route inventory

Enumerated with `rg -n "@app\.(get|post|put|delete|websocket)" orchestrator/dashboard/server.py`:

| Route                          | Route name              | Operator | Stakeholder | Notes                                    |
|--------------------------------|-------------------------|:--------:|:-----------:|------------------------------------------|
| `GET /`                        | `index`                 | ✅       | ❌ 403      | Full task table                          |
| `GET /kanban`                  | `kanban_page`           | ✅       | ❌ 403      | Full kanban with per-model tag           |
| `GET /metrics`                 | `metrics_page`          | ✅       | ❌ 403      | Per-model + per-day spend                |
| `GET /logs`                    | `logs_page`             | ✅       | ❌ 403      | Raw event log                            |
| `GET /logs/stream`             | `logs_stream`           | ✅       | ❌ 403      | SSE — same content live                  |
| `GET /api/events/stream`       | `api_events_stream`     | ✅       | ❌ 403      | SSE alias                                |
| `GET /api/tasks`               | `api_tasks`             | ✅       | ❌ 403      | Full task JSON                           |
| `GET /api/task/{id}`           | `api_task_detail`       | ✅       | ❌ 403      | Per-task detail                          |
| `GET /api/budgets`             | `api_budgets`           | ✅       | ❌ 403      | Per-provider budget snapshot             |
| `GET /api/metrics`             | `api_metrics`           | ✅       | ❌ 403      | Aggregated spend + tokens                |
| `GET /snapshot`                | `snapshot`              | ✅       | ❌ 403      | Full raw dump                            |
| `GET /partials/task-modal/{id}`| `partial_task_modal`    | ✅       | ❌ 403      | HTMX partial                             |
| `GET /partials/task-row/{id}`  | `partial_task_row`      | ✅       | ❌ 403      | HTMX partial                             |
| `GET /stakeholder`             | `stakeholder_index`     | ✅ (in `both`) | ✅     | Curated HTML                             |
| `GET /stakeholder/summary`     | `stakeholder_summary_json` | ✅ (in `both`) | ✅  | Curated JSON                             |
| `GET /static/*`                | `static` (mount)        | ✅       | ✅          | CSS/JS assets — path-prefix allow-listed |

## Middleware wiring

`server.create_app()` registers middleware ONLY when `profile != "operator"`.

```python
if dash_cfg.profile != PROFILE_OPERATOR:
    app.add_middleware(ProfileGuardMiddleware, config=dash_cfg)
    app.add_middleware(TokenAuthMiddleware, config=dash_cfg)
```

Starlette runs the LAST-added middleware FIRST on the wire, so on-wire order
is:

    incoming request → TokenAuth → ProfileGuard → route handler

- `TokenAuth`: If the request is under the stakeholder context, require a
  matching Bearer token OR `?token=`. Use `hmac.compare_digest` for constant
  time comparison. Reject with 401 + `Cache-Control: no-store` when missing or
  wrong. When server has no token configured, refuse with 401 identically
  (no distinguishable "misconfig" 500).
- `ProfileGuard`: If the request is under the stakeholder context, allow only
  routes whose `.name` OR path-prefix appears in
  `DashboardConfig.stakeholder_routes`. Everything else 403s with a bland
  `"forbidden"` body.

The stakeholder allow-list is INCLUSIVE (must be explicitly listed).
Default allow-list:

```python
DEFAULT_STAKEHOLDER_ROUTES = (
    "stakeholder_index",
    "stakeholder_summary_json",
    "/static/",
)
```

Operators can EXTEND (never shrink) via `config.yaml`:

```yaml
dashboard:
  profile: stakeholder
  token: change-me
  stakeholder_routes:
    - /public-extra/
    - some_custom_route_name
```

## Template feature flags

Even though the middleware 403s operator routes for stakeholder callers,
every sensitive UI block is ALSO wrapped in `{% if profile != 'stakeholder' %}`.
If a future refactor accidentally removes the middleware, the template still
won't leak per-model cost or raw log links.

Injection point: `create_app()._tpl_ctx()` merges `profile` into every
`TemplateResponse` context. Templates just read `profile` — they never call
into the middleware.

## CLI flags

```
orch dashboard [--profile operator|stakeholder|both]
               [--token TOKEN]
               [--host HOST]
               [--port PORT]
               [--project-root PATH] [--project-id ID]
               [--config PATH] [--reload]
```

Preflight validation runs BEFORE uvicorn boots:

- `--profile stakeholder` requires a token via flag, env, or config —
  otherwise exit 2 with a clear error listing all three options.
- `--token` is exported into `os.environ["ORCH_DASHBOARD_TOKEN"]` so
  downstream config loading sees it, then also passed as a
  `token_override` kwarg for explicit precedence.

## Data-flow diagram

```
                    ┌─────────────────────────┐
     browser  ────► │   TokenAuthMiddleware   │  401 if missing/wrong
                    └────────────┬────────────┘
                                 │  auth passed
                    ┌────────────▼────────────┐
                    │ ProfileGuardMiddleware  │  403 if not on allow-list
                    └────────────┬────────────┘
                                 │  guard passed
                    ┌────────────▼────────────┐
                    │      route handler      │  reads state, renders
                    └────────────┬────────────┘
                                 │
                                 │  (profile injected into ctx)
                                 ▼
                    ┌─────────────────────────┐
                    │  Jinja template — with  │
                    │  {% if profile == ... %}│
                    └─────────────────────────┘
```

## Test surface

- `test_dashboard_middleware.py` — 22 unit tests. Config precedence,
  auth acceptance/rejection, guard allow-list, ws handshake denial.
- `test_dashboard_stakeholder_view.py` — 13 tests. Curated payload shape,
  hidden fields, operator regression.
- `test_dashboard_security.py` — 42 tests. Full route matrix operator ↔
  stakeholder, info-hiding response contracts, both-mode wiring, mutation
  verbs, ws rejection.
- `test_dashboard_cli.py` — 9 tests. Flag parsing, token requirement,
  precedence.
- `test_dashboard_security_integration.py` — 28 tests. Backend parity
  (file + sqlite), attack matrix, static mount preserved.

Total: 114 new tests. Baseline dashboard tests (45) still green.

## Cloudflare Tunnel (docs only)

We deliberately do NOT ship a tunnel config or auto-setup script. The
operator provisions the tunnel outside orch:

1. `cloudflared tunnel create orch-<project>`
2. `cloudflared tunnel route dns <TUNNEL_ID> orch.example.com`
3. Point the tunnel `ingress` to `http://127.0.0.1:7420`.
4. Run `orch dashboard --profile both --token $ORCH_DASHBOARD_TOKEN`.
5. Optional: put Cloudflare Access (email-PIN) in front of the tunnel so
   the token stops being the only line of defense.

Full step-by-step in `docs/DASHBOARD-PROFILES.md`.

## Decisions we deliberately did NOT make

- **No IP allowlist**. Not orch's job. If you want it, sit CF Access in
  front of the tunnel.
- **No per-stakeholder tokens**. One shared secret per deployment.
  Multi-tenant auth is out of scope for MVP.
- **No rate limiting on 401s**. cloudflared / CF Access handles it upstream.
- **No CSRF**. Every stakeholder route is GET-only + read-only.
- **No cookies**. Bearer only. Bookmarking with `?token=` is the escape
  hatch for browsers that can't set Authorization.

## Non-goals for Sprint E-2

- Interactive control surface for stakeholders (comments, approvals). If a
  stakeholder wants to interact, they should talk to the operator.
- Per-project stakeholder allow-list overrides beyond `stakeholder_routes`.
- Real-time WebSocket updates on the curated view — polling `?refresh=30s`
  is sufficient at MVP scale.
