package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// comment is one entry in a task's trail, in the shape every writer writes.
//
// `{"author", "body", "at"}` — captured from a database Python wrote
// (internal/state/testdata/orch-py-0.11.0.db), not copied from
// `prompt_builder.py`'s docstring, which says `text`/`ts` and is the reason
// bug 24 survived three passing tests in test_prompt_builder.py.
//
// The author is a model name because these stand in for an agent's report.
// engineComment is the other writer.
func comment(body string) json.RawMessage {
	return entry("claude/claude-sonnet-4-6", body)
}

// engineComment is a note orch itself wrote: the dispatch marker, the reaper's
// "dispatch succeeded", the poller's "CI passed", or the bare status name when
// a transition carried no note. Every real trail ends with one of these, which
// is why the renderer cannot simply take the last entry.
func engineComment(body string) json.RawMessage {
	return entry(EngineAuthor, body)
}

func entry(author, body string) json.RawMessage {
	raw, err := json.Marshal(map[string]string{
		"author": author, "body": body, "at": "2026-01-01T09:00:00+00:00",
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func render(t *testing.T, task model.Task, deps []model.Task, specRef string, opts Options) string {
	t.Helper()
	body, _ := Render(task, deps, specRef, opts)
	return body
}

// ---- the spec-ref line -----------------------------------------------------

func TestSpecRootDefaultsWhenUnset(t *testing.T) {
	body := render(t, model.Task{ID: "T-A"}, nil, "f0.md#T1",
		Options{ProjectRoot: "/tmp/p"})
	want := "Spec ref (READ FIRST): " + DefaultSpecRoot + "/f0.md#T1"
	if !strings.Contains(body, want) {
		t.Errorf("prompt does not contain %q", want)
	}
	// The default is `specs`, not orch's own repo layout. It was
	// `docs/rewrite-plan` until #102, which pointed every scaffolded project
	// at a directory that only exists in orch's source tree.
	if strings.Contains(body, "docs/rewrite-plan") {
		t.Error("the default spec root is orch's own layout again")
	}
}

func TestSpecRootIsNotDoubled(t *testing.T) {
	// Bug 12: the templates carried the prefix `spec_root` already supplies.
	// The renderer concatenates whatever it is given, so this is a guard on
	// the shape of the output rather than on the renderer's arithmetic — if a
	// template regrows the prefix, the goldens move and this says why.
	body := render(t, model.Task{ID: "T-A"}, nil, "f0-foundation.md#T1",
		Options{ProjectRoot: "/tmp/p", SpecRoot: "specs"})
	if strings.Contains(body, "specs/specs") {
		t.Error("the spec-ref line doubles the spec root")
	}
}

// A missing spec ref produces a placeholder AND a warning. The placeholder is
// what the agent reads; the warning is what an operator needs, because a task
// dispatched with no spec is usually a tasks.json someone meant to finish.
func TestMissingSpecRefWarnsAndPlaceholders(t *testing.T) {
	body, warnings := Render(model.Task{ID: "T-A"}, nil, "",
		Options{ProjectRoot: "/tmp/p"})

	if !strings.Contains(body, "(no spec ref provided — proceed with the description only)") {
		t.Error("no placeholder on the spec-ref line")
	}
	// The spec root must not leak into the placeholder line.
	if strings.Contains(body, "specs/") {
		t.Error("the placeholder line still mentions the spec root")
	}
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "spec_ref") {
		t.Errorf("warning %q does not name the missing field", warnings[0])
	}
}

func TestPresentSpecRefWarnsAboutNothing(t *testing.T) {
	_, warnings := Render(model.Task{ID: "T-A"}, nil, "f0.md",
		Options{ProjectRoot: "/tmp/p"})
	if len(warnings) != 0 {
		t.Errorf("got warnings for a well-formed task: %v", warnings)
	}
}

// ---- files -----------------------------------------------------------------

// The literal `[]` is load-bearing: it tells the agent there is no strict-file
// guard on this task, which is a different statement from an omitted line.
func TestFilesRenderAsPythonsListRepr(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"none", nil, "Files you may write: []"},
		{"empty slice", []string{}, "Files you may write: []"},
		{"one", []string{"src/a.ts"}, "Files you may write: ['src/a.ts']"},
		{"several", []string{"src/a.ts", "src/b.ts"},
			"Files you may write: ['src/a.ts', 'src/b.ts']"},
		// Single quotes, so a path containing one flips the repr to double
		// quotes — Python's rule, via pyfmt.Quote.
		{"a quote in the path", []string{"it's.ts"}, `Files you may write: ["it's.ts"]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := render(t, model.Task{ID: "T-A", Files: c.files}, nil, "f0.md",
				Options{ProjectRoot: "/tmp/p"})
			if !strings.Contains(body, c.want) {
				t.Errorf("prompt does not contain %q", c.want)
			}
		})
	}
}

// The list appears once. An earlier Python revision printed it twice — in the
// `Files you may write:` line and again in the constraint — and the constraint
// now says "the list above" instead.
func TestFilesListAppearsOnce(t *testing.T) {
	body := render(t, model.Task{ID: "T-A", Files: []string{"src/a.py", "src/b.py"}},
		nil, "f0.md", Options{ProjectRoot: "/tmp/p"})
	if n := strings.Count(body, "['src/a.py', 'src/b.py']"); n != 1 {
		t.Errorf("the files list appears %d times, want 1", n)
	}
	if n := strings.Count(body, "Do NOT touch files outside"); n != 1 {
		t.Errorf("the constraint appears %d times, want 1", n)
	}
}

// ---- dependency comments ---------------------------------------------------

func TestLastCommentTakesTheMostRecentAgentNote(t *testing.T) {
	dep := model.Task{ID: "T-DEP", Comments: []json.RawMessage{
		comment("first"), comment("second"), comment("third"),
	}}
	if got := LastComment(dep); got != "third" {
		t.Errorf("LastComment = %q, want %q", got, "third")
	}
}

func TestLastCommentEdgeCases(t *testing.T) {
	cases := []struct {
		name     string
		comments []json.RawMessage
		want     string
	}{
		{"no comments", nil, ""},
		{"empty list", []json.RawMessage{}, ""},
		{"an entry with no body key", []json.RawMessage{json.RawMessage(`{"author":"a"}`)}, ""},
		{"an empty body", []json.RawMessage{comment("")}, ""},
		// Only orch ever wrote: there is no report to show, and echoing the
		// bookkeeping would read as the dependency having said "done".
		{"nothing but engine notes", []json.RawMessage{
			engineComment("in-progress"), engineComment("dispatch succeeded"),
		}, ""},
		// The agent's note is not the last entry in any real trail.
		{"an agent note under engine notes", []json.RawMessage{
			engineComment("in-progress"), comment("wired the parser"),
			engineComment("dispatch succeeded"),
		}, "wired the parser"},
		// Not an object: Python stringifies the whole entry, and a bare JSON
		// string is the only form of that which reads as a comment.
		{"a bare string entry", []json.RawMessage{json.RawMessage(`"just a string"`)}, "just a string"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LastComment(model.Task{ID: "T", Comments: c.comments}); got != c.want {
				t.Errorf("LastComment = %q, want %q", got, c.want)
			}
		})
	}
}

// 500 characters plus the ellipsis, counted in characters.
//
// Python slices a `str`, so the cap counts code points. A byte-wise port
// passes every ASCII case and fails only on text that is not ASCII — which is
// most comments an agent writes about anything with a name in it.
func TestLastCommentTruncatesByCharacter(t *testing.T) {
	for _, c := range []struct {
		name string
		ch   string
		// bytes per character, for the byte-length assertion
		size int
	}{
		{"ascii", "x", 1},
		{"two-byte", "ñ", 2},
		{"three-byte", "☕", 3},
		{"four-byte", "🙂", 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			dep := model.Task{ID: "T", Comments: []json.RawMessage{
				comment(strings.Repeat(c.ch, 600)),
			}}
			got := LastComment(dep)
			runes := []rune(got)
			if len(runes) != depCommentMaxChars+1 {
				t.Fatalf("got %d characters, want %d (500 + the ellipsis)",
					len(runes), depCommentMaxChars+1)
			}
			if runes[len(runes)-1] != '…' {
				t.Errorf("truncated comment ends with %q, want the ellipsis", runes[len(runes)-1])
			}
			if want := depCommentMaxChars*c.size + len("…"); len(got) != want {
				t.Errorf("got %d bytes, want %d", len(got), want)
			}
		})
	}
}

func TestCommentExactlyAtTheCapIsNotTruncated(t *testing.T) {
	dep := model.Task{ID: "T", Comments: []json.RawMessage{
		comment(strings.Repeat("x", depCommentMaxChars)),
	}}
	got := LastComment(dep)
	if strings.Contains(got, "…") {
		t.Error("a comment exactly at the cap must not be truncated")
	}
	if len(got) != depCommentMaxChars {
		t.Errorf("got %d characters, want %d", len(got), depCommentMaxChars)
	}
}

// ---- the deps block --------------------------------------------------------

func TestDepsBlockDisappearsWhenThereAreNone(t *testing.T) {
	body := render(t, model.Task{ID: "T-A"}, nil, "f0.md", Options{ProjectRoot: "/tmp/p"})
	if strings.Contains(body, "Completed dependencies") {
		t.Error("the block is present with no dependencies")
	}
	// The rest of the prompt is intact around the gap.
	for _, want := range []string{"TASK_ID=T-A", "Coordination protocol:", "Constraints:"} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

func TestDepWithoutACommentGetsThePlaceholder(t *testing.T) {
	body := render(t, model.Task{ID: "T-A"},
		[]model.Task{{ID: "T-DEP"}}, "f0.md", Options{ProjectRoot: "/tmp/p"})
	if !strings.Contains(body, "  - T-DEP: (no comment)") {
		t.Error("a dependency that said nothing has no placeholder")
	}
}

// ---- the template ----------------------------------------------------------

// Line 1 is the marker the id-spoofing check reads (AS-10). Anything before it
// — a blank line, a banner — breaks that check silently.
func TestTaskIDMarkerIsTheFirstLine(t *testing.T) {
	body := render(t, model.Task{ID: "B-020", Title: "x"}, nil, "f0.md",
		Options{ProjectRoot: "/tmp/p"})
	if first := strings.SplitN(body, "\n", 2)[0]; first != "TASK_ID=B-020" {
		t.Errorf("first line is %q, want %q", first, "TASK_ID=B-020")
	}
}

// The scheduler's fields stay out of the prompt. An agent told its task was
// estimated at 0.3 hours would have an opinion about it.
func TestPromptExcludesSchedulerFields(t *testing.T) {
	body := render(t, model.Task{
		ID: "T-A", Phase: 7, Title: "t", Reason: "because the model is cheap",
		EstimateHours: 0.3,
	}, nil, "f0.md", Options{ProjectRoot: "/tmp/p"})

	for _, unwanted := range []string{"Phase:", "Estimate:", "Model reason:", "because the model is cheap"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("prompt leaks %q", unwanted)
		}
	}
}

// The substitution is one pass. A chain of ReplaceAll would rescan its own
// output, so a description containing the literal `{model}` would come out
// with the model name in it — Python's `str.format` reads the template once.
func TestSubstitutionDoesNotRescanItsOutput(t *testing.T) {
	body := render(t, model.Task{
		ID:          "T-A",
		Title:       "{id} and {files}",
		Description: "the placeholder {model} must survive, and so must {deps_block}",
		Model:       "claude/sonnet",
	}, nil, "f0.md", Options{ProjectRoot: "/tmp/p"})

	if !strings.Contains(body, "Title: {id} and {files}") {
		t.Error("a placeholder inside the title was substituted")
	}
	if !strings.Contains(body,
		"Description: the placeholder {model} must survive, and so must {deps_block}") {
		t.Error("a placeholder inside the description was substituted")
	}
	// The real substitutions still happened.
	if !strings.Contains(body, "TASK_ID=T-A") ||
		!strings.Contains(body, `"claude/sonnet"`) {
		t.Error("the template's own placeholders were not substituted")
	}
}

// A dependency comment is arbitrary text an agent wrote, and it lands in the
// prompt after the template has already been read.
func TestPlaceholderInADepCommentSurvives(t *testing.T) {
	dep := model.Task{ID: "T-DEP", Comments: []json.RawMessage{comment("wrote {model} to the config")}}
	body := render(t, model.Task{ID: "T-A", Model: "claude/sonnet"},
		[]model.Task{dep}, "f0.md", Options{ProjectRoot: "/tmp/p"})
	if !strings.Contains(body, "  - T-DEP: wrote {model} to the config") {
		t.Error("a placeholder inside a dependency comment was substituted")
	}
}

// ---- Write -----------------------------------------------------------------

func TestWritePathShape(t *testing.T) {
	dir := t.TempDir()
	path, warnings, err := Write(model.Task{ID: "T-A"}, nil, "f0.md", Options{
		RunID: "run-42", StateDir: dir, ProjectRoot: "/tmp/p",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	want := filepath.Join(dir, "prompts", "run-42", "T-A.txt")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- the path this test just created
	if err != nil {
		t.Fatalf("read the prompt back: %v", err)
	}
	rendered, _ := Render(model.Task{ID: "T-A"}, nil, "f0.md",
		Options{RunID: "run-42", StateDir: dir, ProjectRoot: "/tmp/p"})
	if string(body) != rendered {
		t.Error("the file on disk is not what Render produced")
	}
}

// One prompt per task per run: a re-render overwrites rather than appending or
// erroring, because a retry renders the same task again in the same run.
func TestWriteOverwrites(t *testing.T) {
	dir := t.TempDir()
	opts := Options{RunID: "r1", StateDir: dir, ProjectRoot: "/tmp/p"}

	first, _, err := Write(model.Task{ID: "T-A", Title: "before"}, nil, "f0.md", opts)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := Write(model.Task{ID: "T-A", Title: "after"}, nil, "f0.md", opts)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("paths differ: %q then %q", first, second)
	}
	body, err := os.ReadFile(first) // #nosec G304 -- the path this test just created
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Title: after") ||
		strings.Contains(string(body), "Title: before") {
		t.Error("the second render did not replace the first")
	}
}

// The warnings survive the trip through Write — an operator learns about a
// spec-less dispatch whether the caller renders or writes.
func TestWriteReturnsRenderWarnings(t *testing.T) {
	_, warnings, err := Write(model.Task{ID: "T-A"}, nil, "", Options{
		RunID: "r1", StateDir: t.TempDir(), ProjectRoot: "/tmp/p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Errorf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
}

// An empty ProjectRoot falls back to the process's directory, matching
// Python's `Path.cwd()`. It is the retro-compatible path for an invocation
// that never passed a root, not a default anyone should rely on.
func TestWorkingDirFallsBackToTheProcessDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	body := render(t, model.Task{ID: "T-A"}, nil, "f0.md", Options{})
	if !strings.Contains(body, "Working dir: "+cwd) {
		t.Errorf("prompt does not name the process's directory %q", cwd)
	}
}

// expand's edges. None of these can happen with the template as it stands —
// it is a const in the same file — which is exactly why the behaviour is
// pinned: the next template is where a stray brace shows up.
func TestExpandHandlesMalformedTemplates(t *testing.T) {
	vars := map[string]string{"id": "T-A"}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no placeholders", "plain text", "plain text"},
		{"a known name", "x {id} y", "x T-A y"},
		// Left as written, not dropped: a visible {typo} is a bug someone
		// reports, an empty line is one nobody notices.
		{"an unknown name", "x {nope} y", "x {nope} y"},
		{"an unclosed brace", "x {id y", "x {id y"},
		{"a trailing brace", "x {", "x {"},
		{"an empty name", "x {} y", "x {} y"},
		{"repeated", "{id}-{id}", "T-A-T-A"},
		{"adjacent", "{id}{id}", "T-AT-A"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := expand(c.in, vars); got != c.want {
				t.Errorf("expand(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// A `body` that is not a JSON string renders as the JSON rather than as
// Python's `str()`. Documented divergence — nothing in the tree writes one —
// and pinned so that "nothing writes one" stops being the only thing holding
// it up.
func TestNonStringCommentBodyRendersAsJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"a number", `{"body": 42}`, "42"},
		{"a bool", `{"body": true}`, "true"},
		// JSON null unmarshals into a string as "", so the prompt says
		// "(no comment)". Python's `str(None)` would put the literal text
		// "None" in front of the agent, which is worse than saying nothing.
		{"null", `{"body": null}`, ""},
		{"an object", `{"body": {"a": 1}}`, `{"a": 1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LastComment(model.Task{ID: "T", Comments: []json.RawMessage{json.RawMessage(c.raw)}})
			if got != c.want {
				t.Errorf("LastComment = %q, want %q", got, c.want)
			}
		})
	}
}

// Write reports where it failed. A prompt that cannot be written is a dispatch
// that should not happen, and "permission denied" on its own does not say
// which directory to fix.
//
// The unwritable path is a file standing where a directory has to go, not a
// read-only directory: CI runs as root, and root ignores the permission bits
// that would make the second version fail.
func TestWriteReportsWhereItFailed(t *testing.T) {
	parent := t.TempDir()
	notADir := filepath.Join(parent, "state")
	if err := os.WriteFile(notADir, []byte("I am a file"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err := Write(model.Task{ID: "T-A"}, nil, "f0.md", Options{
		RunID: "r1", StateDir: notADir, ProjectRoot: "/tmp/p",
	})
	if err == nil {
		t.Fatal("expected an error writing under a path that is a file")
	}
	if !strings.Contains(err.Error(), "prompt") || !strings.Contains(err.Error(), notADir) {
		t.Errorf("error %q does not say what failed and where", err)
	}
}
