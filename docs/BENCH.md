# `orch bench`

`orch bench` runs the same project once with each provider and puts the
results in one table: how many tasks each finished, how long it took, and what
it cost.

**It costs real money.** Every run dispatches real agents, exactly as
`orch run` does, and each provider bills you for its run. Start with
`--dry-run` and a small project.

```bash
# What would run, and nothing else.
orch bench --dry-run --providers claude,codex --max-usd 3

# Two providers, $3 cap each, the packaged python-api template.
orch bench --providers claude,codex --max-usd 3 --yes --markdown --out bench.json

# Your own project, codex on a specific model, three runs each.
orch bench --project ./my-app --providers claude,codex --model codex=gpt-5.4 \
  --runs 3 --max-usd 5 --yes --out bench.json
```

## What happens to your project

Nothing. Each run works on a throwaway copy in the system temp directory,
deleted afterwards:

- `.git`, `node_modules` and `.orchestrator/state` are not copied, so the copy
  starts with an empty database.
- Every task that is not in the backlog is reset to `todo`.
- Every route in the copy's `model_router.yaml` is pointed at the provider
  being measured. Route keys and tiers stay; the model comes from a route
  that already uses that provider (same tier first), or from `--model`.
- A copy never uses worktrees, never pushes and never opens a pull request,
  whatever `dispatch.worktree_mode` and `vcs.auto_pr` say.

A project with an absolute `state.sqlite_path` is refused: the copy would
write to the real database.

## Spend limits

- `--max-usd` is required. Each run's spend is checked every 2 seconds and the
  run stops (draining like Ctrl-C) once it reaches the cap. A dispatch already
  running finishes, so a run can end one dispatch's cost above the cap.
- `--yes-i-know-it-costs` runs without a cap.
- The project's `budgets.yaml`, if it has one, keeps limiting each provider's
  token window as it does for `orch run`.
- Without `--yes`, bench asks at a terminal and refuses anywhere else.

## Results

JSON (`bench_version: 1`) on stdout, or a Markdown table with `--markdown`;
`--out` also writes the JSON to a file. The counts, times and costs are the
run's receipt — the same numbers `orch report receipt` prints for a run — so a
bench and a receipt of the same run cannot disagree. Per run:

| field | meaning |
|---|---|
| `outcome` | `finished`, `stopped: max-usd`, `exit N` (the run ended with that code, e.g. 130 after a signal) or `error: …` |
| `tasks`, `done`, `blocked`, `unfinished` | every task in the project; done and blocked by their last outcome in the run; the rest (backlog included) |
| `dispatches`, `failed_attempts`, `retries` | dispatch, fail/timeout and retry events of the run |
| `wall_s`, `agent_s` | from the run's first event to its end, and the sum of its dispatches' durations |
| `cost_usd`, `estimated_cost_usd`, `cost_source` | what the CLI reported plus the `pricing.yaml` estimate for rows it did not price, the estimated part, and `reported`, `estimated` or `no_data` |
| `tokens_in`, `tokens_out` | as the CLI reported them, cache included |
| `weighted_tokens` | the same tokens as the budget window counts them (cache reads at 10%, cache writes at 125%) |
| `cli_version` | the provider CLI's `--version` |

The report also records the orch version, OS, architecture, date, project and
cap. Numbers from one run are noisy; use `--runs` before drawing conclusions.
