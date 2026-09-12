package report

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// render produces an UNCOMPRESSED PDF, which is what lets these tests read the
// page's own text back out of the bytes. The command compresses; the tests do
// not, so that an assertion about what the page says is an assertion about
// bytes rather than about a decompressor.
func render(t *testing.T, s snapshot.Snapshot, now time.Time) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := PDF(&buf, s, Options{Compress: false, Now: now}); err != nil {
		t.Fatalf("PDF: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("PDF wrote nothing")
	}
	return buf.Bytes()
}

// pageCount is what the document itself says: the `/Count` on its page tree,
// cross-checked against the number of `/Type /Page` objects.
//
// Both, because either alone can lie in a way the other catches. A `/Count`
// that disagrees with the objects is a malformed document, and counting only
// the objects means matching `/Type /Page` without also matching `/Type
// /Pages` — a distinction one character wide, and the kind of thing that
// silently reports two pages as one.
func pageCount(t *testing.T, b []byte) int {
	t.Helper()

	m := regexp.MustCompile(`/Type\s*/Pages\b[^>]*?/Count\s+(\d+)`).FindSubmatch(b)
	if m == nil {
		m = regexp.MustCompile(`/Count\s+(\d+)`).FindSubmatch(b)
	}
	if m == nil {
		t.Fatal("no /Count on the page tree")
	}
	declared, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("/Count is not a number: %q", m[1])
	}

	// `/Type /Page` not followed by another letter — so `/Type /Pages` does
	// not count as a page.
	objects := len(regexp.MustCompile(`/Type\s*/Page(?:[^s]|$)`).FindAll(b, -1))
	if objects != declared {
		t.Errorf("the page tree declares %d pages and the document has %d page objects",
			declared, objects)
	}
	return declared
}

// mediaBoxes returns every /MediaBox on the document, so a test can say "A4"
// rather than "some size".
func mediaBoxes(b []byte) []string {
	re := regexp.MustCompile(`/MediaBox\s*\[([^\]]*)\]`)
	var out []string
	for _, m := range re.FindAllSubmatch(b, -1) {
		out = append(out, strings.Join(strings.Fields(string(m[1])), " "))
	}
	return out
}

// containsText looks for a string in the page's text-drawing operators.
//
// Two transformations stand between a Go string and the bytes on the page, and
// a helper that skipped either would quietly fail to find text that IS there.
// The first is CP1252, which is the point of `latin1`. The second is PDF's own
// string escaping: `(`, `)` and `\` are backslash-escaped inside a text
// object, because parentheses delimit the string. "(unnamed project)" is on
// the page as `\(unnamed project\)`.
func containsText(b []byte, want string) bool {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`(`, `\(`,
		`)`, `\)`,
	).Replace(latin1(want))
	return bytes.Contains(b, []byte(escaped))
}

func demoSnapshot(milestones int) snapshot.Snapshot {
	eta := 42.0
	spend := 12.5
	s := snapshot.Snapshot{
		Schema:      1,
		GeneratedAt: "2026-09-12T08:30:00Z",
		ProjectName: "Facturación Ágil",
		Summary: snapshot.Summary{
			Total: 40, Done: 12, InProgress: 3, Blocked: 2, Backlog: 23,
			PercentDone: 30, EstimateHoursTotal: 120, ETAHours: &eta,
		},
		Blockers: []snapshot.Blocker{
			{Phase: 2, Title: "Pasarela de pago", Reason: "Esperando una decisión del equipo."},
		},
		Budget: snapshot.Budget{Enabled: true, SpendUSD: &spend},
		ExecutiveSummary: snapshot.ExecutiveSummary{
			Language: "es",
			Text:     "12 de 40 tareas completadas (30%). Restan ~42h al ritmo actual.",
		},
	}
	for i := 1; i <= milestones; i++ {
		s.Milestones = append(s.Milestones, snapshot.Milestone{
			Phase: i, Name: fmt.Sprintf("F%d — Fase número %d", i, i),
			Total: 5, Done: i % 6, PercentDone: float64(i%6) * 20,
		})
	}
	return s
}

// The requirement, and the test the requirement was specified against: eight
// milestones still fit on ONE A4 page.
func TestEightMilestonesFitOnOnePage(t *testing.T) {
	b := render(t, demoSnapshot(8), time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))

	if n := pageCount(t, b); n != 1 {
		t.Errorf("the report is %d pages; it is meant to be one", n)
	}
	boxes := mediaBoxes(b)
	if len(boxes) == 0 {
		t.Fatal("no /MediaBox in the document")
	}
	// A4 in PDF points: 210mm × 297mm = 595.28 × 841.89.
	for _, box := range boxes {
		if !strings.HasPrefix(box, "0 0 595.28 841.89") {
			t.Errorf("/MediaBox is %q, want A4 (0 0 595.28 841.89)", box)
		}
	}
}

// More milestones than fit are summarised, not spilled onto a second page.
func TestMoreMilestonesThanFitAreSummarised(t *testing.T) {
	b := render(t, demoSnapshot(14), time.Time{})

	if n := pageCount(t, b); n != 1 {
		t.Errorf("14 milestones produced %d pages", n)
	}
	if !containsText(b, "+6 more") {
		t.Error("the table does not say what it left out")
	}
	// The eighth is on the page and the ninth is not, so the cut is where it
	// claims to be rather than somewhere convenient.
	if !containsText(b, "Fase número 8") {
		t.Error("the eighth milestone is missing")
	}
	if containsText(b, "Fase número 9") {
		t.Error("the ninth milestone was printed past the cap")
	}
}

// Everything the page promises to carry is on it.
func TestTheReportCarriesTheSnapshotsFigures(t *testing.T) {
	b := render(t, demoSnapshot(3), time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))

	for _, want := range []string{
		"Facturación Ágil",             // the project, accents intact
		"12 / 40",                      // done of total
		"30%",                          // progress
		"42 h",                         // the ETA
		"Restan ~42h al ritmo actual.", // the executive summary, verbatim
		"Milestones",
		"What is blocked",
		"Pasarela de pago",       // the blocker, by title
		"Esperando una decisión", // its business-language reason
		"$12.50 to date",         // spend
		"Snapshot taken 12 September 2026, 08:30 UTC.",
		"3 hours old.", // freshness
	} {
		if !containsText(b, want) {
			t.Errorf("the report does not carry %q", want)
		}
	}
}

// Accents reach the page as CP1252, not as the raw UTF-8 Go strings hold.
// Without the transcoding the PDF is still valid and still "contains the
// text" — it just reads "FacturaciÃ³n" on screen, which no byte-level test
// written from the code would notice.
func TestAccentsAreTranscodedForTheCoreFonts(t *testing.T) {
	b := render(t, demoSnapshot(1), time.Time{})

	if bytes.Contains(b, []byte("Facturación")) {
		t.Error("the raw UTF-8 form is in the document; it will render as mojibake")
	}
	if !bytes.Contains(b, []byte("Factura")) {
		t.Fatal("the project name is not in the document at all")
	}
	// 0xF3 is CP1252 'ó'. Its UTF-8 spelling is 0xC3 0xB3.
	if !bytes.Contains(b, []byte{'i', 0xF3, 'n'}) {
		t.Error("'ó' is not in its CP1252 form")
	}
}

func TestLatin1(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []byte
	}{
		{"plain", []byte("plain")},
		{"ó", []byte{0xF3}},
		{"ñ", []byte{0xF1}},
		{"¿qué?", []byte{0xBF, 'q', 'u', 0xE9, '?'}},
		{"—", []byte{0x97}},      // em dash: NOT its code point
		{"…", []byte{0x85}},      // ellipsis: same
		{"€", []byte{0x80}},      // euro: same
		{"日本", []byte{'?', '?'}}, // no CP1252 spelling: marked, not dropped
	} {
		if got := latin1(tc.in); got != string(tc.want) {
			t.Errorf("latin1(%q) = % x, want % x", tc.in, got, tc.want)
		}
	}
}

// Spend appears only when the snapshot carries it. The decision belongs to
// `snapshot.Build`, which reads `show_spend_to_stakeholder`; a second check in
// this package would be a second place for the flag to be wrong. What this
// asserts is that the renderer honours the field rather than reaching past it.
func TestSpendIsAbsentWhenTheSnapshotWithholdsIt(t *testing.T) {
	s := demoSnapshot(3)
	s.Budget = snapshot.Budget{Enabled: false}

	b := render(t, s, time.Time{})
	if containsText(b, "Spend") || containsText(b, "to date") {
		t.Error("the report printed a spend section the snapshot did not carry")
	}
	// And the rest of the page is unaffected.
	if !containsText(b, "Milestones") {
		t.Error("withholding spend removed more than the spend")
	}
}

// A snapshot with nothing in it still produces a readable page. This is the
// state of a project on the day it is scaffolded, and a report that crashes or
// renders blank on it is one nobody trusts afterwards.
func TestAnEmptyProjectStillRenders(t *testing.T) {
	b := render(t, snapshot.Snapshot{GeneratedAt: "2026-09-12T08:30:00Z"}, time.Time{})

	if n := pageCount(t, b); n != 1 {
		t.Errorf("an empty snapshot produced %d pages", n)
	}
	if !containsText(b, "(unnamed project)") {
		t.Error("a project with no name is not named as such")
	}
	if !containsText(b, "Snapshot taken") {
		t.Error("the freshness line is missing")
	}
}

// The freshness line is the reason a PDF can be trusted at all: it outlives
// the moment it was made, and the reader has no other way to tell.
func TestFreshnessPhrasing(t *testing.T) {
	const taken = "2026-09-12T08:30:00Z"
	for _, tc := range []struct {
		name string
		now  time.Time
		want string
	}{
		{"minutes", time.Date(2026, 9, 12, 8, 45, 0, 0, time.UTC), "15 minutes old."},
		{"hours", time.Date(2026, 9, 12, 20, 30, 0, 0, time.UTC), "12 hours old."},
		{"days", time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC), "8 days old."},
		{"a clock in the future", time.Date(2026, 9, 11, 8, 30, 0, 0, time.UTC),
			"timestamp is in the future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := demoSnapshot(2)
			s.GeneratedAt = taken
			if b := render(t, s, tc.now); !containsText(b, tc.want) {
				t.Errorf("the footer does not say %q", tc.want)
			}
		})
	}

	// With no clock injected there is no age claim — only the timestamp.
	b := render(t, demoSnapshot(2), time.Time{})
	if containsText(b, "old.") {
		t.Error("an age was claimed with no clock to compute it from")
	}
}

// An unparseable timestamp is printed as it arrived. Rendering "1 January
// 0001" because a parse failed would be a lie the reader cannot detect.
func TestAnUnreadableTimestampIsPrintedVerbatim(t *testing.T) {
	s := demoSnapshot(2)
	s.GeneratedAt = "not a timestamp"
	b := render(t, s, time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))

	if !containsText(b, "not a timestamp") {
		t.Error("the raw timestamp is not on the page")
	}
	if containsText(b, "0001") {
		t.Error("a zero time was rendered as a date")
	}
}

// Compression is what the command uses, and it must still be a valid one-page
// A4 document — the uncompressed form the other tests read is a test affordance,
// not the shipped artefact.
func TestTheCompressedFormIsStillOnePageOfA4(t *testing.T) {
	var buf bytes.Buffer
	if err := PDF(&buf, demoSnapshot(8), Options{Compress: true}); err != nil {
		t.Fatalf("PDF: %v", err)
	}
	b := buf.Bytes()

	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatalf("not a PDF: % x", b[:8])
	}
	if n := pageCount(t, b); n != 1 {
		t.Errorf("the compressed report is %d pages", n)
	}
	for _, box := range mediaBoxes(b) {
		if !strings.HasPrefix(box, "0 0 595.28 841.89") {
			t.Errorf("/MediaBox is %q, want A4", box)
		}
	}
	// And it is meaningfully smaller than the readable form, which is the
	// only reason the command turns it on.
	uncompressed := render(t, demoSnapshot(8), time.Time{})
	if len(b) >= len(uncompressed) {
		t.Errorf("compressed %d bytes vs uncompressed %d", len(b), len(uncompressed))
	}
}

func TestTruncateCutsAtRuneBoundaries(t *testing.T) {
	// Cutting bytes would split 'ó' in half and leave a broken sequence.
	got := truncate("Facturación Ágil y otras cosas", 12)
	if strings.Contains(got, "�") {
		t.Errorf("truncate produced a replacement character: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate did not mark the cut: %q", got)
	}
	if r := []rune(got); len(r) != 12 {
		t.Errorf("truncate returned %d runes, want 12: %q", len(r), got)
	}
	if s := "short"; truncate(s, 12) != s {
		t.Error("truncate shortened a string that already fit")
	}
}
