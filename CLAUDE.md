# orch — Project instructions

Task orchestrator that walks a `tasks.json` DAG and dispatches each task to a
local AI CLI (`claude` | `codex` | `opencode` | `gemini` | `agy`). Single-user,
local, no daemon. One static Go binary with the React SPA embedded.

## Stack

- **CLI / engine / dashboard server**: Go. The `go` directive in `go.mod` is the
  minimum and the only place the version lives (see *Go toolchain* below).
- **Deps**: `spf13/cobra`, `gopkg.in/yaml.v3`, `modernc.org/sqlite` (pure Go, no
  cgo), `modelcontextprotocol/go-sdk`, `go-pdf/fpdf`, `rogpeppe/go-internal`
  (testscript). `go.mod` is the authoritative list; a new third-party dependency
  is a decision stated in its PR, never a silent `go get`.
- **Frontend** (`web/`): Vite + React + TypeScript + shadcn/ui + Tailwind, `pnpm`,
  oxlint, vitest. Two bundles, both embedded — see *Gotchas*.
- **State**: SQLite is the single source of truth for runtime status
  (`.orchestrator/state/<project>/orch.db`); `tasks.json` is static input.
  Migrations embedded in `internal/state/migrations/`.
- **Release**: goreleaser on `v*` tags (`.github/workflows/release-go.yml`,
  `docs/RELEASING.md`); `scripts/install.sh` and a Homebrew tap.

## The Python line is archived

orch was a Python package until the Go rewrite (phases G0–G8, checklist in the
"Orch en Go" artifact). Since G7.5 `main` holds no Python: the package, its
tests, the wheel workflows and the Go↔Python parity job live on the
**`python-legacy`** branch and its `-py` tags (`git tag -l '*-py'` for the final
one). What stays on `main` from that era is deliberate:

- **Frozen goldens** under `internal/**/testdata/` and `testdata/` were generated
  by running Python; each directory's README says so. Nothing regenerates them. A
  Go change that moves one is a divergence from the last Python release, edited
  by hand in the PR that says why.
- **`testdata/python-frozen/`** copies (migrations 001–005, the three packaged
  defaults, the project templates) keep the "same as the last Python release"
  guards alive. Never edit them; declare intended differences in
  `defaultsDivergedFromPython` / `divergedFromPython` / `goOnly`.
- **`internal/pyfmt`** and the many comments citing `orchestrator/*.py` or
  `scripts/parity.sh`: output shapes (`--json`, `validate` messages, float
  formatting) were fixed to match Python byte for byte, and that shape is still
  the contract with existing projects and scripts.
- **`.github/review/parse-review.py`** is the Gemini reviewer's response parser,
  run with the CI runner's system `python3`. It is reviewer tooling, not orch;
  porting it is its own task.
- **`scripts/ui-dom.py`** is a local helper for inspecting the dashboard in a
  headless browser (`docs/UI-CHECKS.md`), not part of the product or of CI.

Bugs found in the Python line are no longer fixed on `main`. The per-lane notes
under `docs/brainstorm/go-migration-notes/` are history.

## Conventions

- **Tests**: `make test` (`go test ./... -race -cover`). Green, except that
  without `make web` exactly two tests fail on purpose (`TestSPARequiresABuild`,
  `TestBundleRequiresABuild`), each naming the command. New work must not regress
  it. A new test is seen failing against the bug it is for before it is trusted
  (review checklist rule 24).
- **Lint**: `make lint` — golangci-lint v2 (falls back to `go vet` with a warning
  if it is not installed; CI runs the real one). 0 issues.
- **Never build after changes.** Test / lint only (`make build` is for releases
  and manual checks).
- **Never use `cat` / `grep` / `find` / `sed` / `ls`.** Use `bat` / `rg` / `fd` / `sd` / `eza`. Install via `brew` if missing.
- **Commits**: conventional-commits format (`feat:` / `fix:` / `test:` / `docs:` / `chore:` / `refactor:`). **No `Co-Authored-By` or AI attribution.**
- **Branches**: task-scoped (e.g. `g6.5/pipeline-skills`). Merge to `main` via PR.
- **PRs are reviewed by Gemini in CI**; see `docs/CI-REVIEW.md` and
  `.github/review/CHECKLIST.md`.
- **Docs follow commands**: a new command, flag or config key gets its row in
  `docs/CLI.md` / `docs/CONFIG.md` in the same PR.
- **Backups**: `orch atomize --apply` writes `tasks.json.bak-<ts>` — leave those alone.
- **Docs live in `docs/`**: `MANUAL.{en,es,pt}.md`, `CLI.md`, `CONFIG.md`, `MCP.md`, dashboard/tunnel guides.

## Layout (top-level)

- `cmd/orch/` — `main`: wires cobra, version, exit codes.
- `internal/` — every package (see the tree below). Nothing is public API.
- `web/` — the SPA. `pnpm build` / `pnpm build:stakeholder` write straight into
  the Go embed directories, not `web/dist/`.
- `testdata/` — cross-package fixtures (`parity-project/`, `file-project/`).
- `docs/` — manuals + design docs. `docs/brainstorm/`, `docs/history/` and
  `docs/superpowers/` are history.
- `scripts/` — `install.sh` (the curl installer) and `ui-dom.py`.
- `state/` — gitignored runtime state; only `.gitkeep` tracked.

## Go tree

**Go toolchain.** `go.mod`'s directive is the only source: `.github/workflows/go.yml`
reads it with `go-version-file: go.mod`. The floor is set by `modernc.org/sqlite`
(and `modernc.org/libc`, `golang.org/x/sys`), which declare `go 1.25.0`.
`golangci-lint` must be built with a Go **at least as new** as that directive or
it refuses to run — hence v2.13.2 in the workflow (v2's config schema differs from
v1's; see `.golangci.yml`).

```
cmd/orch/            — main package, arg parsing, entrypoint
internal/
  cli/               — subcommands (docs/CLI.md is the living list)
  config/            — config.yaml load + defaults + overrides (docs/CONFIG.md)
  model/             — Task, Route, Status, DAG types (may import pyfmt, rule 11)
  graph/             — validate / cycles / order / DOT / analytics over the DAG
  pyfmt/             — Python-compatible formatting (repr, thousands separator)
  atomize/           — spec markdown -> tasks.json
  router/            — model_router.yaml
  budget/            — per-provider guardrails
  pricing/           — pricing.yaml for spend estimates
  state/             — SQLite backend, the single source of truth
  engine/            — dispatch loop: refill, reaper, scheduler, run loop, CI poller
  project/           — what CLI and dashboard share: tasks.json hydrated with state
  providers/         — per-CLI adapters: claude, codex, opencode, gemini, agy (real captures in testdata/)
  prompt/            — dispatch prompt builder
  worktree/          — per-task git isolation
  vcs/               — github/gitlab via gh/glab
  dashboard/         — HTTP server, access model, endpoints, SSE; embeds web/ (spa.go)
  publish/           — stakeholder snapshot (snapshot/), the static site of `orch publish --to dir|git`, and `--to cloud` (cloud.go + credentials.go, ~/.orch/credentials; contract in docs/CLOUD.md, Worker in the orch-cloud repo); embeds the stakeholder bundle
  report/            — `orch report pdf`
  mcp/               — MCP stdio server (orch_* tools; docs/MCP.md)
  skills/            — embedded skills (orch + the planning pipeline orch-plan/prd/arch/spec/tasks, G6.5) + `orch install-skills`; internal/cli/skills_contract_test.go checks them against the command tree and the atomize parser
  templates/         — embedded project templates
  scaffold/          — `orch init`: batch, wizard, confirm gate (not `init`: that name needs an alias everywhere)
  doctor/            — `orch doctor` checks
  explain/           — `orch explain` / orch_context
  notify/            — Slack/Discord webhooks, digest
  tunnel/            — dashboard tunnel supervisor: autossh (Pinggy) and bore
  telemetry/         — opt-in anonymous telemetry (off by default)
web/                 — SPA, embedded by internal/dashboard and internal/publish
site/                — the public project page (static, no build), deployed to GitHub Pages by .github/workflows/pages.yml; site/architecture/ holds the five archify diagrams, whose sources and facts are in docs/architecture/ (update FACTS.md and re-render when the code they describe changes)
```

**Commands**: `make build` (bin/orch, version from `git describe`), `make test`,
`make lint`, `make web` (builds both bundles; `make build` depends on it). CI in
`.github/workflows/go.yml` (jobs `go-test`, `go-lint`, `goreleaser-dry-run`);
`review.yml` is the Gemini reviewer and the auto-merge policy.

## Things NOT to invoke unless the user asks

To keep context small, do not proactively call these MCP servers or skills on orch work — they are unrelated to a Go CLI + Vite SPA:

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

- **Branch**: `main` (per `git status`; check for drift).
- **Version**: the Go binary's first release is `v0.12.0` (check `git tag -l 'v*'`
  — it may not be tagged yet). The Python line ended on `python-legacy`.
- **Gate**: G7.3 — reproduce the GIF of `docs/media/GIF-SCRIPT.md` with the Go
  binary and real providers. Python was deleted from `main` only after it passed
  (ADR-G6).

## Gotchas already learned

- `go test ./...` fails locally in `internal/dashboard` and `internal/publish` until you run `make web` once: both packages embed a built bundle, and `TestSPARequiresABuild` / `TestBundleRequiresABuild` say so on purpose. CI runs `make web` before `make test`, so it never sees this.
- `web/`'s builds do NOT emit `web/dist/`: vite's `build.outDir` points straight at `internal/dashboard/dist/build/` (operator SPA) and `internal/publish/dist/stakeholder/` (stakeholder bundle), because `//go:embed` cannot reach outside its own package directory. Each `dist/` tracks only its `README.md`; the build output is gitignored and rebuilt from scratch.
- Project templates (`internal/templates/files/projects/*/tasks.json.tmpl`) must use camelCase keys (`estimateHours`, `specRef`) — snake_case silently defaults to `0`/`""` instead of erroring.
- Timestamps in one `orch.db` come in both `+00:00` and `Z` spellings (Python wrote both); never filter them with a string comparison in SQL — parse in Go.
- A field that moved from `tasks.json` to the database needs its reader moved too: `.Status` / `.Comments` read off a `model.Task` without `project.Hydrate` upstream is the recurring bug (review checklist rule 30).
