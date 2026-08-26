# orch — Product Checklist

**Fecha**: 2026-08-26
**Estado**: Living document — actualizar con cada sprint
**Base**: vision-and-positioning.md + estado técnico v0.7.0

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
- [x] **Doctor / preflight / validate** — validación antes de correr. El usuario no corre a ciegas.
- [x] **CI/CD propio** — wheel + SPA embedded + release automático en GitHub. El propio proyecto es ejemplo del proceso.
- [x] **SPA sin dependencias heavy** — hand-written shadcn, SVG charts propios, 380kB / ~114kB gzipped. Disciplina que paga en 2 años.
- [x] **Dogfooding real** — usamos orch para desarrollar orch. Cada pain que sentimos lo resolvemos antes de que lo sienta un usuario.

---

## 🔶 Mejorar — existe, necesita más trabajo

Esto ya está en el producto pero la experiencia es incompleta o técnica.

- [ ] **Dashboard UX stakeholder** — hoy muestra task IDs, backend names, status técnico. El cliente necesita ver "¿cuándo está listo mi chatbot?" en lenguaje de negocio. La vista `/stakeholder/summary` es un esqueleto. (Labels configurables en F-3 ✅, milestone view ✅)
- [ ] **ETA automático del sprint** — `estimate_h` existe en `tasks_definition` pero no se proyecta a "ETA del sprint completo" en el dashboard. Es un cálculo simple: tareas pendientes × velocidad promedio.
- [ ] **Graph visual en el browser** — `orch graph` genera DOT/texto, pero el dashboard no lo renderiza. Un DAG visual es table stakes para que el stakeholder entienda qué depende de qué.
- [ ] **Observability en el dashboard** — los logs existen pero la vista en browser es básica. Sin filtros, sin live tail, sin búsqueda. El dev trabaja con logs crudos.
- [ ] **Error messaging para stakeholders** — errores como `ID_SPOOF`, `VERSION_DRIFT`, `PARSER` son útiles para el dev pero ilegibles para el cliente en el dashboard. Necesitan traducción a lenguaje humano.
- [x] **Retry policy configurable** — `retry.max_attempts` ahora configurable en `config.yaml`. Estrategias de retry (TRANSIENT, TIMEOUT, VERSION_DRIFT) siguen hardcoded en código — eso es intencional (conocimiento del sistema). (F-3 ✅)
- [ ] **orch init wizard** — funciona pero es lineal y genérico. No adapta el scaffolding al tipo de proyecto. Con templates (punto ➕ más abajo) este punto se resuelve solo.
- [x] **Onboarding** — `config.yaml` es ahora el único archivo obligatorio. `budgets.yaml`, `model_router.yaml`, `dashboard/dashboard.yaml` son overrides opcionales con deep-merge. Defaults inteligentes vía `_apply_defaults`. (F-3 ✅)

---

## 🔄 Cambiar — hay que rediseñar el approach

Cosas que están mal planteadas, no solo incompletas.

- [x] **Sprint model → Feature/Milestone model en el dashboard** — tabla `milestones` en SQLite, `MilestonesPage` en SPA, `orch task set --milestone`, `GET /api/milestones`. Stakeholders ven deliverables, no sprint codes. (F-3 ✅)
- [x] **Status labels** — `presentation.status_labels` configurable en `config.yaml` con defaults en español. Helper `labelForStatus()` en SPA. (F-3 ✅)
- [x] **Board tab con ExcaliDash** — eliminado. `BoardPage.tsx` borrado, tab reemplazado por Milestones. (F-3 ✅)
- [x] **README** — reescritura completa orientada a stakeholder/agency. Tagline nuevo, comparison table, quickstart de 3 pasos. Dev notes archivados en `docs/README-dev.md`. (F-3 ✅)
- [x] **Consolidar archivos de config** — `config.yaml` es el único obligatorio. `config_loader.py` con deep-merge para override files opcionales. (F-3 ✅)

---

## ➕ Agregar para ser TOP vs mercado

Dividido por horizonte de tiempo y zona de impacto.

### Stakeholder dashboard — Q1 (2-3 semanas) 🔥

Esto es lo que cierra la brecha vs el mercado. El diferenciador principal.

- [ ] **Timeline visual (Gantt-like)** — por milestone/feature, con ETA calculado desde `estimate_h` y velocidad actual. El cliente ve "¿cuándo entrega?" sin preguntar. Nativo en el dashboard, no un iframe externo.
- [ ] **Executive summary auto-generado** — la IA genera resumen en lenguaje de negocio: *"Esta semana: auth completado, API 60% avanzada, bloqueado esperando credenciales del cliente. ETA revisado +2 días."* Se muestra en el dashboard Y se puede reenviar por email.
- [ ] **PDF export del sprint/milestone** — el cliente lo lleva a reunión de board. Una página: features entregadas, spend, ETA, blockers.
- [ ] **Budget vs actual chart** — gráfico visual de gasto proyectado vs real, por proveedor y por feature. Transparencia financiera real.
- [ ] **"What's blocked" view** — vista rápida de tasks bloqueadas con su razón. El cliente puede actuar (dar acceso, aprobar algo) sin tener que preguntar.
- [ ] **Spend dashboard para el cliente** — "Este sprint costó $X en AI tokens" con desglose por proveedor y por feature. Si el cliente paga el AI spend, esto es ESENCIAL.
- [x] **Milestone tracking** — grupos de tasks con fecha objetivo. "Milestone: MVP Login — ETA 15/09 — 3/7 tasks done". Como Jira Epics pero sin el overhead. (F-3 ✅)

### CI/CD y calidad — Sprint F-2/F-3 🔥

Cierra el loop de calidad. Hoy orch marca `done` sin saber si el código funciona.

- [x] **Git worktrees por task** — cada agente trabaja en su branch aislada. Cero conflictos de archivos entre tasks paralelas. `dispatch.worktree_mode: true` en config. (F-2, PR #48)
- [ ] **PR automático por task** — cuando el agente termina, orch hace push + `gh pr create`. El dashboard muestra el link al PR.
- [ ] **CI polling en `tasks_runtime`** — orch espera resultado de GitHub Actions y lo registra en SQLite. El stakeholder ve "✅ 47 tests passed — Merged at 14:32".
- [ ] **`orch init` genera `.github/workflows/orch-ci.yml`** — configurable via `github.test_command: "pytest"`. El workflow corre automáticamente en cada PR de task.
- [ ] **auto_merge opt-in** — si CI pasa, merge automático. Config: `github.auto_merge: false` (off por default, requiere branch protection activa).

### Notificaciones y comunicación — Q1

- [ ] **Email digest semanal al stakeholder** — resumen automático generado desde `tasks_runtime`. Config: `stakeholder.email`, `digest.schedule: weekly`.
- [ ] **Slack/Discord webhook nativo** — notificación cuando una task termina, el sprint cierra, o algo se bloquea. Config: `notifications.slack_webhook`.
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
| Stakeholder dashboard | ❌ | ❌ | ❌ | ❌ | ✅ F-3 |
| Client-shareable URL | ❌ | ❌ | ❌ | ❌ | ✅ |
| DAG-based dependencies | ✅ | ✅ | ❌ | ❌ | ✅ |
| Spec → tasks pipeline | ❌ | ❌ | ❌ | ❌ | ✅ |
| Git worktree por task | ❌ | ❌ | ❌ | ❌ | ✅ F-2 |
| PR automático por task | ❌ | ❌ | ❌ | ✅ | 🔜 F-4 |
| CI auto-validación | ❌ | ❌ | ❌ | ✅ | 🔜 F-4 |
| Template system | ❌ | ❌ | ❌ | ❌ | 🔜 Q2 |
| Timeline / Gantt | ❌ | ❌ | ❌ | ❌ | 🔜 Q1 |
| PDF / email reports | ❌ | ❌ | ❌ | ❌ | 🔜 Q1 |
| Executive summary por IA | ❌ | ❌ | ❌ | ❌ | 🔜 Q1 |
| DAG visual interactivo | ❌ | ❌ | ❌ | ❌ | 🔜 Q3 |
| Browser tool use | ❌ | ❌ | ✅ | ✅ | ❌ |
| IDE built-in | ❌ | ❌ | ✅ | ✅ | ❌ (plugin) |
| Ecosystem / plugins | ✅✅ | ✅ | ✅ | ❌ | ❌ |

**Conclusión**: orch gana en el eje stakeholder — y ese eje está completamente vacío en el mercado. Pierde en autonomía de agente (browser tools, long-horizon planning). La apuesta: el eje stakeholder vale más para el ICP (freelancers, agencias) que la autonomía absoluta.

**El eje stakeholder está virgen. Nadie más lo está atacando. Ahí está la ventana.**

---

## 🗓️ Resumen de prioridades

```
✅ Completo
   F-1  ── SQLite como SSOT, AGENTS.md, orch task set
   F-2  ── Git worktrees por task (dispatch.worktree_mode)
   F-3  ── Config consolidation + milestones + status labels + README rewrite (PR #49)

Q1 (próximo)
   F-4  ── CI/CD: PR automático por task + CI polling en tasks_runtime
   F-5  ── Stakeholder polish: timeline/Gantt, exec summary, PDF export, budget chart

Q2 (mes 3-4)  ── Template system (5 templates canónicos)
               ── Multi-project dashboard + client auth por proyecto
               ── VS Code / Cursor extension
               ── Notificaciones (Slack, email digest)

Q3 (mes 5-6)  ── DAG visual interactivo
               ── Preview panels (Level 3 dashboard)
               ── Task type deploy + Docker/Coolify templates
               ── Community, Discord, v1.0 stable release
```

---

*Última actualización: 2026-08-26 — v0.7.0 post Sprint F-3 (config consolidation, milestones, status labels, README — PR #49)*
