package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// These read the fixture Python wrote, which is the point: `orch status`
// reporting zero on a project with history is the bug that prompted them, and
// a test against rows Go inserted itself would not have caught it.

func pythonBackend(t *testing.T) *SQLite {
	t.Helper()
	db, applied, err := Open(context.Background(), copyPythonFixture(t))
	if err != nil {
		t.Fatalf("Open the Python fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if applied != 0 {
		t.Fatalf("applied %d migrations to a v0.11.0 database", applied)
	}
	return NewSQLite(db, "billing-api", "/tmp/billing-api")
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// ---- spend -----------------------------------------------------------------

// The fixture's two rows: F0.T1 at 2026-09-01T10:30:00+00:00 and F0.T2 at
// 2026-09-02T11:15:00+00:00, both on the claude backend.
func TestSpendSinceWindows(t *testing.T) {
	ctx := context.Background()
	b := pythonBackend(t)

	cases := []struct {
		name    string
		backend string
		since   string
		want    []string // task ids, oldest first
	}{
		{"everything", "claude", "2026-01-01T00:00:00Z", []string{"F0.T1", "F0.T2"}},
		{"a window that excludes the older row", "claude", "2026-09-02T00:00:00Z", []string{"F0.T2"}},
		{"a window that starts after both", "claude", "2026-10-01T00:00:00Z", nil},
		// The cutoff is inclusive, matching Python's `ts >= cutoff`. An
		// exclusive one would drop a row written in the same second the
		// window opened.
		{"the cutoff is inclusive", "claude", "2026-09-02T11:15:00Z", []string{"F0.T2"}},
		{"a backend with no spend", "codex", "2026-01-01T00:00:00Z", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.SpendSince(ctx, c.backend, mustTime(t, c.since))
			if err != nil {
				t.Fatalf("SpendSince: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d rows, want %d: %+v", len(got), len(c.want), got)
			}
			for i, want := range c.want {
				if got[i].TaskID != want {
					t.Errorf("row %d is %q, want %q (oldest first)", i, got[i].TaskID, want)
				}
			}
		})
	}
}

// Every field Python wrote must come back, because the budget gate sums
// tokens and the dashboard shows cost — a row read with zeroes is worse than
// no row, since it looks like real usage of nothing.
func TestSpendSinceReturnsWhatPythonWrote(t *testing.T) {
	ctx := context.Background()
	b := pythonBackend(t)

	got, err := b.SpendSince(ctx, "claude", mustTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("SpendSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want the 2 Python wrote", len(got))
	}

	first := got[0]
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"task_id", first.TaskID, "F0.T1"},
		{"backend", first.Backend, "claude"},
		{"model", first.Model, "claude/claude-sonnet-4-6"},
		{"tokens_in", first.TokensIn, 18000},
		{"tokens_out", first.TokensOut, 5200},
		{"cost_usd", first.CostUSD, 0.42},
		{"duration_s", first.DurationS, 5400.0},
		{"project_id", first.ProjectID, "billing-api"},
		{"estimated", first.Estimated, false},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, Python wrote %v", c.name, c.got, c.want)
		}
	}
}

// The budget gate's actual question: how many tokens has this provider used
// in the window? This is the calculation that silently returned zero on every
// sqlite project in Python, because its gate read JSONL files that the sqlite
// backend never writes.
func TestSpendSinceAnswersTheBudgetGatesQuestion(t *testing.T) {
	ctx := context.Background()
	b := pythonBackend(t)

	rows, err := b.SpendSince(ctx, "claude", mustTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("SpendSince: %v", err)
	}
	tokens := 0
	for _, s := range rows {
		tokens += s.TokensIn + s.TokensOut
	}
	// 18000+5200 and 41000+9100.
	if tokens != 73300 {
		t.Errorf("tokens in window = %d, want 73300 — the budget gate sums "+
			"exactly this, and a zero here is a guardrail that never trips", tokens)
	}
}

func TestSpendSinceIsScopedToTheProject(t *testing.T) {
	ctx := context.Background()
	b := pythonBackend(t)
	other := NewSQLite(b.db, "someone-else", "/tmp/other")

	got, err := other.SpendSince(ctx, "claude", mustTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("SpendSince: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("another project read %d of billing-api's spend rows", len(got))
	}
}

// A row with no usable timestamp cannot be placed in a window. Excluding it
// under-reports; including it over-reports and stalls dispatch on a guess.
// Under-reporting is the safer of the two, and it is what Python does — its
// `_entries_since` skips a row whose ts is missing or unparseable.
func TestSpendSinceExcludesRowsWithNoTimestamp(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	if _, err := b.db.write.ExecContext(ctx,
		`INSERT INTO spend (project_id, ts, task_id, backend, model, tokens_in,
		                    tokens_out, cost_usd, duration_s, estimated, dedup_hash)
		 VALUES ('proj', '', 'T1', 'claude', 'm', 999, 999, 1.0, 1.0, 0, 'h1')`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := b.RecordSpend(ctx, Spend{
		TS: "2026-09-11T10:00:00Z", TaskID: "T1", Backend: "claude",
		Model: "m", TokensIn: 10, TokensOut: 5, CostUSD: 0.1, DurationS: 1,
	}); err != nil {
		t.Fatalf("RecordSpend: %v", err)
	}

	got, err := b.SpendSince(ctx, "claude", mustTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("SpendSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want only the dated one: %+v", len(got), got)
	}
	if got[0].TokensIn != 10 {
		t.Errorf("the undated row was counted: %+v", got[0])
	}
}

func TestTotalSpendUSD(t *testing.T) {
	ctx := context.Background()
	b := pythonBackend(t)

	// 0.42 + 1.0, across two different models.
	total, err := b.TotalSpendUSD(ctx, mustTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("TotalSpendUSD: %v", err)
	}
	if total != 1.42 {
		t.Errorf("total = %v, want 1.42", total)
	}

	// SUM over no rows is NULL in SQLite. Zero spent, not an error — the
	// difference between "nothing yet" and a crash on a fresh project.
	empty, err := b.TotalSpendUSD(ctx, mustTime(t, "2030-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("TotalSpendUSD over an empty window: %v", err)
	}
	if empty != 0 {
		t.Errorf("total over an empty window = %v, want 0", empty)
	}
}

// ---- runs ------------------------------------------------------------------

func TestLatestRunReadsThePythonFixture(t *testing.T) {
	ctx := context.Background()
	b := pythonBackend(t)

	got, err := b.LatestRun(ctx)
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if got.RunID != "fixture-run-0001" {
		t.Errorf("run_id = %q, want the one Python created", got.RunID)
	}
	if got.Mode != "auto" {
		t.Errorf("mode = %q, want auto", got.Mode)
	}
	if got.Status != "live" {
		t.Errorf("status = %q, want live", got.Status)
	}
	// The fixture has one in-flight dispatch on F1.T1.
	if got.InFlight != 1 {
		t.Errorf("in_flight = %d, want 1 — a run left mid-dispatch is exactly "+
			"what the reconciler looks for", got.InFlight)
	}
}

func TestLatestRunOnAProjectThatNeverRan(t *testing.T) {
	b := seeded(t, "T1")
	_, err := b.LatestRun(context.Background())
	if !errors.Is(err, ErrNoRuns) {
		t.Fatalf("got %v, want ErrNoRuns", err)
	}
}

// Two runs can share a started_at at second precision. Without the run_id
// tiebreaker, "latest" would vary between calls and `orch status` would
// flicker between them.
func TestLatestRunIsDeterministicOnATie(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	const ts = "2026-09-11T12:00:00Z"
	for _, id := range []string{"run-a", "run-b", "run-c"} {
		if _, err := b.db.write.ExecContext(ctx,
			`INSERT INTO runs (run_id, project_id, started_at, updated_at, mode,
			                   parent_pid, status)
			 VALUES (?, 'proj', ?, ?, 'auto', 0, 'live')`, id, ts, ts); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	first, err := b.LatestRun(ctx)
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if first.RunID != "run-c" {
		t.Errorf("run_id = %q, want run-c (the highest id wins a tie)", first.RunID)
	}
	for i := 0; i < 5; i++ {
		again, err := b.LatestRun(ctx)
		if err != nil {
			t.Fatalf("LatestRun: %v", err)
		}
		if again.RunID != first.RunID {
			t.Fatalf("LatestRun returned %q then %q on the same data",
				first.RunID, again.RunID)
		}
	}
}

func TestRunsAreNewestFirst(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	for _, r := range []struct{ id, ts string }{
		{"run-old", "2026-09-01T09:00:00Z"},
		{"run-new", "2026-09-11T09:00:00Z"},
		{"run-mid", "2026-09-05T09:00:00Z"},
	} {
		if _, err := b.db.write.ExecContext(ctx,
			`INSERT INTO runs (run_id, project_id, started_at, updated_at, mode,
			                   parent_pid, status)
			 VALUES (?, 'proj', ?, ?, 'semi', 0, 'done')`, r.id, r.ts, r.ts); err != nil {
			t.Fatalf("seed %s: %v", r.id, err)
		}
	}

	got, err := b.Runs(ctx)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d runs, want 3", len(got))
	}
	want := []string{"run-new", "run-mid", "run-old"}
	for i, w := range want {
		if got[i].RunID != w {
			t.Errorf("run %d is %q, want %q", i, got[i].RunID, w)
		}
	}
	// And the first of that list is what LatestRun returns.
	latest, err := b.LatestRun(ctx)
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if latest.RunID != got[0].RunID {
		t.Errorf("LatestRun says %q but Runs()[0] is %q", latest.RunID, got[0].RunID)
	}
}

func TestRunsOnAProjectThatNeverRanIsEmptyNotAnError(t *testing.T) {
	got, err := seeded(t, "T1").Runs(context.Background())
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d runs on a project that never ran", len(got))
	}
}

func TestRunsAreScopedToTheProject(t *testing.T) {
	ctx := context.Background()
	b := pythonBackend(t)
	other := NewSQLite(b.db, "someone-else", "/tmp/other")

	got, err := other.Runs(ctx)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("another project read %d of billing-api's runs", len(got))
	}
	if _, err := other.LatestRun(ctx); !errors.Is(err, ErrNoRuns) {
		t.Errorf("LatestRun leaked across projects: %v", err)
	}
}

// The two timestamp forms that coexist in a real orch.db, plus the shapes a
// hand-edit can leave behind. This is the helper the window depends on, so it
// gets its own table rather than being tested only through its callers.
func TestParseTSAcceptsBothStoredForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
		want string // RFC3339 in UTC
	}{
		{"the +00:00 form `spend.ts` uses", "2026-09-01T10:30:00+00:00", true, "2026-09-01T10:30:00Z"},
		{"the Z form `runs.started_at` uses", "2026-09-11T18:32:54Z", true, "2026-09-11T18:32:54Z"},
		{"a non-UTC offset normalises", "2026-09-01T12:30:00+02:00", true, "2026-09-01T10:30:00Z"},
		{"fractional seconds", "2026-09-01T10:30:00.123Z", true, "2026-09-01T10:30:00Z"},
		{"no zone at all is read as UTC", "2026-09-01T10:30:00", true, "2026-09-01T10:30:00Z"},

		{"empty", "", false, ""},
		{"a date with no time", "2026-09-01", false, ""},
		{"prose", "yesterday", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseTS(c.in)
			if ok != c.ok {
				t.Fatalf("ParseTS(%q) ok = %v, want %v", c.in, ok, c.ok)
			}
			if !ok {
				return
			}
			if got.Format("2006-01-02T15:04:05Z") != c.want {
				t.Errorf("ParseTS(%q) = %s, want %s", c.in, got.Format(time.RFC3339), c.want)
			}
		})
	}
}

// The bug this whole approach exists to avoid, stated directly: a lexical
// comparison puts "+" (0x2B) before "Z" (0x5A), so a `+00:00` row would be
// dropped by a `Z` cutoff at the same instant. Both forms are in the fixture.
func TestWindowDoesNotDependOnTheTimestampForm(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	const instant = "2026-09-01T10:30:00"
	rows := []struct{ task, ts string }{
		{"T-zulu", instant + "Z"},
		{"T-offset", instant + "+00:00"},
	}
	for _, r := range rows {
		if err := b.RecordSpend(ctx, Spend{
			TS: r.ts, TaskID: r.task, Backend: "claude", Model: "m",
			TokensIn: 100, TokensOut: 100, CostUSD: 1, DurationS: 1,
		}); err != nil {
			t.Fatalf("RecordSpend %s: %v", r.task, err)
		}
	}

	// A cutoff at exactly that instant must find BOTH, however each was
	// written. A lexical SQL comparison would return one.
	got, err := b.SpendSince(ctx, "claude", mustTime(t, instant+"Z"))
	if err != nil {
		t.Fatalf("SpendSince: %v", err)
	}
	if len(got) != 2 {
		ids := make([]string, 0, len(got))
		for _, s := range got {
			ids = append(ids, s.TaskID)
		}
		t.Errorf("found %v, want both rows — the window is form-sensitive", ids)
	}

	total, err := b.TotalSpendUSD(ctx, mustTime(t, instant+"Z"))
	if err != nil {
		t.Fatalf("TotalSpendUSD: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %v, want 2 — one of the rows was dropped by its form", total)
	}
}

// Runs are ordered by parsed time, so a database holding both forms still
// lists newest-first. Python sorts these lexically in SQL and would get this
// wrong.
func TestRunsOrderDoesNotDependOnTheTimestampForm(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	for _, r := range []struct{ id, ts string }{
		{"run-older", "2026-09-05T09:00:00Z"},      // Z, earlier
		{"run-newer", "2026-09-11T09:00:00+00:00"}, // offset, later
	} {
		if _, err := b.db.write.ExecContext(ctx,
			`INSERT INTO runs (run_id, project_id, started_at, updated_at, mode,
			                   parent_pid, status)
			 VALUES (?, 'proj', ?, ?, 'auto', 0, 'done')`, r.id, r.ts, r.ts); err != nil {
			t.Fatalf("seed %s: %v", r.id, err)
		}
	}

	got, err := b.Runs(ctx)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d runs, want 2", len(got))
	}
	// Lexically "2026-09-11T09:00:00+00:00" < "2026-09-05T09:00:00Z", so a
	// SQL sort would put the OLDER run first.
	if got[0].RunID != "run-newer" {
		t.Errorf("newest run is %q, want run-newer — the sort is form-sensitive",
			got[0].RunID)
	}
	latest, err := b.LatestRun(ctx)
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if latest.RunID != "run-newer" {
		t.Errorf("LatestRun = %q, want run-newer", latest.RunID)
	}
}

// A run with an unreadable started_at is kept and sorts last. Dropping it
// would hide a project's history; a spend row is dropped instead, because
// counting an undatable one distorts a guardrail.
func TestRunsKeepAnUndatedRunAtTheEnd(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	for _, r := range []struct{ id, ts string }{
		{"run-dated", "2026-09-05T09:00:00Z"},
		{"run-undated", "who knows"},
	} {
		if _, err := b.db.write.ExecContext(ctx,
			`INSERT INTO runs (run_id, project_id, started_at, updated_at, mode,
			                   parent_pid, status)
			 VALUES (?, 'proj', ?, ?, 'auto', 0, 'done')`, r.id, r.ts, r.ts); err != nil {
			t.Fatalf("seed %s: %v", r.id, err)
		}
	}

	got, err := b.Runs(ctx)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d runs, want both kept", len(got))
	}
	if got[0].RunID != "run-dated" || got[1].RunID != "run-undated" {
		t.Errorf("order = %s, %s; want the dated run first", got[0].RunID, got[1].RunID)
	}
}

// ---- run tallies -----------------------------------------------------------

// The fixture cannot test this and that is exactly why the rows are built here.
//
// `orch-py-0.11.0.db` has one run whose three JSON columns are empty arrays, so
// a parser that always answered 0 would pass against it — and pass
// `scripts/parity.sh` too, because Python reports 0 for the same rows. The same
// blind spot that let `orch status` report no spend on a project full of it.
func TestRunTalliesCountTheJSONLists(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	const ts = "2026-09-11T12:00:00Z"
	if _, err := b.db.write.ExecContext(ctx,
		`INSERT INTO runs (run_id, project_id, started_at, updated_at, mode,
		                   parent_pid, status,
		                   completed_json, blocked_json, deferred_json)
		 VALUES ('run-tally', 'proj', ?, ?, 'auto', 0, 'live', ?, ?, ?)`,
		ts, ts,
		`["F0.T1","F0.T2","F1.T1"]`,
		`["F1.T2"]`,
		`["F2.T1","F2.T2"]`); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	got, err := b.LatestRun(ctx)
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if got.CompletedCount != 3 {
		t.Errorf("CompletedCount = %d, want 3", got.CompletedCount)
	}
	if got.BlockedCount != 1 {
		t.Errorf("BlockedCount = %d, want 1", got.BlockedCount)
	}
	if got.DeferredCount != 2 {
		t.Errorf("DeferredCount = %d, want 2", got.DeferredCount)
	}
	// All three distinct, so a scan that read the same column three times
	// cannot pass.
	if got.CompletedCount == got.BlockedCount || got.BlockedCount == got.DeferredCount {
		t.Error("the three tallies must come from three different columns")
	}
}

// An empty string and `[]` are both zero — Python's `json.loads(col or "[]")`.
//
// NULL is not tested because the schema forbids it: `completed_json TEXT NOT
// NULL DEFAULT '[]'` (001_init.sql). The COALESCE in the query is there for a
// database that predates that constraint, not for one this code could write.
func TestRunTalliesTreatEmptyAsZero(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	const ts = "2026-09-11T12:00:00Z"
	if _, err := b.db.write.ExecContext(ctx,
		`INSERT INTO runs (run_id, project_id, started_at, updated_at, mode,
		                   parent_pid, status,
		                   completed_json, blocked_json, deferred_json)
		 VALUES ('run-empty', 'proj', ?, ?, 'auto', 0, 'live', '[]', '', '[]')`,
		ts, ts); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	got, err := b.LatestRun(ctx)
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if got.CompletedCount != 0 || got.BlockedCount != 0 || got.DeferredCount != 0 {
		t.Errorf("tallies = %d/%d/%d, want 0/0/0",
			got.CompletedCount, got.BlockedCount, got.DeferredCount)
	}
}

// The divergence, asserted: a column that does not hold a JSON array counts as
// zero instead of failing the read.
//
// Python's `list_runs` lets `json.loads` raise, so one corrupt column takes
// `orch status` down entirely. These three numbers feed no decision — they are
// rendered and nothing else — and "a tally says 0" beats "orch cannot tell you
// anything about this project".
func TestRunTalliesDegradeOnUnparseableJSON(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	const ts = "2026-09-11T12:00:00Z"
	if _, err := b.db.write.ExecContext(ctx,
		`INSERT INTO runs (run_id, project_id, started_at, updated_at, mode,
		                   parent_pid, status,
		                   completed_json, blocked_json, deferred_json)
		 VALUES ('run-corrupt', 'proj', ?, ?, 'auto', 0, 'live', ?, ?, ?)`,
		ts, ts,
		`{"not":"an array"}`, // parses, wrong shape
		`["unterminated`,     // does not parse
		`["F2.T1"]`); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	got, err := b.LatestRun(ctx)
	if err != nil {
		t.Fatalf("a corrupt tally column must not fail the read: %v", err)
	}
	if got.CompletedCount != 0 {
		t.Errorf("CompletedCount = %d, want 0 for JSON that is not an array", got.CompletedCount)
	}
	if got.BlockedCount != 0 {
		t.Errorf("BlockedCount = %d, want 0 for JSON that does not parse", got.BlockedCount)
	}
	// The good column is still read: one bad value must not zero the others.
	if got.DeferredCount != 1 {
		t.Errorf("DeferredCount = %d, want 1 — a corrupt sibling must not affect it",
			got.DeferredCount)
	}
}

// The fixture still has to report its real (zero) tallies rather than erroring,
// because empty arrays are the common case on a fresh project.
func TestRunTalliesOnThePythonFixture(t *testing.T) {
	got, err := pythonBackend(t).LatestRun(context.Background())
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if got.CompletedCount != 0 || got.BlockedCount != 0 || got.DeferredCount != 0 {
		t.Errorf("tallies = %d/%d/%d, want 0/0/0 — the fixture's arrays are empty",
			got.CompletedCount, got.BlockedCount, got.DeferredCount)
	}
}

// ---- in-flight dispatches ---------------------------------------------------

// The read half of RecordDispatch/ClearDispatch, and the third time the port
// landed a writer without its reader (spend and runs were the first two).
//
// The fixture has exactly one in-flight dispatch — F1.T1, pid 4242 — so the
// simple case is covered by a row Python wrote. Everything else is seeded
// here, because the fixture cannot express two runs or a cleared dispatch.
func TestInFlightDispatchesOnThePythonFixture(t *testing.T) {
	got, err := pythonBackend(t).InFlightDispatches(context.Background())
	if err != nil {
		t.Fatalf("InFlightDispatches: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d dispatches, want the fixture's one: %+v", len(got), got)
	}
	d := got[0]
	if d.RunID != "fixture-run-0001" || d.TaskID != "F1.T1" {
		t.Errorf("got %s/%s, want fixture-run-0001/F1.T1", d.RunID, d.TaskID)
	}
	if d.PID != 4242 {
		t.Errorf("pid = %d, want 4242", d.PID)
	}
	if d.Backend != "claude" {
		t.Errorf("backend = %q, want claude", d.Backend)
	}
	// The whole row, not just what a reconciler happens to need. Python's
	// reconcile query reads four columns; the type has ten, and a caller that
	// wants the log path should not have to add a second query.
	if d.SessionID != "sess-abc123" {
		t.Errorf("session_id = %q, want sess-abc123", d.SessionID)
	}
	if d.PromptPath == "" || d.LogPath == "" || d.OutputPath == "" {
		t.Errorf("paths are empty: %+v", d)
	}
	if d.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", d.Attempt)
	}
}

func TestInFlightDispatchesSpansRunsAndIsOrdered(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1", "T2", "T3")

	const ts = "2026-09-11T12:00:00Z"
	for _, runID := range []string{"run-b", "run-a"} {
		if err := b.StartRun(ctx, runID, "auto"); err != nil {
			t.Fatalf("StartRun %s: %v", runID, err)
		}
	}
	for _, d := range []Dispatch{
		{RunID: "run-b", TaskID: "T2", Backend: "claude", PID: 20, StartedAt: ts},
		{RunID: "run-a", TaskID: "T3", Backend: "codex", PID: 30, StartedAt: ts},
		{RunID: "run-a", TaskID: "T1", Backend: "claude", PID: 10, StartedAt: ts},
	} {
		if err := b.RecordDispatch(ctx, d); err != nil {
			t.Fatalf("RecordDispatch %s/%s: %v", d.RunID, d.TaskID, err)
		}
	}

	got, err := b.InFlightDispatches(ctx)
	if err != nil {
		t.Fatalf("InFlightDispatches: %v", err)
	}
	want := []string{"run-a/T1", "run-a/T3", "run-b/T2"}
	if len(got) != len(want) {
		t.Fatalf("got %d dispatches, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if key := got[i].RunID + "/" + got[i].TaskID; key != w {
			t.Errorf("dispatch %d = %s, want %s (sorted by run then task)", i, key, w)
		}
	}

	// Go randomises nothing here, but SQLite promises no order without an
	// ORDER BY, so a single agreeing run is not evidence.
	for i := 0; i < 10; i++ {
		again, err := b.InFlightDispatches(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for j := range got {
			if again[j] != got[j] {
				t.Fatalf("run %d differs at %d: %+v vs %+v", i, j, again[j], got[j])
			}
		}
	}
}

// A cleared dispatch is not in flight. Obvious, and the reason it is asserted
// is that the query filters on a status column the writer sets implicitly —
// nothing in RecordDispatch's signature says `in_flight`, so nothing but a
// test connects the two.
func TestClearedDispatchesAreNotInFlight(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1", "T2")

	if err := b.StartRun(ctx, "run-1", "auto"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"T1", "T2"} {
		if err := b.RecordDispatch(ctx, Dispatch{
			RunID: "run-1", TaskID: id, Backend: "claude", PID: 7,
			StartedAt: "2026-09-11T12:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.ClearDispatch(ctx, "run-1", "T1"); err != nil {
		t.Fatalf("ClearDispatch: %v", err)
	}

	got, err := b.InFlightDispatches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "T2" {
		t.Errorf("got %+v, want only T2 still in flight", got)
	}
}

// A row with no usable PID is still returned.
//
// The engine decides what to do about it — that is where the aliveness probe
// and the orphan policy live. Dropping it here would hide the row from the
// only code that can act on it, and a dispatch recorded without a PID is
// exactly the kind of thing someone needs to see.
func TestInFlightDispatchesReturnsRowsWithNoPID(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	if err := b.StartRun(ctx, "run-1", "auto"); err != nil {
		t.Fatal(err)
	}
	if err := b.RecordDispatch(ctx, Dispatch{
		RunID: "run-1", TaskID: "T1", Backend: "claude", PID: 0,
		StartedAt: "2026-09-11T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := b.InFlightDispatches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want the pid-less one: %+v", len(got), got)
	}
	if got[0].PID != 0 {
		t.Errorf("pid = %d, want 0 reported as it is", got[0].PID)
	}
}

// Nothing in flight is an empty slice and no error — a project between runs
// is the normal case, not a missing one.
func TestInFlightDispatchesEmpty(t *testing.T) {
	got, err := seeded(t, "T1").InFlightDispatches(context.Background())
	if err != nil {
		t.Fatalf("InFlightDispatches: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}
