# orch — Manual de uso paso a paso

> También disponible en: [English](MANUAL.en.md) · [Português](MANUAL.pt.md)

Este manual asume que usás **Claude Code** (o cualquier CLI equivalente con
soporte de skills). El workflow completo es: **chateás la feature con Claude
→ Claude genera el spec en formato orch → orch atomiza a `tasks.json` → orch
ejecuta → mirás el dashboard**.

**Tiempo total de setup**: ~5 minutos.
**Tiempo por feature nueva**: ~2 minutos de chat + ejecución desatendida.

---

## Índice

1. [Setup inicial (una vez por máquina)](#1-setup-inicial-una-vez-por-máquina)
2. [Crear un proyecto nuevo (una vez por proyecto)](#2-crear-un-proyecto-nuevo-una-vez-por-proyecto)
3. [Chatear la feature en Claude Code](#3-chatear-la-feature-en-claude-code)
4. [Cómo se generan las tasks](#4-cómo-se-generan-las-tasks)
5. [Preview antes de ejecutar (siempre)](#5-preview-antes-de-ejecutar-siempre)
6. [Ejecutar en auto](#6-ejecutar-en-auto)
7. [Abrir el dashboard](#7-abrir-el-dashboard)
8. [Qué mirar durante la corrida](#8-qué-mirar-durante-la-corrida)
9. [Cuando algo falla](#9-cuando-algo-falla)
10. [Actualizar orch](#10-actualizar-orch)

---

## 1. Setup inicial (una vez por máquina)

### Instalar orch

```bash
# Recomendado: venv aislado, `orch` en PATH globalmente
pipx install git+https://github.com/hectorcanaimero/orch.git

# Verificá
orch --help
orch init --help
orch dashboard --help
```

### CLIs de AI que necesitás

Al menos **una** de estas tres en tu PATH, autenticada con suscripción o API key:

- **`claude`** — Claude Code CLI (Anthropic)
- **`codex`** — GPT Codex CLI (OpenAI)
- **`opencode`** — opencode CLI (multi-provider: DeepSeek, Grok, GLM, MiMo, etc.)

Confirmá:

```bash
which claude codex opencode
claude --version
```

Si te falta alguna, orch igual funciona — solo va a rutear las tasks a los
backends que tenés instalados. Pero si tu spec pide `claude-opus-4-7` y no
tenés `claude` instalado, se muere al startup con exit 1.

### SDD skills (opcional pero recomendado)

Verificá que tenés las skills instaladas en `~/.claude/skills/`:

```bash
ls ~/.claude/skills/ | grep -E 'orch|sdd'
# esperado:
# orch-plan
# orch-prd
# orch-arch
# orch-spec
# orch-tasks
# sdd-apply
# sdd-archive
# sdd-design
# ...
```

Si no las tenés, podés seguir usando orch escribiendo specs a mano
(ver [`SPEC-FORMAT.md`](SPEC-FORMAT.md)) — pero el flow con SDD es mucho más
fluido.

---

## 2. Crear un proyecto nuevo (una vez por proyecto)

```bash
orch init ~/work/mi-app --sdd
```

Esto te deja:

```
~/work/mi-app/
├── tasks.json                    ← skeleton vacío
├── specs/                        ← acá van las specs escritas por Claude
│   └── README.md                 ← referencia del formato
├── scripts/
│   ├── task-start.sh             ← ejecutables, funcionales, jq-based
│   ├── task-finish.sh
│   └── task-block.sh
├── orchestrator/
│   ├── state/.gitkeep            ← runtime state (gitignored)
│   ├── config.yaml               ← concurrency, timeouts, retries
│   ├── model_router.yaml         ← mapeo modelos → CLIs
│   └── budgets.yaml              ← guardrails Sprint 7
├── openspec/                     ← SDD (por el flag --sdd)
│   ├── README.md
│   ├── changes/                  ← proposals en curso
│   └── specs/                    ← specs archivadas (source of truth)
└── .gitignore
```

Al final del init te va a decir si tenés SDD instalado y qué hacer:

```
✓ orch project initialized at /Users/vos/work/mi-app

Next steps:
  1. Write your first spec:
       $EDITOR specs/f0-foundation.md
  2. Preview atomize (dry, shows diff):
       orch atomize --file specs/f0-foundation.md
     Then apply:
       orch atomize --file specs/f0-foundation.md --apply
  ...

Spec-Driven Development:
  ✓ SDD skills detected: orch-plan, orch-spec, orch-tasks, ...
    Use `/sdd-explore <topic>` in Claude Code to design specs.
```

---

## 3. Chatear la feature en Claude Code

Acá está la magia. Abrí Claude Code **desde el directorio del proyecto**:

```bash
cd ~/work/mi-app
claude
```

Ahora tenés tres formas de diseñar la feature, de más a menos "hands-off":

### Opción A — Full pipeline con `/orch-plan` (recomendado para features grandes)

En el chat de Claude escribís:

```
/orch-plan quiero agregar autenticación con email/password + Google + Apple, con
recuperación de password por email y verificación de cuenta
```

Claude va a correr internamente:

1. **`/orch-prd`** — genera un PRD (Product Requirements Document) con
   frontmatter YAML orch-friendly
2. **`/orch-arch`** — genera un ARCH (Architecture Design) técnico
3. **`/orch-spec`** — genera specs en formato F<n>.<pkg>.T<n> (que es el que
   `orch atomize` sabe parsear)
4. **`/orch-tasks`** — invoca `orch atomize` en **modo diff-first**: te muestra
   qué tasks NUEVAS se van a agregar a tu `tasks.json` y **te pide
   confirmación antes de escribir**

Salida esperada (Claude va a ir imprimiendo):

```
[/orch-plan] Generando PRD para "auth email+google+apple"...
✓ openspec/changes/auth/prd.md

[/orch-plan] Generando ARCH...
✓ openspec/changes/auth/arch.md
  Módulos: 3 packages nuevos (auth_core, auth_google, auth_apple)
  Decisiones clave: Riverpod para state, GoRouter para deep links

[/orch-plan] Generando SPEC atomizer-ready...
✓ openspec/changes/auth/spec.md
  15 tasks generadas:
    - F1.1.T1: Setup del package auth_core
    - F1.1.T2: Domain: entities (User, Credentials, ...)
    - F1.1.T3: Data: AuthRepository interface
    ...

[/orch-tasks] Diff contra tasks.json actual:
  + 15 tasks nuevas en fase 1
  Modelos usados: claude-sonnet-4-6 (10), opencode-go/glm-5.1 (3), claude-haiku-4-5 (2)
  Estimación total: 24h

¿Aplicar? [y/N]
```

Vos escribís `y` y Claude ejecuta `orch atomize` real. **`tasks.json` queda
actualizado** con las 15 tasks nuevas, con status `todo`, dependencias
correctas, files declarados, modelo asignado.

### Opción B — Granular con `/orch-spec` (cuando ya tenés el diseño en la cabeza)

Si ya sabés QUÉ hay que hacer y solo querés que Claude arme el spec en el
formato correcto:

```
/orch-spec

Quiero un package Flutter llamado auth_core con:
- Domain: entities User, Credentials, AuthMethod
- Data: AuthRepositoryImpl que use supabase.auth
- Presentation: AuthController con Riverpod
- 3 casos de uso: signIn, signUp, resetPassword

Modelos: usar claude-sonnet-4-6 para todo lo que sea domain/data, opencode
para tests puros y boilerplate.

Estimación total: ~8h.
```

Claude te devuelve un spec en formato atomizer-ready. Después:

```
/orch-tasks
```

Y merge al `tasks.json`.

### Opción C — Manual (cuando querés control total)

Editás `specs/mi-feature.md` a mano siguiendo el formato:

```markdown
# F1 — Auth

## F1.1 — Package: auth_core

### F1.1.T1 — Setup del package

- **Modelo**: opencode-go/glm-5.1
- **Estimación**: 30m
- **Razón**: Boilerplate simple.
- **Dependencies**:
- **Files**:
  - `packages/auth_core/pubspec.yaml`
  - `packages/auth_core/lib/auth_core.dart`

### F1.1.T2 — Domain entities

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 2h
- **Razón**: Diseño de tipos requiere razonamiento.
- **Dependencies**: F1.1.T1
- **Files**:
  - `packages/auth_core/lib/src/domain/user.dart`
  - `packages/auth_core/lib/src/domain/credentials.dart`
```

Y en la terminal:

```bash
# 1) Preview (dry-run — muestra qué se va a agregar sin escribir)
orch atomize --file specs/mi-feature.md

# 2) Apply — escribe tasks.json + crea backup tasks.json.bak-<ts>
orch atomize --file specs/mi-feature.md --apply
```

Los tres flujos terminan en lo mismo: **`tasks.json` con las tasks nuevas
en `status: backlog`, listas para dispatch**.

---

## 4. Cómo se generan las tasks

Cuando `/orch-tasks` (o `orch atomize` manual) corre, procesa la spec y
genera entries así en `tasks.json`:

```json
{
  "id": "F1.1.T2",
  "phase": 1,
  "title": "Domain entities",
  "description": "",
  "model": "claude-sonnet-4-6",
  "reason": "Diseño de tipos requiere razonamiento.",
  "status": "backlog",
  "dependencies": ["F1.1.T1"],
  "estimateHours": 2.0,
  "files": [
    "packages/auth_core/lib/src/domain/user.dart",
    "packages/auth_core/lib/src/domain/credentials.dart"
  ],
  "specRef": "specs/mi-feature.md",
  "comments": []
}
```

El status arranca en `backlog` (default del atomizer). El main loop de `orch`
promueve `backlog` → `todo` cuando las dependencias están OK, después `todo`
→ `in-progress` en el momento del dispatch.

**Qué garantiza el atomizer:**

- **Idempotencia**: si volvés a correr el atomize con la misma spec, las
  tasks existentes NO se tocan. Solo agrega ids que no estaban.
- **Validación de modelo**: si el modelo declarado no existe en
  `model_router.yaml`, orch se muere al primer startup con exit 1 y te dice
  cuál es el offender.
- **Deps se preservan tal cual**: no valida que existan (podés declarar
  deps a tasks que agregarás después).

**Qué NO garantiza:**

- Que los `files` sean únicos (dos tasks pueden declarar el mismo archivo
  → orch usa el `per_file: 1` del `config.yaml` para dispatchar UNA sola a
  la vez sobre ese archivo).
- Que el DAG no tenga ciclos (orch los detecta al startup con exit 1).

---

## 5. Preview antes de ejecutar (siempre)

**Nunca ejecutes `--mode auto` sin ver el plan primero.** El dry-run es
gratis y te muestra exactamente qué se va a hacer:

```bash
orch --project-root ~/work/mi-app --dry-run
```

Salida:

```
==== ORCH DRY RUN ====
Project: mi-app
Ready tasks: 15
Blocked tasks: 0
Deferred (semi-mode critical): 0

Plan (dispatch order):
  Wave 1 (parallel, no deps):
    F1.1.T1  [opencode/glm-5.1]     Setup del package auth_core          0.5h
    F1.2.T1  [opencode/glm-5.1]     Setup del package auth_google        0.5h
    F1.3.T1  [opencode/glm-5.1]     Setup del package auth_apple         0.5h

  Wave 2 (deps: T1):
    F1.1.T2  [claude/sonnet-4-6]    Domain entities                      2.0h
    F1.2.T2  [claude/sonnet-4-6]    Google OAuth flow                    1.5h
    F1.3.T2  [claude/sonnet-4-6]    Apple Sign-In flow                   1.5h

  Wave 3 (deps: T2):
    ...

Concurrency plan: max 8 in-flight, per-provider caps: claude=3 codex=2 opencode=3
Budget preset: conservative
  claude:   0 / 800000 tokens used (0.0%, threshold 60%)
  codex:    0 / 400000 tokens used (0.0%, threshold 60%)
  opencode: 0 / 2000000 tokens used (0.0%, threshold 70%)

Estimated total: 24h (parallelizable to ~6h wall clock)
Estimated cost: $12-18 USD (opencode ~$0.50, claude ~$14, codex $0)
```

Si algo no te cierra —una task con el modelo mal, un archivo raro, deps
incorrectas— **este es el momento de editar la spec y re-atomizar**.

---

## 6. Ejecutar en auto

Cuando el plan te cierra, dispatch:

```bash
# Auto mode — sin prompts, dispatcha todo
orch --project-root ~/work/mi-app --mode auto

# O con preset de budget más agresivo si querés maximizar throughput
orch --project-root ~/work/mi-app --mode auto --budgets-preset aggressive

# O semi mode — te pregunta antes de tasks marcadas como "critical"
orch --project-root ~/work/mi-app --mode semi
```

**Qué ves en la terminal** (auto mode):

```
2026-08-21 14:30:00 INFO project_root=~/work/mi-app project_id=mi-app config=orchestrator/config.yaml
2026-08-21 14:30:00 INFO budget gate enabled: preset=conservative providers=['claude', 'codex', 'opencode']
2026-08-21 14:30:00 INFO 15 tasks todo, 0 in-flight, 0 done
2026-08-21 14:30:01 INFO dispatch F1.1.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:30:01 INFO dispatch F1.2.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:30:01 INFO dispatch F1.3.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:32:15 INFO success F1.1.T1 (2m14s, 4.2K tokens, $0.001)
2026-08-21 14:32:16 INFO dispatch F1.1.T2 → claude/sonnet-4-6 (attempt 1)
...
```

**Ctrl-C** = drain graceful (espera a que los in-flight terminen antes de
salir). **Ctrl-C 2 veces** = force kill.

**Podés dejarlo corriendo desatendido**. El budget gate se encarga de que
no te queme la suscripción:

- Cuando `claude` llega al 60% (threshold del preset conservative) → pausa
  dispatches a claude, sigue con codex/opencode
- Cuando TODOS los providers están capped → dormir hasta el próximo reset
  (chunk de 30s para que Ctrl-C responda)
- Al reset → resume automático, sigue donde quedó

---

## 7. Abrir el dashboard

**En OTRA terminal** (dejando `orch --mode auto` corriendo en la primera):

```bash
orch dashboard --project-root ~/work/mi-app
```

Salida:

```
INFO:     Started server process
INFO:     Waiting for application startup.
INFO:     Application startup complete.
INFO:     Uvicorn running on http://127.0.0.1:7420
```

Abrí en el browser:

```bash
open http://127.0.0.1:7420
```

### Qué ves en el dashboard

**Home (`/`)** — Tabla estilo Jira con TODAS las tasks:
- Columnas: ID / Title / Phase / Status / Model / Files / Owner
- Filtros por phase, status, model
- Live-update via SSE (no hace falta refrescar)
- Click en una task → modal con detalles (deps, comments, últimos logs)

**Kanban (`/kanban`)** — Vista tipo Trello agrupada por fase:
- Columnas: backlog / todo / in-progress / done / blocked
- Cards con model + estimate + progress
- Colores por criticidad
- Drag & drop DESHABILITADO (read-only por seguridad)

**Metrics (`/metrics`)** — Costo y burndown:
- Total gastado (USD) por día
- Por modelo (barras)
- Burndown chart (tasks restantes vs tiempo)
- Critical path (cadena más larga de deps)

**Logs (`/logs`)** — Feed live de eventos:
- SSE stream, se actualiza en tiempo real
- Filtrable por task-id o event_type
- Muestra: dispatch, success, fail, timeout, retry, budget_skip, budget_pause

**Budgets (`/api/budgets` — endpoint JSON o mirar la barra)**:
```json
{
  "disabled": false,
  "preset": "conservative",
  "providers": {
    "claude": {
      "tokens_used": 240000,
      "token_budget": 800000,
      "usage_pct": 30.0,
      "threshold_pct": 60,
      "window_hours": 5,
      "capped": false,
      "reset_at": null
    },
    "codex": {"tokens_used": 0, "capped": false, ...},
    "opencode": {"tokens_used": 15000, "capped": false, ...}
  }
}
```

En la UI se ve como 3 barras horizontales:
- Verde 0-60% → OK
- Amber 60-80% → warning
- Rojo 80-100% → PAUSED, con countdown al próximo reset

---

## 8. Qué mirar durante la corrida

**Cheat sheet de "está todo bien"**:

| Señal | Dónde | Qué significa |
|---|---|---|
| `success` events consistentes | `/logs` | Tasks completándose OK |
| Budget bars ≤ 60% verdes | `/api/budgets` | Consumo saludable |
| Kanban avanza left → right | `/kanban` | Progreso normal |
| Cost/hour razonable | `/metrics` | Sin runaway costs |

**Señales de alerta**:

| Señal | Dónde | Acción |
|---|---|---|
| Muchos `retry` seguidos | `/logs` | Puede haber rate-limit — chequeá backend |
| `budget_pause` events | `/logs` | Todos los providers capped, va a esperar el reset |
| Task queda en `in-progress` mucho tiempo | `/kanban` | Puede estar colgada — chequeá el log |
| `timeout` events | `/logs` | Ajustá `default_timeout_multiplier` en config.yaml |
| `blocked` tasks acumulándose | `/kanban` | Chequeá `state/logs/<task>.log` |

**Comandos útiles desde la terminal en paralelo**:

```bash
# Live tail de todos los eventos
tail -f ~/work/mi-app/orchestrator/state/events-*.jsonl | jq -r '"\(.ts | .[11:19])  \(.event_type|ascii_upcase)  \(.task_id)  \(.backend)"'

# Log de UNA task específica
tail -f ~/work/mi-app/orchestrator/state/logs/F1.1.T2.log

# Status snapshot (usa jq)
./status.sh ~/work/mi-app
```

---

## 9. Cuando algo falla

### Task blocked

1. Abrí el modal de la task en el dashboard → mirá el último comment
2. Terminal: `cat ~/work/mi-app/orchestrator/state/logs/<task-id>.log | tail -100`
3. Editá la spec o el código a mano según haga falta
4. Marcá la task como `todo` de nuevo:
   ```bash
   jq --arg id "F1.1.T5" '(.tasks[] | select(.id == $id) | .status) = "todo"' \
      tasks.json > tasks.json.tmp && mv tasks.json.tmp tasks.json
   ```
5. Corré `orch --mode auto` de nuevo — retoma solo las `todo`

### Budget capped más rápido de lo esperado

1. Verificá el consumo real: `/api/budgets` en el dashboard
2. Si el `token_budget` en `budgets.yaml` está mal calibrado, subilo (o
   bajá `threshold_pct` para más margen)
3. Los cambios en `budgets.yaml` se toman en la próxima corrida — no
   necesitás restart si es el mismo run

### Rate limit del provider

Es distinto al budget gate — es la CLI real que te tira 429.

1. orch detecta el fail y hace **retry-once con backoff extendido** (60s
   default para rate limits, configurable en `config.yaml → retry.rate_limit_backoff_seconds`)
2. Si sigue fallando, la task queda `blocked` con el error
3. Solución típica: esperar el reset window (~5h Anthropic, ~3h OpenAI) y
   `orch --mode auto` de nuevo

### `orch` no arranca — exit 1 con "unrouted model"

Tu spec pide un modelo que no está en `model_router.yaml`. El error te dice
cuál:

```
UnroutedModelError: task F1.1.T2 uses model 'claude-opus-5-0' which is not in router
```

Editá `orchestrator/model_router.yaml`, agregá:

```yaml
"claude-opus-5-0":
  backend: claude
  cli_model: claude-opus-5-0
  tier: premium
  is_premium: true
```

Y retry.

### `orch` no arranca — exit 2 con "project layout invalid"

Falta `tasks.json` o `scripts/task-*.sh`. Corré `orch init --force` si es
un proyecto nuevo, o creá lo que falta a mano.

### `orch` no arranca — exit 3 con "flock contention"

Ya hay OTRA instancia de `orch` corriendo contra el mismo `state/`.
Chequeá:

```bash
lsof ~/work/mi-app/orchestrator/state/.lock
```

Si es una instancia zombi, kill al PID. Si es intencional (dos runs en
paralelo), usá `--task-locks` en las dos.

---

## 10. Actualizar orch

Cuando yo (o vos) pusheés cambios al repo:

```bash
# Try upgrade first
pipx upgrade orchestrator

# Si dice "already up to date" pero sabés que hay cambios, forzá:
pipx install --force git+https://github.com/hectorcanaimero/orch.git

# Verificá qué versión estás corriendo
orch --help | head -3
```

**Ojo**: `pipx upgrade` NO toca los YAMLs que ya se copiaron a tus
proyectos (`~/work/mi-app/orchestrator/*.yaml`). Si querés los nuevos
defaults en un proyecto viejo:

```bash
# Ver el diff primero
diff ~/work/mi-app/orchestrator/config.yaml \
     $(python3 -c 'import orchestrator, pathlib; print(pathlib.Path(orchestrator.__file__).parent)')/config.yaml

# Aplicar (⚠️ pisa tu config custom)
orch init ~/work/mi-app --force
```

Es intencional que no se sobreescriban: si tuneaste `budgets.yaml` para
tu proyecto, no querés que un `upgrade` te lo pise.

---

## Workflow completo — resumen visual

```
┌─────────────────────────────────────────────────────────────────┐
│  UNA VEZ POR MÁQUINA                                            │
│    pipx install git+https://github.com/hectorcanaimero/orch.git │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  UNA VEZ POR PROYECTO                                           │
│    orch init ~/work/mi-app --sdd                                │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  POR CADA FEATURE                                               │
│                                                                 │
│  1. En Claude Code (dentro del proyecto):                       │
│                                                                 │
│       /orch-plan quiero agregar autenticación con email + google│
│                                                                 │
│  2. Claude genera PRD → ARCH → SPEC → propone diff a tasks.json │
│     Vos confirmás con `y`                                       │
│                                                                 │
│  3. Preview:                                                    │
│       orch --project-root ~/work/mi-app --dry-run               │
│                                                                 │
│  4. Ejecutar:                                                   │
│       orch --project-root ~/work/mi-app --mode auto             │
│                                                                 │
│  5. En OTRA terminal, dashboard:                                │
│       orch dashboard --project-root ~/work/mi-app               │
│       → http://127.0.0.1:7420                                   │
│                                                                 │
│  6. Café ☕                                                     │
└─────────────────────────────────────────────────────────────────┘
```

---

## Referencias

- Formato de spec del atomizer: [`SPEC-FORMAT.md`](SPEC-FORMAT.md)
- Historia del proyecto: [`history/README.md`](history/README.md)
- Config exhaustiva: [`../README.md#configuration`](../README.md#configuration)
- Sprint 7 (budget guardrails): [`../README.md#budget-guardrails-sprint-7`](../README.md#budget-guardrails-sprint-7)

## Feedback

Este manual está vivo. Si te encontrás con un caso que no está cubierto,
abrí un issue en <https://github.com/hectorcanaimero/orch/issues> o
mandá un PR con la sección nueva.
