package cli

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// scaffoldProject writes the two files resolveAndValidate and the portfolio
// opener look for, plus a config.
func scaffoldProject(t *testing.T, root, configYAML string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".orchestrator"), 0o750); err != nil {
		t.Fatal(err)
	}
	const tasks = `{"meta":{"project":"p"},"tasks":[
	  {"id":"T-1","phase":0,"title":"One","model":"claude/sonnet","status":"todo",
	   "dependencies":[],"estimateHours":1.0}]}`
	if err := os.WriteFile(filepath.Join(root, "tasks.json"), []byte(tasks), 0o600); err != nil {
		t.Fatal(err)
	}
	if configYAML != "" {
		if err := os.WriteFile(filepath.Join(root, ".orchestrator", "config.yaml"),
			[]byte(configYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOpenPortfolioSkipsWhatIsNotAProject is the property the whole command
// rests on: a glob over a directory of work matches things that are not orch
// projects, and every one of them has to become a named row rather than a
// reason to refuse to start.
func TestOpenPortfolioSkipsWhatIsNotAProject(t *testing.T) {
	dir := t.TempDir()
	scaffoldProject(t, filepath.Join(dir, "alpha"), "spec_root: specs\n")
	scaffoldProject(t, filepath.Join(dir, "beta"), "spec_root: specs\n")
	// A directory that is not a project, and a plain file — a real glob over
	// ~/projects catches both.
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	projects, unavailable, closeAll, err := openPortfolio(
		context.Background(), filepath.Join(dir, "*"), http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatalf("openPortfolio: %v", err)
	}
	defer closeAll()

	var ids []string
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "beta" {
		t.Errorf("projects = %v, want [alpha beta] in glob order", ids)
	}
	// The directory is reported; the file is not. A README is not a broken
	// project and saying so would be noise on every start.
	if len(unavailable) != 1 {
		t.Fatalf("unavailable = %+v, want just the non-project directory", unavailable)
	}
	if filepath.Base(unavailable[0].Root) != "notes" {
		t.Errorf("unavailable names %q, want notes", unavailable[0].Root)
	}
	if !strings.Contains(unavailable[0].Reason, "tasks.json") {
		t.Errorf("reason = %q; it must say what is missing", unavailable[0].Reason)
	}
}

// Two directories can resolve to one project id — the id is the directory's
// base name — and the second would collide under /p/. It becomes a row naming
// the first, because "rename one of these two" is the fix and only the
// operator can make it.
func TestOpenPortfolioReportsADuplicateID(t *testing.T) {
	dir := t.TempDir()
	scaffoldProject(t, filepath.Join(dir, "a", "billing"), "spec_root: specs\n")
	scaffoldProject(t, filepath.Join(dir, "b", "billing"), "spec_root: specs\n")

	projects, unavailable, closeAll, err := openPortfolio(
		context.Background(), filepath.Join(dir, "*", "billing"), http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatalf("openPortfolio: %v", err)
	}
	defer closeAll()

	if len(projects) != 1 {
		t.Fatalf("%d projects, want 1 — the duplicate must not be served", len(projects))
	}
	if len(unavailable) != 1 || !strings.Contains(unavailable[0].Reason, "already taken") {
		t.Fatalf("unavailable = %+v; want the duplicate, named", unavailable)
	}
	// The message has to name the directory that won, or the operator cannot
	// tell which of two identically-named projects is being served.
	if !strings.Contains(unavailable[0].Reason, filepath.Join(dir, "a", "billing")) {
		t.Errorf("reason = %q; it must name the directory that took the id", unavailable[0].Reason)
	}
}

// A stakeholder project with no token is refused by Config.Validate. In a
// portfolio that refusal has to be a row, not a dead process: one
// misconfigured project out of ten is exactly what the unavailable list is
// for.
func TestOpenPortfolioReportsAMisconfiguredProject(t *testing.T) {
	dir := t.TempDir()
	scaffoldProject(t, filepath.Join(dir, "good"), "spec_root: specs\n")
	scaffoldProject(t, filepath.Join(dir, "tokenless"),
		"dashboard:\n  profile: stakeholder\n  token: \"\"\n")

	projects, unavailable, closeAll, err := openPortfolio(
		context.Background(), filepath.Join(dir, "*"), http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatalf("openPortfolio: %v", err)
	}
	defer closeAll()

	if len(projects) != 1 || projects[0].ID != "good" {
		t.Fatalf("projects = %+v, want only `good`", projects)
	}
	if len(unavailable) != 1 || filepath.Base(unavailable[0].Root) != "tokenless" {
		t.Fatalf("unavailable = %+v", unavailable)
	}
	if unavailable[0].Reason == "" {
		t.Error("the misconfigured project gives no reason")
	}
}

func TestOpenPortfolioRefusesAGlobThatMatchesNothing(t *testing.T) {
	_, _, _, err := openPortfolio(
		context.Background(), filepath.Join(t.TempDir(), "nothing-here-*"), http.NotFoundHandler(), nil)
	if err == nil {
		t.Fatal("a glob matching nothing was accepted")
	}
	// The shell expanding the glob before orch sees it is the likeliest
	// cause, and the message has to say so or the operator retries the same
	// command.
	if !strings.Contains(err.Error(), "quote it") {
		t.Errorf("error = %q; it does not mention quoting", err)
	}
}

// TestPortfolioRefusesFlagsItCannotHonour covers the three that are refused
// rather than ignored.
//
// Each is a flag whose single-project meaning has no portfolio equivalent, and
// accepting one silently would be the exact failure this port keeps finding: a
// flag that promises a behaviour nothing implements. They are checked before
// any I/O, so this test opens nothing.
func TestPortfolioRefusesFlagsItCannotHonour(t *testing.T) {
	cases := []struct {
		name  string
		flags portfolioFlags
		want  string
	}{
		{
			// A stakeholder portfolio would show every project's counters to
			// a token holder scoped to one of them.
			name:  "a stakeholder profile",
			flags: portfolioFlags{profile: "stakeholder"},
			want:  "operator-only",
		},
		{
			name:  "both is not operator either",
			flags: portfolioFlags{profile: "both"},
			want:  "operator-only",
		},
		{
			// Accepting one would suggest a shared portfolio token exists.
			name:  "a token",
			flags: portfolioFlags{token: "test-token-stakeholder"},
			want:  "each project keeps its own token",
		},
		{
			name:  "a tunnel",
			flags: portfolioFlags{withTunnel: true},
			want:  "configured per project",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A glob that would match nothing, so a case that wrongly got
			// past the checks fails on the glob instead of quietly passing.
			err := runPortfolio(newPortfolioTestCmd(t), filepath.Join(t.TempDir(), "*"), tc.flags)
			if err == nil {
				t.Fatal("the flag was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q; it does not explain %q", err, tc.want)
			}
		})
	}
}

// An explicit `--profile operator` is the one profile value that IS honoured:
// it says what the portfolio already is, so refusing it would be pedantry.
func TestPortfolioAcceptsAnExplicitOperatorProfile(t *testing.T) {
	err := runPortfolio(newPortfolioTestCmd(t), filepath.Join(t.TempDir(), "*"),
		portfolioFlags{profile: "operator"})
	if err == nil {
		t.Fatal("expected the empty-glob error, got none")
	}
	// It got past the flag checks and failed on the glob, which is the
	// distinction: a refused profile never reaches the filesystem.
	if !strings.Contains(err.Error(), "matched no directories") {
		t.Errorf("error = %q; want the glob error, not a profile refusal", err)
	}
}

func newPortfolioTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "dashboard"}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())
	return cmd
}
