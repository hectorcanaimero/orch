package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/engine"
)

// A curation whose lessons no longer parse, or outgrow what a prompt
// carries, is sent back even when it touched only .orch/knowledge/ (#452).
func TestCurateScopeSendsBackBrokenAndOverCapLessons(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=o@example.com", "-c", "user.name=o", "-c", "commit.gpgsign=false"}, args...)...) // #nosec G204 -- fixed git arguments in a test
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, ".orch", "knowledge", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "--initial-branch=main")
	write("ok.md", "---\nid: ok\ntitle: fine\nsource: operator\ncreated: \"2026-10-01\"\n---\nshort\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "orch/knowledge-curate-x")

	scope := curateScope{base: "main"}
	verify := func() string {
		git("add", "-A")
		git("commit", "-q", "--allow-empty", "-m", "work")
		return scope.Verify(context.Background(), engine.VerifyRequest{Dir: dir}).Failure
	}
	write("ok.md", "---\nid: ok\ntitle: still fine\nsource: operator\ncreated: \"2026-10-01\"\n---\nshorter\n")
	if got := verify(); got != "" {
		t.Fatalf("a lessons-only change within the cap was sent back: %s", got)
	}

	write("long.md", "---\nid: long\ntitle: long\nsource: operator\ncreated: \"2026-10-01\"\n---\n"+strings.Repeat("a line\n", 16))
	if got := verify(); !strings.Contains(got, ".orch/knowledge/long.md is over the cap (16 lines") {
		t.Errorf("an over-cap lesson passed: %q", got)
	}

	write("long.md", "no front matter\n")
	if got := verify(); !strings.Contains(got, "A lesson no longer parses") {
		t.Errorf("a broken lesson passed: %q", got)
	}
}
