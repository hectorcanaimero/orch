# `internal/providers` fixtures

Every file here is input to a parser test. The directory name after the
backend is the **CLI version the bytes came from** — `claude/2.1.269/` is real
output from `claude 2.1.269`, `codex/synthetic/` is not real output at all.
Checklist rule 21 wants captured CLI output, so the distinction is spelled out
per file below rather than left to the reader.

Changing one byte of a file here should break exactly one test, and that
test's name should say which backend and which version drifted. If a fixture
needs updating because a CLI changed its format, add a **new** version
directory; do not edit an existing one. The old directory is the record of
what the parser used to have to cope with.

---

## `claude/2.1.269/` — real capture

Captured on 2026-09-11 on this VPS with the argv `ClaudeProvider.Argv`
builds, prompt on stdin, stdout and stderr merged into one stream (which is
what `orchestrator/dispatcher.py::_spawn_generic` does — `stderr=STDOUT`):

```
echo "<prompt>" | claude -p --output-format json \
    --model claude-haiku-4-5-20251001 \
    --session-id <uuid> --add-dir . --permission-mode acceptEdits
```

| File | How it was produced | Exit |
|---|---|---|
| `success.json` | prompt `Reply with exactly: ok`, model `claude-haiku-4-5-20251001` | 0 |
| `unrecognized-model.log` | same, `--model no-such-model-xyz` | 1 |
| `killed-empty.log` | a real run `SIGKILL`ed at ~6s via `killpg` | — |
| `truncated.json` | `success.json` cut at 900 bytes | — |

`unrecognized-model.log` is two streams merged: a bracketed
`[claude-code:unrecognized_model] {...}` line that the CLI writes to stderr,
then the JSON envelope on stdout. It is the fixture that proves
`parseEnvelope`'s last-non-empty-line fallback earns its keep — the whole blob
is not valid JSON.

`killed-empty.log` is intentionally **zero bytes**. That is what a timed-out
`claude` dispatch actually leaves behind: the CLI buffers the entire envelope
and writes it once at the end, so killing it mid-run yields no output at all,
not a half-written object. `truncated.json` covers the half-written case
anyway, because nothing guarantees that stays true.

### Why there is no real rate-limit capture

A 429 cannot be provoked on demand without hammering the API, so
`claude/synthetic/rate-limit.json` is **hand-written**. It reuses the envelope
shape from the real captures with `api_error_status` set to 429 and a
`terminal_reason` carrying the phrase. It exists to pin `Classify`, not the
envelope parser — the parser is pinned by the real files above.

`claude/synthetic/auth-error.json` is also **hand-written**: it is the
long-standing fixture from `orchestrator/tests/fixtures/dispatcher/claude_error.txt`,
kept byte-for-byte so the Go parser is held to the same envelope the Python
suite has been asserting on for several sprints. It is the only fixture where
`api_error_status` is an object rather than a number, which is the branch
where Python reaches for `err["message"]`.

---

---

## Where codex went

`CodexProvider` and its fixtures are not in this tree yet. They live on
`g2/opus2-codex` and stay there until the `codex` CLI can be installed and its
output captured for real: everything that could be written for it was written
against hand-made fixtures, and checklist rule 21 exists precisely because
that is the test which passes while the real CLI breaks. The risk is not a
mis-written parser — it is a schema that drifted since `dispatcher.py` was
written, and only a capture can show that.
