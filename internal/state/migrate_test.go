package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const pythonFixture = "testdata/orch-py-0.11.0.db"

// copyPythonFixture returns a writable copy of the checked-in Python database.
//
// Opening it in WAL mode writes -wal/-shm sidecars and can rewrite the header,
// so a test must never open the file in the repo: that would let a Go-side bug
// quietly edit the very evidence it is being checked against.
func copyPythonFixture(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(pythonFixture)
	if err != nil {
		t.Fatalf("read the Python fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "orch.db")
	// #nosec G703 -- the destination is t.TempDir(), not caller input.
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatalf("copy the fixture: %v", err)
	}
	return path
}

func TestLoadMigrationsIsAContiguousRun(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no migrations embedded — check the //go:embed directive")
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Errorf("migration %d is %s (version %d)", i, m.name, m.version)
		}
		if m.sql == "" {
			t.Errorf("migration %s is empty", m.name)
		}
	}
	if got, want := ms[len(ms)-1].version, 6; got != want {
		t.Errorf("highest migration is %d, want %d — bump this when 007 ships", got, want)
	}
}

// Migrations 001-005 are copied byte for byte from the Python tree (ADR-G3).
// If somebody edits one here instead of adding a new file, a database that
// round-trips between the two implementations diverges. 006 onward has no
// Python counterpart — the Python line froze at v0.11.0-py (ADR-G0) before
// 006 existed — so this only checks the five that predate the freeze; see
// migrate.go's own comment.
func TestEmbeddedMigrationsMatchThePythonTree(t *testing.T) {
	const lastMigrationSharedWithPython = 5

	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	pythonDir := filepath.Join("..", "..", "orchestrator", "state", "sqlite_migrations")
	if _, err := os.Stat(pythonDir); os.IsNotExist(err) {
		t.Skip("the Python tree is gone; the embedded copies are now the only source")
	}
	for _, m := range ms {
		if m.version > lastMigrationSharedWithPython {
			continue
		}
		// #nosec G304 -- m.name comes from our own embedded migrations, and
		// pythonDir is a constant relative path inside the repo.
		want, err := os.ReadFile(filepath.Join(pythonDir, m.name))
		if err != nil {
			t.Errorf("read %s from the Python tree: %v", m.name, err)
			continue
		}
		if m.sql != string(want) {
			t.Errorf("%s differs from orchestrator/state/sqlite_migrations/%s", m.name, m.name)
		}
	}
}

func TestOpenFreshAppliesEveryMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "orch.db")

	db, applied, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if applied != 6 {
		t.Errorf("applied %d migrations on a fresh DB, want 6", applied)
	}
	v, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 6 {
		t.Errorf("user_version = %d, want 6", v)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "orch.db")

	db, applied, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if applied == 0 {
		t.Fatal("first Open applied nothing")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, applied2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	if applied2 != 0 {
		t.Errorf("second Open applied %d migrations, want 0", applied2)
	}
}

// The claim ADR-G3 makes to users: swap the binary, keep your database.
// That claim was never "the schema stops moving" — it is "nothing Python
// wrote is lost or misread." A v0.11.0 database is frozen at user_version 5
// (Python never gets a 006); opening it here still applies every Go-only
// migration past that point, the same as it would for a database Go itself
// wrote at an older version of this binary.
func TestOpenPythonWrittenDatabaseAppliesOnlyGoOnlyMigrations(t *testing.T) {
	ctx := context.Background()

	path := copyPythonFixture(t)

	db, applied, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open a Python-written database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if applied != 1 {
		t.Errorf("applied %d migrations to a v0.11.0 (schema 5) database, want 1 (006, Go-only)", applied)
	}
	v, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 6 {
		t.Errorf("user_version = %d, want 6", v)
	}
}

// Every table the Python backend writes must be readable here. This is a
// shape check, not a data check — the data assertions live in the Backend
// tests once the model types land.
func TestPythonFixtureHasTheExpectedRows(t *testing.T) {
	ctx := context.Background()
	path := copyPythonFixture(t)
	db, _, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cases := []struct {
		table string
		want  int
	}{
		{"projects", 1},
		{"tasks_definition", 5},
		{"tasks_runtime", 5},
		// Five events were appended; one was a byte-identical duplicate and
		// the dedup hash dropped it. If this reads 5, INSERT OR IGNORE is
		// not doing its job.
		{"events", 4},
		{"spend", 2},
		{"dispatches", 1},
		{"runs", 1},
		{"milestones", 1},
		{"findings", 0},
	}
	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			var n int
			err := db.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+c.table).Scan(&n)
			if err != nil {
				t.Fatalf("count %s: %v", c.table, err)
			}
			if n != c.want {
				t.Errorf("%s has %d rows, want %d", c.table, n, c.want)
			}
		})
	}
}

// The dedup hashes Python stored must be exactly the ones Go computes. This
// reads them out of the fixture rather than restating the constants, so the
// test cannot drift from the file it is defending.
func TestDedupHashesInThePythonFixtureMatchGo(t *testing.T) {
	ctx := context.Background()
	path := copyPythonFixture(t)
	db, _, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	t.Run("spend", func(t *testing.T) {
		rows, err := db.read.QueryContext(ctx,
			`SELECT project_id, ts, task_id, backend, model, cost_usd, duration_s, dedup_hash
			 FROM spend ORDER BY id`)
		if err != nil {
			t.Fatalf("query spend: %v", err)
		}
		defer func() { _ = rows.Close() }()

		n := 0
		for rows.Next() {
			var pid, ts, taskID, backend, model, stored string
			var cost, dur float64
			if err := rows.Scan(&pid, &ts, &taskID, &backend, &model, &cost, &dur, &stored); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got := spendDedupHash(pid, ts, taskID, backend, model, cost, dur)
			if got != stored {
				t.Errorf("spend %s: Go computes %s, Python stored %s (cost=%v duration=%v)",
					taskID, got, stored, cost, dur)
			}
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate spend: %v", err)
		}
		if n != 2 {
			t.Errorf("checked %d spend rows, want 2", n)
		}
	})
}
