// Package prompt renders the per-dispatch prompt an agent receives.
//
// Ported from `orchestrator/prompt_builder.py`. The template body is contract
// with the agent, not a message to a human: the report-back lines name the
// only two channels that write a status, and the `TASK_ID=` marker on line 1
// is what the id-spoofing check reads (AS-10). Rewording any of it changes
// what agents do.
//
// # Two things the template does not do
//
// Specs are referenced by path and never inlined. They run 100-150 KB, and a
// dispatch that carried one would spend most of its context window before the
// agent read a word of the task.
//
// The prompt carries no phase, estimate or model reason. Those are the
// scheduler's business; an agent that knew its task was estimated at 0.3 hours
// would have an opinion about it.
//
// # MCP first, the scripts as fallback (G6.6)
//
// There is one template, not a variant switch. The protocol block offers the
// `orch_*` MCP tools first and `scripts/task-{finish,block}.sh` if the agent
// does not have them, because orch cannot know which it got: `.mcp.json` being
// on disk does not mean the agent CLI loaded it, and a project scaffolded
// before G6.4 has no `.mcp.json` at all. A variant chosen from config would be
// orch guessing at something the agent can simply look at. The cost of being
// wrong is one tool-not-found error the agent recovers from by reading the
// next line; the cost of guessing wrong is a task that cannot report at all.
//
// # Where the goldens stand now
//
// Everything from `TASK_ID=` through the `Spec ref (READ FIRST):` line is
// unchanged from Python and is still compared byte for byte against goldens
// Python rendered — see golden_test.go. The two blocks below it changed on
// purpose (the protocol, and a dependency block that finally has content), so
// they have Go goldens of their own and the Python ones are kept as the
// evidence for bug 24.
package prompt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// DefaultSpecRoot is where `specRef` values are resolved from when config.yaml
// does not say otherwise.
//
// It was `docs/rewrite-plan` until #102 — orch's own repository layout, which
// meant every scaffolded project pointed its agents at a directory that only
// existed in orch's source tree.
const DefaultSpecRoot = "specs"

// depCommentMaxChars caps a dependency's comment in the prompt.
//
// Characters, not bytes. Python slices a `str`, so the limit counts code
// points; a byte-wise port cuts a multi-byte character in half and hands the
// agent invalid UTF-8. `testdata/custom-spec-root.txt` has a 600-character
// comment of `ñ` for exactly this reason — at 500 bytes it would truncate to
// 250 characters and the golden would not match.
const depCommentMaxChars = 500

// Options are everything about a prompt that is not the task.
type Options struct {
	// RunID and StateDir decide where Write puts the file.
	RunID    string
	StateDir string
	// ProjectRoot fills the `Working dir:` line. Empty means the process's
	// current directory, matching Python's `Path.cwd()` fallback.
	ProjectRoot string
	// SpecRoot prefixes `specRef`. Empty means DefaultSpecRoot.
	SpecRoot string
}

// Render returns the prompt body and any warnings worth showing an operator.
//
// Warnings are returned rather than logged. Python logs them from inside the
// renderer, which puts a line in a log file nobody is reading at dispatch
// time; the caller here can put it where the operator is looking.
func Render(task model.Task, completedDeps []model.Task, specRef string, opts Options) (string, []string) {
	specRoot := opts.SpecRoot
	if specRoot == "" {
		specRoot = DefaultSpecRoot
	}

	workingDir := opts.ProjectRoot
	if workingDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			workingDir = cwd
		}
	}

	var warnings []string
	specLine := fmt.Sprintf("%s/%s", specRoot, specRef)
	if specRef == "" {
		specLine = "(no spec ref provided — proceed with the description only)"
		warnings = append(warnings,
			"prompt rendered without spec_ref — proceeding with description only")
	}

	return expand(template, map[string]string{
		"id":            task.ID,
		"title":         task.Title,
		"description":   task.Description,
		"files":         renderFiles(task.Files),
		"spec_ref_line": specLine,
		"deps_block":    renderDepsBlock(completedDeps),
		"model":         task.Model,
		"working_dir":   workingDir,
	}), warnings
}

// expand substitutes `{name}` placeholders in one left-to-right pass.
//
// Not a chain of strings.ReplaceAll, which would rescan its own output: a task
// whose description contained the literal text `{model}` would come out with
// the model name substituted into it, because `{description}` is replaced
// first. Python's `str.format` reads the template once and copies values in
// without looking at them again, and titles, descriptions and dependency
// comments are all free text written by whoever wrote tasks.json.
//
// An unknown `{name}` is left as written rather than dropped. The template is
// a const in this file so it cannot happen today; if it ever does, a visible
// `{typo}` in the prompt is a bug someone reports, and a silently empty line
// is one nobody notices.
func expand(tmpl string, vars map[string]string) string {
	var b strings.Builder
	b.Grow(len(tmpl))
	for i := 0; i < len(tmpl); {
		open := strings.IndexByte(tmpl[i:], '{')
		if open < 0 {
			b.WriteString(tmpl[i:])
			break
		}
		open += i
		b.WriteString(tmpl[i:open])
		closed := strings.IndexByte(tmpl[open:], '}')
		if closed < 0 {
			b.WriteString(tmpl[open:])
			break
		}
		closed += open
		name := tmpl[open+1 : closed]
		if value, ok := vars[name]; ok {
			b.WriteString(value)
		} else {
			b.WriteString(tmpl[open : closed+1])
		}
		i = closed + 1
	}
	return b.String()
}

// Write renders the prompt and stores it at
// `{StateDir}/prompts/{RunID}/{task.ID}.txt`, returning the path.
//
// The file is written even though delivery to the CLI is over stdin: after a
// failed run it is the only record of what the agent was actually told.
// Overwritten on re-render — one prompt per task per run.
func Write(task model.Task, completedDeps []model.Task, specRef string, opts Options) (string, []string, error) {
	body, warnings := Render(task, completedDeps, specRef, opts)

	dir := filepath.Join(opts.StateDir, "prompts", opts.RunID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", warnings, fmt.Errorf("create prompt dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, task.ID+".txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", warnings, fmt.Errorf("write prompt %s: %w", path, err)
	}
	return path, warnings, nil
}

// renderFiles is the `Files you may write:` value.
//
// A literal `[]` when the task names no files, and Python's list repr
// otherwise. The empty case is load-bearing: it tells the agent there is no
// strict-file guard on this task, which is a different statement from an
// omitted line.
func renderFiles(files []string) string {
	if len(files) == 0 {
		return "[]"
	}
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = pyfmt.Quote(f)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// renderDepsBlock lists what each completed dependency reported, or returns
// the empty string so the block disappears entirely when there are none.
func renderDepsBlock(completedDeps []model.Task) string {
	if len(completedDeps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Completed dependencies (context):\n")
	for _, dep := range completedDeps {
		comment := LastComment(dep)
		if comment == "" {
			comment = "(no comment)"
		}
		fmt.Fprintf(&b, "  - %s: %s\n", dep.ID, comment)
	}
	return b.String()
}

// EngineAuthor is the author on every note orch itself writes.
//
// `StateRecorder.Transition` hardcodes it, as does `sqlite_backend.py`'s
// default. An agent's note carries its model name instead, because
// `scripts/task-finish.sh` passes `--author "$AUTHOR"` and `orch_set_status`
// takes an `author` argument. That one field is what tells a report apart
// from a bookkeeping entry — see AgentComment.
const EngineAuthor = "orch"

// LastComment is what the agent that finished dep reported, truncated to the
// prompt's cap. That sentence is the only context a downstream task gets
// about work it depends on, which is why it is worth 500 characters.
func LastComment(dep model.Task) string {
	return truncateRunes(AgentComment(dep.Comments), depCommentMaxChars)
}

// AgentComment is the most recent note a dispatched agent wrote, untruncated.
//
// # Why not simply the last entry — bug 24
//
// Two things write into a task's comment trail and only one of them is a
// report. A task that finished normally ends up with a trail like:
//
//	{"author": "orch",                    "body": "in-progress"}
//	{"author": "claude/claude-sonnet-4-6", "body": "added the health endpoint"}
//	{"author": "orch",                    "body": "dispatch succeeded"}
//
// The agent calls task-finish (or orch_set_status) from inside its run; the
// reaper transitions the task to done afterwards, with its own note. So the
// LAST entry is always the engine's bookkeeping — `dispatch succeeded`, or
// `CI passed` from the poller, or the bare status name for a hand-made
// `orch task set --status done` with no note. Taking `comments[-1]`, as
// `prompt_builder.py` does, renders "dispatch succeeded" in a block whose
// heading promises what the dependency reported.
//
// Scanning back for the first non-`orch` author returns the agent's sentence,
// and returns nothing for a task no agent ever reported on — which the caller
// renders as "(no comment)", honestly.
//
// # Why the key is `body`, not `text`
//
// `prompt_builder.py` reads `text`. Nothing writes `text`: all three comment
// writers in `orchestrator/state/sqlite_backend.py` marshal
// `{"author", "body", "at"}`, and so does Go's `state.appendComment`. The key
// appears in `orchestrator/` in exactly three places — that reader, Slack's
// webhook payload, and `test_prompt_builder.py`'s own invented fixtures — so
// the block has rendered empty on every dispatch since it was written. There
// is no Python behaviour to be faithful to here, only a Python bug, which is
// why this diverges rather than porting it. Recorded as bug 24 in
// docs/brainstorm/go-migration-notes/opus-2.md.
//
// # Divergence kept: a non-string body
//
// Python runs the value through `str()`, so a numeric or boolean body would
// render as Python spells it — `42.0`, `True`, and the literal word `None`
// for an explicit null. This renders the JSON instead (`42`, `true`), and an
// explicit null comes out empty, which the caller shows as "(no comment)".
// Reproducing `str()` means pulling Python's float and list repr into a prompt
// renderer to serve a shape nothing writes.
func AgentComment(comments []json.RawMessage) string {
	for i := len(comments) - 1; i >= 0; i-- {
		author, body := commentFields(comments[i])
		if author == EngineAuthor {
			continue
		}
		if body != "" {
			return body
		}
	}
	return ""
}

// commentFields pulls the author and the body out of one entry.
//
// An entry that is not an object at all has no author, so it can never be
// skipped as the engine's: a bare JSON string is returned as its own body,
// which is the only form of Python's "stringify the whole entry" that is
// readable.
func commentFields(raw json.RawMessage) (author, body string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", jsonScalarString(raw)
	}
	if a, ok := obj["author"]; ok {
		author = jsonScalarString(a)
	}
	if b, ok := obj["body"]; ok {
		body = jsonScalarString(b)
	}
	return author, body
}

// jsonScalarString unwraps a JSON string and leaves anything else as written.
func jsonScalarString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// truncateRunes cuts to at most n characters, appending an ellipsis when it
// cuts. The ellipsis is one character, so the result is n+1 — which is what
// `test_read_dep_last_comment_truncates_at_500_chars` asserts.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// template is `prompt_builder._TEMPLATE` (itself verbatim from
// `sdd/orchestrator/explore.md §3`) with the protocol and constraints blocks
// rewritten for G6.6.
//
// Do not reword the head. The `TASK_ID=` marker on line 1 powers the
// id-spoofing check, and every line down to `Spec ref (READ FIRST):` is
// compared byte for byte against what Python rendered.
//
// The protocol block is deliberately different, and three things in it are
// load-bearing:
//
//   - **Two channels, one row.** MCP first, the scripts if the agent has no
//     tools. Saying "use one, not both" matters because both write through
//     `Backend.Transition`, and a second `done` is a legal idempotent move
//     that appends a second comment — harmless, but it buries the first.
//   - **The note is named as the next task's context.** It is: a downstream
//     task's prompt renders it in "Completed dependencies (context)". An
//     agent that knows this writes a sentence instead of "done".
//   - **The orchestrator has already moved the task to in-progress.** The old
//     wording was a `Note:` about `scripts/task-start.sh`, a script the agent
//     must not run. Stating the state rather than naming the script it should
//     not call is the same fact with one fewer thing to get wrong.
const template = `TASK_ID={id}
You are executing task {id}.

Working dir: {working_dir}
Title: {title}
Description: {description}
Files you may write: {files}
Spec ref (READ FIRST): {spec_ref_line}
{deps_block}
Coordination protocol:
1. Read the spec ref for the exact acceptance criteria for {id}.
2. Do the work. {id} is already in-progress — the orchestrator moved it before launching you.
3. Report back once, through ONE of these two channels.
   If you have orch's MCP tools, use them:
     done:    orch_set_status  task_id "{id}", status "done", note "<what you did>", author "{model}"
     blocked: orch_block       task_id "{id}", reason "<why>", author "{model}"  — then STOP.
   If you do not have those tools, run the project's scripts instead:
     done:    scripts/task-finish.sh {id} "<what you did>" "{model}"
     blocked: scripts/task-block.sh {id} "<why>" "{model}"  — then STOP.
   Both write the same row, so use one, not both. Your note is what the tasks
   depending on {id} are shown, so make it a sentence about what changed.
   orch_get_task and orch_context (task_id "{id}") read this task and its
   dependencies back if you need them again.

Constraints:
- Do NOT edit tasks.json directly.
- Do NOT touch files outside the list above unless the spec explicitly requires it.
- Report progress through one of the two channels above only.
`
