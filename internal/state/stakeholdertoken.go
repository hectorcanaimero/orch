package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StakeholderToken and SetStakeholderToken back G8.2 (F3.3): the dashboard's
// stakeholder token, moved out of config.yaml into this project's own
// database row so it can be rotated with `orch dashboard token rotate` —
// no YAML edit, no restart. Only a SHA-256 hash is ever stored; see
// internal/dashboard.HashToken, the one place that hash is computed, on
// both the write side (rotate) and the read side (the access middleware).
//
// Neither method takes a projectID parameter, matching every other Backend
// method here — this SQLite value is already bound to one project (see its
// own doc comment), and `stakeholder_tokens` is scoped by that same
// project_id like every other table.

// StakeholderToken returns this project's stored token hash and when it was
// last rotated. ok is false when no row exists yet — a fresh project, or one
// that has never called `orch dashboard token rotate` — and the caller falls
// back to config.yaml/--token.
func (b *SQLite) StakeholderToken(ctx context.Context) (tokenHash string, rotatedAt time.Time, ok bool, err error) {
	var rotatedAtRaw string
	err = b.db.read.QueryRowContext(ctx,
		`SELECT token_hash, rotated_at FROM stakeholder_tokens WHERE project_id = ?`,
		b.projectID).Scan(&tokenHash, &rotatedAtRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("query stakeholder token for %q: %w", b.projectID, err)
	}
	t, ok := ParseTS(rotatedAtRaw)
	if !ok {
		return "", time.Time{}, false, fmt.Errorf(
			"stakeholder token for %q: rotated_at %q is not a valid timestamp", b.projectID, rotatedAtRaw)
	}
	return tokenHash, t, true, nil
}

// SetStakeholderToken stores a freshly rotated token's hash, replacing any
// row this project already had. tokenHash must be non-empty — an empty hash
// would mean "no token", which is what deleting the row (not this method)
// would express, and this method has no caller that means that.
func (b *SQLite) SetStakeholderToken(ctx context.Context, tokenHash string, rotatedAt time.Time) error {
	if tokenHash == "" {
		return errors.New("set stakeholder token: hash is empty")
	}
	return b.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stakeholder_tokens (project_id, token_hash, rotated_at)
			 VALUES (?, ?, ?)
			 ON CONFLICT(project_id) DO UPDATE
			   SET token_hash = excluded.token_hash, rotated_at = excluded.rotated_at`,
			b.projectID, tokenHash, b.ts(rotatedAt)); err != nil {
			return fmt.Errorf("upsert stakeholder token for %q: %w", b.projectID, err)
		}
		return nil
	})
}
