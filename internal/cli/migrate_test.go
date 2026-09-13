package cli_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/cli"
)

// fileProjectFixtureDir is testdata/file-project — a synthetic file-mode
// project (tasks.json + state/{events,spend}-run1.jsonl, no orch.db) with a
// golden.json produced by running the REAL orchestrator.migrate.run_migrate
// against it. Frozen since the Python tree left main: see that directory's
// README.md for where the generator lives now.
func fileProjectFixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "file-project"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("file-project fixture not found at %s: %v", dir, err)
	}
	return dir
}

// copyMigrateFixture is copyProjectFixture's counterpart for this fixture:
// same idea (walk + copyFile, both already defined in testscript_test.go),
// excluding golden.json/README.md instead of goldens/README.md — this
// fixture keeps its non-project files at the top level, not in a subdir.
func copyMigrateFixture(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "golden.json" || rel == "README.md" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		return copyFile(path, target)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newFileProject(t *testing.T) (root string, common []string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "proj")
	copyMigrateFixture(t, fileProjectFixtureDir(t), root)
	return root, []string{"--project-root", root, "--project-id", "file-project"}
}

func TestMigrateDryRunTouchesNothing(t *testing.T) {
	root, common := newFileProject(t)
	args := append([]string{"migrate", "--dry-run"}, common...)
	if rc := cli.Run("test", args); rc != 0 {
		t.Fatalf("migrate --dry-run: rc = %d, want 0", rc)
	}
	for _, dbPath := range []string{
		filepath.Join(root, ".orchestrator", "state", "orch.db"),
		filepath.Join(root, ".orchestrator", "state", "file-project", "orch.db"),
	} {
		if _, err := os.Stat(dbPath); err == nil {
			t.Errorf("dry-run created %s — must touch nothing", dbPath)
		}
	}
	backups, _ := filepath.Glob(filepath.Join(root, ".orchestrator", "state-backups", "*"))
	if len(backups) != 0 {
		t.Errorf("dry-run created a backup: %v", backups)
	}
}

// TestMigrateImportsFileProjectMatchingPythonGolden is the acceptance test
// the G4.6 brief asks for: migrate a real file-mode project and check the
// result against what Python's own migrate.py produced on the same fixture.
func TestMigrateImportsFileProjectMatchingPythonGolden(t *testing.T) {
	root, common := newFileProject(t)
	args := append([]string{"migrate"}, common...)
	if rc := cli.Run("test", args); rc != 0 {
		t.Fatalf("migrate: rc = %d, want 0", rc)
	}

	dbPath := filepath.Join(root, ".orchestrator", "state", "file-project", "orch.db")
	got := dumpMigratedRows(t, dbPath, "file-project")

	var want goldenDump
	loadGolden(t, filepath.Join(fileProjectFixtureDir(t), "golden.json"), &want)

	if diff := goldenDiff(got, want); diff != "" {
		t.Errorf("migrated rows mismatch:\n%s", diff)
	}
}

func TestMigrateGuardsAgainstDoubleImportWithoutForce(t *testing.T) {
	_, common := newFileProject(t)
	first := append([]string{"migrate"}, common...)
	if rc := cli.Run("test", first); rc != 0 {
		t.Fatalf("first migrate: rc = %d, want 0", rc)
	}
	second := append([]string{"migrate"}, common...)
	if rc := cli.Run("test", second); rc == 0 {
		t.Fatalf("second migrate without --force: rc = 0, want non-zero (already migrated)")
	}
}

// TestMigrateForceReimportIsIdempotent re-runs with --force and checks the
// dedup hash (Backend.AppendEvent/RecordSpend) actually prevented doubling —
// the whole point of reusing Backend instead of a bespoke import path.
func TestMigrateForceReimportIsIdempotent(t *testing.T) {
	root, common := newFileProject(t)
	if rc := cli.Run("test", append([]string{"migrate"}, common...)); rc != 0 {
		t.Fatalf("first migrate: rc = %d, want 0", rc)
	}
	if rc := cli.Run("test", append([]string{"migrate", "--force"}, common...)); rc != 0 {
		t.Fatalf("second migrate --force: rc = %d, want 0", rc)
	}

	dbPath := filepath.Join(root, ".orchestrator", "state", "file-project", "orch.db")
	got := dumpMigratedRows(t, dbPath, "file-project")
	if len(got.Events) != 4 {
		t.Errorf("events = %d after re-import, want 4 (deduped, not doubled)", len(got.Events))
	}
	if len(got.Spend) != 2 {
		t.Errorf("spend = %d after re-import, want 2 (deduped, not doubled)", len(got.Spend))
	}
}

// TestMigrateSkipsUnknownEventTypeInsteadOfFailing covers the one place
// this port's behavior can't match Python bit for bit: Backend.AppendEvent
// validates event_type against a closed set, migrate.py's raw INSERT does
// not. A historical file-mode project can carry an event type from years
// ago that the current set doesn't recognize; migrate must skip that one
// row and keep going; it must not abort the whole import over it.
func TestMigrateSkipsUnknownEventTypeInsteadOfFailing(t *testing.T) {
	root, common := newFileProject(t)
	eventsPath := filepath.Join(root, ".orchestrator", "state", "events-run1.jsonl")
	extra := `{"ts":"2026-01-01T09:00:00Z","task_id":"F1.T1","event_type":"some_ancient_type","backend":"claude","extra":{}}` + "\n"
	appendToFile(t, eventsPath, extra)

	if rc := cli.Run("test", append([]string{"migrate"}, common...)); rc != 0 {
		t.Fatalf("migrate with one unknown event type: rc = %d, want 0", rc)
	}

	dbPath := filepath.Join(root, ".orchestrator", "state", "file-project", "orch.db")
	got := dumpMigratedRows(t, dbPath, "file-project")
	if len(got.Events) != 4 {
		t.Errorf("events = %d, want 4 (the unknown-type row skipped, the other 4 still imported)", len(got.Events))
	}
	for _, e := range got.Events {
		if e.EventType == "some_ancient_type" {
			t.Errorf("unknown event type made it into the database: %+v", e)
		}
	}
}

func TestMigrateCreatesABackupOfTheStateDir(t *testing.T) {
	root, common := newFileProject(t)
	if rc := cli.Run("test", append([]string{"migrate"}, common...)); rc != 0 {
		t.Fatalf("migrate: rc = %d, want 0", rc)
	}
	backups, err := filepath.Glob(filepath.Join(root, ".orchestrator", "state-backups", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one", backups)
	}
	if _, err := os.Stat(filepath.Join(backups[0], "events-run1.jsonl")); err != nil {
		t.Errorf("backup missing events-run1.jsonl: %v", err)
	}
}

func appendToFile(t *testing.T, path, content string) {
	t.Helper()
	// #nosec G304 -- path is this test's own fixture file under t.TempDir().
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// ---- golden comparison ----------------------------------------------------

type goldenTaskRuntime struct {
	TaskID       string `json:"task_id"`
	Status       string `json:"status"`
	CommentsJSON string `json:"comments_json"`
}

type goldenEvent struct {
	RunID     string `json:"run_id"`
	EventType string `json:"event_type"`
	TaskID    string `json:"task_id"`
	Backend   string `json:"backend"`
	TS        string `json:"ts"`
	ExtraJSON string `json:"extra_json"`
}

type goldenSpend struct {
	TS        string  `json:"ts"`
	TaskID    string  `json:"task_id"`
	Backend   string  `json:"backend"`
	Model     string  `json:"model"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
	DurationS float64 `json:"duration_s"`
	Estimated int     `json:"estimated"`
}

type goldenDump struct {
	TasksRuntime []goldenTaskRuntime `json:"tasks_runtime"`
	Events       []goldenEvent       `json:"events"`
	Spend        []goldenSpend       `json:"spend"`
}

func loadGolden(t *testing.T, path string, v *goldenDump) {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- path is this test's own fixed golden.json path.
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

// dumpMigratedRows reads dbPath with the same three queries
// make-fixture.py used to build the golden, via a plain database/sql
// connection — the "sqlite" driver is already registered process-wide by
// internal/state's blank import, which this package pulls in transitively.
func dumpMigratedRows(t *testing.T, dbPath, projectID string) goldenDump {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	var out goldenDump

	taskRows, err := db.Query(
		"SELECT task_id, status, comments_json FROM tasks_runtime WHERE project_id = ? ORDER BY task_id", projectID)
	if err != nil {
		t.Fatal(err)
	}
	for taskRows.Next() {
		var r goldenTaskRuntime
		if err := taskRows.Scan(&r.TaskID, &r.Status, &r.CommentsJSON); err != nil {
			t.Fatal(err)
		}
		out.TasksRuntime = append(out.TasksRuntime, r)
	}
	if err := taskRows.Err(); err != nil {
		t.Fatal(err)
	}

	eventRows, err := db.Query(
		"SELECT run_id, event_type, task_id, backend, ts, extra_json FROM events WHERE project_id = ? ORDER BY id", projectID)
	if err != nil {
		t.Fatal(err)
	}
	for eventRows.Next() {
		var r goldenEvent
		if err := eventRows.Scan(&r.RunID, &r.EventType, &r.TaskID, &r.Backend, &r.TS, &r.ExtraJSON); err != nil {
			t.Fatal(err)
		}
		out.Events = append(out.Events, r)
	}
	if err := eventRows.Err(); err != nil {
		t.Fatal(err)
	}

	spendRows, err := db.Query(
		`SELECT ts, task_id, backend, model, tokens_in, tokens_out, cost_usd, duration_s, estimated
		   FROM spend WHERE project_id = ? ORDER BY ts, task_id`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	for spendRows.Next() {
		var r goldenSpend
		if err := spendRows.Scan(&r.TS, &r.TaskID, &r.Backend, &r.Model, &r.TokensIn, &r.TokensOut,
			&r.CostUSD, &r.DurationS, &r.Estimated); err != nil {
			t.Fatal(err)
		}
		out.Spend = append(out.Spend, r)
	}
	if err := spendRows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

// goldenDiff compares two dumps, treating each row's *_json field as parsed
// JSON rather than a literal string — Python's json.dumps and Go's
// encoding/json format an equivalent object differently (space after ':',
// key order), and that's not a real mismatch.
func goldenDiff(got, want goldenDump) string {
	if len(got.TasksRuntime) != len(want.TasksRuntime) {
		return sprintfMismatch("tasks_runtime count", got.TasksRuntime, want.TasksRuntime)
	}
	for i := range got.TasksRuntime {
		g, w := got.TasksRuntime[i], want.TasksRuntime[i]
		if g.TaskID != w.TaskID || g.Status != w.Status || !jsonEqual(g.CommentsJSON, w.CommentsJSON) {
			return sprintfMismatch("tasks_runtime["+g.TaskID+"]", g, w)
		}
	}
	if len(got.Events) != len(want.Events) {
		return sprintfMismatch("events count", got.Events, want.Events)
	}
	for i := range got.Events {
		g, w := got.Events[i], want.Events[i]
		if g.RunID != w.RunID || g.EventType != w.EventType || g.TaskID != w.TaskID ||
			g.Backend != w.Backend || g.TS != w.TS || !jsonEqual(g.ExtraJSON, w.ExtraJSON) {
			return sprintfMismatch("events[]", g, w)
		}
	}
	if len(got.Spend) != len(want.Spend) {
		return sprintfMismatch("spend count", got.Spend, want.Spend)
	}
	for i := range got.Spend {
		if !reflect.DeepEqual(got.Spend[i], want.Spend[i]) {
			return sprintfMismatch("spend[]", got.Spend[i], want.Spend[i])
		}
	}
	return ""
}

func jsonEqual(a, b string) bool {
	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func sprintfMismatch(label string, got, want any) string {
	gj, _ := json.MarshalIndent(got, "", "  ")
	wj, _ := json.MarshalIndent(want, "", "  ")
	var b strings.Builder
	b.WriteString(label)
	b.WriteString("\ngot:\n")
	b.Write(gj)
	b.WriteString("\nwant:\n")
	b.Write(wj)
	return b.String()
}
