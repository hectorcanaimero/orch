package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// `orch init` ends by printing the commands to run next, and a new user
// copies them literally. Its "Run it" step printed `orch --project-root X
// --mode semi` — `--mode` belongs to `orch run`, so the first command anyone
// ran after init failed with "unknown flag". Every `orch …` line init prints,
// with and without a template, has to resolve against the real command tree.
func TestInitNextStepsOnlyNameCommandsThatExist(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"blank project", nil},
		{"from a template", []string{"--template", "nextjs-saas"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := newRootCmd("test")
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			dir := filepath.Join(t.TempDir(), "proj")
			root.SetArgs(append([]string{"init", dir}, tc.args...))
			if err := root.Execute(); err != nil {
				t.Fatalf("orch init: %v\n%s", err, out.String())
			}

			checked := 0
			for _, line := range strings.Split(out.String(), "\n") {
				inv := strings.TrimSpace(line)
				if !strings.HasPrefix(inv, "orch ") {
					continue
				}
				checked++
				fresh, _ := newRootCmd("test")
				if problem := resolveInvocation(fresh, inv); problem != "" {
					t.Errorf("init tells the user to run `%s`: %s", inv, problem)
				}
			}
			// Three next steps plus the router hint: an extractor that finds
			// none would make this a test of nothing.
			if checked < 3 {
				t.Fatalf("found %d orch commands in init's output, want at least 3:\n%s", checked, out.String())
			}
		})
	}
}
