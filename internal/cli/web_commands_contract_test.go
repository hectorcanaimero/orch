package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The dashboard's empty states and hints tell the operator what to run next.
// A command there that does not exist — or a flag whose own help says it is
// not implemented — sends someone to a terminal to read an error, which is
// worse than saying nothing. `orch task set --milestone M1` did exactly that
// on the Milestones page. This is TestEmbeddedSkillsOnlyNameCommandsThatExist
// pointed at web/src, with the same resolver.
//
// Only code is checked, never prose: the contents of a `<code>` element, and
// backtick spans inside a quoted string ("the token from `orch dashboard
// --token`"). A template literal is not a code span, and "orch waits on …"
// in a sentence is not an invocation.
func TestDashboardOnlyNamesCommandsThatExist(t *testing.T) {
	webSrc := filepath.Join("..", "..", "web", "src")
	root, _ := newRootCmd("test")

	checked := 0
	err := filepath.WalkDir(webSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		source := strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".ts")
		if d.IsDir() || !source || strings.Contains(name, ".test.") {
			return nil
		}
		content, err := os.ReadFile(path) // #nosec G304 G122 -- a file under this repo's web/src
		if err != nil {
			return err
		}
		for _, inv := range webOrchInvocations(string(content)) {
			checked++
			if problem := resolveInvocation(root, inv); problem != "" {
				t.Errorf("%s: `%s`: %s", path, inv, problem)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// An extractor that finds nothing would make this a test of nothing.
	if checked < 5 {
		t.Errorf("found only %d orch invocations under %s — is the extractor broken?", checked, webSrc)
	}
}

var (
	codeElement    = regexp.MustCompile(`<code[^>]*>([^<]*)</code>`)
	quotedCodeSpan = regexp.MustCompile("[\"'][^\"'\n]*`(orch [^`\n]+)`")
)

// webOrchInvocations returns the orch command lines in a TSX/TS source.
// A `<code>` element may wrap its text over several lines; whitespace is
// collapsed before the check.
func webOrchInvocations(content string) []string {
	var out []string
	for _, m := range codeElement.FindAllStringSubmatch(content, -1) {
		if inv := strings.Join(strings.Fields(m[1]), " "); strings.HasPrefix(inv, "orch ") {
			out = append(out, inv)
		}
	}
	for _, m := range quotedCodeSpan.FindAllStringSubmatch(content, -1) {
		out = append(out, m[1])
	}
	return out
}
