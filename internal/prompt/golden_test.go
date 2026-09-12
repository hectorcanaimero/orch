package prompt

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// Two sets of goldens, and the split is the point.
//
// `testdata/*.txt` were rendered by Python's prompt_builder
// (`testdata/make-goldens.py`). Until G6.6 they were the whole target. They
// still are for the HEAD of the prompt — everything down to and including the
// `Spec ref (READ FIRST):` line, which did not change and must not.
//
// Below that line Go diverges on purpose: the protocol block offers MCP first
// (G6.6), and the dependency block finally renders what a dependency reported
// instead of `(no comment)` (bug 24). `testdata/go/*.txt` are the goldens for
// the whole body, and the Python files keep earning their place as the
// evidence for the bug — see TestPythonRendersEveryDependencyAsNoComment.
//
// Three of the five cases are tasks out of the shipped project templates,
// which is what makes them worth having: those are the prompts a real `orch
// init` + `orch run` sends to an agent, so a difference here is a difference
// an agent would act on.

// updateGoGoldens rewrites testdata/go/*.txt from the current renderer.
//
// `go test ./internal/prompt -update` after a deliberate template change, then
// read the diff. A flag rather than a script because the renderer is the only
// thing that can produce these — there is no Python to run for them — and
// because a golden nobody looked at is a golden that pins a typo.
var updateGoGoldens = flag.Bool("update", false, "rewrite testdata/go/*.txt from the current renderer")

// headMarker ends the part of the prompt that is still Python's, byte for
// byte. It is a line the template itself emits, not a guessed offset: the
// blocks below it are the ones G6.6 rewrote.
const headMarker = "Spec ref (READ FIRST):"

type goldenCases struct {
	ProjectRoot string `json:"project_root"`
	RunID       string `json:"run_id"`
	Cases       []struct {
		Golden   string `json:"golden"`
		SpecRoot string `json:"spec_root"`
		Task     struct {
			ID          string   `json:"id"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Model       string   `json:"model"`
			Files       []string `json:"files"`
			SpecRef     string   `json:"specRef"`
			Phase       int      `json:"phase"`
		} `json:"task"`
		Deps []struct {
			ID       string            `json:"id"`
			Comments []json.RawMessage `json:"comments"`
		} `json:"deps"`
	} `json:"cases"`
}

func loadCases(t *testing.T) goldenCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cases.json"))
	if err != nil {
		t.Fatalf("read cases.json: %v", err)
	}
	var cases goldenCases
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("decode cases.json: %v", err)
	}
	if len(cases.Cases) == 0 {
		t.Fatal("cases.json lists no cases")
	}
	return cases
}

// renderCase drives Render with one case's inputs.
func renderCase(cases goldenCases, i int) string {
	c := cases.Cases[i]
	task := model.Task{
		ID:          c.Task.ID,
		Title:       c.Task.Title,
		Description: c.Task.Description,
		Model:       c.Task.Model,
		Files:       c.Task.Files,
		SpecRef:     c.Task.SpecRef,
		Phase:       c.Task.Phase,
	}
	deps := make([]model.Task, 0, len(c.Deps))
	for _, d := range c.Deps {
		deps = append(deps, model.Task{ID: d.ID, Comments: d.Comments})
	}
	got, _ := Render(task, deps, c.Task.SpecRef, Options{
		RunID:       cases.RunID,
		ProjectRoot: cases.ProjectRoot,
		SpecRoot:    c.SpecRoot,
	})
	return got
}

func readGolden(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"testdata"}, parts...)...)
	raw, err := os.ReadFile(path) // #nosec G304 -- a name out of cases.json, under testdata
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// head is everything up to and including the headMarker line.
//
// Not a normalisation that decides not to see something (rule 23): it is the
// section boundary the template defines, and the bytes it drops are covered by
// their own golden in the very next test.
func head(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, headMarker)
	if i < 0 {
		t.Fatalf("no %q line in:\n%s", headMarker, body)
	}
	end := strings.Index(body[i:], "\n")
	if end < 0 {
		t.Fatalf("the %q line is not terminated in:\n%s", headMarker, body)
	}
	return body[:i+end+1]
}

// TestHeadStillMatchesThePythonGoldens is the parity that survived G6.6.
//
// The `TASK_ID=` marker feeds the id-spoofing check (AS-10) and the five lines
// under it are what an agent reads to know what it is building, so a diff in
// any byte of them is a failure, not a formatting nit.
func TestHeadStillMatchesThePythonGoldens(t *testing.T) {
	cases := loadCases(t)
	for i, c := range cases.Cases {
		t.Run(c.Golden, func(t *testing.T) {
			want := head(t, readGolden(t, c.Golden))
			got := head(t, renderCase(cases, i))
			if got != want {
				t.Errorf("the prompt's head differs from Python's.\n--- go ---\n%s\n--- python ---\n%s",
					got, want)
			}
		})
	}
}

// TestRenderMatchesTheGoGoldens pins the whole body, divergent blocks included.
func TestRenderMatchesTheGoGoldens(t *testing.T) {
	cases := loadCases(t)
	if *updateGoGoldens {
		if err := os.MkdirAll(filepath.Join("testdata", "go"), 0o750); err != nil {
			t.Fatalf("create testdata/go: %v", err)
		}
	}
	for i, c := range cases.Cases {
		t.Run(c.Golden, func(t *testing.T) {
			got := renderCase(cases, i)
			path := filepath.Join("testdata", "go", c.Golden)
			if *updateGoGoldens {
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				t.Logf("wrote %s (%d bytes)", path, len(got))
				return
			}
			if want := readGolden(t, "go", c.Golden); got != want {
				t.Errorf("rendered prompt differs from its golden.\n--- got ---\n%s\n--- want ---\n%s",
					got, want)
			}
		})
	}
}

// TestPythonRendersEveryDependencyAsNoComment pins bug 24 as evidence.
//
// The fixtures in cases.json carry the REAL comment shape — `author`/`body`/
// `at`, captured from a database Python wrote. `prompt_builder.py` reads
// `text`, which nothing writes, so every dependency line in every Python
// golden reads `(no comment)` — including the one whose fixture says
// "created the warehouse schema" three lines above it in cases.json.
//
// This test exists so the bug cannot be quietly "fixed" by regenerating the
// Python goldens from a fixture shape that humours the reader again, which is
// how it survived three passing tests in test_prompt_builder.py. If Python is
// ever fixed, this test fails and the note in
// docs/brainstorm/go-migration-notes/opus-2.md comes down with it.
func TestPythonRendersEveryDependencyAsNoComment(t *testing.T) {
	cases := loadCases(t)
	checked, withNote := 0, 0
	for i, c := range cases.Cases {
		if len(c.Deps) == 0 {
			continue
		}
		python := readGolden(t, c.Golden)
		for _, d := range c.Deps {
			want := "  - " + d.ID + ": (no comment)"
			if !containsLine(python, want) {
				t.Errorf("%s: Python golden has no %q line — has bug 24 been fixed?",
					c.Golden, want)
			}
			checked++
		}

		// And the same case, rendered by Go, shows what the agent said — for
		// every dependency that had an agent note at all.
		goBody := renderCase(cases, i)
		for _, d := range c.Deps {
			note := AgentComment(d.Comments)
			if note == "" {
				continue // dep_unreported: "(no comment)" is the right answer
			}
			withNote++
			want := "  - " + d.ID + ": " + truncateRunes(note, depCommentMaxChars)
			if !containsLine(goBody, want) {
				t.Errorf("%s: Go did not render %q", c.Golden, want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no case carries a dependency — the evidence is gone")
	}
	// Without this the test passes vacuously on a fixture that stopped
	// carrying a real note — which is precisely the regression it is here to
	// catch, since `body` renamed back to `text` empties AgentComment AND
	// leaves the Python goldens saying "(no comment)" for the right-looking
	// reason. Rule 26: assert the property the fixture is FOR, not only that
	// the comparison still holds.
	if withNote == 0 {
		t.Fatal("no dependency in cases.json carries an agent note — " +
			"the fixtures have gone back to a shape prompt_builder's reader agrees with")
	}
}

// What the goldens are asserted to still contain.
//
// A golden proves the bytes match; it does not prove the bytes are the ones
// that matter. If a future regeneration quietly loses the multi-byte
// truncation case or the empty-files case, the comparisons keep passing while
// testing less — the same trap the budget window vector has. These are the
// properties worth keeping, checked against the GO goldens, which are the ones
// that now describe what an agent receives.
func TestGoldensCoverWhatTheyAreFor(t *testing.T) {
	cases := loadCases(t)

	var sawEmptyFiles, sawPopulatedFiles, sawDeps, sawNoDeps bool
	var sawNoSpecRef, sawTruncation, sawNoComment, sawCustomRoot bool
	var sawAgentNote, sawEngineNoteSkipped bool
	for _, c := range cases.Cases {
		body := readGolden(t, "go", c.Golden)
		switch {
		case containsLine(body, "Files you may write: []"):
			sawEmptyFiles = true
		case strings.Contains(body, "Files you may write: ['"):
			sawPopulatedFiles = true
		}
		if strings.Contains(body, "Completed dependencies (context):") {
			sawDeps = true
		} else {
			sawNoDeps = true
		}
		if strings.Contains(body, "no spec ref provided") {
			sawNoSpecRef = true
		}
		if strings.Contains(body, "…") {
			sawTruncation = true
		}
		if strings.Contains(body, ": (no comment)") {
			sawNoComment = true
		}
		if c.SpecRoot != "" && c.SpecRoot != DefaultSpecRoot {
			sawCustomRoot = true
		}
		// The two halves of bug 24's fix, asserted on the rendered bytes:
		// an agent's note appears, and the engine's own note — which is the
		// LAST entry in every real trail — does not.
		for _, d := range c.Deps {
			if AgentComment(d.Comments) != "" {
				sawAgentNote = true
			}
			for _, raw := range d.Comments {
				author, engineBody := commentFields(raw)
				if author != EngineAuthor || engineBody == "" {
					continue
				}
				if containsLine(body, "  - "+d.ID+": "+engineBody) {
					t.Errorf("%s: the dependency block shows orch's own note %q",
						c.Golden, engineBody)
				}
				sawEngineNoteSkipped = true
			}
		}
	}

	for _, check := range []struct {
		ok   bool
		what string
	}{
		{sawEmptyFiles, "a task with no files (the literal [])"},
		{sawPopulatedFiles, "a task with files (Python's list repr)"},
		{sawDeps, "a task with completed dependencies"},
		{sawNoDeps, "a task with none, so the block disappears"},
		{sawNoSpecRef, "a task with no spec ref (the placeholder)"},
		{sawTruncation, "a dependency comment over the 500-character cap"},
		{sawNoComment, "a dependency no agent ever reported on"},
		{sawCustomRoot, "a spec_root the operator moved"},
		{sawAgentNote, "a dependency whose agent left a note"},
		{sawEngineNoteSkipped, "a trail whose last entry is orch's own bookkeeping"},
	} {
		if !check.ok {
			t.Errorf("no golden covers %s", check.what)
		}
	}
}

// TestProtocolBlockOffersBothChannels reads the bytes an agent acts on.
//
// Rule 29, in test form: the protocol block is the part of the prompt that
// makes an agent do something, and "it renders" is not the same as "it names
// both channels and the right ids". Checked on a golden rather than on a
// freshly rendered string so a template edit that drops a line has to pass
// through a visible golden diff too.
func TestProtocolBlockOffersBothChannels(t *testing.T) {
	cases := loadCases(t)
	body := readGolden(t, "go", cases.Cases[0].Golden)
	id := cases.Cases[0].Task.ID
	model := cases.Cases[0].Task.Model

	for _, want := range []string{
		"TASK_ID=" + id,
		`orch_set_status  task_id "` + id + `", status "done"`,
		`orch_block       task_id "` + id + `", reason "<why>", author "` + model + `"`,
		`scripts/task-finish.sh ` + id + ` "<what you did>" "` + model + `"`,
		`scripts/task-block.sh ` + id + ` "<why>" "` + model + `"`,
		"use one, not both",
		id + " is already in-progress",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the prompt does not contain %q", want)
		}
	}
	// task-start is the one script an agent must never run. Naming it at all
	// was the old wording's mistake.
	if strings.Contains(body, "task-start") {
		t.Error("the prompt names task-start.sh; the agent must not call it")
	}
}

// The truncation is by character. A byte-wise port passes every ASCII case and
// fails only on the one that matters, so the count is asserted directly rather
// than left to the golden to catch.
func TestTruncationCountsCharactersNotBytes(t *testing.T) {
	body := ""
	for _, c := range loadCases(t).Cases {
		if candidate := readGolden(t, "go", c.Golden); strings.Contains(candidate, "…") {
			body = candidate
			break
		}
	}
	if body == "" {
		t.Fatal("no golden contains a truncated comment")
	}

	line := ""
	for _, l := range strings.Split(body, "\n") {
		if len(l) > 4 && l[:4] == "  - " && strings.Contains(l, "…") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("the truncated comment is not on a dependency line")
	}
	comment := line[strings.Index(line, ": ")+2:]
	if runes := []rune(comment); len(runes) != depCommentMaxChars+1 {
		t.Errorf("truncated comment is %d characters, want %d (500 + the ellipsis)",
			len(runes), depCommentMaxChars+1)
	}
	// The multi-byte half of the point: 500 two-byte characters plus a
	// three-byte ellipsis is 1003 bytes, so a 500-BYTE cut would have produced
	// half as many characters.
	if len(comment) <= depCommentMaxChars {
		t.Errorf("comment is %d bytes; a character-wise cut of multi-byte text must exceed %d",
			len(comment), depCommentMaxChars)
	}
}

func containsLine(body, line string) bool {
	for _, l := range strings.Split(body, "\n") {
		if l == line {
			return true
		}
	}
	return false
}
