package state

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The CI columns arrived in migration 005 and `scanTask` has read them since
// the start; nothing could write them or query on them, which made the
// CI-polling path unbuildable. Fourth time in this port a set of columns
// landed without its methods — see the table in opus.md.

func ciSeeded(t *testing.T, ids ...string) *SQLite {
	t.Helper()
	return seeded(t, ids...)
}

// The filter is on BOTH columns, and each half is asserted by the row it must
// exclude. A version filtering on `ci_status` alone would hand the poller a
// row with an empty pr_url and it would ask the forge about a blank URL once
// per tick, forever.
func TestTasksWithPendingCIFiltersOnBothColumns(t *testing.T) {
	ctx := context.Background()
	b := ciSeeded(t, "T1", "T2", "T3", "T4")

	// T1: PR open, CI pending → the only one that should come back.
	if err := b.SetTaskPR(ctx, "T1", "https://example.test/pr/1"); err != nil {
		t.Fatal(err)
	}
	// T2: PR open, CI already resolved → out.
	if err := b.SetTaskPR(ctx, "T2", "https://example.test/pr/2"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetTaskCIStatus(ctx, "T2", "success"); err != nil {
		t.Fatal(err)
	}
	// T3: CI pending written directly, no PR → out. This is the row the
	// one-column filter would have let through.
	if _, err := b.db.write.ExecContext(ctx,
		`UPDATE tasks_runtime SET ci_status = 'pending'
		  WHERE project_id = ? AND task_id = ?`, b.projectID, "T3"); err != nil {
		t.Fatal(err)
	}
	// T4: untouched.

	got, err := b.TasksWithPendingCI(ctx)
	if err != nil {
		t.Fatalf("TasksWithPendingCI: %v", err)
	}
	if len(got) != 1 {
		ids := make([]string, len(got))
		for i, task := range got {
			ids[i] = task.ID
		}
		t.Fatalf("got %v, want only T1", ids)
	}
	if got[0].ID != "T1" {
		t.Errorf("got %q, want T1", got[0].ID)
	}
	if got[0].PRURL != "https://example.test/pr/1" {
		t.Errorf("PRURL = %q", got[0].PRURL)
	}
	if got[0].CIStatus != "pending" {
		t.Errorf("CIStatus = %q, want pending", got[0].CIStatus)
	}
}

// An empty-string pr_url is excluded too. Python checks only `IS NOT NULL`,
// which is enough there because nothing writes the empty string — but a row
// that got one would be polled forever against a blank URL, and the extra
// clause costs nothing.
func TestTasksWithPendingCIIgnoresAnEmptyPRURL(t *testing.T) {
	ctx := context.Background()
	b := ciSeeded(t, "T1")

	if _, err := b.db.write.ExecContext(ctx,
		`UPDATE tasks_runtime SET pr_url = '', ci_status = 'pending'
		  WHERE project_id = ? AND task_id = ?`, b.projectID, "T1"); err != nil {
		t.Fatal(err)
	}
	got, err := b.TasksWithPendingCI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing — an empty PR URL is not a PR", got)
	}
}

// SetTaskPR marks CI pending in the same write. Without that second half a
// task would have a PR nobody ever polls, which is the failure this pairing
// exists to prevent.
func TestSetTaskPRAlsoMarksCIPending(t *testing.T) {
	ctx := context.Background()
	b := ciSeeded(t, "T1")

	if err := b.SetTaskPR(ctx, "T1", "https://example.test/pr/9"); err != nil {
		t.Fatal(err)
	}
	task, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if task.PRURL != "https://example.test/pr/9" {
		t.Errorf("PRURL = %q", task.PRURL)
	}
	if task.CIStatus != "pending" {
		t.Errorf("CIStatus = %q, want pending — the PR is not polled otherwise", task.CIStatus)
	}
	// And it is immediately in the poller's set.
	pending, err := b.TasksWithPendingCI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != "T1" {
		t.Errorf("the task did not enter the pending set: %+v", pending)
	}
}

func TestSetTaskPRRejectsAnEmptyURL(t *testing.T) {
	b := ciSeeded(t, "T1")
	if err := b.SetTaskPR(context.Background(), "T1", ""); err == nil {
		t.Error("an empty PR URL must be refused, not stored")
	}
}

// Four statuses, not three. `skipped` is the one that gets forgotten: it is
// how a project with auto_pr on and no CI configured says "there was nothing
// to wait for", which is a different answer from `success`.
func TestSetTaskCIStatusAcceptsTheFourSchemaValues(t *testing.T) {
	ctx := context.Background()
	b := ciSeeded(t, "T1")

	for _, status := range []string{"pending", "success", "failure", "skipped"} {
		if err := b.SetTaskCIStatus(ctx, "T1", status); err != nil {
			t.Errorf("SetTaskCIStatus(%q): %v", status, err)
			continue
		}
		task, err := b.Task(ctx, "T1")
		if err != nil {
			t.Fatal(err)
		}
		if task.CIStatus != status {
			t.Errorf("CIStatus = %q, want %q", task.CIStatus, status)
		}
	}
}

// The value is checked before it reaches the schema's CHECK constraint. The
// constraint would catch it, as a bare "constraint failed" naming neither the
// column nor what was allowed — and this is a string a caller composes, so a
// typo is how it goes wrong.
func TestSetTaskCIStatusRejectsAnythingElse(t *testing.T) {
	ctx := context.Background()
	b := ciSeeded(t, "T1")

	for _, status := range []string{"", "Pending", "passed", "green", "ok"} {
		err := b.SetTaskCIStatus(ctx, "T1", status)
		if err == nil {
			t.Errorf("SetTaskCIStatus(%q) was accepted", status)
			continue
		}
		// The message names the value and the alternatives, because the
		// caller composed the string and has to see which part is wrong.
		msg := err.Error()
		for _, want := range []string{"failure", "pending", "skipped", "success"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not list %q", msg, want)
			}
		}
	}
}

// The counter comes back post-increment, which IS the number of the attempt
// that just started.
//
// Python returns nothing and its retry branch compares `ci_attempts` before
// incrementing, then emits an event carrying `ci_attempts + 1` — arithmetic
// done by hand at the call site, wrong only when `ci_max_retries > 1`, which
// is not the default. Returning the value removes the arithmetic.
func TestIncrementCIAttemptsReturnsTheNewCount(t *testing.T) {
	ctx := context.Background()
	b := ciSeeded(t, "T1")

	for want := 1; want <= 3; want++ {
		got, err := b.IncrementCIAttempts(ctx, "T1")
		if err != nil {
			t.Fatalf("IncrementCIAttempts: %v", err)
		}
		if got != want {
			t.Errorf("attempt %d: got %d", want, got)
		}
		task, err := b.Task(ctx, "T1")
		if err != nil {
			t.Fatal(err)
		}
		if task.CIAttempts != want {
			t.Errorf("stored CIAttempts = %d, want %d", task.CIAttempts, want)
		}
	}
}

// An UPDATE whose WHERE matches nothing succeeds, so every one of these would
// report success for a task id that does not exist. That is issue #81, the bug
// `Transition` was fixed for in #107 — and there are four more writes here to
// make it in.
func TestCIWritesReportAMissingTask(t *testing.T) {
	ctx := context.Background()
	b := ciSeeded(t, "T1")

	if err := b.SetTaskPR(ctx, "NOPE", "https://example.test/pr/1"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("SetTaskPR on a missing task = %v, want ErrTaskNotFound", err)
	}
	if err := b.SetTaskCIStatus(ctx, "NOPE", "success"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("SetTaskCIStatus on a missing task = %v, want ErrTaskNotFound", err)
	}
	if _, err := b.IncrementCIAttempts(ctx, "NOPE"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("IncrementCIAttempts on a missing task = %v, want ErrTaskNotFound", err)
	}
	// And nothing was written to the task that does exist.
	task, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if task.PRURL != "" || task.CIStatus != "" || task.CIAttempts != 0 {
		t.Errorf("a write aimed at a missing task touched T1: %+v", task)
	}
}

// Nothing pending is an empty slice and no error — a project between CI runs
// is the normal case.
func TestTasksWithPendingCIEmpty(t *testing.T) {
	got, err := ciSeeded(t, "T1").TasksWithPendingCI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}
