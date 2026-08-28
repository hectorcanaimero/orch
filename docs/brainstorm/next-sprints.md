# orch — Próximos Sprints

**Fecha**: 2026-08-26
**Estado**: Living document — actualizar al inicio de cada sprint
**Base**: product-checklist.md + estado técnico v0.7.0 (post F-5)

> Hoja de ruta táctica. Cada sprint es una unidad autónoma de valor — entregable, testeable, mergeable en un día. El orden responde a impacto en el eje stakeholder + deuda técnica que bloquea sprints posteriores.

---

## Estado actual (post Serie G — v0.8.0)

| Capa | Qué hay | Qué falta |
|------|---------|-----------|
| **Dispatch** | DAG, worktrees, multi-backend, budget guardrails, server-side commit_pending (F-6) | — |
| **CI/CD** | PR automático, CI polling, redispatch con logs, workflow auto-generado (`orch init`), `auto_merge` opt-in (G-1) | — |
| **Dashboard** | KPIs, sprint health, ETA, blockers, velocity, milestones (G-2/F-3), Gantt timeline (G-3), exec summary (G-4) | — |
| **Stakeholder UX** | Status labels, profiles, tunnel URL, budget/spend view (G-5) | PDF export, lenguaje de negocio |
| **Onboarding** | `orch init` (1 archivo obligatorio — H-2), doctor, validate, `orch config consolidate` | Wizard guiado + templates canónicos (H-1) |
| **Notificaciones** | Slack/Discord webhooks + `orch notify digest` (G-6) | — |

---

## Fixes fuera de serie

### F-6 — Worktree commit before push (bugfix crítico #60) ✅ (PR #61)

**Bug**: con `dispatch.worktree_mode: true` + `vcs.auto_pr: true`, el prompt no le pedía al agente `git add`/`git commit`. `WorktreeManager.push()` pusheaba un HEAD sin cambios y `remove(--force)` borraba el worktree — 35/35 tasks del reproducer terminaron `done` con cero código recuperable, gastando API tokens reales.

**Fix (server-side, no prompt)**: `WorktreeManager.commit_pending(task_id, message) → bool` — `git add -A`, `status --porcelain` para detectar clean tree, `commit` con identidad inline. Llamado desde `_reap_once` justo antes de `wm.push()`. Best-effort: fallo de commit loguea warning pero no bloquea el push.

**Fuera de scope**: semántica "empty diff → blocked" es feature, no fix. Queda para PR aparte si se pide.

---

## Serie G — Stakeholder UX completo (Q1)

### G-0 — CLI polish (`orch --version`)

**Objetivo**: ergonomía básica de CLI. Un orchestrator que se distribuye por `pipx` sin `--version` es fricción — el usuario no puede reportar qué build corre.

**Entregables**:
- [x] `orch --version` — imprime `orch <version>` desde `importlib.metadata.version("orchestrator")` y sale 0. Flag argparse nativo (`action="version"`), con fallback `0+unknown` si corre desde un source tree sin instalar.
- [x] Test: `test_version_flag.py` — `main(["--version"])` → `SystemExit(0)` + stdout coincide con la metadata instalada.

**Nota**: micro-item standalone, no pertenece temáticamente a ninguna feature de la Serie G. Se implementó como polish suelto, sin sprint TDD dedicado.

---

### G-1 — CI workflow auto-generation + auto_merge ✅ (PR #53)

**Objetivo**: cerrar el loop CI/CD que abrió F-4. Hoy orch crea el PR y espera CI — pero el workflow de CI tiene que existir previamente en el repo. Eso es fricción para el onboarding.

**Entregables**:
- [x] `orch init` detecta si existe `.github/workflows/orch-ci.yml`. Si no, lo genera desde un template configurable (soft target — nunca pisa uno existente).
- [x] Config nueva en `config.yaml`:
  ```yaml
  github:
    test_command: "pytest"      # comando que corre el workflow
    auto_merge: false           # off por default — requiere branch protection
  ```
- [x] `auto_merge: true` hace merge automático via `gh pr merge --squash --auto` (GitHub) / `glab mr merge --squash` (GitLab) cuando CI pasa. `merge_pr()` agregado al protocolo `VcsProvider`.
- [x] Tests: `test_vcs_github.py`, `test_vcs_gitlab.py`, `test_config_loader.py`, `test_init.py`, `test_ci_dispatch.py` — 15 tests nuevos, suite completa en 1208 passed + 3 skipped.

**Impacto**: un dev nuevo hace `orch init` en un repo de Python → tiene CI + PR automático + auto-merge sin tocar GitHub Actions manualmente.

---

### G-2 — Milestone tracking ✅ (entregado en F-3, PR #49)

**Objetivo**: agrupar tasks por deliverable visible al cliente. Hoy los sprints son técnicos; el cliente piensa en features/milestones.

**Entregables**:
- [x] Migración SQLite **004** (`milestones` table + `milestone_id` FK en `tasks_definition`; no fue 006 como se planeó).
- [x] `GET /api/milestones` — agrupa tasks por milestone, calcula progreso done/total.
- [x] Componente `MilestonesPage` en el SPA: cards de milestone con barra de progreso.
- [x] Nav item "Milestones" (reemplazó al Board tab).
- [x] `orch task set <id> --milestone <name>` para asignar desde CLI.
- [ ] Pendiente: ETA/fecha objetivo por milestone y tasks colapsables → llega con **G-3 (Gantt)**.

**Impacto**: el stakeholder ve "Milestone: Auth — 5/7 tasks" en lugar de una lista plana de 40 tasks técnicas. Falta la proyección temporal (Gantt).

---

### G-3 — Timeline visual (Gantt ligero) ✅

**Objetivo**: proyección temporal de milestones sobre un eje de tiempo. Sin librerías pesadas — SVG propio como los budget charts.

**Entregables**:
- [x] Helper puro `milestone_eta()` en `metrics.py` — ETA **tasks-based** (`remaining / velocity_per_day`), confianza high/low vs `target_date` (o regla de 30d).
- [x] `GET /api/milestones` engancha `eta {eta_date, eta_days, confidence}` por milestone, reusando la velocity de `sprint_health`.
- [x] Componente `GanttChart.tsx` en SVG hand-written: barras por milestone, overlay de progreso, marcador "hoy", tick de ETA con color por confianza (verde high / ámbar low).
- [x] Integrado en `MilestonesPage` como sección colapsable (`<details>`) + badge de ETA por card.
- [x] Exportable como SVG (botón "Download", `XMLSerializer` — cero deps).
- [ ] Refinamiento futuro: ETA **hours-based** (`estimate_h` por milestone) para más precisión.

**Impacto**: el cliente ve un Gantt real en el dashboard sin que el dev haya tocado nada. Cierra el gap vs Jira/Linear en el eje "¿cuándo entrega?".

---

### G-4 — Executive summary (template determinista) ✅

**Objetivo**: resumen en lenguaje de negocio, listo para copiar/pegar al cliente.

**Decisión de diseño clave**: NO es una llamada a LLM en vivo (como floteaba el roadmap original). Es un **template determinista** llenado desde `sprint_health` + spend. ¿Por qué? El dashboard puede correr headless sin el CLI `claude` instalado — una llamada a LLM ahí se rompería. Toda la data que un LLM parafrasearía YA está estructurada. Determinista ⇒ testeable, gratis, instantáneo, cero dependencia de host.

**Descubrimiento**: el Sprint E-7 YA tenía un exec summary determinista inline (sin tests, inglés fijo, sin copiar). G-4 **consolidó** en vez de duplicar:

**Entregables**:
- [x] Helper puro `executive_summary()` en `metrics.py` (12 tests) — pct, in-progress, blocked + reasons, ETA (date/hours), spend, idioma es/en.
- [x] `GET /api/summary` nuevo + el payload stakeholder (E-7) ahora usan el MISMO helper (single source of truth). La lógica inline del E-7 se plegó adentro.
- [x] Config `dashboard.summary_language: es` (default) — es | en.
- [x] Botón "Copiar" (Clipboard API, cero deps) en el card `<ExecSummary>` que ya existía.
- [ ] Fuera de alcance: llamada LLM en vivo, entrega por email/Slack (eso es G-6).

**Nota de honestidad**: la tabla comparativa decía "Executive summary por IA" — se corrigió a "(auto)" porque NO llamamos a un LLM.

**Impacto**: el dev deja de escribir el "update semanal" al cliente. Lo genera orch.

---

### G-5 — Budget vs actual chart + spend dashboard stakeholder ✅

**Objetivo**: transparencia financiera real. "Este sprint costó $X en AI tokens" con desglose por proveedor.

**Decisión de diseño clave**: el budget config mide en **tokens** (`token_budget`), no en dólares. La barra compara `tokens_used / token_budget` (la unidad que el guardrail realmente enforcea); el `cost_usd` se muestra AL LADO como cifra informativa. No inventamos un límite en USD que la config no tiene.

**Entregables**:
- [x] Helper puro `budget_vs_actual()` en `metrics.py` — cruza límite configurado vs tokens usados (en ventana) + USD, por proveedor.
- [x] `GET /api/budget/summary` — reusa `BudgetGate.snapshot()` (single source of truth con el gate de dispatch) + `spend_reader` para el USD.
- [x] `BudgetChart.tsx` SVG hand-written: barra por proveedor (track=budget, fill=used), rule en threshold, color ámbar al pasarlo, label pct% + ~$cost.
- [x] `BudgetPage.tsx` + nav item + ruta `/budget`.
- [x] Config `dashboard.show_spend_to_stakeholder: false` (off por default). Operator siempre lo ve; stakeholder solo con el flag on.
- [ ] Fuera de alcance (futuro): spend por milestone, proyección de gasto futuro.

**Impacto**: si el cliente paga el AI spend, esto es ESENCIAL. Cierra el ciclo de transparencia financiera que abrió el budget guardrail.

---

### G-6 — Notificaciones (Slack/Discord + `orch notify digest`) ✅ (PR #62)

**Objetivo**: orch notifica sin que el dev tenga que estar mirando el dashboard.

**Decisión de diseño clave**: scope acotado deliberadamente. NO se implementó (a) sprint-done detection — requiere estado shared en el main loop y merece PR aparte; (b) SMTP nativo — `orch notify digest` escribe a stdout, el operador cronjobea `orch notify digest --send | mail -s ...`; (c) scheduling interno — orch no tiene daemon.

**Entregables**:
- [x] Config nueva en `config.yaml`:
  ```yaml
  notifications:
    slack_webhook: ""
    discord_webhook: ""
    timeout_s: 5
  ```
- [x] Módulo `orchestrator/notifications.py` — `Notifier` con `urllib.request` (stdlib, cero deps), silent-fail: webhook roto NUNCA tumba el dispatch loop.
- [x] Hooks live en 2 sitios: `_reap_once` post-block y `_check_ci_once` post-ci_blocked.
- [x] Subcomando `orch notify test` — probe de webhook (exit 1 si nadie acepta).
- [x] Subcomando `orch notify digest [--send] [--language es|en]` — imprime resumen determinista (reusa `executive_summary` + tabla de milestones con ETA) para cron + mail/sendmail.
- [x] Tests: `test_notifications.py` (17), `test_notify_cmd.py` (4), `test_ci_dispatch.py` (+2), `test_config_loader.py` (+2).

**Impacto**: el cliente recibe update semanal via cron sin fricción del dev.

---

## Serie H — Onboarding y distribución (Q2)

### H-1 — `orch init` wizard guiado por CLI + template system

**Decisión**: el wizard se queda en CLI (no browser). El problema hoy es que el flujo es lineal y genérico — el dev no sabe qué contestar, se pierde entre archivos. La solución es hacerlo guiado e inteligente, no visual.

**Entregables del wizard**:
- Pregunta el tipo de proyecto → sugiere el template más adecuado con descripción de una línea.
- Muestra un resumen de lo que va a crear (archivos, config keys, dependencias) antes de escribir nada. El dev confirma o ajusta.
- Detecta si el proyecto ya tiene `config.yaml`, `tasks.json`, `pyproject.toml` y adapta las preguntas (no te pregunta el nombre del proyecto si ya está en pyproject.toml).
- Al final imprime un checklist de "próximos pasos" en color: qué archivos editaste, cómo correr el primer dispatch, cómo abrir el dashboard.
- `orch init --template <name>` para saltear el wizard y ir directo al template.

**Templates canónicos (5 primeros)**:
- `python-api` — FastAPI + pytest + ruff + `orch-ci.yml`
- `nextjs-saas` — Next.js 14 + Clerk + Supabase + Vercel deploy
- `chatbot-whatsapp` — Waha + agentes Claude + webhook config
- `expo-mobile` — Expo SDK + Supabase + EAS build
- `data-pipeline` — Python ETL + DuckDB + scheduled tasks

Cada template incluye: `tasks.json` de ejemplo, `config.yaml` preconfigurado, `AGENTS.md` con contexto del stack, `.github/workflows/orch-ci.yml` listo.

### H-2 — Consolidación de configs (1 archivo obligatorio) ✅ (PR #63, opción B)

**Decisión de diseño clave**: consolidación **selectiva**, no literal. Un merge total (budgets 76 líneas + model_router 286 líneas + dashboard 86 líneas → config.yaml) infla el archivo a ~500 líneas y fight the intent. Solo se consolidó lo que realmente ayuda:

- [x] `dashboard.yaml` → sección `dashboard:` inline en `config.yaml` shipped. Backwards compat: override en project-root sigue funcionando via deep-merge.
- [x] `orch init` deja de scaffoldear `dashboard.yaml` y `budgets.yaml`. Proyecto nuevo escribe **1 archivo** (config.yaml) en vez de 4.
- [x] `budgets.yaml` era ya opcional (`load_budget_config` retorna None si falta) — solo se paró de crear copia redundante.
- [x] `model_router.yaml` se genera como stub vacío. `orch router add-missing` (issue #55) lo puebla cuando el dev agrega tasks con modelos nuevos.
- [x] Nuevo subcomando `orch config consolidate [--dry-run]` — migra proyectos existentes: deep-merge `dashboard.yaml` en `config.yaml`, backup `.bak-<ts>`.
- [ ] Fuera de alcance (deliberado): consolidar `budgets.yaml` o `model_router.yaml` — son data (presets, mapping tables), no config narrativo. Separados son más legibles.

### H-3 — Brand integration

Assets finales en `logos/export/` (combination mark + icon cuadrado, SVG + PNG en 7 tamaños).

- Reemplazar `frontend/public/favicon.svg` con `logos/export/icon.svg`.
- Copiar `logos/export/icon-192.png` y `icon-512.png` a `frontend/public/` para PWA manifest.
- Actualizar `frontend/public/manifest.json` con los nuevos iconos.
- Usar `logos/export/logo.svg` como hero image del README.
- Actualizar `orchestrator/spa/` con el nuevo favicon (rebuild de la SPA).

### H-4 — README reescritura + HN launch prep

- Nuevo tagline: *"El primer orchestrator con dashboard para stakeholders. Mandá este link a tu cliente."*
- Hero image: `logos/export/logo.svg` + screenshot del stakeholder view con ETA + blockers.
- GIF de 30s: `orch init` → dispatch → PR creado → dashboard stakeholder con ETA.
- Comparison table honesta (ya existe en product-checklist).
- Sección "Quick start" en 3 comandos.
- Target: HN "Show HN" post cuando H-1, H-2 y H-3 estén listos.

---

## Serie I — Dashboard avanzado (Q3)

| Sprint | Feature | Complejidad |
|--------|---------|-------------|
| I-1 | DAG visual interactivo (editable en browser) | Alta |
| I-2 | Preview panels — file tree + syntax highlight read-only | Media |
| I-3 | PDF export del sprint/milestone | Media |
| I-4 | White-label (logo + colores por proyecto) | Media |
| I-5 | Multi-project dashboard (`orch dashboard --portfolio`) | Alta |
| I-6 | VS Code / Cursor extension (sidebar) | Alta |

---

## Criterios de priorización

1. **Impacto en stakeholder** — ¿el cliente percibe el valor sin que el dev explique nada?
2. **Cierra un loop abierto** — ¿completa algo que está a medias? (G-1 cierra F-4)
3. **Deuda que bloquea** — ¿otros sprints dependen de esto? (milestones bloquea Gantt, Gantt bloquea PDF)
4. **Complejidad de implementación** — un sprint = un día de trabajo autónomo

---

## Tabla de dependencias

```
F-6 (worktree commit fix)  ──── standalone ✅ done (PR #61) — bloqueaba TODO el eje worktree/PR/CI
G-1 (CI workflow)          ──── standalone ✅ done (PR #53)
G-2 (milestones)           ──── standalone ✅ done (F-3, PR #49)
G-3 (Gantt)                ──── depende de G-2 ✅ done
G-4 (exec summary)         ──── depende de G-2 ✅ done (PR #59, consolidó E-7)
G-5 (budget chart)         ──── standalone ✅ done (PR #58)
G-6 (notificaciones)       ──── ✅ done (PR #62) — reusa `executive_summary` (G-4) para digest

H-1 (wizard + templates)   ──── depende de G-1 (CI workflow en template) + H-2 ✅
H-2 (config consolidation) ──── standalone ✅ done (PR #63, opción B)
H-3 (brand integration)    ──── standalone
H-4 (README + HN)          ──── depende de H-1 + H-3 (screenshots + brand)
```

---

*Documento creado: 2026-08-26 post Sprint F-5. Actualizado: 2026-08-28 — Serie G completa (G-0 PR #54, G-1 PR #53, G-2 en F-3 #49, G-3 Gantt, G-4 PR #59, G-5 PR #58, G-6 PR #62) + F-6 bugfix crítico #60 (PR #61) + H-2 consolidación config opción B (PR #63). v0.8.0 en `main`. Próximo sprint: H-1 (wizard + 5 templates canónicos). Próxima revisión: al completar H-1.*
