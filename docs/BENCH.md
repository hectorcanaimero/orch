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
`--out` also writes the JSON to a file. Per run:

| field | meaning |
|---|---|
| `outcome` | `finished`, `stopped: max-usd`, `exit N` (the run ended with that code, e.g. 130 after a signal) or `error: …` |
| `tasks`, `done`, `blocked`, `unfinished` | task counts in the copy when the run ended |
| `dispatches`, `retries` | spend rows recorded, and attempts beyond each task's first |
| `wall_s`, `agent_s` | the run's elapsed time, and the sum of every dispatch's duration |
| `cost_usd`, `cost_source` | what the CLI reported plus the `pricing.yaml` estimate for rows it did not price; `reported`, `estimated`, `reported+estimated` or `no_data` |
| `tokens_in`, `tokens_out`, `cache_read_tokens`, `cache_creation_tokens` | as the CLIs reported them |
| `weighted_tokens` | the same tokens as the budget window counts them (cache reads at 10%, cache writes at 125%) |
| `cli_version` | the provider CLI's `--version` |

The report also records the orch version, OS, architecture, date, project and
cap. Numbers from one run are noisy; use `--runs` before drawing conclusions.
