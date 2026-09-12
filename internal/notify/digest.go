package notify

import (
	"fmt"
	"strings"
)

// Milestone is one row of the digest's table: a name, its progress, and an
// ETA if one has been computed.
//
// A struct rather than the loose dict Python passes around, so the three
// fallbacks it applies — name or id or "?", a missing progress counting as
// 0/0, a missing ETA rendering as an em dash — are visible here rather than
// spread across `.get()` calls.
type Milestone struct {
	Name  string
	ID    string
	Done  int
	Total int
	// ETADate is a date string, or empty when none has been computed.
	ETADate string
}

// label is Python's `m.get("name") or m.get("id") or "?"`.
func (m Milestone) label() string {
	switch {
	case m.Name != "":
		return m.Name
	case m.ID != "":
		return m.ID
	default:
		return "?"
	}
}

// eta is the ETA column: the date, or an em dash when there is none.
func (m Milestone) eta() string {
	if m.ETADate == "" {
		return "—"
	}
	return m.ETADate
}

// DigestText composes the digest body. Pure: no I/O and no clock, so it can
// be pinned against what Python produced for the same input.
//
// summaryText is `executive_summary`'s own text, reused verbatim rather than
// rebuilt — that function is the single source of wording for the dashboard
// and for this, and two phrasings of the same number is worse than one.
//
// The shape, which is what testdata/digest-*.txt pin:
//
//	<summary text>
//
//	Milestones:
//	  - Billing: 3/5 — ETA 2026-09-19
//
// with the whole thing trimmed and exactly one trailing newline — so an empty
// summary and no milestones produce a lone "\n", not an empty string.
func DigestText(summaryText string, milestones []Milestone) string {
	var lines []string
	if head := strings.TrimSpace(summaryText); head != "" {
		lines = append(lines, head)
	}
	if len(milestones) > 0 {
		lines = append(lines, "", "Milestones:")
		for _, m := range milestones {
			lines = append(lines, fmt.Sprintf("  - %s: %d/%d — ETA %s",
				m.label(), m.Done, m.Total, m.eta()))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")) + "\n"
}
