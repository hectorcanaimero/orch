# orch — Project instructions

Task orchestrator that walks a `tasks.json` DAG and dispatches each task to a
local AI CLI (`claude` | `codex` | `opencode`). Single-user, local, no daemon.

## Stack

- **Backend / CLI**: Python `>=3.11`, distributed as `orch` script (see `pyproject.toml`).
- **Deps runtime**: `pyyaml`, `rich`, `fastapi>=0.115,<0.116` (pinned — 0.116+ regresses closure-scoped `Request` annotation resolution), `uvicorn[standard]`. See `pyproject.toml [project.dependencies]` for the authoritative list.
- **Dev**: `pytest>=8.0`, `httpx>=0.27` (FastAPI TestClient uses it).
- **Frontend** (`frontend/`, Sprint E-3 SPA spike): Vite + React + TypeScript + shadcn/ui + Tailwind, `pnpm` package manager, oxlint.
- **State backend**: SQLite by default since v0.11 (PR #91); `file` is legacy JSONL kept for `orch migrate`. See `orchestrator/state/`.
- **Persistence**: `state/` at runtime, never committed (`state/.gitkeep` only).

## Conventions

- **Tests**: `pytest` from repo root. Full suite is 1448 passed + 3 skipped. Two known time-boundary flakes pass in isolation on a fast run but can fail on slow I/O (they seed rows at `datetime('now','-N days')` and race the query cutoff): `test_sprint_metrics.py::test_count_done_last_n_days` and `test_tunnel_manager.py::test_start_writes_atomic_state_json`. A single failure in either on a slow run is NOT a regression. New work must not regress the green count. When you add tests, bump this number in the same commit so the baseline stays honest.
- **Never build after changes.** Type-check / test only.
- **Never use `cat` / `grep` / `find` / `sed` / `ls`.** Use `bat` / `rg` / `fd` / `sd` / `eza`. Install via `brew` if missing.
- **Commits**: conventional-commits format (`feat:` / `fix:` / `test:` / `docs:` / `chore:` / `refactor:`). **No `Co-Authored-By` or AI attribution.**
- **Branches**: sprint-scoped (e.g. `sprint-e3/spa-spike`). Merge to `main` via PR.
- **PRs are reviewed by Gemini in CI**; see `docs/CI-REVIEW.md`.
- **Backups**: `orch atomize --apply` writes `tasks.json.bak-<ts>` — leave those alone.
- **Docs live in `docs/`**: `MANUAL.{en,es,pt}.md`, dashboard/tunnel guides.

## Layout (top-level)

- `orchestrator/` — Python package. Subpackages: `dashboard/` (FastAPI app + tunnel manager), `state/` (file/SQLite backends, see `interface.py` + `adapters.py`), `vcs/` (GitHub/GitLab), `templates/`, `skills/`. Per-CLI dispatch adapters (`ClaudeBackend`, `CodexBackend`, `OpencodeBackend`) live in `orchestrator/dispatcher.py`, not a separate `providers/` package.
- `frontend/` — Vite SPA (E-3 spike). Builds to `frontend/dist/`, served by dashboard when present.
- `docs/` — manuals + design docs.
- `scripts/` — repo helpers (not the per-project `task-*.sh` contract).
- `state/` — gitignored runtime state; only `.gitkeep` tracked.

## Migración a Go (en curso)

orch se está reescribiendo en Go (ver checklist en el artefacto "Orch en Go";
fases G0–G8). Layout objetivo — planificado (Go), ninguna de estas rutas
existe todavía en este repo:

```
cmd/orch/            — main package, arg parsing, entrypoint
internal/
  cli/               — subcomandos (status, tasks, dispatch, findings…)
  config/            — carga + merge de config.yaml / overrides
  model/             — Task, Finding, DAG y demás tipos de dominio
  atomize/           — tasks.json <-> spec
  router/            — model_router.yaml
  budget/            — guardrails por proveedor
  state/             — backend (SQLite única fuente de verdad, ver F-12)
  engine/            — el loop de dispatch (equivalente a dispatcher.py)
  providers/         — adapters por CLI (claude/codex/opencode/agy…)
  prompt/            — prompt_builder.py equivalente
  worktree/          — aislamiento git por task
  vcs/               — github/gitlab
  dashboard/         — servidor HTTP (reemplaza FastAPI)
  publish/           — snapshot del stakeholder: export estático, watch, destino git/cloud
  mcp/               — servidor MCP stdio (tools orch_*) para agentes
  skills/            — instalación de skills (`orch install-skills`)
  templates/         — plantillas de proyecto
  doctor/            — `orch doctor` / `orch validate`
  notify/            — Slack/Discord webhooks
  tunnel/            — supervisor de túneles del dashboard
web/                 — SPA (movida desde frontend/), servida embebida en el binario
```

Regla de la migración: **no se añaden features nuevas en la versión Python.**
Los bugs que aparezcan mientras dure la migración se anotan en
`docs/brainstorm/go-migration-notes.md` en lugar de arreglarse con una
feature nueva o un refactor grande — fixes puntuales sí, features no.

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
- **Version**: v0.11.0 on `main` (PR #98, freeze of the Python line; `python-legacy` branch + `v0.11.0-py` tag). G0 hygiene: #91–#97.
- **Latest sprints**: H-7 wizard confirm gate (#80), H-6 `/orch` skill (#78), H-1a/b/c/d templates (#66/#67/#68/#77), H-3 brand (#65), H-4 README+HN (#66). Fixes fuera de serie: F-11 upgrade (#79), F-12 SQLite SoT (#75), F-13 bootstrap hygiene (#74/#76/#83), F-14 `agy` backend (#82).
- **Pending explicit**: H-1e `expo-mobile` template (last of the 5 canonical).
- **Prior sprints** (auto-memory has details): 7 budget guardrails · 8 packaging (v0.2.0, MIT, pipx) · 9 `orch init` · A runtime robustness · B SQLite backend · C observability subcommands · D `doctor`/`validate`/interactive `init` · E-1..E-8 dashboard iterations · F-1..F-6 clean foundation + PR automation · G-0..G-6 stakeholder UX · H-2 config consolidation · H-3..H-7 templates + brand + wizard.

## Gotchas already learned

- `fastapi<0.116` is a hard cap. Starlette 1.0 breaks the legacy `TemplateResponse` signature; pinning FastAPI keeps the compatible Starlette. The dashboard itself is the React SPA now (server-rendered Jinja templates are gone) — `jinja2` is no longer a runtime dependency.
- Project templates (`orchestrator/templates/projects/*/tasks.json.tmpl`) must use `Task.from_json`'s camelCase keys (`estimateHours`, `specRef`) — snake_case silently defaults to `0.0`/`""` instead of erroring (see `docs/brainstorm/go-migration-notes.md`).
- `orch dashboard` ships templates + `pricing.yaml` + `dashboard.yaml` + `static/` inside the wheel (see `pyproject.toml [tool.setuptools.package-data]`).
- Runtime YAML defaults (`config.yaml`, `model_router.yaml`, `budgets.yaml`) also ship in the wheel so `pipx`-installed `orch` works without a manual copy.
