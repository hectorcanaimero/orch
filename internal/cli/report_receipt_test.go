package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/demo"
	"github.com/hectorcanaimero/orch/internal/receipt"
)

// `orch report receipt` prints the same receipt the dashboard shows, from the
// same builder: against the demo project, the finished history run.
func TestReportReceiptAgainstTheDemo(t *testing.T) {
	paths, err := demo.Build(context.Background(), filepath.Join(t.TempDir(), "demo"), time.Now())
	if err != nil {
		t.Fatalf("demo.Build: %v", err)
	}
	run := func(args ...string) string {
		t.Helper()
		root, _ := newRootCmd("test")
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs(append([]string{"report", "receipt", "--project-root", paths.Root, "--project-id", paths.ID}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("orch report receipt %v: %v\n%s", args, err, errOut.String())
		}
		return out.String()
	}

	md := run()
	if !strings.HasPrefix(md, "## orch run `"+demo.HistoryRunID+"` — finished") || !strings.Contains(md, "16 done") {
		t.Errorf("markdown:\n%s", md)
	}
	if !strings.HasSuffix(md, receipt.BuiltWith+"\n") {
		t.Errorf("markdown should end with the Built with line:\n%s", md)
	}
	if bare := run("--no-built-with"); strings.Contains(bare, receipt.BuiltWith) {
		t.Errorf("--no-built-with still carries the footer:\n%s", bare)
	}

	var rec receipt.Receipt
	if err := json.Unmarshal([]byte(run("--json", "--run", demo.RunID)), &rec); err != nil {
		t.Fatalf("--json: %v", err)
	}
	if rec.RunID != demo.RunID || rec.Finished {
		t.Errorf("--run %s = %+v, want the unfinished live run", demo.RunID, rec)
	}
}
