# orch — Project instructions

Task orchestrator that walks a `tasks.json` DAG and dispatches each task to a
local AI CLI (`claude` | `codex` | `opencode`). Single-user, local, no daemon.

## Stack

- **Backend / CLI**: Python `>=3.11`, distributed as `orch` script (see `pyproject.toml`).
- **Deps runtime**: `pyyaml`, `rich`, `fastapi>=0.115,<0.116` (pinned — 0.116+ regresses closure-scoped `Request` annotation resolution), `uvicorn[standard]`, `jinja2`.
- **Dev**: `pytest>=8.0`, `httpx>=0.27` (FastAPI TestClient uses it).
- **Frontend** (`frontend/`, Sprint E-3 SPA spike): Vite + React + TypeScript + shadcn/ui + Tailwind, `pnpm` package manager, oxlint.
- **State backend**: dual mode — file (default) or SQLite (`orch migrate`). See `orchestrator/state_backend/`.
- **Persistence**: `state/` at runtime, never committed (`state/.gitkeep` only).

## Conventions

- **Tests**: `pytest` from repo root. Full suite is 1370 passed + 2 skipped (both SSE endpoints in `test_dashboard_security.py`, covered separately). Two known time-boundary flakes pass in isolation on a fast run but can fail on slow I/O (they seed rows at `datetime('now','-N days')` and race the query cutoff): `test_sprint_metrics.py::test_count_done_last_n_days` and `test_tunnel_manager.py::test_start_writes_atomic_state_json`. A single failure in either on a slow run is NOT a regression. New work must not regress the green count. When you add tests, bump this number in the same commit so the baseline stays honest.
- **Never build after changes.** Type-check / test only.
- **Never use `cat` / `grep` / `find` / `sed` / `ls`.** Use `bat` / `rg` / `fd` / `sd` / `eza`. Install via `brew` if missing.
- **Commits**: conventional-commits format (`feat:` / `fix:` / `test:` / `docs:` / `chore:` / `refactor:`). **No `Co-Authored-By` or AI attribution.**
- **Branches**: sprint-scoped (e.g. `sprint-e3/spa-spike`). Merge to `main` via PR.
- **Backups**: `orch atomize --apply` writes `tasks.json.bak-<ts>` — leave those alone.
- **Docs live in `docs/`**: `MANUAL.{en,es,pt}.md`, dashboard/tunnel guides.

## Layout (top-level)

- `orchestrator/` — Python package. Subpackages: `dashboard/`, `state_backend/`, `templates/`, `providers/`.
- `frontend/` — Vite SPA (E-3 spike). Builds to `frontend/dist/`, served by dashboard when present.
- `docs/` — manuals + design docs.
- `scripts/` — repo helpers (not the per-project `task-*.sh` contract).
- `state/` — gitignored runtime state; only `.gitkeep` tracked.

## Things NOT to invoke unless the user asks

To keep context small, do not proactively call these MCP servers or skills on orch work — they are unrelated to a Python CLI + Vite SPA:

- **MCP servers**: `claude_ai_Figma__*`, `claude_ai_Miro__*`, `claude_ai_Supabase__*`, `claude_ai_Excalidraw__*`, `claude_ai_Google_Drive__*`, `claude_ai_Atlassian_Rovo__*`, `pencil__*`, `plugin_cloudflare_*`, `plugin_playwright_playwright__*`.
- **Design/marketing skills**: `hyperframes`, `da-vinci`, `hallmark`, `copywriting`, `marketing-psychology`, `ui-ux-pro-max`, `html-to-image`, `mobile-app-ui-design`.
- **Unrelated stacks**: `expo-*`, `flutterflow`, `remotion-*`, `prisma-*`, `nestjs-*`, `nextjs-*`, `fastify-*`, `maplibre-*`, `postgis-*`, `supabase*`, `rupies-*`, `realai-*`, `cloudflare*`, `wrangler`, `agents-sdk`, `durable-objects`, `workers-*`, `sandbox-sdk`, `turnstile-*`, `obsidian:*`, `go-testing`.
- **Web/browser tooling**: `Playwright MCP`, `web-perf` — only if the user explicitly asks to profile the dashboard/SPA.

## Skills that ARE relevant

- `orch-plan`, `orch-prd`, `orch-arch`, `orch-spec`, `orch-tasks` — the pipeline this project is built to run.
- `sdd-*` (explore/propose/spec/design/tasks/apply/verify/archive) — spec-driven development suite orch consumes.
- `superpowers:*` — TDD, debugging, plan-writing, code review, git-worktrees, brainstorming.
- `agent-skills:*` — build/plan/test/review/ship/webperf, plus TDD and doubt-driven-development.
- `shadcn`, `frontend-design`, `frontend-design-system`, `tailwind-design-system`, `typescript-best-practices` — only when touching `frontend/`.

## Current context

- **Branch**: `main` (per `git status`; check for drift — sprint branches like `sprint-e3/*` are historical).
- **Version**: v0.9.1 on `main` (after PR #83).
- **Latest sprints**: H-7 wizard confirm gate (#80), H-6 `/orch` skill (#78), H-1a/b/c/d templates (#66/#67/#68/#77), H-3 brand (#65), H-4 README+HN (#66). Fixes fuera de serie: F-11 upgrade (#79), F-12 SQLite SoT (#75), F-13 bootstrap hygiene (#74/#76/#83), F-14 `agy` backend (#82).
- **Pending explicit**: H-1e `expo-mobile` template (last of the 5 canonical).
- **Prior sprints** (auto-memory has details): 7 budget guardrails · 8 packaging (v0.2.0, MIT, pipx) · 9 `orch init` · A runtime robustness · B SQLite backend · C observability subcommands · D `doctor`/`validate`/interactive `init` · E-1..E-8 dashboard iterations · F-1..F-6 clean foundation + PR automation · G-0..G-6 stakeholder UX · H-2 config consolidation · H-3..H-7 templates + brand + wizard.

## Gotchas already learned

- `fastapi<0.116` is a hard cap. Starlette 1.0 breaks the legacy `TemplateResponse` signature; pinning FastAPI keeps the compatible Starlette. Also, `jinja2` is required in the core dep set (not an extra) so `orch dashboard` boots without a second install step.
- `orch dashboard` ships templates + `pricing.yaml` + `dashboard.yaml` + `static/` inside the wheel (see `pyproject.toml [tool.setuptools.package-data]`).
- Runtime YAML defaults (`config.yaml`, `model_router.yaml`, `budgets.yaml`) also ship in the wheel so `pipx`-installed `orch` works without a manual copy.
