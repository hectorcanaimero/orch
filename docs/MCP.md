# `orch mcp` — the MCP server

`orch mcp` serves orch's state to an MCP-capable agent over stdio. It is what
lets a dispatched agent ask questions instead of only reporting answers.

New in the Go port. The Python `orch` has no MCP server.

---

## Why

Until now the only channel from an agent back to orch was a shell-out:

```bash
scripts/task-finish.sh F1.T3 "added the health endpoint" "claude/claude-sonnet-4-6"
```

which runs `orch task-status F1.T3 done --author … --note …`. That works, and
it still works — but it is one-way. An agent can write a status and nothing
else. It cannot ask what its dependencies concluded, where its spec ref
actually lives, how much budget is left, or what the run has been doing. Every
one of those answers is already in `orch.db`; the scripts just have no way to
ask for it.

The seven tools below are that surface. The two that write keep the shape the
scripts already have, so a project can use either:

| script | tool |
|---|---|
| `scripts/task-start.sh ID` | `orch_set_status{task_id, status: "in-progress"}` |
| `scripts/task-finish.sh ID "summary" "author"` | `orch_set_status{task_id, status: "done", note, author}` |
| `scripts/task-block.sh ID "reason" "author"` | `orch_block{task_id, reason, author}` |

Since G6.6 the dispatch prompt names both, MCP first:

```
3. Report back once, through ONE of these two channels.
   If you have orch's MCP tools, use them:
     done:    orch_set_status  task_id "F1.T3", status "done", note "<what you did>", author "claude/claude-sonnet-4-6"
     blocked: orch_block       task_id "F1.T3", reason "<why>", author "claude/claude-sonnet-4-6"  — then STOP.
   If you do not have those tools, run the project's scripts instead:
     done:    scripts/task-finish.sh F1.T3 "<what you did>" "claude/claude-sonnet-4-6"
     blocked: scripts/task-block.sh F1.T3 "<why>" "claude/claude-sonnet-4-6"  — then STOP.
```

Both, rather than one chosen from config, because orch cannot know which the
agent got: `.mcp.json` on disk does not mean the agent CLI loaded it, and a
project scaffolded before G6.4 has none. Being wrong costs one tool-not-found
error the agent recovers from by reading the next line; guessing wrong costs a
task that cannot report at all.

---

## Setup

`orch init` writes `.mcp.json` at the project root:

```json
{
  "mcpServers": {
    "orch": {
      "command": "orch",
      "args": ["mcp"]
    }
  }
}
```

A project scaffolded before this existed can paste that block in — `orch
doctor`'s `mcp.config` check is what tells you it is missing. The file is
never overwritten by a later `orch init`, `--force` included: it is shared
with every other MCP server the project uses.

The server resolves the project from its working directory, which is the
directory holding `.mcp.json` for every client that launches a stdio server.
A client that does otherwise should pass the project explicitly:

```json
"args": ["mcp", "--project-root", "/path/to/project"]
```

`orch mcp` speaks JSON-RPC on stdin and stdout and writes nothing else to
stdout — anything it printed there would be framed as a protocol message.
Run it from a client, not from a terminal.

---

## The tools

### `orch_list_tasks`

Every task with its live status. Filters: `status` (a list), `ids` (a list),
`milestone`, `limit`, and `ready` — the tasks that could be dispatched right
now, which is `backlog` or `todo` with every dependency `done`, the same
question the dispatch loop asks.

Returns `tasks[]` plus `count` (rows returned) and `total` (rows matched), so
a truncated page is distinguishable from a complete one.

### `orch_get_task`

One task in full: description, `files[]`, `spec_ref`, `estimate_hours`,
dependencies, attempts, PR URL and CI status, the comment trail, and
`legal_transitions` — every status it may move to next.

### `orch_set_status`

`{task_id, status, note?, author?}`. An agent passes its model name, exactly as
the scripts do.

**`author` matters more than it looks.** It defaults to `agent`, deliberately
*not* to `orch` the way `orch task-status --author` does. `orch` is the author
on every note the engine writes — the dispatch marker, the reaper's `dispatch
succeeded`, the poller's `CI passed` — and that field is how orch tells a
report from its own bookkeeping when it renders a downstream task's
`Completed dependencies (context):` block. A note attributed to `orch` is read
as bookkeeping and never shown to the next agent.

**`note` is the next task's context**, not a log line: it is what every task
depending on this one gets rendered in its own prompt. "done" tells the next
agent nothing.

A move the transition table forbids is **not** a protocol error. It comes back
as a tool error whose structured payload names where the task is and where it
may go:

```json
{
  "ok": false,
  "task_id": "F0.T1",
  "from": "done",
  "to": "in-progress",
  "error": {
    "code": "illegal_transition",
    "message": "F0.T1 cannot move from done to in-progress",
    "from": "done",
    "valid_transitions": ["todo", "done"]
  }
}
```

Codes: `illegal_transition`, `unknown_task`, `invalid_status`,
`missing_required_argument`. The list is there because the caller is a model
that can retry, and a refusal without it is a dead end.

### `orch_block`

`{task_id, reason, author?}` — `orch_set_status` with the status fixed to
`blocked`. `reason` is required: a block nobody explained is a task nobody can
pick up.

### `orch_budget`

The rolling-window guardrail per provider: `tokens_used`, `token_budget`,
`usage_pct`, `threshold_pct`, `window_hours`, `capped`, `reset_at`. Same shape
the dashboard renders. `enabled: false` with an empty `providers` object when
the project has no `budgets.yaml` — see [`CONFIG.md`](CONFIG.md).

### `orch_events`

`{task_id?, limit?}` — the run's event log, oldest first. `limit` defaults to
20 (matching `orch events --tail`) and `0` means every event. Without
`task_id`, events across every task.

### `orch_context`

`{task_id?}`. Without a task: the project's `id`, `root`, `spec_root`, and
task counts per status (every status is a key, zeroes included).

With a task, it also returns what that task's dispatch prompt carries —
`description`, `files`, `spec_ref_path` (already joined to `spec_root`), its
finished `dependencies` **with the last thing each one reported**, and
`pending_dependencies`, the ids that are not done yet.

`last_comment` is chosen exactly as the prompt chooses it
(`prompt.AgentComment` — one implementation, two callers), so a tool call and a
prompt never disagree about what a dependency said. Untruncated here, where the
prompt caps it at 500 characters: a prompt is a fixed budget, a tool result is
fetched on demand.

---

## What it does not do

- **No dispatching.** Nothing here starts a subprocess or spends money. The
  engine is driven by `orch run`, and an agent that could dispatch other
  agents is a different product.
- **No writes outside the status.** Titles, descriptions, dependencies and
  estimates live in `tasks.json`, which a human or `orch atomize` owns.
- **No second view of the state.** The server shares one `state.Backend` with
  the engine, so an agent asking "am I still in-progress?" gets the run loop's
  answer and not a stale copy.

---

## Related

- [`CLI.md`](CLI.md) — every implemented subcommand, this one included
- [`CONFIG.md`](CONFIG.md) — `budgets.yaml`, `spec_root` and the rest
- [`PREFLIGHT.md`](PREFLIGHT.md) — what `orch doctor` checks, `mcp.config` included
