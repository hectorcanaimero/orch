package snapshot

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
)

// onePixelPNG is a real 1×1 PNG. A hand-written byte slice would not survive
// `http.DetectContentType`, which is the point of the check it feeds.
var onePixelPNG = mustDecode(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func mustDecode(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func demoInput() Input {
	return Input{
		Tasks: []model.Task{
			{ID: "T-1", Phase: 0, Title: "Scaffold", Status: model.StatusDone, EstimateHours: 1},
			{ID: "T-2", Phase: 0, Title: "Database", Status: model.StatusTodo, EstimateHours: 2},
		},
		Phases:      []model.Phase{{ID: 0, Name: "F0 — Foundation"}},
		ProjectName: "demo",
		Language:    "en",
		Now:         time.Date(2026, 9, 12, 8, 30, 0, 0, time.UTC),
	}
}

// The criterion the whole feature rests on: a project with no branding
// produces EXACTLY the bytes it produced before branding existed.
//
// Not "renders the same" and not "has the same fields" — the same bytes, so a
// static export that somebody diffs, or a viewer with a cached copy, cannot
// tell that this feature shipped.
func TestWithoutBrandingTheDocumentIsByteIdentical(t *testing.T) {
	before, err := json.Marshal(Build(demoInput()))
	if err != nil {
		t.Fatal(err)
	}

	// The same input, through the branded path, with nothing configured.
	in := demoInput()
	in.Branding = Branding{}
	after, err := json.Marshal(Build(in))
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Errorf("the bytes differ:\n before: %s\n  after: %s", before, after)
	}
	// And the key is absent, not present-and-empty. `omitempty` does not omit
	// a struct — only a nil pointer does — so this is the assertion that
	// catches the version of this feature that ships `"branding":{}`.
	if strings.Contains(string(after), "branding") {
		t.Errorf("an unbranded document mentions branding: %s", after)
	}
}

// With branding, the fields are there and nothing else moved.
func TestBrandingAppearsWhenConfigured(t *testing.T) {
	in := demoInput()
	in.Branding = Branding{
		Name:        "Acme Digital",
		Logo:        "data:image/png;base64," + base64.StdEncoding.EncodeToString(onePixelPNG),
		AccentColor: "#ff6600",
		Footer:      "Confidential — Acme Digital",
	}

	var got map[string]any
	raw, err := json.Marshal(Build(in))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	b, ok := got["branding"].(map[string]any)
	if !ok {
		t.Fatalf("no branding in %s", raw)
	}
	if b["name"] != "Acme Digital" || b["accent_color"] != "#ff6600" {
		t.Errorf("branding = %v", b)
	}
	if logo, _ := b["logo"].(string); !strings.HasPrefix(logo, "data:image/png;base64,") {
		t.Errorf("the logo is not an embedded data URI: %q", logo)
	}
	// Nothing else changed: the summary a client reads is the same one.
	if summary, _ := got["summary"].(map[string]any); summary["total"] != float64(2) {
		t.Errorf("branding moved the figures: %v", summary)
	}
}

// A partial block is legal — an agency that wants only its name should not
// have to invent a colour.
func TestPartialBrandingOmitsWhatIsNotSet(t *testing.T) {
	in := demoInput()
	in.Branding = Branding{Name: "Acme"}

	raw, err := json.Marshal(Build(in))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"branding":{"name":"Acme"}`) {
		t.Errorf("a name-only block did not round-trip cleanly: %s", raw)
	}
}

func TestResolveLogoFromAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(path, onePixelPNG, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveLogo(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("got %q", got)
	}
	// It round-trips back to the bytes the PDF will draw.
	raw, format, ok := DecodeLogo(got)
	if !ok || format != "png" {
		t.Fatalf("DecodeLogo returned %q, %v", format, ok)
	}
	if string(raw) != string(onePixelPNG) {
		t.Error("the decoded bytes are not the file's")
	}
}

// The declared extension is never consulted: a PNG named `.jpg` works, and a
// text file named `.png` is caught here rather than as a broken image three
// surfaces later.
func TestResolveLogoReadsTheBytesNotTheName(t *testing.T) {
	dir := t.TempDir()

	mislabelled := filepath.Join(dir, "logo.jpg")
	if err := os.WriteFile(mislabelled, onePixelPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveLogo(mislabelled)
	if err != nil {
		t.Fatalf("a PNG named .jpg was rejected: %v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("got %q, want it typed by its bytes", got)
	}

	lying := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(lying, []byte("this is not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLogo(lying); err == nil {
		t.Error("a text file named .png was accepted")
	}
}

// Every refusal names the file and the reason, because a logo that does not
// appear is invisible: a silent skip sends the operator to the SPA, the PDF
// and their browser cache before they suspect the path.
func TestResolveLogoRefusals(t *testing.T) {
	dir := t.TempDir()

	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, make([]byte, maxLogoBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	svg := filepath.Join(dir, "logo.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, in, want string
	}{
		{"missing file", filepath.Join(dir, "nope.png"), "no such file"},
		{"too big", big, "the limit is"},
		{"an SVG, which the PDF cannot draw", svg, "PNG and JPEG"},
		{"a data URI that is not base64", "data:image/png,notbase64", "must be base64"},
		{"a data URI whose base64 is broken", "data:image/png;base64,!!!!", "does not decode"},
		{"a data URI that lies about its type",
			"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("plain text")),
			"carries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveLogo(tc.in)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "branding.logo") {
				t.Errorf("err = %q, want it to name the config key", err)
			}
		})
	}
}

// An empty logo is not an error — it is the common case.
func TestResolveLogoEmpty(t *testing.T) {
	got, err := ResolveLogo("   ")
	if err != nil || got != "" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, _, ok := DecodeLogo(""); ok {
		t.Error("DecodeLogo claimed a logo where there is none")
	}
}

// allowedKeys is every key name this document is documented to emit, from
// docs/SNAPSHOT-SCHEMA.md.
//
// It exists because `TestNoOperatorFields` is a DENY-list — it catches keys
// whose names are known to be operator-only — and a deny-list cannot catch a
// field nobody thought to forbid. The schema doc claims "a new field this
// package emits must be added to that list on purpose before the test can
// pass again", and that was not true: a new field passed silently. This is
// the test that makes the claim true.
//
// Names rather than paths: the question is "did something new appear", and a
// name is enough to ask it. A key that legitimately repeats at two depths
// (`name`, `total`) is listed once.
var allowedKeys = map[string]bool{
	// top level
	"schema": true, "generated_at": true, "project_name": true,
	"refresh_interval_s": true, "summary": true, "milestones": true,
	"blockers": true, "budget": true, "executive_summary": true,
	"branding": true, "deliveries": true, "documents": true,
	// summary
	"total": true, "done": true, "in_progress": true, "blocked": true,
	"backlog": true, "percent_done": true, "estimate_hours_total": true,
	"eta_hours": true, "eta_date": true, "eta_confidence": true,
	// milestones
	"phase": true, "name": true, "complete": true,
	"packages": true, "deliverables": true, "status": true, "finished_at": true,
	// documents
	"id": true, "updated_at": true, "markdown": true,
	// blockers
	"title": true, "reason": true,
	// budget
	"enabled": true, "spend_usd": true, "spend_by_day": true,
	"date": true, "cost_usd": true,
	// executive summary
	"text": true, "language": true,
	// branding
	"logo": true, "accent_color": true, "footer": true,
	// quality
	"quality": true, "gates": true, "delivered": true, "verified": true, "first_pass": true,
}

// A field this package emits and nobody documented fails here, which is what
// the schema doc already promised and the deny-list could not deliver.
func TestEveryEmittedKeyIsInTheSchema(t *testing.T) {
	in := demoInput()
	in.ShowSpend = true
	in.DoneInVelocityWindow = 7                                      // so eta_date and eta_confidence are emitted
	in.FinishedAt = map[string]string{"T-1": "2026-09-11T08:30:00Z"} // so deliveries and finished_at are
	in.Documents = []Document{{ID: "prd", Title: "PRD", UpdatedAt: "2026-09-10T00:00:00Z", Markdown: "# PRD\n"}}
	in.Gates = []string{"tests", "review"} // so quality is emitted
	in.Branding = Branding{
		Name: "Acme", Logo: "data:image/png;base64," +
			base64.StdEncoding.EncodeToString(onePixelPNG),
		AccentColor: "#ff6600", Footer: "Confidential",
	}

	raw, err := json.Marshal(Build(in))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch v := node.(type) {
		case map[string]any:
			for k, val := range v {
				if !allowedKeys[k] {
					t.Errorf("undocumented key %q at %s — add it to "+
						"docs/SNAPSHOT-SCHEMA.md and to allowedKeys, on purpose",
						k, path)
				}
				walk(val, path+"."+k)
			}
		case []any:
			for _, val := range v {
				walk(val, path)
			}
		}
	}
	walk(doc, "$")
}
