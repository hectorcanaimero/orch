package pricing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type pricingGolden struct {
	Table map[string]struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"table"`
	Resolutions []struct {
		RecordedCost float64 `json:"recorded_cost"`
		Model        string  `json:"model"`
		TokensIn     int     `json:"tokens_in"`
		TokensOut    int     `json:"tokens_out"`
		CostUSD      float64 `json:"cost_usd"`
	} `json:"resolutions"`
}

func loadGolden(t *testing.T) pricingGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "pricing.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g pricingGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	return g
}

// The embedded pricing.yaml is compared against the table PYTHON loads, not
// against the file it was copied from. A copied data file is a file that
// drifts, and this is the only thing that would notice.
func TestEmbeddedTableMatchesPython(t *testing.T) {
	golden := loadGolden(t)
	table := Load("")

	got := table.Models()
	if len(got) != len(golden.Table) {
		t.Fatalf("the embedded table has %d models, Python's has %d: %v",
			len(got), len(golden.Table), got)
	}
	for model, want := range golden.Table {
		if !table.Has(model) {
			t.Errorf("the embedded table has no row for %q", model)
			continue
		}
		price := table.ForModel(model)
		if price.Input != want.Input || price.Output != want.Output {
			t.Errorf("%s = %v/%v, want %v/%v",
				model, price.Input, price.Output, want.Input, want.Output)
		}
	}
}

func TestResolveCostMatchesPython(t *testing.T) {
	table := Load("")
	for _, tc := range loadGolden(t).Resolutions {
		got := table.ResolveCost(tc.RecordedCost, tc.Model, tc.TokensIn, tc.TokensOut)
		if got != tc.CostUSD {
			t.Errorf("ResolveCost(%v, %q, %d, %d) = %v, want %v",
				tc.RecordedCost, tc.Model, tc.TokensIn, tc.TokensOut, got, tc.CostUSD)
		}
	}
}

// A recorded cost of exactly zero means "nobody billed", not "free". The
// backends that cost nothing to call are the same ones that report no cost, so
// the two are the same row — and treating zero as free would show a project
// running entirely on opencode as having cost nothing at all.
func TestZeroRecordedCostIsEstimatedNotTakenLiterally(t *testing.T) {
	table := Load("")
	// claude-sonnet-4-6 is 3.00 in / 15.00 out per 1M.
	if got := table.ResolveCost(0, "claude-sonnet-4-6", 1_000_000, 0); got != 3.0 {
		t.Errorf("a zero cost resolved to %v, want an estimate of 3", got)
	}
	if got := table.ResolveCost(0.5, "claude-sonnet-4-6", 1_000_000, 0); got != 0.5 {
		t.Errorf("a recorded cost resolved to %v, want it kept at 0.5", got)
	}
	// A negative recorded cost is a broken row, not a credit: it estimates.
	if got := table.ResolveCost(-2, "claude-sonnet-4-6", 1_000_000, 0); got != 3.0 {
		t.Errorf("a negative cost resolved to %v, want an estimate of 3", got)
	}
}

// An unknown model falls through to `default`, and negative tokens are read as
// zero rather than as a refund.
func TestUnknownModelsAndNegativeTokens(t *testing.T) {
	table := Load("")
	if table.Has("a-model-nobody-priced") {
		t.Error("Has() claims a row for a model that is not in the table")
	}
	// The default row is 1.00 / 4.00.
	if got := table.EstimateCost("a-model-nobody-priced", 1_000_000, 1_000_000); got != 5.0 {
		t.Errorf("unknown model estimated at %v, want the default row's 5", got)
	}
	if got := table.ForModel("claude-sonnet-4-6").Cost(-5_000_000, 1_000_000); got != 15.0 {
		t.Errorf("negative tokens cost %v, want them read as zero (15)", got)
	}
}

// A project's pricing.yaml REPLACES a default row wholesale — you cannot tweak
// `input` and inherit `output`. One row is one complete price, which is the
// only rule anybody can hold in their head while editing the file.
func TestProjectOverridesReplaceWholeRows(t *testing.T) {
	root := t.TempDir()
	const override = "models:\n  claude-sonnet-4-6:\n    input: 99.0\n"
	if err := os.WriteFile(filepath.Join(root, "pricing.yaml"), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}

	table := Load(root)
	price := table.ForModel("claude-sonnet-4-6")
	if price.Input != 99.0 {
		t.Errorf("input = %v, want the override's 99", price.Input)
	}
	if price.Output != 0 {
		t.Errorf("output = %v, want 0 — the row was replaced, not merged", price.Output)
	}
	if got := table.ForModel("gpt-5").Input; got != 2.50 {
		t.Errorf("gpt-5 input = %v, want the default 2.50 for a model the override omits", got)
	}
}

// Every bad file resolves to "no override", never to an error. This table is
// an estimate; refusing to render a page because somebody mistyped a price
// would trade a wrong number for no number at all.
func TestBadOverridesAreIgnored(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"corrupt YAML", "models: [this is not a mapping\n"},
		{"no models key", "something_else: 1\n"},
		{"models is a list", "models:\n  - claude-sonnet-4-6\n"},
		{"a row that is not a mapping", "models:\n  claude-sonnet-4-6: 3.00\n"},
		{"empty file", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "pricing.yaml"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := Load(root).ForModel("gpt-5").Input; got != 2.50 {
				t.Errorf("gpt-5 input = %v, want the defaults to survive", got)
			}
		})
	}
}

// A missing project root, and a project with no pricing.yaml, both mean
// "defaults only" rather than an empty table.
func TestLoadWithoutAnOverride(t *testing.T) {
	for _, root := range []string{"", t.TempDir(), filepath.Join(t.TempDir(), "nope")} {
		if got := Load(root).ForModel("gpt-5").Input; got != 2.50 {
			t.Errorf("root %q: gpt-5 input = %v, want 2.50", root, got)
		}
	}
}

// A price written as a quoted string still reads as a number. A hand-edited
// YAML file has them, and a price silently read as 0 would make a model look
// free.
func TestQuotedPricesParse(t *testing.T) {
	root := t.TempDir()
	const override = "models:\n  gpt-5:\n    input: \"7.5\"\n    output: \"30\"\n"
	if err := os.WriteFile(filepath.Join(root, "pricing.yaml"), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	price := Load(root).ForModel("gpt-5")
	if price.Input != 7.5 || price.Output != 30 {
		t.Errorf("got %v/%v, want 7.5/30", price.Input, price.Output)
	}
}
