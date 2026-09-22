# Dogfooding — agents report problems with orch

Every agent running under orch will notice bugs, missing features and quirks
of orch itself. The MCP tool **`orch_report_finding`** is the paved path from
"the agent noticed something" to "a GitHub issue on the orch repo", without
the operator copying it out by hand.

It is one tool call, not a queue: there is no local capture, review or
publish step, and no `orch findings` command. The Python line had one
(`orch findings capture|list|review|publish|dismiss`); it was not ported, and
its `findings:` config block is ignored with a warning (see
[`CONFIG.md`](CONFIG.md#keys-that-are-ignored)).

---

## On in new projects

It files **public issues under your `gh` login**, so it is on only where
someone chose it. `orch init` writes, in `.orchestrator/config.yaml`:

```yaml
report_findings:
  enabled: true
```

with a comment saying the issues are public and under whose login. The
wizard asks before writing it; batch mode prints a notice at the end;
`orch init --no-report-findings` writes `enabled: false` instead. A config
without the block — every project scaffolded before this, or one you wrote by
hand — is **off**: upgrading orch never turns it on. To turn it off later, set
`enabled: false`; to turn it on in an older project, add the block above.

It also needs:

- orch's MCP server loaded by the agent CLI. `orch init` writes `.mcp.json`
  for that; `orch doctor`'s `mcp.config` check says when it is missing (see
  [`MCP.md`](MCP.md#setup)).
- `gh` installed and authenticated (`gh auth status`). The tool runs it from
  the MCP server process, so it works even when the agent's own Bash is
  denied.

With the key off, the tool is still listed, and a call answers with an error
telling the agent to tell the operator instead.

---

## What agents are asked

With `report_findings.enabled: true`, every dispatch prompt ends with an
optional block, after the report-back step:

```
Feedback about orch itself (optional, after you report back):
- If orch got in your way, or you noticed something orch should do better or does
  not do at all (a missing command or flag, a step you had to do by hand, a
  confusing message or status), report it once with orch_report_finding:
  type "bug" | "improvement" | "feature", title, summary.
- It is about orch, not this project: no secrets, no project code, no project or
  customer names. Skip it if nothing comes to mind.
```

With the key off, the prompt is unchanged. An agent you run outside `orch run`
(your own sub-agents, an interactive session in the project) can call the
tool too, as long as it has orch's MCP server.

---

## The call

`{type, title, summary, evidence?, repro?, suggested_fix?, confidence?, confirm_new?}`

- **`type`** — `bug` (orch misbehaves; include a repro), `improvement` (a
  quality-of-life correction, not a full feature) or `feature` (something
  orch does not do).
- **`title`** — one line naming the problem or the idea.
- **`summary`** — what happens and what should happen instead.
- **`evidence`**, **`repro`**, **`suggested_fix`** — optional sections of the
  issue body: log lines, command output, `file:line` in orch, minimal steps.
- **`confidence`** — `high`, `medium` or `low`, for whoever triages it. If
  the agent is guessing, `low`. Recorded, not enforced.

It is **only about orch**. A problem in the project's own code belongs on the
project's tracker; the tool always files on `hectorcanaimero/orch`.

---

## What the tool enforces

| Check | Behavior |
|---|---|
| `report_findings.enabled` off | Error; nothing filed |
| `type` not bug / improvement / feature | Error; nothing filed |
| `title` or `summary` empty | Error; nothing filed |
| An `auto-reported` issue (open or closed) with the same title, ignoring case and punctuation | Returns `duplicate`; nothing filed |
| Other `auto-reported` issues match the title search | Returns `similar`; nothing filed unless the call sets `confirm_new: true` |
| Project root or home directory in the title or body | Replaced with `<project>` and `~` |

Every filed issue gets the `auto-reported` label. `orch sync issues` refuses
to ingest that label, so a report never loops back into a project as a task.
The body records the type, confidence, capture time, orch version and OS, and
ends with a line saying it came through `orch_report_finding`.

The result is `{filed, url?, duplicate?, similar?, message}`.

Nothing else is filtered: the redaction knows the two paths, not what else in
your project is private. That is why the prompt and the tool's description
ask for no secrets, project code or names.

---

## For the operator

- **See what was filed**:
  `gh issue list --repo hectorcanaimero/orch --label auto-reported --author @me`.
- **Stop it**: set `report_findings.enabled: false`. `orch run` and `orch mcp`
  read the key when they start, so the next run's prompts drop the block and
  the next MCP server refuses the call.
- **Report by hand**: `gh issue create --repo hectorcanaimero/orch` works the
  same without the tool.

The tool's reference, with the rest of the MCP surface, is in
[`MCP.md`](MCP.md#orch_report_finding).
