# orch — Manual do usuário passo a passo (CLI em Go)

> Também disponível em: [English](MANUAL.en.md) · [Español](MANUAL.es.md)
>
> Este manual documenta o binário em **Go** do `orch` — a reescrita como
> binário único. Cada comando abaixo é a saída real de `orch --help` num
> binário compilado; a referência completa de flags vive em
> [`CLI.md`](CLI.md), que é a fonte de verdade com a qual este manual é
> mantido sincronizado. Procurando o `orch` em Python? Veja
> [Python legacy](#python-legacy) no final.

**Tempo total de setup**: ~2 minutos (um `curl | sh`, sem Python, sem venv).
**Tempo por feature**: alguns minutos escrevendo o spec + execução não supervisionada.

---

## Conteúdo

1. [Instalação](#1-instalação)
2. [Criar um projeto novo](#2-criar-um-projeto-novo)
3. [Escrever specs e atomizá-los em tasks.json](#3-escrever-specs-e-atomizá-los-em-tasksjson)
4. [Revisar o plano antes de rodar](#4-revisar-o-plano-antes-de-rodar)
5. [Run — despachar tasks para os agentes de IA](#5-run--despachar-tasks-para-os-agentes-de-ia)
6. [Abrir o dashboard](#6-abrir-o-dashboard)
7. [Inspecionar uma execução: status, tasks, events, logs](#7-inspecionar-uma-execução-status-tasks-events-logs)
8. [Corrigir coisas manualmente: task set, task-status, reset](#8-corrigir-coisas-manualmente-task-set-task-status-reset)
9. [O roteador de modelos](#9-o-roteador-de-modelos)
10. [Inspecionar a config](#10-inspecionar-a-config)
11. [MCP — deixando um agente controlar o orch diretamente](#11-mcp--deixando-um-agente-controlar-o-orch-diretamente)
12. [Instalar o skill de Claude Code do orch](#12-instalar-o-skill-de-claude-code-do-orch)
13. [Quando algo falha](#13-quando-algo-falha)
14. [Atualizar o orch](#14-atualizar-o-orch)
15. [Python legacy](#python-legacy)

---

## 1. Instalação

```bash
curl -fsSL https://raw.githubusercontent.com/hectorcanaimero/orch/main/scripts/install.sh | sh
```

Baixa o tarball certo para `linux`/`darwin` × `amd64`/`arm64` a partir do
último GitHub Release, verifica o checksum, e instala em
`~/.local/bin` (use `INSTALL_DIR=...` para mudar). Sem toolchain de Go,
sem Python, sem dependências para resolver — `orch` é um binário estático.

Ou via Homebrew:

```bash
brew install hectorcanaimero/orch/orch
```

Ou baixe um tarball diretamente da
[página de Releases](https://github.com/hectorcanaimero/orch/releases) e
coloque `orch` no seu `PATH` manualmente. Veja [`RELEASING.md`](RELEASING.md)
para o esquema completo de tags e como os releases são construídos.

Verifique:

```bash
orch --version
orch --help
```

### CLIs de IA que você precisa

Pelo menos **uma** dessas no seu `PATH`, autenticada com uma assinatura
ou API key — hoje o binário em Go só despacha para `claude`, o resto
ainda é somente-Python (veja a linha de `run` em [`CLI.md`](CLI.md)):

- **`claude`** — Claude Code CLI (Anthropic)

Uma task roteada para um backend que ainda não tem adapter em Go
(`codex`, `opencode`, `gemini`, `agy`) é pulada no despacho com
`backend-unavailable:<name>` em vez de bloquear toda a execução.

Rode `orch doctor` a qualquer momento para ver o que está realmente
acessível — veja [§13](#13-quando-algo-falha).

---

## 2. Criar um projeto novo

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

Rode sem argumentos e, em vez disso, ele faz uma série curta de perguntas
(template, nome do projeto, layout de SDD), mostrando tudo o que vai
escrever antes de escrever qualquer coisa:

```bash
orch init
```

Isso escreve `.orchestrator/config.yaml`, `.orchestrator/model_router.yaml`,
`tasks.json`, e (com `--sdd`) um layout `openspec/`. Também escreve um
`.mcp.json` apontando para `orch mcp` — veja
[§11](#11-mcp--deixando-um-agente-controlar-o-orch-diretamente) — que fica
intocado num re-`init`, `--force` incluso, já que pode já listar outros
servidores MCP do projeto.

Um projeto com template (`--template`) já vem com tasks reais e roteadas
em `tasks.json` — `orch tasks` funciona na hora, sem precisar atomizar
nada para experimentar o resto deste manual.

---

## 3. Escrever specs e atomizá-los em tasks.json

Specs são arquivos markdown sob `<project-root>/docs` (ou onde
`spec_root` em `config.yaml` apontar) no formato descrito em
[`SPEC-FORMAT.md`](SPEC-FORMAT.md). `orch atomize` os parseia e faz merge
de tasks novas em `tasks.json` — **somente leitura a menos que você passe
`--apply`**:

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

(O texto de ajuda dos flags está em espanhol no binário — é a saída real,
não um detalhe de tradução deste manual.)

```bash
# Preview: parseia cada spec sob o diretório de specs, mostra um diff, não escreve nada
orch atomize

# Só um arquivo
orch atomize --file docs/f1-auth.md

# Apply — escreve tasks.json (com backup tasks.json.bak-<ts>, salvo --no-backup)
orch atomize --file docs/f1-auth.md --apply
```

**Garantias**: idempotente (rodar de novo com o mesmo spec não toca nos
IDs de tasks existentes, só adiciona os novos); uma task que declara um
modelo que não está em `model_router.yaml` é pega pelo `orch validate` /
no despacho, não é aceita silenciosamente; blocos de código com fences
num spec são pulados pelo parser (assim um spec de exemplo dentro de
`specs/README.md` não é importado como tasks reais).

---

## 4. Revisar o plano antes de rodar

O binário em Go **ainda não tem flag `--dry-run`** em `run` — é um gap
conhecido e documentado em relação ao Python (veja a linha de `run` em
[`CLI.md`](CLI.md)). Para pré-visualizar o que uma execução faria, use os
comandos de somente leitura:

```bash
# Tudo que está errado no DAG ou no roteamento, antes de gastar um token
orch validate

# O que está pronto para despachar agora
orch tasks --status todo

# O grafo de dependências, como Graphviz DOT
orch graph | dot -Tpng -o plan.png
```

`orch validate` roda os mesmos checks que o preflight do Python tinha e
que já têm um lar em Go: carregamento de config/router/tasks, schema,
ciclos de dependência e rotas de modelo não resolvidas. Exit code `0`
limpo, `2` se encontrou pelo menos um erro.

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

Rode também `orch doctor` aqui (veja [§13](#13-quando-algo-falha)) — ele
cobre o lado do ambiente que `validate` não toca: CLIs de provedor
efetivamente no `PATH`, prontidão do VCS, worktrees órfãos, sanidade do
preset de budget, saúde do SQLite.

---

## 5. Run — despachar tasks para os agentes de IA

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
# Despacha tudo que está pronto, sem perguntar
orch run

# Pergunta antes de cada task marcada como crítica
orch run --mode semi

# Isola cada task no seu próprio git worktree/branch e abre um PR por
# task quando tem sucesso (precisa de dispatch.worktree_mode /
# vcs.auto_pr — veja CONFIG.md)
orch run --worktree-mode

# Ainda não há remote para fazer push
orch run --no-push
```

**Ctrl-C** drena o trabalho em andamento antes de sair, com exit code
**130**. Um **segundo** Ctrl-C manda SIGKILL para todos os grupos de
processos filhos imediatamente.

**Modo worktree**: cada task ganha seu próprio git worktree e branch, em
`<projeto>.worktrees/<task-id>/` ao lado do projeto (veja `CONFIG.md`);
quando tem sucesso, `orch` faz commit, push, e — se `vcs.auto_pr`
estiver ativo — abre um PR para `dispatch.base_branch`, e deixa a task
`in-progress` para o poller de CI terminar. Um check de CI verde a marca
como `done` (e também faz merge, se `github.auto_merge` estiver ativo);
um vermelho recebe uma nova tentativa com os logs da falha anexados, e
depois `blocked`. Uma execução que abre um PR termina antes de fazer
polling do CI desse PR — uma execução que termina verde é percebida na
*próxima* `orch run`, igual ao Python.

**Concorrência, budgets, retries** são todos controlados por config, não
por flags — veja [`CONFIG.md`](CONFIG.md).

---

## 6. Abrir o dashboard

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
      --tunnel           Also start the configured tunnel, and stop it on exit
```

```bash
# Tudo, para você
orch dashboard

# Uma URL somente leitura para passar a um cliente, com túnel para ficar
# acessível fora da sua máquina (o provedor/comando é configurado em
# dashboard.tunnel no config.yaml — --tunnel só liga/desliga)
orch dashboard --profile stakeholder --token "$(openssl rand -hex 16)" --tunnel
```

`--profile operator` (o padrão) mostra tudo: tasks, gasto por modelo,
logs. `--profile stakeholder` protege cada rota de dados atrás do
`--token` e de uma allow-list de rotas seguras para o stakeholder — sem
linhas de log, sem prompts, sem detalhamento de custo por modelo a menos
que `dashboard.show_spend_to_stakeholder` esteja ativo. Veja
[`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md) para o modelo de acesso
completo e [`DELIVERING-TO-STAKEHOLDERS.md`](DELIVERING-TO-STAKEHOLDERS.md)
para o passo a passo voltado ao cliente (resumo executivo, timeline de
fases, ETA, bloqueios).

---

## 7. Inspecionar uma execução: status, tasks, events, logs

```bash
# Resumo em uma tela: tasks por status, gasto, últimos eventos, resumo da execução
orch status

# Todas as tasks com status/roteamento/deps, filtrável
orch tasks --status todo,in-progress
orch tasks --only 'F1.*'

# Histórico de eventos de uma task
orch events F1.1.T2 --tail 50

# O log bruto do agente para essa task
orch logs F1.1.T2 --tail 200
```

Os quatro aceitam `--json` (exceto `logs`, que também nunca teve isso em
Python) para scripting, e compartilham os flags de projeto
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

## 8. Corrigir coisas manualmente: task set, task-status, reset

```bash
# Mover o status de uma task diretamente (transições ilegais saem com exit 3)
orch task-status F1.1.T5 todo --note "re-queued after manual fix"

# A mesma coisa, pela superfície mais nova `task set` — também onde vai
# pousar futuramente um override de model/backend/milestone assim que
# state.Backend suportar escrevê-los (hoje esses três flags estão
# registrados mas retornam um erro claro de "not implemented yet" em vez
# de não fazer nada silenciosamente)
orch task set --id F1.1.T5 --status todo

# Ver quais tasks in-progress parecem travadas, sem tocar em nada
orch reset

# Reverter de fato para todo
orch reset --requeue --only 'F1.*'
```

```bash
$ orch task set --help
Set a task's model, backend, milestone, or status

Flags:
      --backend string     Override the backend for this task (not implemented yet)
  -h, --help               help for set
      --id string          Task ID, e.g. F1.1.T3
      --milestone string   Assign the task to a milestone ID (not implemented yet)
      --model string       Override the model for this task (not implemented yet)
      --status string      Set the task status (e.g. done, in-progress, blocked)
```

`reset` lê o status **real** de runtime a partir do backend de estado,
não o campo `status` (potencialmente desatualizado) do `tasks.json` — uma
diferença deliberada em relação ao Python, documentada em
[`CLI.md`](CLI.md).

---

## 9. O roteador de modelos

`model_router.yaml` mapeia o modelo declarado de uma task para um
backend CLI, seu nome `cli_model`, e um tier de custo.

```bash
# Toda task.model resolve para uma entrada do router?
orch router validate

# Adicionar entradas inferidas para o que não estiver roteado, num tier dado
orch router add-missing --tier standard
orch router add-missing --yes   # pula o prompt de confirmação
```

```bash
$ orch router --help
Inspect and maintain model_router.yaml

Available Commands:
  add-missing Append inferred model_router.yaml entries for every unrouted task model
  validate    Check that every task.model resolves to a model_router.yaml entry
```

`add-missing` mostra o plano e pergunta `y/N` antes de escrever, igual ao
Python — `--yes` é como um script pula o prompt (nunca por detecção de
TTY: uma execução não supervisionada com `--mode auto` não pode pausar
silenciosamente esperando stdin).

---

## 10. Inspecionar a config

```bash
orch config show
```

Imprime a config efetiva — padrões combinados com `config.yaml` e
quaisquer overrides — seguido de onde cada valor veio, e quaisquer
chaves no seu `config.yaml` que o orch não reconhece (uma boa forma de
pegar uma chave digitada errada que não faz nada silenciosamente).
Referência completa de chaves: [`CONFIG.md`](CONFIG.md).

```bash
$ orch config --help
Config helpers

Available Commands:
  show        Print the effective config, merged with defaults, and where each value came from
```

---

## 11. MCP — deixando um agente controlar o orch diretamente

```bash
$ orch mcp --help
Serve orch's state to an MCP-capable agent over stdio.

Seven tools: orch_list_tasks, orch_get_task, orch_set_status,
orch_block, orch_budget, orch_events, orch_context.

Speaks JSON-RPC on stdin/stdout; run it from an MCP client, not
from a terminal. See docs/MCP.md.
```

`orch init` já escreveu um `.mcp.json` apontando para `orch mcp` — um
agente compatível com MCP (Claude Code incluso) o pega automaticamente a
partir da raiz do projeto. Não há equivalente em Python; veja
[`MCP.md`](MCP.md) para a lista completa de tools e o formato do erro de
transição ilegal (ele nomeia cada status para o qual a task pode se mover
legalmente, já que quem chama é um modelo que pode tentar de novo).

---

## 12. Instalar o skill de Claude Code do orch

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
orch install-skills                          # em ~/.claude/skills, --all
orch install-skills --target codex --target opencode
orch install-skills --dry-run                # preview primeiro
```

`--target claude` escreve diretórios de skills reais sob
`~/.claude/skills`. `codex`/`opencode` não têm mecanismo próprio de
skills, então recebem uma seção claramente delimitada e substituível de
forma idempotente no `AGENTS.md` do projeto; `cursor` recebe um arquivo
`.cursor/rules/<name>.mdc`. Seis skills vêm embutidos no binário: `orch`,
o manual de operação para um agente dentro de um projeto, e o pipeline de
planejamento — `orch-plan` executa `orch-prd` (ideia → `docs/prd/`),
`orch-arch` (→ `docs/arch/`), `orch-spec` (→ `specs/`, no formato exato
que `orch atomize` lê) e `orch-tasks` (→ `tasks.json` via
`orch atomize --apply`), parando para você depois de cada documento e
antes de aplicar qualquer coisa.

---

## 13. Quando algo falha

### Rode `orch doctor` primeiro

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

Verifica: CLIs de provedor referenciadas por
`tasks.json`+`model_router.yaml` efetivamente no `PATH`, completude do
roteamento, sanidade do preset de budget, worktrees de git órfãos,
prontidão do VCS (repo/remote/auth), se `.mcp.json` existe, e saúde do
SQLite. Mesma convenção de exit code que `validate`: `0` limpo, `1`
apenas warnings, `2` pelo menos um erro.

### Task bloqueada

1. `orch logs <task-id> --tail 100` — leia a saída do próprio agente
2. `orch events <task-id>` — veja o que o orch mesmo registrou (dispatch,
   fail, retry, budget_pause…)
3. Conserte o spec ou o código manualmente
4. `orch task-status <task-id> todo --note "..."` para reenfileirar
5. `orch run` de novo — só pega tasks em `todo`

### `orch` não inicia — exit 1, erro de layout de projeto

`tasks.json` ou `.orchestrator/config.yaml` está faltando ou ilegível.
`orch init --force` se de fato é um projeto real, ou verifique as
entradas `config.parse`/`router.parse` de `orch doctor --json` para o
próprio erro de parsing.

### `orch validate` / `orch doctor` saem com exit 2

Pelo menos um achado de severidade erro — leia a saída humana (ou
`--json`) para ver qual check falhou e por quê; ambos os comandos
imprimem todos os checks que rodaram, não só os que falharam.

### Uma transição de status ilegal — exit 3

`task-status`/`task set --status` recusam uma transição que a máquina de
estados não permite (ex.: `done` → `todo` direto). O erro nomeia o
status atual da task.

---

## 14. Atualizar o orch

```bash
# Rode o instalador de novo — ele sempre busca o último release v* (sem -py)
curl -fsSL https://raw.githubusercontent.com/hectorcanaimero/orch/main/scripts/install.sh | sh

# Ou, via Homebrew
brew upgrade orch

# Verifique
orch --version
```

Atualizar o binário nunca toca no `.orchestrator/config.yaml`,
`model_router.yaml` ou `budgets.yaml` próprios de um projeto — se você
quer levar um padrão novo para um projeto existente, rode
`orch init --force` lá e compare antes de fazer commit (ele sobrescreve
o ajuste próprio do projeto, então não faça isso às cegas).

---

## Python legacy

O `orch` anterior à reescrita — o dashboard em FastAPI,
`pipx install orch`, todo o histórico de sprints que este binário em Go
está substituindo feature por feature — está congelado na branch
`python-legacy`, com a tag **`v0.11.0-py`**. Ele instala e roda
exatamente como documentado no manual próprio dessa tag:

```bash
git checkout v0.11.0-py
pipx install .
```

ou

```bash
pipx install git+https://github.com/hectorcanaimero/orch.git@v0.11.0-py
```

Essa linha não recebe features novas — só o binário em Go avança. Veja
[`RELEASING.md`](RELEASING.md) para entender por que as duas linhas
compartilham o mesmo namespace de tags, separadas por um sufixo `-py` em
vez de um prefixo de branch separado.

---

## Referências

- Referência completa de flags do CLI, notas de paridade com Python: [`CLI.md`](CLI.md)
- Chaves de config: [`CONFIG.md`](CONFIG.md)
- Formato markdown de specs: [`SPEC-FORMAT.md`](SPEC-FORMAT.md)
- Servidor / tools de MCP: [`MCP.md`](MCP.md)
- Modelo de acesso do dashboard: [`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md)
- Compartilhar o dashboard com um cliente: [`DELIVERING-TO-STAKEHOLDERS.md`](DELIVERING-TO-STAKEHOLDERS.md)
- Processo de release / esquema de tags: [`RELEASING.md`](RELEASING.md)

## Feedback

Este manual é um documento vivo, atualizado no mesmo PR que adiciona um
subcomando novo. Se você encontrar um caso que não está coberto, abra
uma issue em <https://github.com/hectorcanaimero/orch/issues> ou envie
um PR com a seção que falta.
