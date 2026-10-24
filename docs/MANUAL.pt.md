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
ou API key. orch despacha para as cinco, e um mesmo projeto pode
misturá-las: cada task vai para a CLI à qual o seu modelo é roteado
([§9](#9-o-roteador-de-modelos)):

- **`claude`** — Claude Code CLI (Anthropic)
- **`codex`** — Codex CLI (OpenAI)
- **`gemini`** — Gemini CLI (Google)
- **`opencode`** — OpenCode (qualquer provedor que esteja configurado)
- **`agy`** — Antigravity (o gateway multi-modelo do Google)
- **`muse`** — Muse Code (Meta). O orch o roda sem aprovações e sem seu
  sandbox (`--approval-mode never --disable-sandbox`), para que o agente
  possa reportar sua tarefa concluída

Você só precisa das CLIs para as quais suas tasks são roteadas.

Rode `orch doctor` a qualquer momento para ver o que está realmente
acessível — veja [§13](#13-quando-algo-falha).

---

## 2. Criar um projeto novo

**O caminho curto**: num repositório git, `orch start "o que você quer construir"`
faz esta seção inteira e a §3 por você — cria o projeto com as CLIs do seu
PATH, instala as skills de planejamento, deixa o claude planejar a ideia com
você (`/orch-plan`), valida e estima o plano, e oferece rodar as duas primeiras
tarefas com o dashboard aberto. O resto da seção é o mesmo caminho passo a
passo.

```bash
orch init ~/work/my-app --template python-api
```

```
$ orch init --help
Scaffold a new orch project.

With no arguments, asks three questions (project id, template, confirm) and shows everything it is about to write before writing any of it; --advanced asks the rest.
The project goes in the current directory, with the default models of the AI CLIs on PATH.
With a path or any of the flags below, scaffolds directly.

Templates: chatbot-whatsapp, data-pipeline, expo-mobile, nextjs-saas, python-api

Usage:
  orch init [PATH] [flags]

Flags:
      --advanced              Also ask the project root, state backend, budget preset, spec root, tier models and openspec layout
      --force                 Overwrite an existing project's files
  -h, --help                  help for init
      --no-permissions        Do not allow orch's agent rules in the settings of the CLIs the project uses; orch claude-permissions / orch agy-permissions add them later
      --no-report-findings    Write report_findings.enabled: false and never offer it (it is off by default; the first run that finishes a task offers it)
      --project-name string   Name used in generated files; default = the directory name
      --sdd                   Also scaffold the openspec/ layout
      --template string       Project template (chatbot-whatsapp, data-pipeline, expo-mobile, nextjs-saas, python-api); omit for a blank project
```

Rode **sem argumentos**, no diretório do seu projeto, e ele faz três
perguntas — project id (padrão: o nome do diretório), template (padrão:
blank) e a confirmação —, então dar Enter em todas deixa um projeto que roda.
Ele escreve no **diretório atual**, escolhe os modelos padrão entre as CLIs de
IA que encontra no `PATH` (primeiro claude, depois codex, gemini, opencode,
agy, muse) e grava a branch do seu repositório como `dispatch.base_branch`.
`orch init --advanced` pergunta também o root, o backend de estado, o preset
de orçamento, o spec root, os modelos por tier e o layout openspec.

```bash
orch init
```

Isso escreve `.orchestrator/config.yaml`, `.orchestrator/model_router.yaml`,
`tasks.json`, e (com `--sdd`) um layout `openspec/`. Também escreve um
`.mcp.json` apontando para `orch mcp` — veja
[§11](#11-mcp--deixando-um-agente-controlar-o-orch-diretamente) — que fica
intocado num re-`init`, `--force` incluso, já que pode já listar outros
servidores MCP do projeto.

Um agente reporta sua tarefa com `scripts/task-finish.sh` / `task-block.sh`
e as ferramentas MCP do orch, e uma CLI rodando headless recusa o que não tem
uma regra. Por isso `orch init` e `orch start` **concedem por padrão as
permissões básicas do orch** a cada CLI para a qual o projeto roteia e que
está no seu `PATH` — nunca a uma que ele não usa, e só essas regras restritas,
nunca uma flag que pula permissões: as do claude no `.claude/settings.json` do
projeto, as do agy em `~/.gemini/antigravity-cli/settings.json` (fora do
projeto, então o caminho e cada regra adicionada são impressos). O wizard
pergunta e Enter aceita; `init` em modo batch e `start --yes` simplesmente as
escrevem. `--no-permissions` não escreve nada e nomeia `orch
claude-permissions` / `orch agy-permissions`, que as adicionam depois. codex,
opencode e muse não precisam de nada escrito; `orch doctor` informa cada CLI
(gemini como desconhecido). O gemini recusa uma pasta que não foi marcada como
confiável, e cada tarefa roda num worktree novo, então o orch define
`GEMINI_CLI_TRUST_WORKSPACE=true` em cada dispatch do gemini; defina como `false`
no seu ambiente para manter esse controle.

Um projeto novo vem com `report_findings.enabled: false`. Os agentes podem
abrir issues **públicas** sobre o orch em hectorcanaimero/orch com o seu login
do `gh` ([`DOGFOODING.md`](DOGFOODING.md)), e isso não é a primeira coisa a
perguntar: o primeiro `orch run` que termina uma task oferece, uma vez.
`--no-report-findings` é um não definitivo e nunca é oferecido.

Um projeto em branco começa com **uma task de exemplo**, `F0.T1` ("Add a
Getting started section to README.md"), no modelo mais barato de uma CLI
instalada, para que o primeiro `orch run` mostre um agente trabalhando. Cada
task parte de um commit, então primeiro faça commit do que o init escreveu —
o init avisa quando o repositório não tem nenhum:

```bash
git add -A && git commit -m "orch init"
orch run
```

Sem remote do git, o trabalho fica na branch local `orch/<task-id>` de cada
task: sem push nem pull request.

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
      --specs-dir string    Directorio de specs .md (default: <project-root>/<spec_root>, "specs")
      --tasks-json string   Path a tasks.json (default: <project-root>/tasks.json)
```

(O texto de ajuda dos flags está em espanhol no binário — é a saída real,
não um detalhe de tradução deste manual.)

```bash
# Preview: parseia cada spec sob o diretório de specs, mostra um diff, não escreve nada
orch atomize

# Só um arquivo
orch atomize --file specs/f1-auth.md

# Apply — escreve tasks.json (com backup tasks.json.bak-<ts>, salvo --no-backup)
orch atomize --file specs/f1-auth.md --apply
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
      --max-tasks int   Start at most this many tasks (their retries do not count); 0 means no limit
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
como `done` (e também faz merge, se `github.auto_merge` estiver ativo — um merge
recusado porque a base acabou de mudar é tentado de novo por alguns
minutos, e um PR atrás da sua base é atualizado antes, para que as
dependentes comecem de uma base que tem o código; um merge que nunca entra
é informado com o erro do próprio gh no evento `pr_auto_merge_failed` e na
nota da task);
um vermelho recebe uma nova tentativa com os logs da falha anexados, e
depois `blocked`. Um PR em conflito com sua base é atualizado sobre o
trabalho já feito da task: o orch faz merge da base e push quando entra
limpo, sem agente; senão o agente recebe os arquivos em conflito e os commits
da base que os causaram para resolver, `retry.conflict` vezes (1 num projeto
novo; uma config sem a chave continua bloqueando como antes) antes de a task
ficar `blocked` nomeando os arquivos. Uma execução que abre um PR termina antes de fazer
polling do CI desse PR — uma execução que termina verde é percebida na
*próxima* `orch run`, igual ao Python.

**Conhecimento do projeto** (`knowledge.enabled`, ligado num projeto novo): o
que uma task aprende chega às que vêm depois. As lições são arquivos markdown
curtos em `.orch/knowledge/`, commitados e revisados como qualquer outro
arquivo. Um agente registra uma com a tool MCP `orch_learn` (ela entra no PR
dele); o orch escreve um rascunho quando o verify local rejeitou uma tentativa
e a seguinte passou, ou quando o agente resolveu um PR em conflito; você
adiciona as suas com `orch knowledge add`. Cada prompt de despacho leva as
poucas mais relevantes em `Project knowledge` — primeiro aquelas cuja `area`
cobre os arquivos da task. `orch knowledge list` / `show` / `rm` as
administram e `orch knowledge stats` compara o sucesso na primeira tentativa
antes e desde a primeira lição. Veja
[`CONFIG.md`](CONFIG.md#knowledge--new-451).

**Concorrência, budgets, retries** são todos controlados por config, não
por flags — veja [`CONFIG.md`](CONFIG.md).

### Começar sem GitHub

Um repositório ainda sem remote — ou uma máquina sem `gh` — não precisa de pull
requests para levar o trabalho ao branch base. Defina `vcs.provider: local`
(`orch init` escreve isso por você nesse caso; `orch start` também, quando o
repositório já tem um commit):

```yaml
dispatch:
  worktree_mode: true
vcs:
  provider: local
```

Cada tarefa continua trabalhando no seu próprio branch, no seu próprio
worktree. Quando termina e passa no verify local, o orch **faz o squash merge
dela em `dispatch.base_branch` aqui mesmo** — um commit por tarefa, com o
título que o PR teria — e apaga o branch. As tarefas são mescladas uma de cada
vez, e uma dependente só é despachada quando as dependências já estão na base,
então ela parte do trabalho delas. Nada é enviado com push e o `gh` nunca roda.
Uma tarefa cujo trabalho conflita com a base recebe a base mesclada no seu
worktree e volta ao agente para resolver o conflito, `retry.conflict` vezes;
depois é bloqueada citando os arquivos (o trabalho fica em `orch/<task-id>`).
Se o branch base estiver em checkout com mudanças não commitadas nos mesmos
arquivos, o merge é recusado e a tarefa é bloqueada: faça commit ou stash e
depois `orch task-status <id> todo`.

`orch status` mostra `merged_locally` como último evento da tarefa, o dashboard
diz "Mesclada localmente" onde estaria o link do PR e o recibo da execução
lista os merges. `orch doctor` não pede remote nem `gh`.

**Migrar para o GitHub depois**: crie o repositório, `git remote add origin
<url>`, `git push -u origin main` e defina `vcs.provider: github` (e
`gh auth login`). A partir do próximo `orch run`, cada tarefa abre um PR como
sempre.

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
      --tunnel           Also start the Cloudflare quick tunnel (needs tunnel.enabled and cloudflared), and stop it on exit
```

```bash
# Tudo, para você
orch dashboard

# Um projeto sintético com histórico realista, para explorar primeiro
# (nada é despachado; os dados são apagados ao parar)
orch dashboard --demo

# A última execução concluída em Markdown, para colar num PR ou num chat:
# tarefas feitas e bloqueadas, tempo real e dos agentes, gasto por provedor,
# PRs (--json para scripts)
orch report receipt

# A mesma execução em palavras simples: o que foi construído, por que cada
# task bloqueada parou e os próximos passos com o comando de cada um (o orch
# run imprime isso ao terminar)
orch report summary

# Um link para passar a um cliente, por um túnel rápido da Cloudflare
# (precisa de `tunnel: enabled: true` no config.yaml e do cloudflared no
# PATH — a página Tunnel mostra como instalar). Imprime o link do portal do
# cliente com o token que toda requisição pelo túnel exige; Ctrl+C o revoga.
orch dashboard --tunnel
```

O dashboard do operador tem quatro destinos. **Now** é a primeira tela: a
execução, cada agente trabalhando com seu relógio, as tasks bloqueadas e as que
esperam uma janela de budget (com o comando `orch task set` que desbloqueia cada
uma — o dashboard só lê), as fases e a janela de budget; depois de uma
execução, o recibo dela com o resumo em palavras simples embaixo — por que cada
task bloqueada parou e os próximos passos com seus comandos. **Work** reúne List,
Kanban, Graph, Phases e Pace como abas que compartilham os filtros na URL;
**Cost** reúne Budget e Metrics; **Delivery**, o resumo para o cliente, CI,
Tests (cada execução E2E do verify local, com o que um teste que falhou
deixou: erro, passo, captura, vídeo, trace) e Share (o túnel). **Logs** abre como painel sobre qualquer página. Os endereços
antigos (`/kanban`, `/budget`, `/tunnel`…) redirecionam para o novo lugar.
**⌘K** (Ctrl+K) busca páginas, tasks e filtros e copia comandos da CLI; `g` e
depois `n`/`w`/`c`/`d` troca de destino, `/` busca na lista de tasks e `?`
mostra todos os atalhos. A página de uma task mostra a descrição como
Markdown: código inline, e blocos com a linguagem, cores de sintaxe e um botão
de copiar (Editar continua mostrando o texto cru).

A aba **Alterações** é a revisão: o que o branch `orch/<id>` da task mudou
em relação à base (`git diff --merge-base`), como lista de arquivos com +/− e
hunks com cores de sintaxe — um arquivo com mais de 400 linhas alteradas abre
recolhido. Ao lado, cada vez que a verificação local devolveu o trabalho, e
os achados da revisão sob o `arquivo:linha` que citam. O operador local age
dali: **Aceitar** faz squash-merge do PR (`orch task accept`), **Devolver**
reabre a task com sua nota para o agente (`orch task note --retry`),
**Bloquear** a retém com a nota como motivo. No modo local (`vcs.provider:
local`) não há PR: Aceitar faz commit do que a task deixou no seu worktree e
squash-merge de `orch/<id>` na base aqui, como o run faz, e marca a task como
concluída; se o trabalho conflita com a base nada muda e o erro cita os
arquivos, para você devolvê-la. Um stakeholder não vê
a aba a menos que `api_task_diff` esteja em `dashboard.stakeholder_routes`.

O dashboard está em inglês, espanhol e português (Brasil). Ele começa no idioma
do seu navegador e, se não for nenhum desses, em inglês; o botão de idioma no
rodapé da barra lateral (ou ⌘K → *Mudar idioma*) troca o idioma e este
navegador lembra a escolha. `dashboard.language` no `config.yaml` é outra
coisa: escolhe o que o **cliente** lê (portal, resumo executivo, PDF), não as
telas do operador. Comandos, flags e chaves de config ficam em inglês.

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
# pousar futuramente um override de model/backend assim que
# state.Backend suportar escrevê-los (hoje esses dois flags estão
# registrados mas retornam um erro claro de "not implemented yet" em vez
# de não fazer nada silenciosamente). Não há flag de milestone: um
# milestone é uma fase, e a fase de uma task vive no tasks.json
orch task set --id F1.1.T5 --status todo

# Ver quais tasks in-progress parecem travadas, sem tocar em nada
orch reset

# Reverter de fato para todo
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

`reset` lê o status **real** de runtime a partir do backend de estado,
não o campo `status` (potencialmente desatualizado) do `tasks.json` — uma
diferença deliberada em relação ao Python, documentada em
[`CLI.md`](CLI.md).

---

## 9. O roteador de modelos

`model_router.yaml` mapeia o modelo declarado de uma task para um
backend CLI, seu nome `cli_model`, e um tier de custo.

**Usar várias IAs.** O `"model"` de uma task no `tasks.json` é uma
chave do `model_router.yaml`, e essa entrada nomeia a CLI que a executa.
Três tasks, três CLIs:

```json
"tasks": [
  { "id": "T1", "model": "claude/claude-sonnet-4-6", ... },
  { "id": "T2", "model": "codex/gpt-5.4", ... },
  { "id": "T3", "model": "gemini/gemini-2.5-flash", ... }
]
```

```yaml
# .orchestrator/model_router.yaml — orch init already writes these entries
claude/claude-sonnet-4-6:
  backend: claude
  cli_model: claude-sonnet-4-6
  tier: standard
  is_premium: false
codex/gpt-5.4:
  backend: codex
  cli_model: gpt-5.4
  tier: standard
  is_premium: false
gemini/gemini-2.5-flash:
  backend: gemini
  cli_model: gemini-2.5-flash
  tier: cheap
  is_premium: false
```

`orch router validate` verifica que cada task resolve para uma entrada,
e `orch doctor` que cada CLI nomeada por essas entradas está instalada.

**Failover quando um provedor fica sem cota.** Por padrão uma task com
rate limit espera `retry.rate_limit` e tenta a mesma CLI de novo. Dê à
sua rota uma lista `failover:` de outras chaves de rota e a nova tentativa
vai na hora para a primeira cujo provedor ainda tem cota, no mesmo
worktree, então o trabalho feito é mantido. O aviso do claude de que a
janela esgotou também conta. O orch lembra o provedor esgotado até o reset
(a hora que o claude informa, senão o backoff de rate limit), e as tasks
seguintes dessa rota na mesma execução vão direto para o failover. Gasto,
eventos e dashboard mostram o backend que realmente rodou. Uma chave que
não é rota faz `orch validate` falhar.

```yaml
claude/claude-sonnet-4-6:
  backend: claude
  cli_model: claude-sonnet-4-6
  tier: standard
  failover: [codex/gpt-5.4, gemini/gemini-2.5-flash]
```

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
`.cursor/rules/<name>.mdc`. Oito skills vêm embutidos no binário: `orch-tdd`,
como um agente trabalha uma tarefa com os testes primeiro (vermelho, verde,
limpar); `orch-e2e`, como um agente percorre o app rodando num navegador e
deixa specs do Playwright; `orch`, o manual de operação para um agente dentro de um projeto, e o pipeline de
planejamento — `orch-plan` executa `orch-prd` (ideia → `docs/prd/`),
`orch-arch` (→ `docs/arch/`), `orch-spec` (→ `specs/`, no formato exato
que `orch atomize` lê) e `orch-tasks` (→ `tasks.json` via
`orch atomize --apply`), parando para você depois de cada documento e
antes de aplicar qualquer coisa. A página Sistema › Skills do dashboard tem
uma aba **Como funciona** que percorre esses passos — cada skill, o que lê e o
que escreve, o comando que a inicia — e marca as que nenhuma CLI tem
instaladas ainda.

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

Ele também é um **relatório de prontidão**, em cinco seções — Config
(config, router, tasks.json, modelos), Backends, Tasks, Permissions,
Environment — e termina com um veredito: ``Ready to `orch run`.`` ou a primeira
coisa no caminho. Tasks diz quais tarefas o `orch run` despacharia agora e
lista as tarefas em `backlog` com as dependências concluídas, com
`orch task-status <id> todo` para cada uma. Permissions tem um check
`<cli>.permissions` por CLI que o projeto usa: se o agente dela consegue
reportar uma tarefa concluída (as regras do claude e do agy, o `--add-dir` com
que o codex recebe o diretório de estado). `orch doctor --json` traz a
`section` de cada check e o `verdict`.

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
