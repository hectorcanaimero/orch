# orch — Manual de usuario paso a paso (CLI en Go)

> También disponible en: [English](MANUAL.en.md) · [Português](MANUAL.pt.md)
>
> Este manual documenta el binario **Go** de `orch` — la reescritura como
> binario único. Cada comando de abajo es la salida real de `orch --help`
> sobre un binario compilado; la referencia completa de flags vive en
> [`CLI.md`](CLI.md), que es la fuente de verdad con la que este manual se
> mantiene sincronizado. ¿Buscás el `orch` en Python? Ver
> [Python legacy](#python-legacy) al final.

**Tiempo de setup total**: ~2 minutos (un `curl | sh`, sin Python, sin venv).
**Tiempo por feature**: unos minutos escribiendo el spec + ejecución desatendida.

---

## Contenido

1. [Instalación](#1-instalación)
2. [Crear un proyecto nuevo](#2-crear-un-proyecto-nuevo)
3. [Escribir specs y atomizarlos a tasks.json](#3-escribir-specs-y-atomizarlos-a-tasksjson)
4. [Revisar el plan antes de correrlo](#4-revisar-el-plan-antes-de-correrlo)
5. [Run — despachar tasks a los agentes de IA](#5-run--despachar-tasks-a-los-agentes-de-ia)
6. [Abrir el dashboard](#6-abrir-el-dashboard)
7. [Inspeccionar una corrida: status, tasks, events, logs](#7-inspeccionar-una-corrida-status-tasks-events-logs)
8. [Arreglar cosas a mano: task set, task-status, reset](#8-arreglar-cosas-a-mano-task-set-task-status-reset)
9. [El router de modelos](#9-el-router-de-modelos)
10. [Inspeccionar la config](#10-inspeccionar-la-config)
11. [MCP — que un agente maneje orch directamente](#11-mcp--que-un-agente-maneje-orch-directamente)
12. [Instalar el skill de Claude Code de orch](#12-instalar-el-skill-de-claude-code-de-orch)
13. [Cuando algo falla](#13-cuando-algo-falla)
14. [Actualizar orch](#14-actualizar-orch)
15. [Python legacy](#python-legacy)

---

## 1. Instalación

```bash
curl -fsSL https://raw.githubusercontent.com/hectorcanaimero/orch/main/scripts/install.sh | sh
```

Descarga el tarball correcto para `linux`/`darwin` × `amd64`/`arm64` desde
el último GitHub Release, verifica su checksum, e instala en
`~/.local/bin` (`INSTALL_DIR=...` para cambiarlo). Sin toolchain de Go,
sin Python, sin dependencias que resolver — `orch` es un binario estático.

O vía Homebrew:

```bash
brew install hectorcanaimero/orch/orch
```

O bajá un tarball directamente de
[la página de Releases](https://github.com/hectorcanaimero/orch/releases)
y poné `orch` en tu `PATH` a mano. Ver [`RELEASING.md`](RELEASING.md) para
el esquema completo de tags y cómo se construyen los releases.

Verificá:

```bash
orch --version
orch --help
```

### CLIs de IA que necesitás

Al menos **una** de estas en tu `PATH`, autenticada con una suscripción o
API key — hoy el binario en Go sólo despacha a `claude`, el resto sigue
siendo sólo-Python (ver la fila de `run` en [`CLI.md`](CLI.md)):

- **`claude`** — Claude Code CLI (Anthropic)

Una task ruteada a un backend que todavía no tiene adapter en Go
(`codex`, `opencode`, `gemini`, `agy`) se salta al despachar con
`backend-unavailable:<name>` en vez de bloquear toda la corrida.

Corré `orch doctor` en cualquier momento para ver qué es realmente
alcanzable — ver [§13](#13-cuando-algo-falla).

---

## 2. Crear un proyecto nuevo

```bash
orch init ~/work/my-app --template python-api
```

```
$ orch init --help
Scaffold a new orch project.

With no arguments, asks a short series of questions and shows everything it is about to write before writing any of it.
With a path or any of the flags below, scaffolds directly.

Templates: chatbot-whatsapp, data-pipeline, expo-mobile, nextjs-saas, python-api

Usage:
  orch init [PATH] [flags]

Flags:
      --force                 Overwrite an existing project's files
  -h, --help                  help for init
      --project-name string   Name used in generated files; default = the directory name
      --sdd                   Also scaffold the openspec/ layout
      --template string       Project template (chatbot-whatsapp, data-pipeline, expo-mobile, nextjs-saas, python-api); omit for a blank project
```

Corrélo **sin argumentos** y en vez de eso hace una serie corta de
preguntas (template, nombre del proyecto, layout de SDD), mostrando todo
lo que va a escribir antes de escribir nada:

```bash
orch init
```

Eso escribe `.orchestrator/config.yaml`, `.orchestrator/model_router.yaml`,
`tasks.json`, y (con `--sdd`) un layout `openspec/`. También escribe un
`.mcp.json` que apunta a `orch mcp` — ver
[§11](#11-mcp--que-un-agente-maneje-orch-directamente) — que se deja
intacto en un re-`init`, `--force` incluido, porque puede que ya liste
otros servidores MCP del proyecto.

Un proyecto con template (`--template`) viene con tasks reales y ruteadas
en `tasks.json` — `orch tasks` funciona de una, sin necesidad de atomizar
nada para probar el resto de este manual.

---

## 3. Escribir specs y atomizarlos a tasks.json

Los specs son archivos markdown bajo `<project-root>/docs` (o donde
apunte `spec_root` en `config.yaml`) con el formato descrito en
[`SPEC-FORMAT.md`](SPEC-FORMAT.md). `orch atomize` los parsea y mergea
tasks nuevas en `tasks.json` — **read-only a menos que pases `--apply`**:

```bash
$ orch atomize --help
Parse markdown specs and merge them into tasks.json (read-only unless --apply)

Usage:
  orch atomize [flags]

Flags:
      --apply               Escribí tasks.json (con backup). Sin este flag es read-only.
      --file string         Sólo un archivo markdown (bypass del walk de --specs-dir)
  -h, --help                help for atomize
      --list                Modo listar: sólo imprime lo parseado, sin merge/diff
      --no-backup           No crear backup .bak-<ts> al escribir (default: sí crea)
      --specs-dir string    Directorio de specs .md (default: <project-root>/docs)
      --tasks-json string   Path a tasks.json (default: <project-root>/tasks.json)
```

(El texto de ayuda de los flags ya está en español en el binario — es la
salida real, no un detalle de traducción de este manual.)

```bash
# Preview: parsea cada spec bajo el directorio de specs, muestra un diff, no escribe nada
orch atomize

# Un solo archivo
orch atomize --file docs/f1-auth.md

# Apply — escribe tasks.json (con backup tasks.json.bak-<ts> salvo --no-backup)
orch atomize --file docs/f1-auth.md --apply
```

**Garantías**: idempotente (re-correrlo con el mismo spec no toca los IDs
de tasks existentes, sólo agrega los nuevos); una task que declara un
modelo que no está en `model_router.yaml` la agarra `orch validate` / al
despachar, no se acepta en silencio; los bloques de código con fences en
un spec se saltean en el parser (así un spec de ejemplo dentro de
`specs/README.md` no se importa como tasks reales).

---

## 4. Revisar el plan antes de correrlo

El binario en Go **todavía no tiene flag `--dry-run`** en `run` — es un
gap conocido y documentado respecto de Python (ver la fila de `run` en
[`CLI.md`](CLI.md)). Para previsualizar qué haría una corrida, usá los
comandos de sólo lectura:

```bash
# Todo lo que está mal en el DAG o el ruteo, antes de gastar un token
orch validate

# Qué está listo para despachar ahora mismo
orch tasks --status todo

# El grafo de dependencias, como Graphviz DOT
orch graph | dot -Tpng -o plan.png
```

`orch validate` corre los mismos checks que el preflight de Python tenía
y que ya tienen un hogar en Go: carga de config/router/tasks, schema,
ciclos de dependencias y rutas de modelo sin resolver. Exit code `0`
limpio, `2` si encontró al menos un error.

```bash
$ orch validate --help
Static validation of tasks.json + routing (schema, deps, cycles, routes)

Usage:
  orch validate [flags]

Flags:
      --files   Also check that parent dirs of each task.files[] entry exist + are writable (not implemented yet)
  -h, --help    help for validate
      --json    Emit the full validation report as JSON on stdout
```

Corré también `orch doctor` acá (ver [§13](#13-cuando-algo-falla)) — cubre
el lado del entorno que `validate` no toca: CLIs de proveedor
efectivamente en `PATH`, disponibilidad de VCS, worktrees huérfanos,
cordura del preset de budget, salud de SQLite.

---

## 5. Run — despachar tasks a los agentes de IA

```bash
$ orch run --help
Walk the DAG, dispatching ready tasks to their CLI agents

Usage:
  orch run [flags]

Flags:
  -h, --help            help for run
      --max-tasks int   Stop after dispatching this many tasks; 0 means no limit
      --mode string     auto dispatches everything ready; semi asks before each critical task (default "auto")
      --no-push         Skip pushing task branches — for a project with no remote
      --only string     Only dispatch tasks whose id matches this glob (dependencies still resolve across the whole DAG)
      --task-locks      Take a per-task lock, so several orch instances can share one project
      --worktree-mode   Give each task its own git worktree and branch (also settable as dispatch.worktree_mode)
```

```bash
# Despacha todo lo que esté listo, sin preguntar
orch run

# Pregunta antes de cada task marcada como crítica
orch run --mode semi

# Aísla cada task en su propio git worktree/branch y abre un PR por task
# al tener éxito (necesita dispatch.worktree_mode / vcs.auto_pr — ver CONFIG.md)
orch run --worktree-mode

# Todavía no hay remote al que pushear
orch run --no-push
```

**Ctrl-C** drena el trabajo en curso antes de salir, con exit code
**130**. Un **segundo** Ctrl-C manda SIGKILL a todos los grupos de
procesos hijos de inmediato.

**Modo worktree**: cada task recibe su propio git worktree y branch, en
`<proyecto>.worktrees/<task-id>/` junto al proyecto (ver `CONFIG.md`); al
tener éxito `orch` commitea, pushea, y — si `vcs.auto_pr` está activo —
abre un PR hacia `dispatch.base_branch`, y deja la task en `in-progress`
para que el poller de CI la termine. Un check de CI verde la marca `done`
(y la mergea también, si `github.auto_merge` está activo); uno rojo recibe
un reintento con los logs del fallo adjuntos, y después `blocked`. Una
corrida que abre un PR termina antes de hacer polling sobre el CI de ese
PR — una corrida que termina en verde se nota en el *próximo* `orch run`,
igual que en Python.

**Concurrencia, budgets, reintentos** son todos manejados por config, no
por flags — ver [`CONFIG.md`](CONFIG.md).

---

## 6. Abrir el dashboard

```bash
$ orch dashboard --help
Serve the operator dashboard on a local HTTP port.

Reads the project's state and shows it; it never writes. The
stakeholder profile gates every data route behind a token and an
allow-list — see `profile` and `token` under `dashboard:` in
config.yaml, which the flags below override.

Usage:
  orch dashboard [flags]

Flags:
  -h, --help             help for dashboard
      --host string      Address to bind; 0.0.0.0 exposes it beyond localhost (default "127.0.0.1")
      --port int         Port to listen on; 0 picks any free one (default 7420)
      --profile string   Access profile: operator, stakeholder or both (default: config.yaml)
      --token string     Shared token a stakeholder session must present (default: config.yaml)
      --tunnel           Also start the Cloudflare quick tunnel (needs tunnel.enabled and cloudflared), and stop it on exit
```

```bash
# Todo, para vos
orch dashboard

# Un proyecto sintético con historial realista, para recorrerlo primero
# (no despacha nada; los datos se borran al detenerlo)
orch dashboard --demo

# La última corrida terminada en Markdown, para pegar en un PR o un chat:
# tareas hechas y bloqueadas, tiempo real y de agentes, gasto por proveedor,
# PRs (--json para scripts)
orch report receipt

# Un link para pasarle a un cliente, por un túnel rápido de Cloudflare
# (necesita `tunnel: enabled: true` en config.yaml y cloudflared en el PATH
# — la página Tunnel muestra cómo instalarlo). Imprime el link del portal
# del cliente con el token que exige cada pedido por el túnel; Ctrl+C lo revoca.
orch dashboard --tunnel
```

El dashboard del operador tiene cuatro destinos. **Now** es la primera
pantalla: la corrida, cada agente trabajando con su reloj, las tasks bloqueadas
y las que esperan una ventana de budget (con el comando `orch task set` que
destraba cada una — el dashboard solo lee), las fases y la ventana de budget.
**Work** reúne List, Kanban, Graph, Phases y Pace como pestañas que comparten
los filtros en la URL; **Cost** reúne Budget y Metrics; **Delivery**, el resumen
para el cliente, CI y Share (el túnel). **Logs** se abre como panel sobre
cualquier página. Las direcciones viejas (`/kanban`, `/budget`, `/tunnel`…)
redirigen a su lugar nuevo.

`--profile operator` (el default) muestra todo: tasks, gasto por modelo,
logs. `--profile stakeholder` protege cada ruta de datos detrás del
`--token` y una allow-list de rutas seguras para el stakeholder — sin
líneas de log, sin prompts, sin desglose de costo por modelo a menos que
`dashboard.show_spend_to_stakeholder` esté activo. Ver
[`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md) para el modelo de acceso
completo y [`DELIVERING-TO-STAKEHOLDERS.md`](DELIVERING-TO-STAKEHOLDERS.md)
para el recorrido de cara al cliente (resumen ejecutivo, timeline de
fases, ETA, bloqueos).

---

## 7. Inspeccionar una corrida: status, tasks, events, logs

```bash
# Resumen de una pantalla: tasks por status, gasto, últimos eventos, resumen de corrida
orch status

# Todas las tasks con status/ruteo/deps, filtrable
orch tasks --status todo,in-progress
orch tasks --only 'F1.*'

# Historial de eventos de una task
orch events F1.1.T2 --tail 50

# El log crudo del agente para esa task
orch logs F1.1.T2 --tail 200
```

Los cuatro aceptan `--json` (salvo `logs`, que tampoco lo tuvo nunca en
Python) para scripting, y comparten los flags de proyecto
(`--project-root`/`--project-id`/`--config`).

```bash
$ orch status --help
Project status: tasks, costs, last events, run summary

Flags:
  -h, --help            help for status
      --json            Emit the raw snapshot as JSON
      --only string     Restrict task rows to ids matching this glob
      --status string   Comma-separated status filter (e.g. todo,in-progress)
```

---

## 8. Arreglar cosas a mano: task set, task-status, reset

```bash
# Mover el status de una task directamente (transiciones ilegales salen con exit 3)
orch task-status F1.1.T5 todo --note "re-queued after manual fix"

# Lo mismo, vía la superficie más nueva `task set` — también donde va a
# aterrizar a futuro un override de model/backend una vez que
# state.Backend soporte escribirlos (hoy esos dos flags están
# registrados pero devuelven un error claro de "not implemented yet" en
# vez de no hacer nada en silencio). No hay flag de milestone: un
# milestone es una fase, y la fase de una task vive en tasks.json
orch task set --id F1.1.T5 --status todo

# Ver qué tasks in-progress parecen trabadas, sin tocar nada
orch reset

# Revertirlas de verdad a todo
orch reset --requeue --only 'F1.*'
```

```bash
$ orch task set --help
Set a task's status (model and backend overrides are not implemented)

Flags:
      --backend string   Override the backend for this task (not implemented yet)
  -h, --help             help for set
      --id string        Task ID, e.g. F1.1.T3
      --model string     Override the model for this task (not implemented yet)
      --status string    Set the task status (e.g. done, in-progress, blocked)
```

`reset` lee el status **real** de runtime desde el backend de estado, no
el campo `status` (potencialmente desactualizado) de `tasks.json` — una
diferencia deliberada respecto de Python, documentada en
[`CLI.md`](CLI.md).

---

## 9. El router de modelos

`model_router.yaml` mapea el modelo declarado de una task a un backend
CLI, su nombre `cli_model`, y un tier de costo.

```bash
# ¿Toda task.model resuelve a una entrada del router?
orch router validate

# Agregar entradas inferidas para lo que no esté ruteado, en un tier dado
orch router add-missing --tier standard
orch router add-missing --yes   # sin el prompt de confirmación
```

```bash
$ orch router --help
Inspect and maintain model_router.yaml

Available Commands:
  add-missing Append inferred model_router.yaml entries for every unrouted task model
  validate    Check that every task.model resolves to a model_router.yaml entry
```

`add-missing` muestra el plan y pregunta `y/N` antes de escribir, igual
que Python — `--yes` es cómo un script se saltea el prompt (nunca por
detección de TTY: una corrida desatendida con `--mode auto` no puede
pausarse en silencio esperando stdin).

---

## 10. Inspeccionar la config

```bash
orch config show
```

Imprime la config efectiva — defaults mergeados con `config.yaml` y
cualquier override — seguido de de dónde salió cada valor, y cualquier
clave en tu `config.yaml` que orch no reconozca (una buena forma de
detectar una clave mal tipeada que no hace nada en silencio). Referencia
completa de claves: [`CONFIG.md`](CONFIG.md).

```bash
$ orch config --help
Config helpers

Available Commands:
  show        Print the effective config, merged with defaults, and where each value came from
```

---

## 11. MCP — que un agente maneje orch directamente

```bash
$ orch mcp --help
Serve orch's state to an MCP-capable agent over stdio.

Seven tools: orch_list_tasks, orch_get_task, orch_set_status,
orch_block, orch_budget, orch_events, orch_context.

Speaks JSON-RPC on stdin/stdout; run it from an MCP client, not
from a terminal. See docs/MCP.md.
```

`orch init` ya escribió un `.mcp.json` que apunta a `orch mcp` — un
agente compatible con MCP (Claude Code incluido) lo levanta
automáticamente desde la raíz del proyecto. No hay equivalente en Python;
ver [`MCP.md`](MCP.md) para la lista completa de tools y la forma del
error de transición ilegal (nombra cada status al que la task puede
moverse legalmente, ya que quien llama es un modelo que puede reintentar).

---

## 12. Instalar el skill de Claude Code de orch

```bash
$ orch install-skills --help
Install orch's Claude Code skill(s) into one or more agent CLIs

Flags:
      --all              Install every embedded skill (default when --skill is omitted)
      --dry-run          Show what would be installed without writing anything
      --force            Overwrite an already-installed skill of the same name
  -h, --help             help for install-skills
      --path string      Claude target install directory (default: ~/.claude/skills)
      --skill strings    Install only this skill (repeatable); default is --all
      --target strings   Agent(s) to install into: claude, codex, opencode, cursor (repeatable) (default [claude])
```

```bash
orch install-skills                          # a ~/.claude/skills, --all
orch install-skills --target codex --target opencode
orch install-skills --dry-run                # preview primero
```

`--target claude` escribe directorios de skills reales bajo
`~/.claude/skills`. `codex`/`opencode` no tienen mecanismo propio de
skills, así que reciben una sección claramente delimitada e
idempotentemente reemplazable en el `AGENTS.md` del proyecto en su lugar;
`cursor` recibe un archivo `.cursor/rules/<name>.mdc`. Seis skills vienen
embebidos en el binario: `orch`, el manual de operación para un agente
dentro de un proyecto, y el pipeline de planificación — `orch-plan` corre
`orch-prd` (idea → `docs/prd/`), `orch-arch` (→ `docs/arch/`),
`orch-spec` (→ `specs/`, en el formato exacto que lee `orch atomize`) y
`orch-tasks` (→ `tasks.json` vía `orch atomize --apply`), parando para
vos después de cada documento y antes de aplicar nada.

---

## 13. Cuando algo falla

### Corré `orch doctor` primero

```bash
$ orch doctor --help
Environment preflight — provider CLIs, VCS, worktrees, budget config, SQLite

Flags:
  -h, --help   help for doctor
      --json   Emit the full doctor report as JSON on stdout
```

```bash
orch doctor
```

Chequea: CLIs de proveedor referenciadas por `tasks.json`+`model_router.yaml`
efectivamente en `PATH`, completitud del ruteo, cordura del preset de
budget, worktrees de git huérfanos, disponibilidad de VCS (repo/remote/auth),
si existe `.mcp.json`, y salud de SQLite. Misma convención de exit code
que `validate`: `0` limpio, `1` sólo warnings, `2` al menos un error.

### Una task bloqueada

1. `orch logs <task-id> --tail 100` — leer la salida propia del agente
2. `orch events <task-id>` — ver qué registró orch mismo (dispatch, fail,
   retry, budget_pause…)
3. Arreglar el spec o el código a mano
4. `orch task-status <task-id> todo --note "..."` para re-encolarla
5. `orch run` de nuevo — sólo levanta tasks en `todo`

### `orch` no arranca — exit 1, error de layout de proyecto

Falta o no se puede leer `tasks.json` o `.orchestrator/config.yaml`.
`orch init --force` si de verdad es un proyecto real, o revisá las
entradas `config.parse`/`router.parse` de `orch doctor --json` para el
error de parseo en sí.

### `orch validate` / `orch doctor` salen con exit 2

Al menos un hallazgo de severidad error — leé la salida humana (o
`--json`) para ver qué check falló y por qué; ambos comandos imprimen
todos los checks que corrieron, no sólo los que fallaron.

### Una transición de status ilegal — exit 3

`task-status`/`task set --status` rechazan una transición que la máquina
de estados no permite (p. ej. `done` → `todo` directo). El error nombra
el status actual de la task.

---

## 14. Actualizar orch

```bash
# Re-correr el instalador — siempre trae el último release v* (sin -py)
curl -fsSL https://raw.githubusercontent.com/hectorcanaimero/orch/main/scripts/install.sh | sh

# O, vía Homebrew
brew upgrade orch

# Verificar
orch --version
```

Actualizar el binario nunca toca el `.orchestrator/config.yaml`,
`model_router.yaml` o `budgets.yaml` propios de un proyecto — si querés
llevar un default nuevo a un proyecto existente, corré `orch init --force`
ahí y hacé diff antes de commitear (sobreescribe el tuning propio del
proyecto, así que no lo hagas a ciegas).

---

## Python legacy

El `orch` previo a la reescritura — el dashboard en FastAPI,
`pipx install orch`, toda la historia de sprints que este binario en Go
está reemplazando feature por feature — está congelado en la branch
`python-legacy`, con el tag **`v0.11.0-py`**. Se instala y corre
exactamente como está documentado en el manual propio de ese tag:

```bash
git checkout v0.11.0-py
pipx install .
```

o

```bash
pipx install git+https://github.com/hectorcanaimero/orch.git@v0.11.0-py
```

Esa línea no recibe features nuevas — sólo el binario en Go avanza. Ver
[`RELEASING.md`](RELEASING.md) para por qué las dos líneas comparten un
mismo namespace de tags, separadas por un sufijo `-py` en vez de un
prefijo de branch aparte.

---

## Referencias

- Referencia completa de flags del CLI, notas de paridad con Python: [`CLI.md`](CLI.md)
- Claves de config: [`CONFIG.md`](CONFIG.md)
- Formato markdown de specs: [`SPEC-FORMAT.md`](SPEC-FORMAT.md)
- Servidor / tools de MCP: [`MCP.md`](MCP.md)
- Modelo de acceso del dashboard: [`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md)
- Compartir el dashboard con un cliente: [`DELIVERING-TO-STAKEHOLDERS.md`](DELIVERING-TO-STAKEHOLDERS.md)
- Proceso de release / esquema de tags: [`RELEASING.md`](RELEASING.md)

## Feedback

Este manual es un documento vivo, que se actualiza en el mismo PR que
agrega un subcomando nuevo. Si te encontrás con un caso que no está
cubierto, abrí un issue en
<https://github.com/hectorcanaimero/orch/issues> o mandá un PR con la
sección que falta.
