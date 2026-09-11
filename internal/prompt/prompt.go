// Package prompt renders the per-dispatch prompt an agent receives.
//
// Ported from `orchestrator/prompt_builder.py`. The template body is contract
// with the agent, not a message to a human: the shell-out lines name the
// scripts the agent must call to report back, and the `TASK_ID=` marker on
// line 1 is what the id-spoofing check reads (AS-10). Rewording any of it
// changes what agents do. It is copied byte for byte, and the goldens in
// testdata are the proof — they were rendered by Python, not by this package.
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
// # The MCP variant
//
// This is the scripts variant: the agent reports progress by running
// `scripts/task-finish.sh` and friends. G6.6 adds an MCP variant where the
// same state transitions arrive as tool calls instead, at which point the
// "Coordination protocol" and "Constraints" blocks are replaced and the rest
// of the prompt stays as it is. Nothing here is built for that yet — an
// unused variant switch would be a guess at a design that has not been made —
// but the template is a package-level const named for its variant so the
// second one has an obvious place to go.
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

	return expand(scriptsTemplate, map[string]string{
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

// LastComment is the most recent comment text on a task, truncated.
//
// `comments` is appended to by `scripts/task-finish.sh`, so the last entry is
// what the agent that finished the dependency said it did. That sentence is
// the only context a downstream task gets about work it depends on, which is
// why it is worth 500 characters of prompt.
//
// # Divergence: a non-string `text`
//
// Python runs the value through `str()`, so a numeric or boolean `text` would
// render as Python spells it — `42.0`, `True`, and the literal word `None`
// for an explicit null. This renders the JSON instead (`42`, `true`), and an
// explicit null comes out empty, which the caller shows as "(no comment)".
//
// Reproducing `str()` properly means pulling Python's float repr and list repr
// into a prompt renderer to serve a shape nothing writes: every producer of a
// comment in the tree writes a string. Where the two differ the Go answer is
// the better one anyway — a dependency that reported nothing should read as
// "(no comment)", not as the word "None".
func LastComment(dep model.Task) string {
	if len(dep.Comments) == 0 {
		return ""
	}
	return truncateRunes(commentText(dep.Comments[len(dep.Comments)-1]), depCommentMaxChars)
}

func commentText(raw json.RawMessage) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		// Not an object at all. Python stringifies the whole entry; a bare
		// JSON string is the only form of that which is readable, so it is
		// the only one unwrapped.
		return jsonScalarString(raw)
	}
	text, ok := obj["text"]
	if !ok {
		return "" // Python's `.get("text", "")`
	}
	return jsonScalarString(text)
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

// scriptsTemplate is verbatim from `sdd/orchestrator/explore.md §3` by way of
// `prompt_builder._TEMPLATE`.
//
// Do not reword. The `TASK_ID=` marker on line 1 powers the id-spoofing check;
// the three numbered steps name the scripts the agent reports through; the
// `Note:` line tells it not to call task-start itself. Every one of those is
// something an agent acts on.
const scriptsTemplate = `TASK_ID={id}
You are executing task {id}.

Working dir: {working_dir}
Title: {title}
Description: {description}
Files you may write: {files}
Spec ref (READ FIRST): {spec_ref_line}
{deps_block}
Coordination protocol:
1. Read the spec ref for the exact acceptance criteria for {id}.
2. Do the work. If blocked, run: scripts/task-block.sh {id} "<reason>" "{model}" and STOP.
3. On success, run: scripts/task-finish.sh {id} "<what you did>" "{model}"
   Note: the orchestrator will call scripts/task-start.sh {id} BEFORE launching you.

Constraints:
- Do NOT edit tasks.json directly.
- Do NOT touch files outside the list above unless the spec explicitly requires it.
- Report progress via the scripts above only.
`
