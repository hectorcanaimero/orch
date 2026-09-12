package report

import (
	"os"
	"testing"
	"time"
)

// Not a test: a hook so the rule-29 manual check can produce the SAME report
// the command writes, uncompressed, on a machine with no PDF viewer.
// Skipped unless ORCH_MANUAL_CHECK names a destination.
func TestManualCheckDumpUncompressed(t *testing.T) {
	dest := os.Getenv("ORCH_REPORT_DUMP")
	if dest == "" {
		t.Skip("set ORCH_REPORT_DUMP=/path/to/out.pdf")
	}
	// #nosec G304,G703 -- the path comes from an env var the operator set to
	// run this check by hand; there is no untrusted input anywhere near it.
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := PDF(f, demoSnapshot(8), Options{
		Compress: false,
		Now:      time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", dest)
}
