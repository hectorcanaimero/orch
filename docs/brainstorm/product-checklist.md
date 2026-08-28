# orch — Product Checklist

**Fecha**: 2026-08-26
**Estado**: Living document — actualizar con cada sprint
**Base**: vision-and-positioning.md + estado técnico v0.7.0 (post F-5)

> Cuatro zonas: lo que ya es top, lo que necesita pulido, lo que hay que cambiar, y lo que falta para ganarle al mercado — todo pensado desde el eje **stakeholder**.

---

## ✅ Lo que ya hacemos TOP — mantener y pulir

Estas son las ventajas reales. Ningún competidor las tiene juntas.

- [x] **Multi-backend dispatch** — Claude, Codex, OpenCode, Gemini en un solo DAG. Abstracción limpia via Protocol. Nadie más hace esto.
- [x] **Budget guardrails por proveedor** — control de gasto real con límites configurables. Combinado con el dashboard es transparencia total al cliente ("gastamos $12 esta semana en AI, el presupuesto es $50").
- [x] **SQLite como single source of truth** (F-1) — sin split-brain, sin `tasks_json_precedence`. La arquitectura está sana.
- [x] **orch atomize pipeline** — spec markdown → tasks.json → SQLite. El trabajo se DEFINE antes de ejecutarse. Único en el mercado.
- [x] **Perfiles operator/stakeholder/both** — el dev ve todo, el cliente ve lo curated. Separación de audiencias limpia.
- [x] **Tunnel manager** — URL pública temporal via Pinggy sin infra adicional. El caso de uso "mandale este link al cliente" funciona hoy.
- [x] **Defense in depth en auth** — TokenAuth + ProfileGuard + whitelist + loopback gate. Cada capa falla loud. Seguro por diseño.
- [x] **Findings loop** — agentes reportan hallazgos a GitHub. Dogfooding auténtico, no marketing.
- [x] **AGENTS.md auto-generation** (F-1) — zero-cost context para agentes. Claude Code lo lee automáticamente al iniciar sesión.
- [x] **orch task set** (F-1) — control manual de model/status/backend sin editar JSON ni hacer dispatch. Cirugía fina.
- [x] **`orch --version`** (G-0, PR #54) — imprime `orch <version>` desde `importlib.metadata`. Ergonomía básica de CLI para reportar qué build corre un usuario de `pipx`.
- [x] **Doctor / preflight / validate** — validación antes de correr. El usuario no corre a ciegas.
- [x] **CI/CD propio** — wheel + SPA embedded + release automático en GitHub. El propio proyecto es ejemplo del proceso.
- [x] **SPA sin dependencias heavy** — hand-written shadcn, SVG charts propios, 380kB / ~114kB gzipped. Disciplina que paga en 2 años.
- [x] **Dogfooding real** — usamos orch para desarrollar orch. Cada pain que sentimos lo resolvemos antes de que lo sienta un usuario.

---

## 🔶 Mejorar — existe, necesita más trabajo

Esto ya está en el producto pero la experiencia es incompleta o técnica.

- [ ] **Dashboard UX stakeholder** — hoy muestra task IDs, backend names, status técnico. El cliente necesita ver "¿cuándo está listo mi chatbot?" en lenguaje de negocio. La vista `/stakeholder/summary` es un esqueleto.
- [x] **ETA automático del sprint** (F-5, PR #52) — velocidad rolling 7 días, proyección ETA con badge alta/baja confianza, remaining hours. Sprint health endpoint en `/api/sprint`.
- [ ] **Graph visual en el browser** — `orch graph` genera DOT/texto, pero el dashboard no lo renderiza. Un DAG visual es table stakes para que el stakeholder entienda qué depende de qué.
- [ ] **Observability en el dashboard** — los logs existen pero la vista en browser es básica. Sin filtros, sin live tail, sin búsqueda. El dev trabaja con logs crudos.
- [ ] **Error messaging para stakeholders** — errores como `ID_SPOOF`, `VERSION_DRIFT`, `PARSER` son útiles para el dev pero ilegibles para el cliente en el dashboard. Necesitan traducción a lenguaje humano.
- [ ] **Retry policy configurable** — las estrategias de retry (TRANSIENT, TIMEOUT, VERSION_DRIFT) son hardcoded. Un policy declarativo en config.yaml daría control real.
- [ ] **orch init wizard guiado** — sigue siendo CLI (no browser). El problema es que el flujo es lineal y el dev se pierde. Fix: pregunta el tipo de proyecto, sugiere template, muestra resumen de lo que va a crear antes de escribir, imprime checklist de próximos pasos al final. Se implementa en H-1 junto con el template system.
- [ ] **Onboarding** — 5 archivos de config (config.yaml, budgets.yaml, model_router.yaml, dashboard.yaml, tasks.json). Curva empinada. Los templates van a aliviar esto, pero hay que pensar también en defaults inteligentes.

---

## 🔄 Cambiar — hay que rediseñar el approach

Cosas que están mal planteadas, no solo incompletas.

- [x] **Sprint model → Feature/Milestone model en el dashboard** (F-3, PR #49) — `MilestonesPage` en el SPA agrupa tasks por deliverable con barra de progreso. El sprint sigue para el dev; el cliente ve milestones. Base entregada; falta el Gantt (G-3) y ETA por milestone.
- [x] **Status labels** (F-3, PR #49) — capa de presentación con `presentation.status_labels` configurable en `config.yaml` + helper `labelForStatus()` en el SPA. "Planificado / En progreso / Entregado".
- [x] **Board tab con ExcaliDash** (F-3, PR #49) — removido. `BoardPage` ya no existe; reemplazado por `MilestonesPage` en el nav.
- [x] **README** (F-3, PR #49) — reescrito para audiencia stakeholder/agencia: nuevo tagline ("Show clients a live dashboard — not a Slack thread"), comparison table, quickstart en 3 comandos. Falta hero image + GIF (H-3/H-4, pre-HN).
- [x] **Consolidar archivos de config** (H-2, PR #63, opción B) — `orch init` scaffoldea 1 archivo (`config.yaml`) en vez de 4. `dashboard:` inline, `budgets.yaml` ya era opcional, `model_router.yaml` stub que `orch router add-missing` puebla on-demand. `orch config consolidate` migra proyectos existentes. Fuera de scope deliberado: consolidar budgets/router (son data grande, no config narrativo).

---

## ➕ Agregar para ser TOP vs mercado

Dividido por horizonte de tiempo y zona de impacto.

### Stakeholder dashboard — Q1 (2-3 semanas) 🔥

Esto es lo que cierra la brecha vs el mercado. El diferenciador principal.

- [x] **Timeline visual (Gantt-like)** (G-3) — `GanttChart.tsx` en SVG hand-written dentro de `MilestonesPage`, con ETA tasks-based (`remaining / velocity`) por milestone, marcador "hoy", color por confianza y export SVG. El cliente ve "¿cuándo entrega?" sin preguntar. Refinamiento futuro: ETA hours-based desde `estimate_h`.
- [x] **Executive summary auto-generado** (G-4) — resumen determinista en lenguaje de negocio (template, NO LLM) desde `sprint_health` + spend: *"Proyecto 62% completo — 5 de 8 tareas entregadas. 1 bloqueada. ETA estimado: 3 sep. Gastado en AI: $12."* Helper `executive_summary()` testeado, idioma es/en, botón copiar. Consolidó la lógica inline del E-7. Falta reenvío por email (G-6).
- [ ] **PDF export del sprint/milestone** — el cliente lo lleva a reunión de board. Una página: features entregadas, spend, ETA, blockers.
- [x] **Budget vs actual chart** (G-5) — `BudgetChart.tsx` SVG: tokens usados vs `token_budget` por proveedor (la unidad del guardrail), con USD real como cifra informativa. `GET /api/budget/summary` reusa `BudgetGate.snapshot()`. Falta desglose por feature/milestone.
- [x] **"What's blocked" view** (F-5, PR #52) — grid de tasks bloqueadas con razón de bloqueo, fase y fecha. Integrado en `/sprint` del dashboard, visible a todos los perfiles.
- [x] **Spend dashboard para el cliente** (G-5) — `BudgetPage` con spend real por proveedor + USD del día, gated por `dashboard.show_spend_to_stakeholder` (off por default). Falta el desglose por feature/milestone.
- [x] **Milestone tracking** (F-3, PR #49) — migración 004 (`milestones` table + `milestone_id` FK), `GET /api/milestones` con progreso done/total, `orch task set --milestone`, `MilestonesPage` en el SPA. Falta la fecha objetivo/ETA por milestone (llega con G-3 Gantt).

### CI/CD y calidad — Sprint F-2/F-3 🔥

Cierra el loop de calidad. Hoy orch marca `done` sin saber si el código funciona.

- [x] **Git worktrees por task** — cada agente trabaja en su branch aislada. Cero conflictos de archivos entre tasks paralelas. `dispatch.worktree_mode: true` en config. (F-2, PR #48)
- [x] **PR automático por task** — cuando el agente termina, orch hace push + `gh pr create`. El dashboard muestra el link al PR. `vcs.auto_pr: true` en config. (F-4, PR #51)
- [x] **CI polling en `tasks_runtime`** — orch espera resultado de GitHub Actions/GitLab CI, registra en SQLite, y re-despacha el agente con los logs de error si falla (hasta `ci_max_retries`). (F-4, PR #51)
- [x] **`orch init` genera `.github/workflows/orch-ci.yml`** — configurable via `github.test_command: "pytest"`. Soft target: nunca pisa un workflow existente. (G-1, PR #53)
- [x] **auto_merge opt-in** — si CI pasa, merge automático (`gh pr merge --squash --auto` / `glab mr merge --squash`). Config: `github.auto_merge: false` (off por default, requiere branch protection activa). (G-1, PR #53)

### Notificaciones y comunicación — Q1

- [x] **Slack/Discord webhook nativo** (G-6, PR #62) — `Notifier` con stdlib `urllib` (cero deps), silent-fail. Hooks live en `_reap_once` (task blocked) y `_check_ci_once` (ci_blocked). Config: `notifications.slack_webhook` / `.discord_webhook`. Subcomando `orch notify test` para validar setup.
- [x] **Email digest — vía CLI + cron** (G-6, PR #62) — `orch notify digest [--send] [--language es|en]` imprime resumen determinista (reusa `executive_summary` + tabla de milestones con ETA). El operador cronjobea `orch notify digest --send | mail -s ...`. NO SMTP nativo (evita nueva dep + credenciales).
- [ ] **Sprint-done detection** — notificar cuando la queue queda vacía. Requiere estado shared en main loop; fuera de scope de G-6.
- [ ] **Progress changelog automático** — "En las últimas 24h: 3 features completadas, 2 en progreso". Generado desde events en `tasks_runtime`. Visible en el dashboard y enviable por email.

### Templates y onboarding — Q2

- [ ] **`orch init --template <name>`** — scaffolding completo con `dashboard.yaml` preconfigurado, token generado, tunnel config incluida, AGENTS.md. El README del template le dice al dev "cuando termines, mandá este URL al cliente".
- [ ] **5 templates canónicos** (del brainstorm):
  - `chatbot-whatsapp` — Waha + agentes
  - `landing-page-nextjs`
  - `saas-b2b-clerk-supabase`
  - `mobile-expo-supabase`
  - `data-pipeline`
- [ ] **Multi-project dashboard** — `orch dashboard --portfolio`. Vista de todos los proyectos activos para agencias con múltiples clientes.
- [ ] **Client auth por proyecto** — hoy el token es global al dashboard. Cada cliente debería tener su token que le da acceso solo a SU proyecto.
- [ ] **Registry local de templates** — `~/.orch/templates/`. Submit de nuevos templates via PR al repo `orch-templates`.

### Integraciones — Q2-Q3

- [ ] **VS Code / Cursor extension** — sidebar con el dashboard, command palette para `orch run <task>`, deep-link al stakeholder URL. El brainstorm lo identifica como el "sweet spot post-HN launch" para el dev.
- [ ] **Task type `deploy`** — tasks con `type: deploy` se despachan a un script (Coolify API, kubectl) en lugar de a un agente IA. El DAG puede incluir "deploy a staging cuando features X e Y estén listas".
- [ ] **`orch init --template webapp-with-staging`** — genera `docker-compose.yml`, `.github/workflows/deploy-staging.yml`, script de `pg_dump prod | pg_restore staging`. Integra con Coolify/Portainer sin meter infra dentro de orch.

### Dashboard web avanzado — Q3+

- [ ] **DAG visual interactivo** — el grafo de dependencias editable en el browser. Hoy es DOT/texto; el stakeholder necesita ver el flujo de trabajo visualmente.
- [ ] **Preview panels (Level 3 IDE)** — syntax highlight read-only del código generado, file tree del proyecto, iframe preview de la app. Sin edición. El stakeholder "ve" el progreso en tiempo real.
- [ ] **White-label** — agencias ponen su logo y colores en el dashboard. Config: `dashboard.branding: {logo, primary_color, company_name}`.
- [ ] **AI-generated sprint proposal** — `orch understand` analiza el repo y propone un sprint: "Tu codebase tiene estas gaps, sugeridas estas 12 tasks". El paso previo a `orch atomize`.

---

## ⚔️ Versus competidores — tabla honesta

| Feature | LangChain | CrewAI | Cursor | Devin | **orch** |
|---------|:---------:|:------:|:------:|:-----:|:--------:|
| Multi-backend en un DAG | ❌ | ❌ | ❌ | ❌ | ✅ |
| Budget guardrails | ❌ | ❌ | ❌ | ❌ | ✅ |
| Stakeholder dashboard | ❌ | ❌ | ❌ | ❌ | ✅ |
| Client-shareable URL | ❌ | ❌ | ❌ | ❌ | ✅ |
| DAG-based dependencies | ✅ | ✅ | ❌ | ❌ | ✅ |
| Spec → tasks pipeline | ❌ | ❌ | ❌ | ❌ | ✅ |
| Git worktree por task | ❌ | ❌ | ❌ | ❌ | ✅ F-2 |
| PR automático por task | ❌ | ❌ | ❌ | ✅ | ✅ F-4 |
| CI auto-validación | ❌ | ❌ | ❌ | ✅ | ✅ F-4 |
| Sprint velocity / ETA | ❌ | ❌ | ❌ | ❌ | ✅ F-5 |
| Blockers dashboard | ❌ | ❌ | ❌ | ❌ | ✅ F-5 |
| CI workflow auto-gen + auto-merge | ❌ | ❌ | ❌ | ✅ | ✅ G-1 |
| Slack/Discord webhooks | ❌ | ❌ | ❌ | ❌ | ✅ G-6 |
| Config: 1 archivo obligatorio | ❌ | ❌ | ❌ | ❌ | ✅ H-2 |
| Template system | ❌ | ❌ | ❌ | ❌ | 🔜 H-1 |
| Timeline / Gantt | ❌ | ❌ | ❌ | ❌ | ✅ G-3 |
| PDF / email reports | ❌ | ❌ | ❌ | ❌ | 🔜 Q2 |
| Executive summary (auto) | ❌ | ❌ | ❌ | ❌ | ✅ G-4 |
| DAG visual interactivo | ❌ | ❌ | ❌ | ❌ | 🔜 Q3 |
| Browser tool use | ❌ | ❌ | ✅ | ✅ | ❌ |
| IDE built-in | ❌ | ❌ | ✅ | ✅ | ❌ (plugin) |
| Ecosystem / plugins | ✅✅ | ✅ | ✅ | ❌ | ❌ |

**Conclusión**: orch gana en el eje stakeholder — y ese eje está completamente vacío en el mercado. Pierde en autonomía de agente (browser tools, long-horizon planning). La apuesta: el eje stakeholder vale más para el ICP (freelancers, agencias) que la autonomía absoluta.

**El eje stakeholder está virgen. Nadie más lo está atacando. Ahí está la ventana.**

---

## 🗓️ Resumen de prioridades

```
Q1 (mes 1-2)  ── Stakeholder polish: ✅ timeline/Gantt (G-3), ✅ exec summary (G-4), PDF, ✅ budget chart (G-5), ✅ blockers view (F-5)
               ── CI/CD: ✅ worktrees (F-2) + ✅ PR automático + ✅ CI polling (F-4) + ✅ CI workflow auto-gen/auto-merge (G-1) + ✅ commit_pending fix (F-6, bugfix crítico #60)
               ── ✅ Notificaciones: Slack/Discord + `orch notify digest` (G-6)
               ── ✅ Config consolidation (H-2, opción B)
               ── README hero image + GIF + HN launch prep

Q2 (mes 3-4)  ── Wizard guiado + 5 templates canónicos (H-1)
               ── Brand integration (H-3) + README/HN prep (H-4)
               ── Multi-project dashboard + client auth por proyecto
               ── VS Code / Cursor extension

Q3 (mes 5-6)  ── DAG visual interactivo
               ── Preview panels (Level 3 dashboard)
               ── Task type deploy + Docker/Coolify templates
               ── Community, Discord, v1.0 stable release
```

---

*Última actualización: 2026-08-28 — v0.8.0. Serie G completa (G-0..G-6). F-6 bugfix crítico #60 (worktree commit before push, PR #61) + G-6 notificaciones Slack/Discord + `orch notify` (PR #62) + H-2 consolidación config opción B (PR #63). Serie previa: G-0 `orch --version` (#54), G-1 CI workflow auto-gen + auto_merge (#53), G-3 Gantt timeline, G-4 exec summary determinista (#59, consolidó E-7), G-5 budget vs actual (#58), F-2 worktree dispatch (#48), F-3 config + milestones + status labels + README (#49), F-4 PR automation + CI polling (#51), F-5 sprint health (#52). Próximo: H-1 (wizard + 5 templates).*
