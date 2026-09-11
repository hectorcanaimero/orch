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
