package doctor

import (
	"context"
	"fmt"

	"github.com/hectorcanaimero/orch/internal/state"
)

// CheckSQLite opens dbPath and reports the outcome. Ports state.db.accessible
// from check_state_backend (orchestrator/preflight.py), adapted to Go's
// state.Open, which applies any pending migrations as part of opening
// rather than only comparing PRAGMA user_version to an expected constant —
// see docs/brainstorm/go-migration-notes.md for why this check leans on
// that behavior (auto-heal and report) instead of re-implementing a
// read-only comparison that would immediately disagree with state.Open's
// own documented contract.
func CheckSQLite(ctx context.Context, dbPath string) Check {
	const name = "state.db.accessible"
	db, applied, err := state.Open(ctx, dbPath)
	if err != nil {
		return Check{
			Name: name, Status: StatusError,
			Detail:      fmt.Sprintf("cannot open %s: %v", dbPath, err),
			Remediation: fmt.Sprintf("Delete %s or run `orch migrate` to re-create.", dbPath),
		}
	}
	check := sqliteOpenedCheck(ctx, db, applied, dbPath)
	if closeErr := db.Close(); closeErr != nil {
		// A close failure doesn't undo what was already read successfully
		// above, but it's still not nothing (rule 19) — downgrade rather
		// than drop it.
		check.Status = StatusWarn
		check.Detail = fmt.Sprintf("%s (also: close failed: %v)", check.Detail, closeErr)
	}
	return check
}

func sqliteOpenedCheck(ctx context.Context, db *state.DB, applied int, dbPath string) Check {
	const name = "state.db.accessible"
	if applied > 0 {
		return Check{Name: name, Status: StatusOK,
			Detail: fmt.Sprintf("%s: opened, applied %d pending migration(s)", dbPath, applied)}
	}
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		return Check{Name: name, Status: StatusWarn,
			Detail: fmt.Sprintf("%s: opened but could not read schema_version: %v", dbPath, err)}
	}
	return Check{Name: name, Status: StatusOK,
		Detail: fmt.Sprintf("%s: opens ok (schema_version=%d)", dbPath, version)}
}
