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

## `codex/0.154.0/` — real capture

Captured on 2026-09-12 on this VPS, prompt on stdin, streams merged:

```
printf '<prompt>' | codex exec --skip-git-repo-check --json \
    -o <out>.codex.json -C . --approve-for-me -m gpt-5.4
```

| File | How it was produced | Exit |
|---|---|---|
| `auth-error.jsonl` | no `~/.codex/auth.json`, so every request 401s | 1 |

Two things this file pins that no hand-written fixture would have:
`thread.started` / `turn.started` / `turn.failed` is the real 0.154.0 event
vocabulary, and the capture has **plain-text stderr interleaved among the
JSONL** (`2026-…Z ERROR codex_api::endpoint::responses_websocket: …`). That is
what `jsonlEvents` skipping unparseable lines is for; before this file, the
only thing exercising that was a truncated final line.

There is no real `success.jsonl`: the CLI is installed but not logged in. The
happy path is still `codex/synthetic/`.

## `opencode/1.18.30/` — real capture

Captured on 2026-09-12, prompt on stdin:

```
printf '<prompt>' | opencode run --format json --model <model> --auto --dir <abs>
```

| File | How it was produced | Exit |
|---|---|---|
| `success.json` | `--model opencode/mimo-v2.5-free` | 0 |
| `unknown-model.json` | `--model no-such-model-xyz` | 1 |

`success.json` is the one real happy path in this tree besides claude's, and
it exists only because opencode's free tier needs no credential — `opencode
models` lists 7 models on an unauthenticated machine and they all work. Its
`cost` is genuinely `0` while both token counts are non-zero, which is the
case that separates "this provider reports no usage" (Issue #8, `Estimated`)
from "this run was free".

`unknown-model.json` carries **two** error events: a generic `"Unexpected
server error"` and then the specific `"Model not found: …"`. Both the Python
parser and the Go port report the first. See bug 28 in
`docs/brainstorm/go-migration-notes/sonnet-2.md`.

## `gemini/0.59.0/` — real capture

```
gemini -p '<prompt>' --model gemini-2.5-flash
```

| File | How it was produced | Exit |
|---|---|---|
| `auth-error.log` | no `GEMINI_API_KEY`, no OAuth credentials | **41** |

The exit code is the point. Nothing should key on "1 means failure" for
gemini, and `GeminiProvider.Parse` reads success as `exit == 0` for exactly
this reason.

**The one redacted byte range in this tree.** gemini names the settings file
by absolute path, so the capture contained the operator's home directory;
`/home/<user>/` was rewritten to `/home/USER/` before committing, because the
reviewer checklist forbids personal paths. Nothing else in the line was
touched and no test asserts on the path. Flagged here rather than left for a
reader to notice, since "these bytes are what the CLI wrote" is the promise
this whole directory makes.

## `agy/1.2.1/` — real capture

```
agy --output-format json --agent executor --model <model> --print '<prompt>'
```

| File | How it was produced | Exit |
|---|---|---|
| `auth-error.json` | not logged in; the OAuth wait timed out after 60s | 1 |

Despite the name this file is **not** pure JSON: agy writes an OAuth prompt
and a waiting line first, and only then the envelope. That is the whole
reason to keep it — it is the fixture behind bug 29, and
`TestAgyParseRealAuthError` asserts the preamble is still there so a later
clean re-capture cannot silently turn that test into a test of nothing.

---

## The synthetic fixtures, and what would retire them

`codex/synthetic/`, `gemini/synthetic/`, `agy/synthetic/` and
`opencode/synthetic/` are hand-written. Everything under them is a **success
path or an edge case that cannot be provoked without credentials**, which is
the honest reading of checklist rule 21's limit: the rule wants captured
output, and a CLI that will not authenticate produces none.

Retiring them is a mechanical job and deliberately not a judgement call:

| Backend | What unblocks a real success capture |
|---|---|
| `codex` | `codex login` |
| `gemini` | `GEMINI_API_KEY`, or one of the two Google auth modes its error names |
| `agy` | the OAuth flow (interactive — a human has to paste the code) |
| `opencode` (paid providers) | credentials for the provider a route names |

Re-capture with the argv above, drop the bytes in a **new** version directory,
and delete the synthetic file the real one replaces. Do not edit a synthetic
file to match a real capture: the two say different things about how much the
test is worth.
