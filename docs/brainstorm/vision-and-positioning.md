# Brainstorm — orch vision & positioning

**Fecha inicial**: 2026-08-22
**Estado**: DRAFT — abierto a más insights
**Objetivo**: definir qué es orch, para quién, y hacia dónde va

Este documento captura una conversación de brainstorm sobre el posicionamiento y visión de orch. **No es una decisión cerrada** — es material para discutir con más calma en próximas sesiones.

---

## Contexto — por qué este brainstorm

Después de una sesión larga de dev (Sprint E-3 + E-5: SPA completa, tunnel manager, Doctor view, release pipeline, provider `bore`), surgió la pregunta natural: **¿hacia dónde va orch?**

El proyecto tiene fundaciones técnicas sólidas pero está en una encrucijada. Tres direcciones posibles se identificaron, y una resonó especialmente. Este doc captura el análisis para retomarlo con calma.

---

## Estado técnico actual (2026-08-22)

### Fortalezas concretas

1. **Arquitectura con boundaries claros** — backend / SPA / state backends / tunnel providers / auth profiles, cada capa con Protocol o interface, sin filtración cruzada.
2. **Defense in depth real en auth** — TokenAuth + ProfileGuard + explicit allow-list + whitelist en `/api/config`. Cada capa hace un check y falla loud.
3. **Zero-dep discipline en la SPA** — hand-written primitives shadcn, SVG charts a mano, sin Recharts, sin Radix. Bundle 380kB gzipped ~114kB. Trabajo up-front alto, paga en 2 años.
4. **Dogfooding auténtico** — Sprint E-1 findings loop hace que orch reporte a GitHub cosas raras que encuentra. No es marketing.
5. **CI/CD proper** — release workflow builda SPA en el wheel + verifica que la SPA está adentro antes de publicar. Cero tolerancia a wheels rotos.

### Riesgos identificados

1. **Scope creep peligroso** — empezó como "dispatcher de tasks", hoy incluye dashboard con 8 pages, tunnel manager multi-provider, architecture generator via LLM, findings loop, DAG visualizer, SPA embedding ExcaliDash. **El dashboard tiene MÁS código que el orquestador core.**
2. **5 archivos de config** (`config.yaml`, `budgets.yaml`, `model_router.yaml`, `dashboard.yaml`, `tasks.json`). Curva de onboarding empinada.
3. **Superficie de ruptura externa grande** — depende de CLIs claude/codex/opencode, Pinggy (cambió free tier), archify skill, Cloudflare, GitHub API. Cada uno un potential outage.
4. **Test suite pesado (979 tests)** — bueno para robustez pero velocity de agregar features cae.
5. **Multi-workstream sin discipline de branches** — 2 sesiones editando el mismo branch en paralelo. Funciona por suerte hoy, pero es riesgo real.

---

## Tres caminos posibles considerados

### A) Plataforma completa de orchestration AI
Como Airflow pero para agentes. Compite con **LangChain / LangGraph, AutoGen, CrewAI, DSPy**. Espacio saturado. Diferenciación tiene que ser brutal.

### B) Tool personal del founder que evoluciona con él
Cada feature responde a un dolor real. Alta densidad de valor por línea. Pero adopción externa depende de que otros tengan el mismo workflow (raro).

### C) CLI focused: batch dispatcher a agents con budget/observability
Core original. Dashboard como observability opcional, no como producto. Scope manejable. Menos "wow factor".

**Diagnóstico previo**: hoy orch está entre A y B. La SPA + dashboard empuja hacia A, pero el positioning parece B. Tensión no resuelta.

---

## PIVOT — el framing que cambia todo

Insight del founder: **"el dashboard es una herramienta para dar seguimiento al trabajo y poder entregar algo a tu stakeholder"**.

Con ese framing, el dashboard **no es feature ancillary** — es **EL diferenciador**.

### Value prop redefinido

> **orch — Build AI-powered products, deliver progress to your stakeholder.**
>
> *The open-source orchestrator that dispatches tasks to Claude/Codex/OpenCode CLIs, tracks budget + progress, and gives your client a live dashboard from day one.*

### El gap del mercado

| Herramienta | Ayuda al dev | Cliente/PM ve algo |
|---|---|---|
| LangChain / LangGraph | ✅ | ❌ |
| CrewAI / AutoGen | ✅ | ❌ |
| DSPy | ✅ | ❌ |
| Cursor / Aider | ✅ | ❌ |
| Claude Code / Codex CLI | ✅ | ❌ |
| **orch** | ✅ | **✅ ← nadie más** |

Este hueco es real y grande. Pain point: *"trabajo con AI pero cuando el cliente pregunta '¿cómo va?' tengo que armar un status report a mano".* orch lo resuelve nativamente.

### Todo lo técnico ya construido cobra sentido bajo este framing

| Feature | Bajo framing "OSS platform" | Bajo framing "build + deliver" |
|---|---|---|
| Profiles operator/stakeholder/both | scope creep | **core** (dev ve TODO, cliente vista curated) |
| Vista `/stakeholder/summary` curated | duplicación | **core** (esto ve el cliente) |
| Token auth via query param | overkill | **core** (URL compartible con cliente) |
| Tunnel manager (autossh, bore) | ancillary | **core** (exponer localhost al cliente) |
| Findings loop | dogfooding cool | **core** (report al cliente qué se encontró) |
| Architecture generator | showcase | **core** (docs auto-generada para cliente) |
| Budget guardrails | protección | **core** (transparencia de costos al cliente) |

**Solo el Board tab (embed ExcaliDash)** se sale del framing — es tuyo personal.

### 3 personas que orch sirve bajo este framing

1. **Consultor freelance** — construye para cliente, entrega dashboard como parte del deliverable ("acá ves el progreso en vivo, no me preguntes por email").
2. **Agencia de desarrollo** — múltiples clientes, cada uno su dashboard con auth, un tunnel exponiendo todos con subdominios distintos.
3. **Product team en startup** — founder construye experimentos, board de inversores ve "AI runway spent, features shipped, blocked on X".

Los 3 tienen el MISMO pain: **necesitan mostrar progress sin abrir Slack cada 2 horas**.

---

## Casos de uso concretos que venden solos

### Consultor construye chatbot WhatsApp para cliente Y

```bash
orch init --template chatbot-whatsapp cliente-y
cd cliente-y
orch dashboard --profile both --token "cliente-y-secret" &
orch tunnel start
# → URL pública tipo https://chatbot-cliente-y.tudominio.dev
```

Mensaje al cliente:
> *"Hola María, acá podés seguir el progreso: `https://chatbot-cliente-y.tudominio.dev` con token `cliente-y-secret`. Actualiza en vivo, ETA en tiempo real, spend visible."*

Ese screenshot vende orch en 30 segundos. Ningún otro framework puede hacer ese pitch.

### Agencia con portfolio

- 5 proyectos activos, cada uno `orch init --template <X>`
- Un solo servidor con Cloudflare Tunnel exponiendo `cliente1.tudominio.dev`, `cliente2.tudominio.dev`, etc.
- Cada cliente con su token
- Dashboard "portfolio view" para el CEO de la agencia

### Startup interna

- Founder usa orch para experimentos PMF
- Weekly digest auto-generado al board: "shipped X, spent $Y en AI, ETA revised Z"

---

## Diferenciación defendible

**¿Por qué este framing es defendible contra LangChain?**

1. **Nadie más lo tiene**. LangChain no piensa en stakeholders. Aider no piensa en stakeholders. Cursor no piensa en stakeholders. Espacio virgen.
2. **Ataca dolor real y universal**. "Cómo le muestro al cliente que trabajé" es un pain UNIVERSAL de freelancers/agencias.
3. **Combina 2 mercados en 1 tool**: dev tools + client reporting. Juntos son diferenciales.
4. **Marketing más fácil**: "muéstrale a tu cliente esto" es más vendible que "usá este SDK".
5. **Justifica todas las features "raras"** ya construidas (profiles, tunnel, findings, arch). No son scope creep — son value.

**Angle en 1 oración**: *"Airflow para AI CLIs con budget guardrails y dashboard-para-el-cliente built-in."*

---

## Lo que HAY que agregar para consolidar este framing

### Prioridad ALTA (2-3 semanas)

- **Vista stakeholder mejorada** — hoy es minimal. Para C-level necesita:
  - Timeline visual (Gantt-like por phase)
  - Budget vs actual chart
  - **Executive summary auto-generado por LLM** ("Last week: shipped auth flow, blocked on DBA approval, ETA revised +2 days")
  - **Weekly digest via email** opcional
  - **Download PDF report** para forwardearlo al board
- **Templates orientados a "entrega a stakeholder"**:
  - Cada template scaffoldea `dashboard.yaml` con `profile: both` + token generado + tunnel config
  - Doc auto-generado: "cuando termines de configurar, mandale este URL al cliente"

### Prioridad MEDIA (4-6 semanas)

- **Sistema de templates** (`orch init --template <name>`)
- **5 templates canónicos**:
  - `chatbot-whatsapp` (Waha + agentes)
  - `landing-page-nextjs`
  - `saas-b2b-clerk-supabase`
  - `mobile-expo-supabase`
  - `data-pipeline`
- **Multi-project view** (para consultores) — `orch dashboard --portfolio`
- **Registry local** — templates en `~/.orch/templates/`, submit vía PR a repo `orch-templates`

### Prioridad BAJA / opcional

- Web wizard (browser-based) para generar `orch init`
- Plugin system (extensions instalables por users)
- Registry hosted / paquete-tipo-homebrew

### Cortar / mover a plugin

- Board tab con ExcaliDash → **plugin opcional** (no core al positioning)
- Tunnel providers exotic (más allá de autossh + bore + cloudflared) → **plugins**

---

## Roadmap sugerido (4-6 meses)

### Q1 (mes 1-2): storytelling + stakeholder polish
- Reescribir README con nuevo tagline + comparison table
- Screenshot del stakeholder dashboard como hero image
- GIF/video de 30s: "click a stakeholder link, see progress live"
- Vista stakeholder mejorada (timeline, exec summary, PDF export)
- Docs: "How to deliver AI-built products to your client with orch"
- **HN launch** con título tipo: *"Show HN: orch — open-source AI orchestrator with a stakeholder dashboard built-in"*

**Validación**: si HN mueve la aguja (200+ upvotes, comments engaged), positioning funciona. Sino, iterar el pitch antes de más código.

### Q2 (mes 3-4): template system
- `orch init --template <name>` funcional
- 3 templates canónicos primero
- Contribution guide para nuevos templates
- 5-10 case studies (blog posts de users construyendo cosas reales)

### Q3 (mes 5-6): community + polish
- GitHub Discussions activo (no solo issues)
- Discord o Slack
- Registry hosted (tipo homebrew)
- **V1.0 stable release**

---

## Advertencias honestas (elefantes en la habitación)

### Este pivot vale la pena SI:
1. Hay bandwidth para dedicar 30-40% del tiempo a **community + marketing** durante 12 meses (issues, PRs, tutoriales, videos, Twitter, HN).
2. Se puede tolerar 0 revenue durante 12-18 meses mientras crece adopción.
3. Se está OK con que otros usen orch de formas no anticipadas.

### NO lo hagas SI:
1. Necesitás monetizar antes de 18 meses.
2. Preferís hackear código sobre responder issues.
3. Tu passion project real es OTRO (construir productos para vos), y orch es solo el side-tool.

### Path intermedio (más pragmático)
"**OSS enfocado, no plataforma**":
- README claro, templates canónicos, docs
- **Sin community aspirations, sin registry hosted**
- Es orch como "cookiecutter para AI products + stakeholder dashboard"
- Los que descubren se lo apropian, los que no, no importa
- Bajo overhead, alto impacto para el subset correcto de users

**Sostenible con 1 persona. Escalás community si emerge demanda real, no por FOMO de plataforma.**

---

## Open questions — para próxima sesión

- [ ] ¿Cuál es el bandwidth realista disponible para community/marketing?
- [ ] ¿Hay interés/tiempo para grabar videos (YouTube/Twitch) como parte del outreach?
- [ ] ¿Se prefiere el path "OSS enfocado" o el "plataforma"?
- [ ] ¿Qué template canónico priorizar primero? (chatbot vs landing vs saas)
- [ ] ¿Cómo se maneja la propiedad intelectual del `dashboard.yaml.example` con tokens generados — hay tema legal con clientes viendo el URL público?
- [ ] Si va a HN launch — ¿cuándo? ¿qué features MÍNIMAS tienen que estar antes del launch?
- [ ] Naming: ¿"orch" es final o hay opción de rebrand más marketable?
- [ ] ¿Monetización a futuro (dual license, hosted managed, sponsorship)?
- [ ] Sobre el `dashboard.yaml.example` con tokens generados — ¿hay tema legal con clientes viendo el URL público?

---

## Insights adicionales del founder

*[Espacio para agregar insights en próximas sesiones. Cada uno con fecha para trackear evolución.]*

### 2026-08-22 — Insight inicial
"El dashboard es una herramienta para dar seguimiento al trabajo y poder entregar algo a tu stakeholder."

Este insight es el pivote de todo el brainstorm. Todo lo demás se re-alineó con esa mirada.

### 2026-08-22 — Insight: ¿orch con IDE propio?
Propuesta del founder: *"orch podría tener su propio IDE, tipo Orca.dev con terminal integrado, usando templates, plugins, el link para los stakeholder, que debe ser web."*

**Análisis técnico**:
- Construir IDE web decente requiere 100+ persona-año (Monaco/CodeMirror + xterm.js + LSP + file tree + git + preview + multi-file + deploy + extensions).
- Mercado saturado: Replit ($100M+ funded, 500+ engs), Gitpod/Codespaces, StackBlitz, Bolt.new, v0.dev, Cursor, Windsurf, Zed. Ninguno se puede outbuild con 1-2 personas.
- Reinventar editors NO agrega valor único.

**Alternativas más viables — 4 niveles**:

| Nivel | Alcance | Esfuerzo | Recomendación |
|---|---|---|---|
| **1 — HOY** | Dev usa Cursor/VS Code + stakeholder ve web dashboard | Cero | Mantener |
| **2 — Extension oficial** | VS Code/Cursor extension: sidebar dashboard, command palette, deep-link al stakeholder | 2-3 meses | ⭐ **Sweet spot post-HN launch** |
| **3 — Dashboard mini-IDE view-only** | Web dashboard suma preview de código (syntax highlight, no edit), file tree read-only, iframe preview app | 4-6 meses | Ambicioso pero doable |
| **4 — IDE completo own** | Todo lo de un editor completo | 6-12 meses full-time (mínimo) | ❌ **No recomendado** — competir con Replit sin fondos |

**Pregunta real detrás de la propuesta**:
> *"¿Cómo hago que el flujo dev + stakeholder sea seamless en una sola experiencia?"*

Se responde MEJOR con:
- Extension VS Code para el dev
- Dashboard web con preview panels para el stakeholder
- Deep-link entre ambos

Un IDE own resuelve 20% del problema por 800% del costo. **Nivel 2 + Nivel 3 combinados** dan el value prop de "orch como platform de dev + delivery integrado" sin la barrera técnica de construir editor.

**Decisión pendiente**: confirmar en próxima sesión si el path 2+3 satisface la visión del founder o si insiste en el nivel 4.

---

## Referencias y contexto

- Sprint E-3: SPA completa (Summary, List, Kanban, Board, Architecture, Doctor, Metrics, Logs)
- Sprint E-5: Tunnel manager (autossh + bore, cloudflared pending)
- Release v0.6.0 (Jinja eliminada, SPA at root)
- Repo: https://github.com/hectorcanaimero/orch
- Sesiones relacionadas en memoria: `sprint_e3_spa_spike.md`

---

**Próximo paso**: revisar este documento con calma, agregar insights, definir bandwidth realista, decidir path (enfocado vs plataforma), y armar plan de 30 días concreto.
