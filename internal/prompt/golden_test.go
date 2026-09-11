package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// The goldens were rendered by Python's prompt_builder, not by this package.
//
// Three of the five cases are tasks out of the shipped project templates,
// which is what makes them worth having: those are the prompts a real `orch
// init` + `orch run` sends to an agent, so a difference here is a difference
// an agent would act on. `testdata/make-goldens.py` regenerates them.
//
// The template body is contract — the `TASK_ID=` marker feeds the id-spoofing
// check, the numbered steps name the scripts the agent reports through — so a
// diff in any byte of it is a failure, not a formatting nit.

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

func TestRenderMatchesThePythonGoldens(t *testing.T) {
	cases := loadCases(t)

	for _, c := range cases.Cases {
		t.Run(c.Golden, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", c.Golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}

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
			if got != string(want) {
				t.Errorf("rendered prompt differs from Python's.\n--- go ---\n%s\n--- python ---\n%s",
					got, want)
			}
		})
	}
}

// What the goldens are asserted to still contain.
//
// A golden proves the bytes match; it does not prove the bytes are the ones
// that matter. If a future regeneration quietly loses the multi-byte
// truncation case or the empty-files case, TestRenderMatchesThePythonGoldens
// keeps passing while testing less — the same trap the budget window vector
// has. These are the properties worth keeping.
func TestGoldensCoverWhatTheyAreFor(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- a name out of cases.json, under testdata
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}
	cases := loadCases(t)

	var sawEmptyFiles, sawPopulatedFiles, sawDeps, sawNoDeps bool
	var sawNoSpecRef, sawTruncation, sawNoComment, sawCustomRoot bool
	for _, c := range cases.Cases {
		body := read(c.Golden)
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
		{sawNoComment, "a dependency that finished without saying anything"},
		{sawCustomRoot, "a spec_root the operator moved"},
	} {
		if !check.ok {
			t.Errorf("no golden covers %s", check.what)
		}
	}
}

// The truncation is by character. A byte-wise port passes every ASCII case and
// fails only on the one that matters, so the count is asserted directly rather
// than left to the golden to catch.
func TestTruncationCountsCharactersNotBytes(t *testing.T) {
	body := ""
	for _, c := range loadCases(t).Cases {
		raw, err := os.ReadFile(filepath.Join("testdata", c.Golden))
		if err != nil {
			t.Fatalf("read %s: %v", c.Golden, err)
		}
		if strings.Contains(string(raw), "…") {
			body = string(raw)
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
