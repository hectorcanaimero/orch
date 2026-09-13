# orch — Project instructions

Task orchestrator that walks a `tasks.json` DAG and dispatches each task to a
local AI CLI (`claude` | `codex` | `opencode`). Single-user, local, no daemon.

## Stack

- **Backend / CLI**: Python `>=3.11`, distributed as `orch` script (see `pyproject.toml`).
- **Deps runtime**: `pyyaml`, `rich`, `fastapi>=0.115,<0.116` (pinned — 0.116+ regresses closure-scoped `Request` annotation resolution), `uvicorn[standard]`. See `pyproject.toml [project.dependencies]` for the authoritative list.
- **Dev**: `pytest>=8.0`, `httpx>=0.27` (FastAPI TestClient uses it).
- **Frontend** (`web/`, Sprint E-3 SPA spike; moved from `frontend/` in G5.1): Vite + React + TypeScript + shadcn/ui + Tailwind, `pnpm` package manager, oxlint. `pnpm build`'s output goes straight to `internal/dashboard/dist/build/` (vite's `build.outDir`), not `web/dist/` — see the Go migration section below.
- **State backend**: SQLite by default since v0.11 (PR #91); `file` is legacy JSONL kept for `orch migrate`. See `orchestrator/state/`.
- **Persistence**: `state/` at runtime, never committed (`state/.gitkeep` only).

## Conventions

- **Tests**: `pytest` from repo root. Full suite is 1530 passed + 3 skipped. Two known time-boundary flakes pass in isolation on a fast run but can fail on slow I/O (they seed rows at `datetime('now','-N days')` and race the query cutoff): `test_sprint_metrics.py::test_count_done_last_n_days` and `test_tunnel_manager.py::test_start_writes_atomic_state_json`. A single failure in either on a slow run is NOT a regression. New work must not regress the green count. When you add tests, bump this number in the same commit so the baseline stays honest.
- **Never build after changes.** Type-check / test only.
- **Never use `cat` / `grep` / `find` / `sed` / `ls`.** Use `bat` / `rg` / `fd` / `sd` / `eza`. Install via `brew` if missing.
- **Commits**: conventional-commits format (`feat:` / `fix:` / `test:` / `docs:` / `chore:` / `refactor:`). **No `Co-Authored-By` or AI attribution.**
- **Branches**: sprint-scoped (e.g. `sprint-e3/spa-spike`). Merge to `main` via PR.
- **PRs are reviewed by Gemini in CI**; see `docs/CI-REVIEW.md`.
- **Backups**: `orch atomize --apply` writes `tasks.json.bak-<ts>` — leave those alone.
- **Docs live in `docs/`**: `MANUAL.{en,es,pt}.md`, dashboard/tunnel guides.

## Layout (top-level)

- `orchestrator/` — Python package. Subpackages: `dashboard/` (FastAPI app + tunnel manager), `state/` (file/SQLite backends, see `interface.py` + `adapters.py`), `vcs/` (GitHub/GitLab), `templates/`, `skills/`. Per-CLI dispatch adapters (`ClaudeBackend`, `CodexBackend`, `OpencodeBackend`) live in `orchestrator/dispatcher.py`, not a separate `providers/` package.
- `web/` — Vite SPA (E-3 spike; moved from `frontend/` in G5.1). `pnpm build` here writes straight to `internal/dashboard/dist/build/` (see the Go migration section), so `orch dashboard` (Python) now always serves it via `orchestrator/spa/` (the wheel-shipped copy `scripts/build-spa.sh` refreshes from that same build) rather than its `<project_root>/frontend/dist` project-specific-override tier — that tier is a generic per-managed-project convention, unrelated to this repo's own layout, and was deliberately left untouched.
- `docs/` — manuals + design docs.
- `scripts/` — repo helpers (not the per-project `task-*.sh` contract).
- `state/` — gitignored runtime state; only `.gitkeep` tracked.

## Migración a Go (en curso)

orch se está reescribiendo en Go (ver checklist en el artefacto "Orch en Go";
fases G0–G8). Layout objetivo — las rutas marcadas **(existe)** ya están en
el repo; el resto sigue planificado.

**Go 1.25.0** es el mínimo, y la directiva `go` de `go.mod` es la única
fuente: `.github/workflows/go.yml` la lee con `go-version-file: go.mod` en
vez de repetir el número. No es una preferencia — `modernc.org/sqlite` (y
`modernc.org/libc`, y `golang.org/x/sys`) declaran `go 1.25.0`, así que el
piso lo fija la dependencia, no nosotros. El artefacto dice "1.23+"; eso era
antes de elegir el driver de SQLite. `golangci-lint` tiene que estar
compilado con un Go **igual o más nuevo** que esa directiva o se niega a
correr, de ahí el v2.13.2 del workflow (el esquema de config de v2 es
distinto al de v1 — ver `.golangci.yml`).


```
cmd/orch/            — main package, arg parsing, entrypoint (existe)
internal/
  cli/               — subcomandos (existe: status, tasks, validate, graph, router, config, atomize, init, migrate, run, task-status…; docs/CLI.md es la lista viva)
  config/            — carga + merge de config.yaml / overrides (existe; docs/CONFIG.md)
  model/             — Task, Finding, DAG y demás tipos de dominio (existe; puede importar pyfmt, regla 11)
  graph/             — validate / cycles / orden / DOT sobre el DAG (#111)
  pyfmt/             — emulación del formato de Python (repr, separador de miles) (#124)
  atomize/           — tasks.json <-> spec (existe)
  router/            — model_router.yaml (existe)
  budget/            — guardrails por proveedor (existe)
  state/             — backend (SQLite única fuente de verdad, ver F-12) (existe)
  engine/            — el loop de dispatch: refill, reaper, scheduler, run loop, poller de CI (existe)
  project/           — lo que CLI y dashboard comparten sobre un proyecto: tasks.json hidratado con el estado, horas humanas, último cambio (llega con G5.2 b1)
  providers/         — adapters por CLI (existen los cinco: claude, codex, opencode, gemini, agy — G3.4; los caminos de éxito de codex/gemini/agy siguen con fixtures sintéticas porque esas CLIs están instaladas pero sin autenticar, ver testdata/README.md)
  prompt/            — prompt_builder.py equivalente (existe)
  worktree/          — aislamiento git por task (existe)
  vcs/               — github/gitlab (existe)
  dashboard/         — servidor HTTP (reemplaza FastAPI). (existe: spa.go embebe web/ — G5.1; servidor, modelo de acceso y endpoints — G5.2, en curso)
  publish/           — snapshot del stakeholder: export estático, watch, destino git/cloud
  mcp/               — servidor MCP stdio (tools orch_*) para agentes (existe: las siete tools sobre state.Backend, G6.4; docs/MCP.md)
  skills/            — instalación de skills (`orch install-skills`) (existe)
  templates/         — plantillas de proyecto (embebidas; #145)
  scaffold/          — `orch init`: batch, wizard, confirm gate (el paquete no se llama `init` porque ese nombre exige alias en cada import)
  doctor/            — `orch doctor` / `orch validate` (existe)
  notify/            — Slack/Discord webhooks
  tunnel/            — supervisor de túneles del dashboard: autossh (Pinggy) y bore, los dos que Python tiene; no hay cloudflared (G5.6, en curso)
web/                 — SPA (existe; movida desde frontend/ en G5.1),
                       embebida por internal/dashboard vía el
                       `build.outDir` de vite (no `web/dist/`)
```

**Go tree**: `make build` (bin/orch, versión desde `git describe`), `make test`
(`go test ./... -race -cover`), `make lint` (golangci-lint si está instalado,
si no `go vet` con aviso), `make web` (construye la SPA que internal/dashboard
embebe; `make build` depende de él), `make parity` (scripts/parity.sh: corre
init → atomize con los dos binarios y compara el árbol; goldens generados
ejecutando Python, nunca a mano). CI en `.github/workflows/go.yml` (jobs
`go-test` / `go-lint` / `parity`); no confundir con `ci-build.yml` (smoke
del wheel Python) ni `review.yml` (revisor Gemini).

Regla de la migración: **no se añaden features nuevas en la versión Python.**
Los bugs que aparezcan mientras dure la migración se anotan en
`docs/brainstorm/go-migration-notes/<carril>.md` (el fichero único quedó congelado el 2026-09-12) en lugar de arreglarse con una
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
- `shadcn`, `frontend-design`, `frontend-design-system`, `tailwind-design-system`, `typescript-best-practices` — only when touching `web/`.

## Current context

- **Branch**: `main` (per `git status`; check for drift — sprint branches like `sprint-e3/*` are historical).
- **Version**: v0.11.0 on `main` (PR #98, freeze of the Python line; `python-legacy` branch + `v0.11.0-py` tag). G0 hygiene: #91–#97.
- **Latest sprints**: H-7 wizard confirm gate (#80), H-6 `/orch` skill (#78), H-1a/b/c/d templates (#66/#67/#68/#77), H-3 brand (#65), H-4 README+HN (#66). Fixes fuera de serie: F-11 upgrade (#79), F-12 SQLite SoT (#75), F-13 bootstrap hygiene (#74/#76/#83), F-14 `agy` backend (#82).
- **Pending explicit**: H-1e `expo-mobile` template (last of the 5 canonical).
- **Prior sprints** (auto-memory has details): 7 budget guardrails · 8 packaging (v0.2.0, MIT, pipx) · 9 `orch init` · A runtime robustness · B SQLite backend · C observability subcommands · D `doctor`/`validate`/interactive `init` · E-1..E-8 dashboard iterations · F-1..F-6 clean foundation + PR automation · G-0..G-6 stakeholder UX · H-2 config consolidation · H-3..H-7 templates + brand + wizard.

## Gotchas already learned

- `go test ./...` fails locally in `internal/dashboard` until you run `make web` once: G5.1 embeds the built SPA and `TestSPARequiresABuild` says so on purpose. CI runs `make web` before `make test`, so it never sees this.
- `fastapi<0.116` is a hard cap. Starlette 1.0 breaks the legacy `TemplateResponse` signature; pinning FastAPI keeps the compatible Starlette. The dashboard itself is the React SPA now (server-rendered Jinja templates are gone) — `jinja2` is no longer a runtime dependency.
- Project templates (`orchestrator/templates/projects/*/tasks.json.tmpl`) must use `Task.from_json`'s camelCase keys (`estimateHours`, `specRef`) — snake_case silently defaults to `0.0`/`""` instead of erroring (see `docs/brainstorm/go-migration-notes.md`).
- `orch dashboard` ships templates + `pricing.yaml` + `dashboard.yaml` + `static/` inside the wheel (see `pyproject.toml [tool.setuptools.package-data]`).
- Runtime YAML defaults (`config.yaml`, `model_router.yaml`, `budgets.yaml`) also ship in the wheel so `pipx`-installed `orch` works without a manual copy.
- `web/`'s `pnpm build` does NOT emit `web/dist/` — its vite config points `build.outDir` straight at `internal/dashboard/dist/build/` so the Go binary's `//go:embed` (which can't reach outside its own package directory) has something to embed with no separate copy step. `internal/dashboard/dist/README.md` is the one file tracked directly under `dist/`; everything under `dist/build/` is gitignored and rebuilt from scratch every `pnpm build`. `scripts/build-spa.sh` (the Python wheel's SPA) reads from that same `dist/build/`, not `web/dist/` — one `pnpm build` feeds both binaries.
