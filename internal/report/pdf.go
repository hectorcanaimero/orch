// Package report renders the stakeholder snapshot as a one-page PDF.
//
// It computes nothing. Everything on the page comes from
// `internal/publish/snapshot.Snapshot` — the same document the stakeholder
// bundle publishes — so a figure in the PDF and the same figure in the web
// snapshot cannot disagree. The spend section is gated there, at build time,
// by `show_spend_to_stakeholder`; this package renders `Budget.Enabled` and
// has no opinion of its own about who may see it.
//
// One page is a requirement, not a target: a report that spills onto a second
// page has stopped being the thing you glance at before a meeting. The layout
// leaves the milestone table the only section that grows, and caps it.
package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// Options tunes the rendering.
type Options struct {
	// Compress is fpdf's stream compression. On by default in the command —
	// a compressed A4 page is a few kB — and OFF in tests, where the point is
	// to read the text back out of the bytes without a decompressor.
	Compress bool
	// Now dates the footer's freshness line. Zero means the snapshot's own
	// GeneratedAt is the only date on the page.
	Now time.Time
}

// Page geometry, in millimetres. A4 is 210 × 297.
const (
	pageW      = 210.0
	marginX    = 15.0
	marginTop  = 15.0
	contentW   = pageW - 2*marginX
	footerFrom = 272.0 // where the footer sits; content must stop above it
)

// maxMilestones is how many rows the table prints. Eight is the figure the
// one-page requirement was specified against, and a project with more phases
// gets a "+N more" line rather than a second page.
const maxMilestones = 8

// PDF writes the report.
func PDF(w io.Writer, s snapshot.Snapshot, opts Options) error {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetCompression(opts.Compress)
	// Nothing but the core fonts: no file to ship, nothing to download, and
	// no font path to resolve at runtime on a machine we have never seen.
	pdf.SetMargins(marginX, marginTop, marginX)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	writeHeader(pdf, s)
	writeSummary(pdf, s)
	writeExecutiveSummary(pdf, s)
	writeMilestones(pdf, s)
	writeBlockers(pdf, s)
	writeBudget(pdf, s)
	writeFooter(pdf, s, opts.Now)

	if err := pdf.Output(w); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}
	return nil
}

func writeHeader(pdf *fpdf.Fpdf, s snapshot.Snapshot) {
	name := s.ProjectName
	if name == "" {
		name = "(unnamed project)"
	}
	pdf.SetFont("Helvetica", "B", 20)
	pdf.CellFormat(contentW, 10, latin1(name), "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(110, 110, 110)
	pdf.CellFormat(contentW, 6, latin1("Progress report · generated "+
		humanTime(s.GeneratedAt)), "", 1, "L", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(4)
}

// writeSummary is the four figures somebody reads first.
func writeSummary(pdf *fpdf.Fpdf, s snapshot.Snapshot) {
	cells := []struct{ label, value string }{
		{"Done", fmt.Sprintf("%d / %d", s.Summary.Done, s.Summary.Total)},
		{"Progress", fmt.Sprintf("%.0f%%", s.Summary.PercentDone)},
		{"In progress", fmt.Sprintf("%d", s.Summary.InProgress)},
		{"Blocked", fmt.Sprintf("%d", s.Summary.Blocked)},
	}
	// The ETA only appears when there is one. A "—" in a box of numbers reads
	// as a broken figure; an absent box reads as "not applicable yet", which
	// is what a project with nothing finished actually is.
	if s.Summary.ETAHours != nil {
		cells = append(cells, struct{ label, value string }{
			"Est. remaining", fmt.Sprintf("%.0f h", *s.Summary.ETAHours)})
	}

	w := contentW / float64(len(cells))
	y := pdf.GetY()

	pdf.SetFont("Helvetica", "B", 16)
	for _, c := range cells {
		pdf.CellFormat(w, 8, latin1(c.value), "", 0, "L", false, 0, "")
	}
	pdf.Ln(8)

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(110, 110, 110)
	for _, c := range cells {
		pdf.CellFormat(w, 5, latin1(c.label), "", 0, "L", false, 0, "")
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(5)

	pdf.SetDrawColor(220, 220, 220)
	pdf.Line(marginX, pdf.GetY()+2, pageW-marginX, pdf.GetY()+2)
	_ = y
	pdf.Ln(6)
}

func writeExecutiveSummary(pdf *fpdf.Fpdf, s snapshot.Snapshot) {
	if strings.TrimSpace(s.ExecutiveSummary.Text) == "" {
		return
	}
	pdf.SetFont("Helvetica", "", 11)
	// MultiCell wraps; the text is already deterministic prose from the
	// snapshot, so nothing here decides what it says.
	pdf.MultiCell(contentW, 5.5, latin1(s.ExecutiveSummary.Text), "", "L", false)
	pdf.Ln(3)
}

func writeMilestones(pdf *fpdf.Fpdf, s snapshot.Snapshot) {
	if len(s.Milestones) == 0 {
		return
	}
	sectionTitle(pdf, "Milestones")

	shown := s.Milestones
	overflow := 0
	if len(shown) > maxMilestones {
		overflow = len(shown) - maxMilestones
		shown = shown[:maxMilestones]
	}

	const (
		nameW = 95.0
		barW  = 55.0
		numW  = contentW - nameW - barW
		rowH  = 6.5
	)
	pdf.SetFont("Helvetica", "", 10)
	for _, m := range shown {
		name := m.Name
		if name == "" {
			name = fmt.Sprintf("Phase %d", m.Phase)
		}
		pdf.CellFormat(nameW, rowH, latin1(truncate(name, 46)), "", 0, "L", false, 0, "")

		// The bar is drawn rather than written: a percentage in a column of
		// percentages is read as a number, and the point of this row is the
		// comparison between rows.
		x, y := pdf.GetX(), pdf.GetY()
		pdf.SetFillColor(235, 235, 235)
		pdf.Rect(x, y+1.5, barW-6, 3.5, "F")
		if m.PercentDone > 0 {
			pdf.SetFillColor(60, 60, 60)
			pdf.Rect(x, y+1.5, (barW-6)*m.PercentDone/100, 3.5, "F")
		}
		pdf.SetX(x + barW)

		pdf.CellFormat(numW, rowH, latin1(fmt.Sprintf("%d/%d", m.Done, m.Total)),
			"", 1, "R", false, 0, "")
	}

	if overflow > 0 {
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(110, 110, 110)
		pdf.CellFormat(contentW, 5, latin1(fmt.Sprintf("+%d more", overflow)),
			"", 1, "L", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}
	pdf.Ln(3)
}

// maxBlockers is how many blocked items the page lists. More than five and the
// reader is being handed a backlog rather than a warning.
const maxBlockers = 5

func writeBlockers(pdf *fpdf.Fpdf, s snapshot.Snapshot) {
	if len(s.Blockers) == 0 {
		return
	}
	// Not "Blocked": that word is already a figure in the summary row above,
	// and the same word twice on one page reads as the same thing twice.
	sectionTitle(pdf, "What is blocked")

	shown := s.Blockers
	overflow := 0
	if len(shown) > maxBlockers {
		overflow = len(shown) - maxBlockers
		shown = shown[:maxBlockers]
	}

	pdf.SetFont("Helvetica", "", 10)
	for _, b := range shown {
		pdf.CellFormat(6, 5.5, latin1("•"), "", 0, "L", false, 0, "")
		// Title and reason on one line: the reason is already
		// business-language prose from the snapshot — never a raw comment or
		// an exit code — so it belongs next to the thing it explains.
		pdf.MultiCell(contentW-6, 5.5,
			latin1(truncate(b.Title, 52)+" — "+b.Reason), "", "L", false)
	}
	if overflow > 0 {
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(110, 110, 110)
		pdf.CellFormat(contentW, 5, latin1(fmt.Sprintf("+%d more", overflow)),
			"", 1, "L", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}
	pdf.Ln(3)
}

// writeBudget prints spend only when the snapshot carries it.
//
// The decision is not taken here: `snapshot.Build` sets `Budget.Enabled` from
// `show_spend_to_stakeholder`, and leaves `SpendUSD` nil when it is off. A
// second check in this package would be a second place for the flag to be
// wrong.
func writeBudget(pdf *fpdf.Fpdf, s snapshot.Snapshot) {
	if !s.Budget.Enabled || s.Budget.SpendUSD == nil {
		return
	}
	sectionTitle(pdf, "Spend")
	pdf.SetFont("Helvetica", "", 11)
	pdf.CellFormat(contentW, 6, latin1(fmt.Sprintf("$%.2f to date", *s.Budget.SpendUSD)),
		"", 1, "L", false, 0, "")
	pdf.Ln(2)
}

// writeFooter is the freshness line, and it is the reason the report is
// trustworthy at all: a PDF outlives the moment it was made, and a reader
// three weeks later has no other way to know that.
func writeFooter(pdf *fpdf.Fpdf, s snapshot.Snapshot, now time.Time) {
	pdf.SetY(footerFrom)
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(130, 130, 130)

	line := "Snapshot taken " + humanTime(s.GeneratedAt) + "."
	if age, ok := ageOf(s.GeneratedAt, now); ok {
		line += " " + age
	}
	pdf.MultiCell(contentW, 4, latin1(line), "", "L", false)
	pdf.SetTextColor(0, 0, 0)
}

func sectionTitle(pdf *fpdf.Fpdf, title string) {
	pdf.SetFont("Helvetica", "B", 11)
	pdf.CellFormat(contentW, 6, latin1(title), "", 1, "L", false, 0, "")
}

// humanTime turns the snapshot's RFC3339 stamp into something a person reads,
// and leaves anything it cannot parse exactly as it found it — a report that
// prints a raw timestamp is readable; one that prints "0001-01-01" because a
// parse failed is a lie.
func humanTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.UTC().Format("2 January 2006, 15:04 UTC")
}

// ageOf phrases how old the snapshot is, or reports that it cannot.
func ageOf(ts string, now time.Time) (string, bool) {
	if now.IsZero() {
		return "", false
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", false
	}
	d := now.UTC().Sub(t.UTC())
	switch {
	case d < 0:
		// A snapshot from the future is a clock problem, and saying so is
		// more use than rendering "-3 hours old".
		return "Its timestamp is in the future — check the clock on the machine that made it.", true
	case d < time.Hour:
		return fmt.Sprintf("%d minutes old.", int(d.Minutes())), true
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours old.", int(d.Hours())), true
	default:
		return fmt.Sprintf("%d days old.", int(d.Hours()/24)), true
	}
}

// truncate cuts at a rune boundary and marks the cut. Cutting bytes would
// split a multi-byte character in half, which is how a name with an accent in
// it becomes a replacement glyph.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
