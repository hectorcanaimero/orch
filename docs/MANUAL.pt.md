# orch — Manual de uso passo a passo

> Também disponível em: [English](MANUAL.en.md) · [Español](MANUAL.es.md)

Este manual assume que você usa **Claude Code** (ou qualquer CLI equivalente
com suporte a skills). O fluxo completo é: **você conversa a feature com o
Claude → o Claude gera o spec no formato do orch → o orch atomiza para
`tasks.json` → o orch executa → você acompanha pelo dashboard**.

**Tempo total de setup**: ~5 minutos.
**Tempo por feature nova**: ~2 minutos de chat + execução desassistida.

---

## Índice

1. [Setup inicial (uma vez por máquina)](#1-setup-inicial-uma-vez-por-máquina)
2. [Criar um projeto novo (uma vez por projeto)](#2-criar-um-projeto-novo-uma-vez-por-projeto)
3. [Conversar a feature no Claude Code](#3-conversar-a-feature-no-claude-code)
4. [Como as tasks são geradas](#4-como-as-tasks-são-geradas)
5. [Preview antes de executar (sempre)](#5-preview-antes-de-executar-sempre)
6. [Executar em modo auto](#6-executar-em-modo-auto)
7. [Abrir o dashboard](#7-abrir-o-dashboard)
8. [O que observar durante a execução](#8-o-que-observar-durante-a-execução)
9. [Quando algo falha](#9-quando-algo-falha)
10. [Atualizar o orch](#10-atualizar-o-orch)

---

## 1. Setup inicial (uma vez por máquina)

### Instalar o orch

```bash
# Recomendado: venv isolado, `orch` no PATH globalmente
pipx install git+https://github.com/hectorcanaimero/orch.git

# Verifique
orch --help
orch init --help
orch dashboard --help
```

### CLIs de AI necessárias

Pelo menos **uma** dessas três no seu PATH, autenticada com assinatura ou
API key:

- **`claude`** — Claude Code CLI (Anthropic)
- **`codex`** — GPT Codex CLI (OpenAI)
- **`opencode`** — opencode CLI (multi-provider: DeepSeek, Grok, GLM, MiMo, etc.)

Confirme:

```bash
which claude codex opencode
claude --version
```

Se alguma faltar, o orch continua funcionando — ele só vai encaminhar tasks
para os backends que você tem instalados. Mas se sua spec pede
`claude-opus-4-7` e você não tem `claude` instalado, o orch morre no startup
com exit 1.

### SDD skills (opcional mas recomendado)

Verifique que as skills estão instaladas em `~/.claude/skills/`:

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

Se você não tem, ainda dá pra usar o orch escrevendo specs à mão (veja
[`SPEC-FORMAT.md`](SPEC-FORMAT.md)) — mas o fluxo com SDD é muito mais
fluido.

---

## 2. Criar um projeto novo (uma vez por projeto)

```bash
orch init ~/work/meu-app --sdd
```

Isso cria:

```
~/work/meu-app/
├── tasks.json                    ← esqueleto vazio
├── specs/                        ← aqui vão as specs escritas pelo Claude
│   └── README.md                 ← referência do formato
├── scripts/
│   ├── task-start.sh             ← executáveis, funcionais, usam jq
│   ├── task-finish.sh
│   └── task-block.sh
├── orchestrator/
│   ├── state/.gitkeep            ← runtime state (gitignored)
│   ├── config.yaml               ← concurrency, timeouts, retries
│   ├── model_router.yaml         ← mapeamento modelos → CLIs
│   └── budgets.yaml              ← guardrails do Sprint 7
├── openspec/                     ← SDD (por causa da flag --sdd)
│   ├── README.md
│   ├── changes/                  ← proposals em andamento
│   └── specs/                    ← specs arquivadas (source of truth)
└── .gitignore
```

No final do init aparece se o SDD está instalado e o que fazer em seguida:

```
✓ orch project initialized at /Users/voce/work/meu-app

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

## 3. Conversar a feature no Claude Code

Aqui está a mágica. Abra o Claude Code **dentro do diretório do projeto**:

```bash
cd ~/work/meu-app
claude
```

Agora você tem três formas de desenhar a feature, da mais para a menos
"hands-off":

### Opção A — Pipeline completo com `/orch-plan` (recomendado para features grandes)

No chat do Claude, escreva:

```
/orch-plan quero adicionar autenticação com email/senha + Google + Apple,
com recuperação de senha por email e verificação de conta
```

O Claude vai rodar internamente:

1. **`/orch-prd`** — gera um PRD (Product Requirements Document) com
   frontmatter YAML no formato do orch
2. **`/orch-arch`** — gera o ARCH técnico (Architecture Design)
3. **`/orch-spec`** — gera specs no formato F<n>.<pkg>.T<n> (o que o
   `orch atomize` sabe parsear)
4. **`/orch-tasks`** — invoca `orch atomize` em **modo diff-first**: mostra
   quais tasks NOVAS serão adicionadas ao seu `tasks.json` e **pede
   confirmação antes de escrever**

Saída esperada (o Claude vai imprimindo progressivamente):

```
[/orch-plan] Gerando PRD para "auth email+google+apple"...
✓ openspec/changes/auth/prd.md

[/orch-plan] Gerando ARCH...
✓ openspec/changes/auth/arch.md
  Módulos: 3 packages novos (auth_core, auth_google, auth_apple)
  Decisões-chave: Riverpod para state, GoRouter para deep links

[/orch-plan] Gerando SPEC pronto para o atomizer...
✓ openspec/changes/auth/spec.md
  15 tasks geradas:
    - F1.1.T1: Setup do package auth_core
    - F1.1.T2: Domain: entities (User, Credentials, ...)
    - F1.1.T3: Data: AuthRepository interface
    ...

[/orch-tasks] Diff contra tasks.json atual:
  + 15 tasks novas na fase 1
  Modelos usados: claude-sonnet-4-6 (10), opencode-go/glm-5.1 (3), claude-haiku-4-5 (2)
  Estimativa total: 24h

Aplicar? [y/N]
```

Você digita `y` e o Claude executa o `orch atomize` real. **`tasks.json`
fica atualizado** com as 15 tasks novas, status `backlog`, dependências
corretas, arquivos declarados, modelo atribuído.

### Opção B — Granular com `/orch-spec` (quando o design já está claro)

Se você já sabe O QUE precisa ser feito e só quer que o Claude monte o
spec no formato correto:

```
/orch-spec

Quero um package Flutter chamado auth_core com:
- Domain: entities User, Credentials, AuthMethod
- Data: AuthRepositoryImpl usando supabase.auth
- Presentation: AuthController com Riverpod
- 3 use cases: signIn, signUp, resetPassword

Modelos: claude-sonnet-4-6 para tudo que for domain/data, opencode para
testes puros e boilerplate.

Estimativa total: ~8h.
```

O Claude devolve um spec pronto para o atomizer. Depois:

```
/orch-tasks
```

E merge no `tasks.json`.

### Opção C — Manual (quando você quer controle total)

Edite `specs/minha-feature.md` à mão seguindo o formato:

```markdown
# F1 — Auth

## F1.1 — Package: auth_core

### F1.1.T1 — Setup do package

- **Model**: opencode-go/glm-5.1
- **Estimate**: 30m
- **Reason**: Boilerplate simples.
- **Dependencies**:
- **Files**:
  - `packages/auth_core/pubspec.yaml`
  - `packages/auth_core/lib/auth_core.dart`

### F1.1.T2 — Domain entities

- **Model**: claude-sonnet-4-6
- **Estimate**: 2h
- **Reason**: Desenho de tipos precisa de raciocínio.
- **Dependencies**: F1.1.T1
- **Files**:
  - `packages/auth_core/lib/src/domain/user.dart`
  - `packages/auth_core/lib/src/domain/credentials.dart`
```

E no terminal:

```bash
# Preview (dry-run — mostra o que será adicionado sem escrever)
orch atomize --file specs/minha-feature.md

# Apply — escreve tasks.json + cria backup tasks.json.bak-<ts>
orch atomize --file specs/minha-feature.md --apply
```

Os três fluxos terminam no mesmo lugar: **`tasks.json` com as tasks novas
em `status: backlog`, prontas para dispatch**.

---

## 4. Como as tasks são geradas

Quando `/orch-tasks` (ou `orch atomize` manual) roda, processa o spec e
gera entries assim no `tasks.json`:

```json
{
  "id": "F1.1.T2",
  "phase": 1,
  "title": "Domain entities",
  "description": "",
  "model": "claude-sonnet-4-6",
  "reason": "Desenho de tipos precisa de raciocínio.",
  "status": "backlog",
  "dependencies": ["F1.1.T1"],
  "estimateHours": 2.0,
  "files": [
    "packages/auth_core/lib/src/domain/user.dart",
    "packages/auth_core/lib/src/domain/credentials.dart"
  ],
  "specRef": "specs/minha-feature.md",
  "comments": []
}
```

O status começa em `backlog` (default do atomizer). O main loop do `orch`
promove `backlog` → `todo` quando as dependências estão OK, depois `todo` →
`in-progress` no momento do dispatch.

**Garantias do atomizer:**

- **Idempotência**: rodar o atomize de novo com o mesmo spec NÃO toca em
  tasks existentes. Só adiciona IDs que não estavam lá.
- **Validação de modelo**: se o modelo declarado não existe em
  `model_router.yaml`, o orch morre no startup com exit 1 e diz qual é o
  culpado.
- **Deps preservadas as-is**: não valida que existam (você pode declarar
  deps para tasks que serão adicionadas depois).

**O que NÃO garante:**

- Que os `files` sejam únicos entre tasks (duas tasks podem declarar o mesmo
  arquivo → o orch usa `per_file: 1` do `config.yaml` para dispatchar UMA
  de cada vez sobre esse arquivo).
- Que o DAG não tenha ciclos (o orch detecta no startup com exit 1).

---

## 5. Preview antes de executar (sempre)

**Nunca execute `--mode auto` sem ver o plano primeiro.** O dry-run é
grátis e mostra exatamente o que vai acontecer:

```bash
orch --project-root ~/work/meu-app --dry-run
```

Saída:

```
==== ORCH DRY RUN ====
Project: meu-app
Ready tasks: 15
Blocked tasks: 0
Deferred (semi-mode critical): 0

Plan (dispatch order):
  Wave 1 (parallel, no deps):
    F1.1.T1  [opencode/glm-5.1]     Setup do package auth_core           0.5h
    F1.2.T1  [opencode/glm-5.1]     Setup do package auth_google         0.5h
    F1.3.T1  [opencode/glm-5.1]     Setup do package auth_apple          0.5h

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

Se algo não bater — task com modelo errado, arquivo estranho, deps
incorretas — **este é o momento de editar a spec e re-atomizar**.

---

## 6. Executar em modo auto

Quando o plano bater, dispatch:

```bash
# Modo auto — sem prompts, dispatcha tudo
orch --project-root ~/work/meu-app --mode auto

# Ou com preset de budget mais agressivo se quiser max throughput
orch --project-root ~/work/meu-app --mode auto --budgets-preset aggressive

# Ou semi mode — pergunta antes de tasks marcadas como "critical"
orch --project-root ~/work/meu-app --mode semi
```

**O que você vê no terminal** (auto mode):

```
2026-08-21 14:30:00 INFO project_root=~/work/meu-app project_id=meu-app config=orchestrator/config.yaml
2026-08-21 14:30:00 INFO budget gate enabled: preset=conservative providers=['claude', 'codex', 'opencode']
2026-08-21 14:30:00 INFO 15 tasks todo, 0 in-flight, 0 done
2026-08-21 14:30:01 INFO dispatch F1.1.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:30:01 INFO dispatch F1.2.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:30:01 INFO dispatch F1.3.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:32:15 INFO success F1.1.T1 (2m14s, 4.2K tokens, $0.001)
2026-08-21 14:32:16 INFO dispatch F1.1.T2 → claude/sonnet-4-6 (attempt 1)
...
```

**Ctrl-C** = drain graceful (espera os in-flight terminarem antes de
sair). **Ctrl-C duas vezes** = force kill.

**Você pode deixar rodando desassistido.** O budget gate garante que não
queime sua assinatura:

- Quando `claude` chega a 60% (threshold do preset conservative) → pausa
  dispatches pro claude, continua com codex/opencode
- Quando TODOS os providers estão capped → dorme até o próximo reset
  (chunks de 30s pra Ctrl-C continuar respondendo)
- No reset → resume automático, continua de onde parou

---

## 7. Abrir o dashboard

**Em OUTRO terminal** (deixando `orch --mode auto` rodando no primeiro):

```bash
orch dashboard --project-root ~/work/meu-app
```

Saída:

```
INFO:     Started server process
INFO:     Waiting for application startup.
INFO:     Application startup complete.
INFO:     Uvicorn running on http://127.0.0.1:7420
```

Abra no navegador:

```bash
open http://127.0.0.1:7420
```

### O que você vê no dashboard

**Home (`/`)** — Tabela estilo Jira com TODAS as tasks:
- Colunas: ID / Title / Phase / Status / Model / Files / Owner
- Filtros por phase, status, model
- Live-update via SSE (não precisa refresh)
- Click numa task → modal com detalhes (deps, comments, últimos logs)

**Kanban (`/kanban`)** — Vista estilo Trello agrupada por fase:
- Colunas: backlog / todo / in-progress / done / blocked
- Cards com model + estimate + progress
- Cores por criticidade
- Drag & drop DESABILITADO (read-only por segurança)

**Metrics (`/metrics`)** — Custo e burndown:
- Total gasto (USD) por dia
- Por modelo (barras)
- Burndown chart (tasks restantes vs tempo)
- Critical path (cadeia mais longa de deps)

**Logs (`/logs`)** — Feed live de eventos:
- SSE stream, atualiza em tempo real
- Filtrável por task-id ou event_type
- Mostra: dispatch, success, fail, timeout, retry, budget_skip, budget_pause

**Budgets (`/api/budgets` — endpoint JSON ou olhar a barra)**:
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

Na UI: 3 barras horizontais por provider:
- Verde 0-60% → OK
- Âmbar 60-80% → warning
- Vermelho 80-100% → PAUSED, com countdown pro próximo reset

---

## 8. O que observar durante a execução

**Cheat sheet de "está tudo bem":**

| Sinal | Onde | Significado |
|---|---|---|
| Eventos `success` consistentes | `/logs` | Tasks completando OK |
| Barras de budget ≤ 60% verdes | `/api/budgets` | Consumo saudável |
| Kanban avança left → right | `/kanban` | Progresso normal |
| Cost/hour razoável | `/metrics` | Sem runaway costs |

**Sinais de alerta:**

| Sinal | Onde | Ação |
|---|---|---|
| Vários `retry` seguidos | `/logs` | Pode ser rate-limit — cheque o backend |
| Eventos `budget_pause` | `/logs` | Todos os providers capped, vai esperar reset |
| Task fica em `in-progress` muito tempo | `/kanban` | Pode estar travada — cheque o log |
| Eventos `timeout` | `/logs` | Ajuste `default_timeout_multiplier` no config.yaml |
| Tasks `blocked` acumulando | `/kanban` | Cheque `state/logs/<task>.log` |

**Comandos úteis num terminal paralelo:**

```bash
# Live tail de todos os eventos
tail -f ~/work/meu-app/orchestrator/state/events-*.jsonl | jq -r '"\(.ts | .[11:19])  \(.event_type|ascii_upcase)  \(.task_id)  \(.backend)"'

# Log de UMA task específica
tail -f ~/work/meu-app/orchestrator/state/logs/F1.1.T2.log

# Status snapshot (usa jq)
./status.sh ~/work/meu-app
```

---

## 9. Quando algo falha

### Task blocked

1. Abra o modal da task no dashboard → veja o último comment
2. Terminal: `cat ~/work/meu-app/orchestrator/state/logs/<task-id>.log | tail -100`
3. Edite a spec ou o código à mão conforme necessário
4. Marque a task como `todo` de novo:
   ```bash
   jq --arg id "F1.1.T5" '(.tasks[] | select(.id == $id) | .status) = "todo"' \
      tasks.json > tasks.json.tmp && mv tasks.json.tmp tasks.json
   ```
5. Rode `orch --mode auto` de novo — só pega as `todo`

### Budget capped mais rápido que o esperado

1. Verifique o consumo real: `/api/budgets` no dashboard
2. Se o `token_budget` em `budgets.yaml` está mal calibrado, aumente (ou
   baixe `threshold_pct` para mais margem)
3. Mudanças em `budgets.yaml` são pegas na próxima corrida — não precisa
   restart se for o mesmo run

### Rate limit do provider

Diferente do budget gate — é a CLI real te tirando 429.

1. o orch detecta a falha e faz **retry-once com backoff estendido** (60s
   default para rate limits, configurável em
   `config.yaml → retry.rate_limit_backoff_seconds`)
2. Se continuar falhando, a task fica `blocked` com o erro
3. Solução típica: esperar a janela de reset (~5h Anthropic, ~3h OpenAI) e
   `orch --mode auto` de novo

### `orch` não sobe — exit 1 com "unrouted model"

Sua spec pede um modelo que não está em `model_router.yaml`. O erro te
diz qual:

```
UnroutedModelError: task F1.1.T2 uses model 'claude-opus-5-0' which is not in router
```

Edite `orchestrator/model_router.yaml`, adicione:

```yaml
"claude-opus-5-0":
  backend: claude
  cli_model: claude-opus-5-0
  tier: premium
  is_premium: true
```

E retry.

### `orch` não sobe — exit 2 com "project layout invalid"

Falta `tasks.json` ou `scripts/task-*.sh`. Rode `orch init --force` se é
projeto novo, ou crie o que falta à mão.

### `orch` não sobe — exit 3 com "flock contention"

Já tem OUTRA instância de `orch` rodando contra o mesmo `state/`. Cheque:

```bash
lsof ~/work/meu-app/orchestrator/state/.lock
```

Se for instância zumbi, kill no PID. Se for intencional (dois runs em
paralelo), use `--task-locks` nos dois.

---

## 10. Atualizar o orch

Quando eu (ou você) push mudanças para o repo:

```bash
# Try upgrade first
pipx upgrade orchestrator

# Se disser "already up to date" mas você sabe que tem mudanças, force:
pipx install --force git+https://github.com/hectorcanaimero/orch.git

# Verifique qual versão você está rodando
orch --help | head -3
```

**Atenção**: `pipx upgrade` NÃO toca nos YAMLs que já foram copiados pros
seus projetos (`~/work/meu-app/orchestrator/*.yaml`). Se quiser os novos
defaults num projeto antigo:

```bash
# Diff primeiro
diff ~/work/meu-app/orchestrator/config.yaml \
     $(python3 -c 'import orchestrator, pathlib; print(pathlib.Path(orchestrator.__file__).parent)')/config.yaml

# Aplicar (⚠️ sobrescreve config custom do projeto)
orch init ~/work/meu-app --force
```

É intencional que overrides não sejam sobrescritos: se você tunou
`budgets.yaml` para um projeto específico, não quer que um `upgrade` apague
tudo.

---

## Workflow completo — resumo visual

```
┌─────────────────────────────────────────────────────────────────┐
│  UMA VEZ POR MÁQUINA                                            │
│    pipx install git+https://github.com/hectorcanaimero/orch.git │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  UMA VEZ POR PROJETO                                            │
│    orch init ~/work/meu-app --sdd                               │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  POR FEATURE                                                    │
│                                                                 │
│  1. No Claude Code (dentro do projeto):                         │
│                                                                 │
│       /orch-plan quero adicionar auth com email + google        │
│                                                                 │
│  2. Claude gera PRD → ARCH → SPEC → propõe diff pro tasks.json  │
│     Você confirma com `y`                                       │
│                                                                 │
│  3. Preview:                                                    │
│       orch --project-root ~/work/meu-app --dry-run              │
│                                                                 │
│  4. Executar:                                                   │
│       orch --project-root ~/work/meu-app --mode auto            │
│                                                                 │
│  5. Em OUTRO terminal, dashboard:                               │
│       orch dashboard --project-root ~/work/meu-app              │
│       → http://127.0.0.1:7420                                   │
│                                                                 │
│  6. Café ☕                                                     │
└─────────────────────────────────────────────────────────────────┘
```

---

## Referências

- Formato de spec do atomizer: [`SPEC-FORMAT.md`](SPEC-FORMAT.md)
- História do projeto: [`history/README.md`](history/README.md)
- Config completa: [`../README.md#configuration`](../README.md#configuration)
- Sprint 7 (budget guardrails): [`../README.md#budget-guardrails-sprint-7`](../README.md#budget-guardrails-sprint-7)

## Feedback

Este manual é um documento vivo. Se encontrar um caso não coberto, abra
uma issue em <https://github.com/hectorcanaimero/orch/issues> ou mande PR
com a seção nova.
