package state

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// AllSpend returns every spend row newer than `since`, across every backend,
// oldest first. The zero Time means all of history.
//
// `SpendSince` answers "what has this provider spent", which is the budget
// gate's question. This answers "what has been spent", which is the metrics
// page's — by model, by day, in total. Doing it per backend would be one query
// per name in a list this package would then have to keep current, and a row
// written by a backend nobody listed would silently stop counting.
//
// Ordering matches `SpendSince`: by timestamp, then by task id, so two rows
// written in the same second land in a fixed order rather than SQLite's.
//
// A row whose timestamp does not parse is DROPPED, not dated to the epoch.
// Same rule as `SpendSince`: a row that cannot be placed in time cannot honour
// a `since`, and counting it anyway would put spend in a window it may not
// belong to.
func (b *SQLite) AllSpend(ctx context.Context, since time.Time) ([]Spend, error) {
	rows, err := b.db.read.QueryContext(ctx,
		`SELECT project_id, ts, task_id, backend, model, tokens_in, tokens_out,
		        cost_usd, duration_s, COALESCE(estimated, 0)
		   FROM spend
		  WHERE project_id = ?`,
		b.projectID)
	if err != nil {
		return nil, fmt.Errorf("query spend: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cutoff := since.UTC()
	var out []spendAt
	for rows.Next() {
		var s Spend
		var estimated int
		if err := rows.Scan(&s.ProjectID, &s.TS, &s.TaskID, &s.Backend, &s.Model,
			&s.TokensIn, &s.TokensOut, &s.CostUSD, &s.DurationS, &estimated); err != nil {
			return nil, fmt.Errorf("scan spend row: %w", err)
		}
		s.Estimated = estimated != 0
		ts, ok := ParseTS(s.TS)
		if !ok {
			continue
		}
		if ts.Before(cutoff) {
			continue
		}
		out = append(out, spendAt{Spend: s, at: ts})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate spend: %w", err)
	}

	sort.Slice(out, func(i, j int) bool {
		if !out[i].at.Equal(out[j].at) {
			return out[i].at.Before(out[j].at)
		}
		return out[i].TaskID < out[j].TaskID
	})
	result := make([]Spend, 0, len(out))
	for _, s := range out {
		result = append(result, s.Spend)
	}
	return result, nil
}
