package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/demo"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/state"
)

// `orch report summary` words the demo's finished run in the language asked
// for, as text or JSON.
func TestReportSummaryAgainstTheDemo(t *testing.T) {
	paths, err := demo.Build(context.Background(), filepath.Join(t.TempDir(), "demo"), time.Now())
	if err != nil {
		t.Fatalf("demo.Build: %v", err)
	}
	run := func(args ...string) (string, error) {
		root, _ := newRootCmd("test")
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"report", "summary", "--project-root", paths.Root, "--project-id", paths.ID}, args...))
		err := root.Execute()
		return out.String(), err
	}
	if out, err := run("--language", "en"); err != nil || !strings.HasPrefix(out, "Run "+demo.HistoryRunID+" finished: 16 done, 0 blocked.") || !strings.Contains(out, "Next:") {
		t.Errorf("en: %v\n%s", err, out)
	}
	if out, err := run("--language", "es"); err != nil || !strings.Contains(out, "16 hechas") {
		t.Errorf("es: %v\n%s", err, out)
	}
	if out, err := run("--json", "--language", "pt"); err != nil || !strings.Contains(out, `"lang":"pt"`) || !strings.Contains(out, `"actions":[`) {
		t.Errorf("--json: %v\n%s", err, out)
	}
	if _, err := run("--language", "fr"); err == nil {
		t.Error("--language fr should be refused")
	}
}

// `--ai` prints the agent's answer and records its spend under
// summary-<run>; a dispatch that fails prints the plain summary after a
// warning (#461).
func TestReportSummaryAI(t *testing.T) {
	ctx := context.Background()
	paths, err := demo.Build(ctx, filepath.Join(t.TempDir(), "demo"), time.Now())
	if err != nil {
		t.Fatalf("demo.Build: %v", err)
	}
	fake := filepath.Join(t.TempDir(), "fake")
	if err := os.MkdirAll(filepath.Join(fake, "claude"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv(engine.FakeProviderEnv, fake)
	canned := func(out, exit string) {
		for ext, body := range map[string]string{".out": out, ".exit": exit} {
			if err := os.WriteFile(filepath.Join(fake, "claude", "_default"+ext), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	run := func() (string, string) {
		root, _ := newRootCmd("test")
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs([]string{"report", "summary", "--ai", "--backend", "claude", "--language", "en",
			"--project-root", paths.Root, "--project-id", paths.ID})
		if err := root.Execute(); err != nil {
			t.Fatalf("report summary --ai: %v\n%s", err, errOut.String())
		}
		return out.String(), errOut.String()
	}
	spendRows := func() int {
		db, _, err := state.Open(ctx, filepath.Join(paths.StateDir(), "orch.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		rows, err := state.NewSQLite(db, paths.ID, paths.Root).AllSpend(ctx, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range rows {
			if r.TaskID == "summary-"+demo.HistoryRunID {
				n++
			}
		}
		return n
	}

	canned(`{"type":"result","subtype":"success","is_error":false,"result":"The run built everything.","total_cost_usd":0.001,"usage":{"input_tokens":10,"output_tokens":5}}`, "0")
	if out, _ := run(); out != "The run built everything.\n" {
		t.Errorf("--ai printed %q, want the agent's answer", out)
	}
	if n := spendRows(); n != 1 {
		t.Errorf("%d spend rows for summary-%s, want 1", n, demo.HistoryRunID)
	}

	canned("boom\n", "1")
	out, errOut := run()
	if !strings.HasPrefix(out, "Run "+demo.HistoryRunID+" finished:") || !strings.Contains(errOut, "[warn] --ai:") {
		t.Errorf("a failed dispatch should fall back with a warning:\nstdout %q\nstderr %q", out, errOut)
	}
}
