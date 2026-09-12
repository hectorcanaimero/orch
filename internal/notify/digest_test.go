package notify

import (
	"os"
	"path/filepath"
	"testing"
)

// golden reads one of the digest files Python produced. See
// testdata/README.md for the exact call that made each.
func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- a test-local literal
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// TestDigestTextMatchesPython compares byte for byte against what Python's
// digest_text returned for the same input — this one IS plain text, so there
// is no encoder in the way and nothing to reconcile.
func TestDigestTextMatchesPython(t *testing.T) {
	const summary = "7 of 12 tasks done. 2 in progress, 1 blocked (spec ambiguous).\nOn track for Friday."

	tests := []struct {
		name       string
		summary    string
		milestones []Milestone
		golden     string
	}{
		{
			name:    "a summary and three milestones",
			summary: summary,
			milestones: []Milestone{
				{Name: "Billing", Done: 3, Total: 5, ETADate: "2026-09-19"},
				// No name, so the id is used; no ETA, so an em dash.
				{ID: "m-2", Done: 0, Total: 4},
				// No progress at all counts as 0/0, not as missing.
				{Name: "Auth"},
			},
			golden: "digest-full.txt",
		},
		{
			name:    "a summary with no milestones leaves the table out",
			summary: summary,
			golden:  "digest-no-milestones.txt",
		},
		{
			// Not the empty string: Python trims and appends one newline, so
			// nothing at all still produces a line.
			name:   "nothing at all",
			golden: "digest-empty.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := golden(t, tt.golden)
			if got := DigestText(tt.summary, tt.milestones); got != want {
				t.Errorf("DigestText =\n%q\nPython produced\n%q", got, want)
			}
		})
	}
}

func TestMilestoneLabelFallbacks(t *testing.T) {
	tests := []struct {
		name string
		m    Milestone
		want string
	}{
		{name: "the name wins", m: Milestone{Name: "Billing", ID: "m-1"}, want: "Billing"},
		{name: "the id is the fallback", m: Milestone{ID: "m-1"}, want: "m-1"},
		{name: "neither leaves a question mark", m: Milestone{}, want: "?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.label(); got != tt.want {
				t.Errorf("label = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMilestoneETAFallback(t *testing.T) {
	if got := (Milestone{ETADate: "2026-09-19"}).eta(); got != "2026-09-19" {
		t.Errorf("eta = %q", got)
	}
	// An em dash, not "none" and not empty: the column has to stay aligned.
	if got := (Milestone{}).eta(); got != "—" {
		t.Errorf("eta = %q, want an em dash", got)
	}
}

// TestDigestAlwaysEndsInExactlyOneNewline. It is posted to a webhook and
// printed to a terminal; a missing or doubled trailing newline shows in both.
func TestDigestAlwaysEndsInExactlyOneNewline(t *testing.T) {
	for _, in := range []string{"", "one line", "trailing\n\n\n", "  padded  "} {
		got := DigestText(in, nil)
		if len(got) == 0 || got[len(got)-1] != '\n' {
			t.Errorf("DigestText(%q) does not end in a newline: %q", in, got)
		}
		if len(got) > 1 && got[len(got)-2] == '\n' {
			t.Errorf("DigestText(%q) ends in two newlines: %q", in, got)
		}
	}
}
