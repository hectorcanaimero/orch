# gh 2.100.0 — real `gh pr checks` output

Captured on 2026-09-12 with `gh version 2.100.0` against hectorcanaimero/orch,
never edited (bug 27: the tests used to feed `{"state": "completed",
"conclusion": "success"}` — a field `gh pr checks` does not have and a state
value it never emits — so they were green for months over a command that
could not run).

- `pr-checks-all-pass.json` — `gh pr checks 209 --json name,state,bucket`
- `pr-checks-one-failure.json` — `gh pr checks 183 --json name,state,bucket`
  (its last head merged with `gemini-review` in FAILURE)
- `pr-checks-with-neutral.json` — `gh pr checks 207 --json name,state,bucket`
  (`automerge-eligible` NEUTRAL, bucket `skipping`)
- `pr-checks-conclusion-rejected.txt` — stderr of
  `gh pr checks 209 --json state,conclusion` (exit 1): the field does not exist.

`state` is uppercase (`SUCCESS`, `FAILURE`, `NEUTRAL`); `bucket` is gh's own
lowercase summary and is deliberately not what `get_ci_status` maps on, so the
Python and Go tables stay comparable.
