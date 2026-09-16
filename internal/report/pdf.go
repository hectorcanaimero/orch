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
	"bytes"
	"fmt"
	"io"
	"strconv"
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
	// A PDF embeds a creation date, and fpdf defaults it to the wall clock —
	// which makes two renders of the same document differ in bytes for a
	// reason that has nothing to do with the document. Dating it from the
	// SNAPSHOT makes the artefact reproducible: the same snapshot yields the
	// same file, so a report can be diffed, cached or checksummed, and a
	// change in it means a change in the project rather than in the minute it
	// was printed.
	pdf.SetCreationDate(creationDate(s, opts.Now))
	// Nothing but the core fonts: no file to ship, nothing to download, and
	// no font path to resolve at runtime on a machine we have never seen.
	pdf.SetMargins(marginX, marginTop, marginX)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	accent := accentRGB(s)
	l := labelsFor(s)
	writeHeader(pdf, s, l, accent)
	writeSummary(pdf, s, l)
	writeExecutiveSummary(pdf, s)
	writeMilestones(pdf, s, l)
	writeBlockers(pdf, s, l)
	writeBudget(pdf, s, l)
	writeFooter(pdf, s, l, opts.Now)

	if err := pdf.Output(w); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}
	return nil
}

// creationDate is the snapshot's own timestamp, the caller's clock, or the
// zero time — in that order, so the most stable available answer wins.
func creationDate(s snapshot.Snapshot, now time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, s.GeneratedAt); err == nil {
		return t.UTC()
	}
	if !now.IsZero() {
		return now.UTC()
	}
	return time.Time{}
}

// logoBox is how much room the header gives a logo, in millimetres. Height is
// what is fixed: a wide logo gets more width and a tall one is scaled down, so
// a client's mark is never stretched to fit a box we chose.
const (
	logoH    = 12.0
	logoMaxW = 45.0
)

func writeHeader(pdf *fpdf.Fpdf, s snapshot.Snapshot, l labels, accent [3]int) {
	// The branded name wins over the project's own: `meta.project` is what
	// the operator calls it, and this is what the client should read.
	name := s.ProjectName
	if s.Branding != nil && s.Branding.Name != "" {
		name = s.Branding.Name
	}
	if name == "" {
		name = l.unnamed
	}

	textX := marginX
	if s.Branding != nil {
		if w := drawLogo(pdf, s.Branding.Logo); w > 0 {
			textX = marginX + w + 5
		}
	}

	pdf.SetXY(textX, marginTop)
	pdf.SetFont("Helvetica", "B", 20)
	pdf.SetTextColor(accent[0], accent[1], accent[2])
	pdf.CellFormat(pageW-marginX-textX, 10, latin1(name), "", 1, "L", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
	pdf.SetX(textX)

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(110, 110, 110)
	pdf.CellFormat(contentW, 6, latin1(l.generated+
		l.humanTime(s.GeneratedAt)), "", 1, "L", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
	pdf.SetX(marginX)
	pdf.SetY(marginTop + 16)
}

// drawLogo places the client's mark and returns the width it used, or 0 when
// there is none to place.
//
// A logo that cannot be decoded is SKIPPED rather than fatal: the report is
// still the document somebody needs, and `ResolveLogo` already refused every
// form this could take at config time — reaching here means a snapshot was
// hand-edited, which is not a reason to withhold the page.
func drawLogo(pdf *fpdf.Fpdf, dataURI string) float64 {
	raw, format, ok := snapshot.DecodeLogo(dataURI)
	if !ok {
		return 0
	}

	const name = "branding-logo"
	info := pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{
		ImageType: format, ReadDpi: false,
	}, bytes.NewReader(raw))
	if pdf.Err() || info == nil || info.Height() == 0 {
		// Clear the error so one unreadable image does not fail the whole
		// document — fpdf latches an error and refuses to Output afterwards,
		// so skipping the logo means clearing it as well as not drawing.
		pdf.ClearError()
		return 0
	}

	w := logoH * info.Width() / info.Height()
	if w > logoMaxW {
		w = logoMaxW
	}
	pdf.ImageOptions(name, marginX, marginTop, w, logoH,
		false, fpdf.ImageOptions{ImageType: format}, 0, "")
	return w
}

// accentRGB is the branded colour, or near-black when there is none.
//
// Parsed here rather than trusted: `config.validateBranding` has already
// refused anything that is not `#rgb` or `#rrggbb`, but this package also
// renders snapshots it did not build — a published `data.json` somebody hands
// it — so an unparseable colour falls back rather than painting garbage.
func accentRGB(s snapshot.Snapshot) [3]int {
	const r, g, b = 17, 17, 17
	if s.Branding == nil {
		return [3]int{r, g, b}
	}
	parsed, ok := parseHexColor(s.Branding.AccentColor)
	if !ok {
		return [3]int{r, g, b}
	}
	return parsed
}

func parseHexColor(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		// #abc is #aabbcc: each digit doubled, which is what CSS does.
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseInt(s[i*2:i*2+2], 16, 32)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = int(v)
	}
	return out, true
}

// writeSummary is the four figures somebody reads first.
func writeSummary(pdf *fpdf.Fpdf, s snapshot.Snapshot, l labels) {
	cells := []struct{ label, value string }{
		{l.done, fmt.Sprintf("%d / %d", s.Summary.Done, s.Summary.Total)},
		{l.progress, fmt.Sprintf("%.0f%%", s.Summary.PercentDone)},
		{l.inProgress, fmt.Sprintf("%d", s.Summary.InProgress)},
		{l.blocked, fmt.Sprintf("%d", s.Summary.Blocked)},
	}
	// The ETA only appears when there is one. A "—" in a box of numbers reads
	// as a broken figure; an absent box reads as "not applicable yet", which
	// is what a project with nothing finished actually is.
	// A finish date wins over hours: it is the figure the executive summary
	// and the dashboard's Sprint page quote, and the box must not contradict
	// the sentence under it.
	switch {
	case s.Summary.ETADate != nil:
		value := *s.Summary.ETADate
		if d, err := time.Parse("2006-01-02", value); err == nil {
			value = l.shortDate(d)
		}
		cells = append(cells, struct{ label, value string }{l.estFinish, value})
	case s.Summary.ETAHours != nil:
		cells = append(cells, struct{ label, value string }{
			l.estRemaining, fmt.Sprintf("%.0f h", *s.Summary.ETAHours)})
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

func writeMilestones(pdf *fpdf.Fpdf, s snapshot.Snapshot, l labels) {
	if len(s.Milestones) == 0 {
		return
	}
	sectionTitle(pdf, l.milestones)

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
			name = fmt.Sprintf(l.phase, m.Phase)
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
		pdf.CellFormat(contentW, 5, latin1(fmt.Sprintf(l.more, overflow)),
			"", 1, "L", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}
	pdf.Ln(3)
}

// maxBlockers is how many blocked items the page lists. More than five and the
// reader is being handed a backlog rather than a warning.
const maxBlockers = 5

func writeBlockers(pdf *fpdf.Fpdf, s snapshot.Snapshot, l labels) {
	if len(s.Blockers) == 0 {
		return
	}
	// Not "Blocked": that word is already a figure in the summary row above,
	// and the same word twice on one page reads as the same thing twice.
	sectionTitle(pdf, l.whatIsBlocked)

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
		pdf.CellFormat(contentW, 5, latin1(fmt.Sprintf(l.more, overflow)),
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
func writeBudget(pdf *fpdf.Fpdf, s snapshot.Snapshot, l labels) {
	if !s.Budget.Enabled || s.Budget.SpendUSD == nil {
		return
	}
	sectionTitle(pdf, l.spend)
	pdf.SetFont("Helvetica", "", 11)
	pdf.CellFormat(contentW, 6, latin1(fmt.Sprintf(l.toDate, *s.Budget.SpendUSD)),
		"", 1, "L", false, 0, "")
	pdf.Ln(2)
}

// writeFooter is the freshness line, and it is the reason the report is
// trustworthy at all: a PDF outlives the moment it was made, and a reader
// three weeks later has no other way to know that.
func writeFooter(pdf *fpdf.Fpdf, s snapshot.Snapshot, l labels, now time.Time) {
	pdf.SetY(footerFrom)
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(130, 130, 130)

	line := fmt.Sprintf(l.taken, l.humanTime(s.GeneratedAt))
	if age, ok := l.ageOf(s.GeneratedAt, now); ok {
		line += " " + age
	}
	// The agency's line goes FIRST: it is what the client's eye lands on at
	// the bottom of the page, and the freshness note is ours.
	if s.Branding != nil && s.Branding.Footer != "" {
		line = s.Branding.Footer + " · " + line
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
func (l labels) humanTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return l.longDate(t.UTC())
}

// ageOf phrases how old the snapshot is, or reports that it cannot.
func (l labels) ageOf(ts string, now time.Time) (string, bool) {
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
		return l.future, true
	case d < time.Hour:
		return fmt.Sprintf(l.minutes, int(d.Minutes())), true
	case d < 48*time.Hour:
		return fmt.Sprintf(l.hours, int(d.Hours())), true
	default:
		return fmt.Sprintf(l.days, int(d.Hours()/24)), true
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
