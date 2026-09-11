package state

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The artifact names "SQLite with several writers" as a top risk: the engine,
// the MCP server and `orch task set` all write to one file. Python never
// covered this — it has no race detector and its concurrency test spawns two
// processes and checks the row count.
//
// Run with -race (the Makefile does). What this pins:
//
//   - 50 goroutines writing concurrently produce zero SQLITE_BUSY errors,
//   - every write lands,
//   - the *sql.DB handle is shared across goroutines without a data race.
func TestConcurrentWritersDoNotCollide(t *testing.T) {
	const (
		workers   = 50
		perWorker = 4
	)
	ctx := context.Background()
	db := newSeededDB(t, workers)

	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			taskID := fmt.Sprintf("T-%03d", i)
			// Each goroutine owns one task, so the writes are logically
			// independent; any contention comes from SQLite alone.
			for s := 0; s < perWorker; s++ {
				status := []string{"todo", "in-progress", "done", "blocked"}[s]
				_, err := db.write.ExecContext(ctx,
					`UPDATE tasks_runtime SET status = ?, updated_at = ?
					 WHERE project_id = ? AND task_id = ?`,
					status, "2026-09-01T00:00:00+00:00", "p", taskID)
				if err != nil {
					errs <- fmt.Errorf("worker %d step %d: %w", i, s, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if strings.Contains(strings.ToUpper(err.Error()), "BUSY") {
			t.Errorf("SQLITE_BUSY under concurrent writers — the single-writer "+
				"pool or busy_timeout is not doing its job: %v", err)
		} else {
			t.Errorf("concurrent write failed: %v", err)
		}
	}

	var n int
	err := db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks_runtime WHERE project_id = 'p' AND status = 'blocked'`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != workers {
		t.Errorf("%d tasks reached the final status, want %d", n, workers)
	}
}

// Readers must not be blocked by an open write transaction — that is the
// property WAL buys, and the dashboard polling a live run depends on it.
func TestReadsProceedDuringAWriteTransaction(t *testing.T) {
	ctx := context.Background()
	db := newSeededDB(t, 1)

	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks_runtime SET status = 'in-progress' WHERE task_id = 'T-000'`,
	); err != nil {
		t.Fatalf("write inside tx: %v", err)
	}

	// The write is uncommitted. A reader on the other pool must still see the
	// old value rather than block until commit.
	var status string
	err = db.read.QueryRowContext(ctx,
		`SELECT status FROM tasks_runtime WHERE task_id = 'T-000'`).Scan(&status)
	if err != nil {
		t.Fatalf("read during an open write tx: %v", err)
	}
	if status != "todo" {
		t.Errorf("reader saw %q, want the pre-transaction value %q", status, "todo")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := db.read.QueryRowContext(ctx,
		`SELECT status FROM tasks_runtime WHERE task_id = 'T-000'`).Scan(&status); err != nil {
		t.Fatalf("read after commit: %v", err)
	}
	if status != "in-progress" {
		t.Errorf("after commit the reader saw %q, want %q", status, "in-progress")
	}
}

// newSeededDB opens a fresh database and seeds one project with n tasks.
func newSeededDB(t *testing.T, n int) *DB {
	t.Helper()
	ctx := context.Background()
	db, _, err := Open(ctx, filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const ts = "2026-09-01T00:00:00+00:00"
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO projects (project_id, project_root, created_at, schema_version)
		 VALUES ('p', '/tmp/p', ?, 1)`, ts); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.write.ExecContext(ctx,
			`INSERT INTO tasks_runtime (project_id, task_id, status, comments_json, updated_at)
			 VALUES ('p', ?, 'todo', '[]', ?)`,
			fmt.Sprintf("T-%03d", i), ts); err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
	}
	return db
}
