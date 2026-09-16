package state

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver: cross-compiles without cgo (ADR-G1)
)

// queryer lets schemaVersion read from either a *sql.DB or a *sql.Tx.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB is a handle on one project's orch.db.
//
// Two pools, deliberately:
//
//   - write is capped at a single connection. SQLite serialises writers
//     anyway; capping the pool means the queue forms in Go, where it is
//     fair and cancellable, instead of inside SQLite as a storm of
//     SQLITE_BUSY retries. The engine, the MCP server and `orch task set`
//     all write, so this is the mechanism the "one writer" rule rests on.
//   - read is a normal pool. WAL lets readers run while a write is in
//     flight, which is the whole reason the dashboard can poll a live run.
//
// Both point at the same file.
type DB struct {
	write *sql.DB
	read  *sql.DB
	path  string
}

// Open opens (creating if needed) the SQLite database at path and applies any
// pending migrations. It returns the number of migrations applied so callers
// can log a first-run differently from a no-op.
//
// Opening a database written by orch v0.11.0 (Python) must apply zero.
func Open(ctx context.Context, path string) (*DB, int, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve %q: %w", path, err)
	}
	// 0750, not 0755: the directory holds a project's task history and spend.
	// Nothing outside the owner's group needs to traverse it.
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, 0, fmt.Errorf("create state dir for %q: %w", abs, err)
	}

	write, err := sql.Open("sqlite", dsn(abs, true))
	if err != nil {
		return nil, 0, fmt.Errorf("open %q for writing: %w", abs, err)
	}
	write.SetMaxOpenConns(1)

	read, err := sql.Open("sqlite", dsn(abs, false))
	if err != nil {
		_ = write.Close()
		return nil, 0, fmt.Errorf("open %q for reading: %w", abs, err)
	}

	db := &DB{write: write, read: read, path: abs}

	applied, err := migrate(ctx, write)
	if err != nil {
		_ = db.Close()
		return nil, 0, err
	}
	return db, applied, nil
}

// dsn builds the connection string. The pragmas match Python's `_connect`
// exactly (`sqlite_backend.py`), because a database is shared between the two
// implementations and a difference here shows up as corruption, not as a
// tidy error:
//
//   - foreign_keys=ON so ON DELETE CASCADE on `projects` actually fires;
//     without it, deleting a project silently orphans its rows, which is the
//     state doctor's F-9 check exists to find.
//   - journal_mode=WAL so readers do not block the writer.
//   - busy_timeout=5000 so a concurrent orch waits five seconds instead of
//     failing immediately.
//
// The writer additionally uses `_txlock=immediate`: a deferred transaction
// takes its write lock on the first write statement, which means two
// transactions that both start with a read can deadlock and one gets
// SQLITE_BUSY at commit. Taking the lock at BEGIN turns that into a wait.
func dsn(path string, writer bool) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	if writer {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Path is the absolute path of the database file.
func (db *DB) Path() string { return db.path }

// SchemaVersion reports `PRAGMA user_version`.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	return schemaVersion(ctx, db.read)
}

// Close releases both pools. Errors from either are reported; the second pool
// is closed even when the first fails.
func (db *DB) Close() error {
	errRead := db.read.Close()
	errWrite := db.write.Close()
	if errWrite != nil {
		return fmt.Errorf("close writer: %w", errWrite)
	}
	if errRead != nil {
		return fmt.Errorf("close reader: %w", errRead)
	}
	return nil
}
