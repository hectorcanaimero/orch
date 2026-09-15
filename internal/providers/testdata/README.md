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

## `claude/2.1.272/` — real capture

Captured on 2026-09-15 on this VPS in an empty directory, with the argv
`orch ci review` runs claude with (`ci.ReadOnlyArgv`: the adapter's flags with
`--permission-mode default --tools "" --strict-mcp-config` in place of
`acceptEdits`), prompt on stdin:

```
claude -p --output-format json --model claude-haiku-4-5-20251001 \
    --add-dir . --permission-mode default --tools "" --strict-mcp-config < prompt.md
```

| File | How it was produced | Exit |
|---|---|---|
| `review-verdict.json` | the prompt `ci.BuildPrompt` builds from the built-in checklist and a two-commit demo repository whose second commit adds a `Load` that drops `os.ReadFile`'s error | 0 |

Its `result` is the verdict JSON **inside a ` ```json ` fence**, not bare, even
though the prompt asks for JSON only. That is what `ci.ParseVerdict` has to see
through, and why the cli end-to-end test replays this file rather than a
hand-written answer.

## `claude/2.1.270/` — real capture

Captured on 2026-09-14 on this VPS in a fresh `git init` directory, with
`ClaudeProvider.Argv`'s flags plus `--setting-sources project` (so the
operator's own allow-list could not let the calls through) and
`--no-session-persistence`, prompt on stdin:

```
printf '<prompt>' | claude -p --output-format json \
    --model claude-haiku-4-5-20251001 --add-dir . \
    --permission-mode acceptEdits \
    --setting-sources project --no-session-persistence
```

| File | How it was produced | Exit |
|---|---|---|
| `permission-denials.json` | prompt: run exactly `curl -sI https://example.com` with Bash, then WebFetch `https://example.com`, no alternatives. Both refused (Bash twice); `subtype` still `success` (#231) | 0 |

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
| `success.jsonl` | prompt `Reply with exactly: ok`, **no `-m`** | 0 |
| `unknown-model.jsonl` | same, `-m no-such-model-xyz` | 1 |
| `auth-error.jsonl` | captured before login, so every request 401s | 1 |
| `truncated.jsonl` | `success.jsonl` cut at 260 bytes | — |

`success.jsonl` was captured **without `-m`**, which is the one place these
bytes and `CodexProvider.Argv` differ. Every model name in
`model_router.yaml`'s codex routes is refused by this account — *"The
'gpt-5.4' model is not supported when using Codex with a ChatGPT account"* —
so the choice was a real success on the account default or no real success at
all. Nothing in the parser reads the model, so the envelope is the same one a
routed dispatch produces; the router's codex ids are a separate problem,
recorded in the migration notes.

`auth-error.jsonl` pins something no hand-written fixture would have: the
capture carries **plain-text stderr interleaved among the JSONL**
(`2026-…Z ERROR codex_api::endpoint::responses_websocket: …`). That is what
`jsonlEvents` skipping unparseable lines is for; before this file, the only
thing exercising it was a truncated final line.

## `opencode/1.18.30/` — real capture

Captured on 2026-09-12, prompt on stdin:

```
printf '<prompt>' | opencode run --format json --model <model> --auto --dir <abs>
```

| File | How it was produced | Exit |
|---|---|---|
| `success.json` | `--model opencode/mimo-v2.5-free` | 0 |
| `unknown-model.json` | `--model no-such-model-xyz` | 1 |
| `unknown-model-prefixed.json` | `--model deepseek/deepseek-v4-flash` | 1 |
| `insufficient-balance.json` | `--model opencode-go/deepseek-v4-flash`, no credit | 1 |

`unknown-model-prefixed.json` is the evidence behind the router note: the
`deepseek/…` spelling `model_router.yaml` ships is refused even with the
account authenticated, and `opencode models` serves those DeepSeek models
under `opencode-go/`.

`insufficient-balance.json` is the only fixture here whose error carries an
HTTP status (`401`), which is what makes it land on `FailurePermission`.
Redacted: the workspace id in the billing URL was replaced with
`wrk_REDACTED`, for the same reason as the gemini path below.

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
| `success.log` | `GEMINI_CLI_TRUST_WORKSPACE=true`, model `gemini-2.5-flash` | 0 |
| `untrusted-directory.log` | the same command **without** that variable | **55** |
| `auth-error.log` | captured before login | **41** |

The exit codes are the point. Nothing should key on "1 means failure" for
gemini, and `GeminiProvider.Parse` reads success as `exit == 0` for exactly
this reason.

`untrusted-directory.log` is the important one, and the reason `success.log`
needed an environment variable to exist at all: gemini refuses to run in a
directory it has not been told to trust, and orch dispatches into a fresh git
worktree every time. Neither this adapter nor `dispatcher.py` passes
`--skip-trust`. See bug 30 in the migration notes — the fix is one flag, but
it turns off a security gate on a CLI orch runs unattended, so it is somebody's
decision and not a drive-by.

`success.log` opens with `Ripgrep is not available. Falling back to GrepTool.`
before the answer. A hand-written fixture would have been the answer alone and
would have hidden that gemini's stdout is a conversation, not a value.

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
| `success.json` | model `gemini-3.7-flash-medium` | 0 |
| `unknown-model.json` | model `no-such-model-xyz` | 1 |
| `auth-error.json` | captured before login; the OAuth wait timed out at 60s | 1 |

Despite the name, `auth-error.json` is **not** pure JSON: agy writes an OAuth
prompt and a waiting line first, and only then the envelope. That is the whole
reason to keep it — it is the fixture behind bug 29, and
`TestAgyParseRealAuthError` asserts the preamble is still there so a later
clean re-capture cannot silently turn that test into a test of nothing.

`success.json` spent 24 thinking tokens and still answered, which makes it the
counter-example the issue-86 carve-out needs: thinking tokens alone are not
absorption, an empty `response` alongside them is.

`unknown-model.json` answers with the list of models agy does accept — and
`Parse` throws that list away, because a non-SUCCESS status renders as
`agy status=ERROR` and nothing reads the `error` field. Ported as-is from
Python; noted because the discarded text is what an operator needs.

---

## What is left synthetic, and why

Every success path in this tree is now a real capture. What remains
hand-written is the set of states that **cannot be provoked on demand**:

| File | Why it cannot be captured |
|---|---|
| `claude/synthetic/rate-limit.json`, `codex/synthetic/rate-limit.jsonl` | a 429 needs the API hammered until it complains |
| `claude/synthetic/auth-error.json` | kept byte-for-byte from the Python suite, which has asserted on it for sprints |
| `codex/synthetic/nonfatal-warning.jsonl` | needs a run that emits a warning AND then succeeds |
| `codex/synthetic/killed-empty.jsonl` | zero bytes; there is nothing to capture |
| `opencode/synthetic/no-usage.json`, `aborted.json` | need a provider that reports no usage, and a cancelled run |
| `agy/synthetic/empty-thinking.json` | needs a model to spend its whole budget on reasoning |

Two of those are worth a real capture if the chance ever comes up.
`codex/synthetic/nonfatal-warning.jsonl` is the sharper one: the real
`codex/0.154.0/unknown-model.jsonl` shows codex emitting
`"Model metadata for X not found. Defaulting to fallback metadata"` as an
`item.completed` error, which the parser treats as fatal because it is not in
`codexNonFatalWarnings`. In that capture the run failed anyway, so it proves
nothing on its own — but a run that emits that warning and *then succeeds*
would be reported as a failure. Deliberately not "fixed" by adding the marker,
because no capture yet shows codex carrying on after it. See bug 31.

If you do capture one, drop the bytes in a **new** version directory and
delete the synthetic file it replaces. Do not edit a synthetic file to match a
real capture: the two say different things about how much the test is worth.
