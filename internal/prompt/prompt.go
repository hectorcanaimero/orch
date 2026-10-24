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

	"github.com/hectorcanaimero/orch/internal/knowledge"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
	"github.com/hectorcanaimero/orch/internal/state"
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
	// ReportFindings adds the closing invitation to report what orch itself
	// should do better (`report_findings.enabled`). Off, the prompt is
	// byte-identical to the one without the feature.
	ReportFindings bool
	// ComposeProject is the docker compose project the task's containers
	// run under (COMPOSE_PROJECT_NAME, dispatch.isolate_compose, #343).
	// Empty, the prompt says nothing about compose.
	ComposeProject string

	// Spec is what the dispatcher found when it looked for the spec ref in
	// the directory the agent will run in. The zero value renders the line
	// as it always has; the other two say so on the line itself, so an agent
	// is never told to READ FIRST a file it cannot open (#341, #342).
	Spec SpecState

	// OperatorNotes are what the operator told this task's agent, oldest
	// first (#356): see OperatorNotes. Empty, the block is not rendered.
	OperatorNotes []string

	// TDD adds the test-first constraint (dispatch.tdd). Off, the prompt is
	// byte-identical to the one without the feature.
	TDD bool

	// FeedbackFile names the file in the working dir that says why the
	// previous attempt was rejected — the CI logs a CI retry leaves there.
	// Empty, the block is not rendered.
	FeedbackFile string

	// Conflict is the merge of the base a conflict retry stopped on (#438).
	// Nil, the block is not rendered.
	Conflict *Conflict

	// Knowledge are the project lessons picked for this task (#451), most
	// relevant first. Empty, the block is not rendered.
	Knowledge []knowledge.Lesson
	// Learn invites the agent to record a lesson with orch_learn
	// (knowledge.enabled). Off, the prompt is byte-identical to the one
	// without the feature.
	Learn bool
}

// Conflict is the base merged into a conflict retry's working dir, the files
// it stopped on and the base commits that touched them ("<hash> <subject>").
type Conflict struct {
	Base    string
	Files   []string
	Commits []string
}

// SpecState is whether the spec ref was found where the agent will run.
type SpecState int

const (
	// SpecUnchecked renders the plain `Spec ref (READ FIRST):` line: the
	// spec was found, or nobody looked.
	SpecUnchecked SpecState = iota
	// SpecFileMissing is a spec file that is not in the agent's working dir.
	SpecFileMissing
	// SpecAnchorMissing is a file that is there without a heading for the
	// ref's `#anchor`.
	SpecAnchorMissing
)

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
	switch {
	case specRef == "":
		specLine = "(no spec ref provided — proceed with the description only)"
		warnings = append(warnings,
			"prompt rendered without spec_ref — proceeding with description only")
	case opts.Spec == SpecFileMissing:
		specLine += " (NOT FOUND in your working dir — do not look for it; take the acceptance criteria from the description)"
		warnings = append(warnings, fmt.Sprintf(
			"spec file of %s is not in the working dir — the agent proceeds with the description only", specRef))
	case opts.Spec == SpecAnchorMissing:
		_, anchor, _ := strings.Cut(specRef, "#")
		specLine += fmt.Sprintf(" (the file has no heading for %s — read it for context, and take the acceptance criteria from the description)", anchor)
		warnings = append(warnings, fmt.Sprintf(
			"spec ref %s: the file has no heading for %s — the agent relies on the description", specRef, anchor))
	}

	return expand(template, map[string]string{
		"id":              task.ID,
		"title":           task.Title,
		"description":     task.Description,
		"files":           renderFiles(task.Files),
		"spec_ref_line":   specLine,
		"deps_block":      renderDepsBlock(completedDeps),
		"knowledge_block": knowledgeBlock(opts.Knowledge),
		"notes_block":     renderNotesBlock(opts.OperatorNotes),
		"feedback_block":  feedbackBlock(opts.FeedbackFile),
		"conflict_block":  conflictBlock(opts.Conflict),
		"model":           task.Model,
		"working_dir":     workingDir,
		"tdd_line":        tddLine(opts.TDD),
		"compose_line":    composeLine(opts.ComposeProject),
		"findings_block":  findingsBlock(opts.ReportFindings),
		"learn_line":      learnLine(opts.Learn),
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

// tddLine is the test-first constraint (dispatch.tdd), or nothing. It is the
// orch-tdd skill in four lines, for the agents that never installed it.
func tddLine(on bool) string {
	if !on {
		return ""
	}
	return "- Work test-first, one acceptance criterion at a time: write its test with the\n" +
		"  framework the repository already uses, run it and see it fail for the expected\n" +
		"  reason, then write the least code that makes it pass. Never weaken or skip an\n" +
		"  existing test. Name the tests and what they prove in your done note.\n"
}

// composeLine tells the agent the docker compose project orch put it in
// (#343), or nothing when orch did not. Other tasks run in sibling worktrees
// at the same time, under projects of their own: `-p` would put this one back
// in a shared project, and a `docker compose down` or `docker rm` aimed at
// "whatever is running" would take a sibling's containers with it.
func composeLine(project string) string {
	if project == "" {
		return ""
	}
	return fmt.Sprintf("- Docker compose runs as project %q (COMPOSE_PROJECT_NAME is set for you): do not pass\n"+
		"  -p/--project-name, and do not stop or remove containers of other compose projects.\n", project)
}

// findingsBlock is the dogfooding invitation, or nothing. It comes last and
// after the report-back step on purpose: the task is the job, and feedback
// about orch must never compete with it.
func findingsBlock(enabled bool) string {
	if !enabled {
		return ""
	}
	return `
Feedback about orch itself (optional, after you report back):
- If orch got in your way, or you noticed something orch should do better or does
  not do at all (a missing command or flag, a step you had to do by hand, a
  confusing message or status), report it once with orch_report_finding:
  type "bug" | "improvement" | "feature", title, summary.
- It is about orch, not this project: no secrets, no project code, no project or
  customer names. Skip it if nothing comes to mind.
`
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

// operatorNotesMax bounds the block: the newest notes win, each cut to the
// dependency comments' cap. An operator steering a long task writes a few
// lines, not a document.
const operatorNotesMax = 5

// renderNotesBlock lists what the operator told this task's agent, or
// nothing. After the dependencies and before the protocol, so it is the last
// thing the agent reads about the work itself.
func renderNotesBlock(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	if len(notes) > operatorNotesMax {
		notes = notes[len(notes)-operatorNotesMax:]
	}
	var b strings.Builder
	b.WriteString("Operator notes (from the person running orch, newest last; where they and the\n")
	b.WriteString("description disagree, follow the notes):\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "  - %s\n", truncateRunes(strings.TrimSpace(n), depCommentMaxChars))
	}
	return b.String()
}

// feedbackBlock points a retried agent at why its last attempt was rejected,
// or is empty. Without it the agent never learns the file exists, and redoes
// the task from the description as if it had never run.
func feedbackBlock(file string) string {
	if file == "" {
		return ""
	}
	return fmt.Sprintf("Your previous attempt at this task was rejected. Its work is already in your\n"+
		"working dir. READ FIRST %s: it says what failed. Fix that rather than\n"+
		"starting over.\n", file)
}

// conflictBlock tells a conflict retry's agent that its kept work is
// mid-merge with the base, where, and because of what; or is empty.
func conflictBlock(c *Conflict) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your work conflicts with its base branch, %s. Your earlier work is in your\n"+
		"working dir, and orch has merged %s into it; the merge stopped on conflicts in:\n", c.Base, c.Base)
	for _, f := range c.Files {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	if len(c.Commits) > 0 {
		b.WriteString("The base commits that touched them:\n")
		for _, line := range c.Commits {
			fmt.Fprintf(&b, "  - %s\n", line)
		}
	}
	b.WriteString("Resolve every conflict keeping both sides' intent, remove all conflict markers,\n" +
		"run the tests, then report back as below. You need not commit: orch commits the\n" +
		"merge with your resolution.\n")
	return b.String()
}

// Caps on the knowledge block (#451): a lesson is a few lines, and the block
// must never crowd out the task.
const (
	lessonMaxLines = knowledge.MaxLines
	lessonMaxChars = knowledge.MaxChars
	knowledgeMax   = 4000
)

// knowledgeBlock lists the project lessons picked for this task, or nothing.
// After the dependencies: what the project learned is context, like what
// they reported; the operator's notes still come last.
func knowledgeBlock(lessons []knowledge.Lesson) string {
	if len(lessons) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Project knowledge (lessons earlier tasks recorded in .orch/knowledge/; follow them\n" +
		"unless the spec or the operator says otherwise):\n")
	head := b.Len()
	for _, l := range lessons {
		var e strings.Builder
		fmt.Fprintf(&e, "  - %s", l.Title)
		if len(l.Area) > 0 {
			fmt.Fprintf(&e, " [%s]", strings.Join(l.Area, ", "))
		}
		e.WriteString("\n")
		lines := strings.Split(truncateRunes(strings.TrimSpace(l.Body), lessonMaxChars), "\n")
		if len(lines) > lessonMaxLines {
			lines = append(lines[:lessonMaxLines], "…")
		}
		for _, line := range lines {
			fmt.Fprintf(&e, "    %s\n", strings.TrimRight(line, " \t"))
		}
		if b.Len()+e.Len() > knowledgeMax && b.Len() > head {
			break
		}
		b.WriteString(e.String())
	}
	return b.String()
}

// learnLine invites the agent to leave a lesson for the tasks after it, or
// is empty (knowledge.enabled off).
func learnLine(on bool) string {
	if !on {
		return ""
	}
	return "- If you learned something a later task in this project should know (a convention,\n" +
		"  a trap, what fixed a failure), record it once with orch_learn: title, lesson, area.\n"
}

// OperatorNotes picks the operator's notes out of a task's own comment
// trail, oldest first: the entries whose kind is state.NoteKindOperator.
func OperatorNotes(comments []json.RawMessage) []string {
	var out []string
	for _, raw := range comments {
		var c struct {
			Kind string `json:"kind"`
			Body string `json:"body"`
		}
		if json.Unmarshal(raw, &c) == nil && c.Kind == state.NoteKindOperator && strings.TrimSpace(c.Body) != "" {
			out = append(out, c.Body)
		}
	}
	return out
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
		// An operator's note is an instruction to this task's agent, not
		// what the task reported (#356).
		if author == EngineAuthor || isOperatorNote(comments[i]) {
			continue
		}
		if body != "" {
			return body
		}
	}
	return ""
}

func isOperatorNote(raw json.RawMessage) bool {
	var c struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(raw, &c) == nil && c.Kind == state.NoteKindOperator
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
//   - **A clean exit is the done of last resort.** A project's own claude
//     settings can deny both done channels (#328, #336, #337), and an agent
//     left with only orch_block used it on finished work — which the reaper
//     honours, so the branch is never pushed. The reaper records a clean
//     exit as done (or waits on CI), so saying so is true in every mode.
//   - **The file list bends for wiring, visibly.** "Do not touch anything
//     else unless the spec says so" left an agent choosing between an
//     unwired feature and a broken rule (#318, #320). It may now make the
//     edits the task cannot work without and say why in its note, and the
//     PR names every file outside the list (engine's scopeSection, #319).
//   - **The compose line exists only when orch set the project** (#343). It
//     follows the constraints and is empty with dispatch.isolate_compose
//     off, so a project without it renders byte for byte as before.
//   - **Operator notes exist only when the operator wrote one** (#356). They
//     follow the dependency block, so a task without notes renders byte for
//     byte as before; the goldens without notes are the proof.
//   - **The test-first line exists only with dispatch.tdd on**, first of
//     the conditional constraints, under the same rule.
//   - **The feedback block exists only on a retry that left a feedback
//     file** (the CI logs). It follows the notes, under the same rule.
//   - **The conflict block exists only on a conflict retry** (#438). It
//     follows the feedback block, under the same rule.
//   - **Project knowledge exists only when lessons were picked** (#451). It
//     follows the dependency block, and the orch_learn line exists only with
//     knowledge.enabled, under the same rule.
const template = `TASK_ID={id}
You are executing task {id}.

Working dir: {working_dir}
Title: {title}
Description: {description}
Files you may write: {files}
Spec ref (READ FIRST): {spec_ref_line}
{deps_block}{knowledge_block}{notes_block}{feedback_block}{conflict_block}
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
   If neither done channel is allowed to you, end with a one-sentence summary
   of what changed and exit: orch records {id} done when you exit cleanly.
   Never block work you finished — a block keeps it from being published.
   orch_get_task and orch_context (task_id "{id}") read this task and its
   dependencies back if you need them again.

Constraints:
- Do NOT edit tasks.json directly.
- Stay inside the files listed above. When the task cannot work without a file
  outside it — wiring the new code in (a route, a module registry), or the
  manifest and lockfile of the package you work in for a dependency the task
  needs — edit it, and name each such file and why in your done note.
- Report progress through one of the two channels above only.
{tdd_line}{compose_line}{learn_line}{findings_block}`
