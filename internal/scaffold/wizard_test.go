package scaffold

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The wizard is driven by a scripted answer list and its whole transcript is
// read back, which is what an explicit WizardIO buys: every test below checks
// what the operator would have SEEN, not only what the wizard returned.

type session struct {
	opts       Options
	confirmed  bool
	err        error
	transcript string
}

func runWizard(t *testing.T, answers []string, args Options) session {
	t.Helper()
	var out strings.Builder
	in := strings.NewReader(strings.Join(answers, "\n") + "\n")
	opts, confirmed, err := Wizard(NewWizardIO(in, &out), args)
	return session{opts: opts, confirmed: confirmed, err: err, transcript: out.String()}
}

// The happy path, accepting every default except the two with none.
//
// `project id` has no default — it is the one answer the wizard cannot guess —
// and everything after it can be an empty line.
func TestWizardAcceptsDefaults(t *testing.T) {
	answers := []string{
		"billing-api", // project id
		"",            // project root → cwd/billing-api
		"blank",       // template
		"",            // state backend → sqlite
		"",            // budget preset → the first
		"",            // spec root → specs
	}
	// One line per tier the packaged router actually groups.
	for range routerTierMapNonEmpty() {
		answers = append(answers, "")
	}
	answers = append(answers, "", "") // sdd → n, proceed → y

	s := runWizard(t, answers, Options{})
	if s.err != nil {
		t.Fatalf("Wizard: %v\n%s", s.err, s.transcript)
	}
	if !s.confirmed {
		t.Fatalf("the wizard did not reach the confirm gate:\n%s", s.transcript)
	}
	if s.opts.Name != "billing-api" {
		t.Errorf("Name = %q, want billing-api", s.opts.Name)
	}
	if filepath.Base(s.opts.Root) != "billing-api" {
		t.Errorf("Root = %q, want it to end in the project id", s.opts.Root)
	}
	if s.opts.Template != "" {
		t.Errorf("Template = %q, want blank", s.opts.Template)
	}
	if s.opts.SDD {
		t.Error("SDD defaulted to true")
	}
}

// The gate is the point of the whole flow: a "no" returns without an error and
// without options anyone should act on.
func TestWizardAbortsAtTheGate(t *testing.T) {
	answers := []string{"proj", "", "blank", "", "", ""}
	for range routerTierMapNonEmpty() {
		answers = append(answers, "")
	}
	answers = append(answers, "", "n") // sdd → n, proceed → n

	s := runWizard(t, answers, Options{})
	if s.err != nil {
		t.Fatalf("declining must not be an error: %v", s.err)
	}
	if s.confirmed {
		t.Error("confirmed = true after answering n")
	}
	if !strings.Contains(s.transcript, "Aborted. Nothing written.") {
		t.Errorf("the transcript does not say nothing was written:\n%s", s.transcript)
	}
}

// Everything the operator chose appears in the summary, before the gate.
//
// The summary is what makes the gate a decision rather than a formality — an
// operator confirming a screen that omits their answers is confirming nothing.
func TestSummaryShowsEveryChoiceBeforeTheGate(t *testing.T) {
	answers := []string{"billing-api", "", "python-api", "file", "aggressive", "docs/specs"}
	for range routerTierMapNonEmpty() {
		answers = append(answers, "")
	}
	answers = append(answers, "y", "y") // sdd → y, proceed → y

	s := runWizard(t, answers, Options{})
	if s.err != nil {
		t.Fatalf("Wizard: %v\n%s", s.err, s.transcript)
	}

	summary := s.transcript[strings.Index(s.transcript, "About to scaffold:"):]
	gate := strings.Index(summary, "Proceed with scaffolding?")
	if gate < 0 {
		t.Fatalf("no confirm gate after the summary:\n%s", s.transcript)
	}
	summary = summary[:gate]

	for _, want := range []string{
		"billing-api", "python-api", "file", "aggressive", "docs/specs",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary omits %q:\n%s", want, summary)
		}
	}
	// And the three VCS flags, which nobody was asked about but which decide
	// whether every task opens a PR.
	for _, want := range []string{"worktree mode", "auto PR", "auto merge"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary omits %q", want)
		}
	}
	if !strings.Contains(summary, "openspec layout  yes") {
		t.Error("the summary does not reflect the SDD answer")
	}
}

// The VCS flags come from the config the scaffold will write, not from a
// hardcoded belief about the defaults.
//
// python-api ships worktree_mode: true and auto_pr: true; auto_merge is false
// everywhere because it needs branch protection. If a template's config is
// edited, this summary has to follow it — which is exactly what a restated
// copy would not do.
func TestVCSFlagsAreReadFromTheConfigThatWillBeWritten(t *testing.T) {
	wt, autoPR, autoMerge := vcsFlagsFor("python-api")
	if !wt || !autoPR {
		t.Errorf("python-api: worktree=%v autoPR=%v, want both true", wt, autoPR)
	}
	if autoMerge {
		t.Error("auto_merge must stay off — it needs branch protection")
	}

	// A blank project reads the packaged default rather than falling back to
	// a guess.
	bwt, bpr, bmerge := vcsFlagsFor("")
	if !bwt || !bpr || bmerge {
		t.Errorf("blank: worktree=%v autoPR=%v autoMerge=%v", bwt, bpr, bmerge)
	}

	// An unknown template cannot read a config, and must not claim to: it
	// falls back to the packaged default's shape rather than to zeroes, which
	// would report "no worktrees, no PRs" for a project that will have both.
	uwt, upr, _ := vcsFlagsFor("no-such-template")
	if !uwt || !upr {
		t.Errorf("unknown template: worktree=%v autoPR=%v, want the default shape", uwt, upr)
	}
}

// A bad answer is re-asked, with the reason, and the wizard does not move on.
func TestBadAnswersAreReAsked(t *testing.T) {
	answers := []string{
		"Billing API", // rejected: spaces and capitals
		"billing/api", // rejected: slash
		"billing-api", // accepted
		"",            // root
		"nope",        // rejected: not a template
		"blank",
		"",
	}
	for range routerTierMapNonEmpty() {
		answers = append(answers, "")
	}
	answers = append(answers, "", "", "", "n")

	s := runWizard(t, answers, Options{})
	if s.err != nil {
		t.Fatalf("Wizard: %v\n%s", s.err, s.transcript)
	}
	if s.opts.Name != "billing-api" {
		t.Errorf("Name = %q, want the accepted answer", s.opts.Name)
	}
	if !strings.Contains(s.transcript, "project id must start with") {
		t.Errorf("the rejection did not say why:\n%s", s.transcript)
	}
	if !strings.Contains(s.transcript, "must be one of:") {
		t.Error("a bad choice did not list the valid ones")
	}
}

// Input running out is an error, not an implicit yes.
//
// The alternative — treating EOF as an empty line — would accept every
// remaining default AND the confirm gate, scaffolding a project nobody agreed
// to. That is the one outcome the gate exists to prevent, so it must not be
// reachable by closing a pipe.
func TestExhaustedInputIsAnErrorNotAConfirmation(t *testing.T) {
	s := runWizard(t, []string{"billing-api"}, Options{})
	if s.err == nil {
		t.Fatal("expected an error when the answers ran out")
	}
	if !errors.Is(s.err, io.EOF) {
		t.Errorf("err = %v, want it to wrap io.EOF", s.err)
	}
	if s.confirmed {
		t.Error("confirmed = true on exhausted input")
	}
}

// An explicit --template is not asked about again.
func TestExplicitTemplateSkipsThePicker(t *testing.T) {
	answers := []string{"proj", "", "", "", ""}
	for range routerTierMapNonEmpty() {
		answers = append(answers, "")
	}
	answers = append(answers, "", "y")

	s := runWizard(t, answers, Options{Template: "data-pipeline"})
	if s.err != nil {
		t.Fatalf("Wizard: %v\n%s", s.err, s.transcript)
	}
	if s.opts.Template != "data-pipeline" {
		t.Errorf("Template = %q, want the one passed in", s.opts.Template)
	}
	if strings.Contains(s.transcript, "Project templates:") {
		t.Error("the picker was shown despite an explicit template")
	}
}

// ---- validators --------------------------------------------------------------

func TestValidateProjectID(t *testing.T) {
	for _, ok := range []string{"a", "0", "billing-api", "billing_api", "x1-2_3"} {
		if err := validateProjectID(ok); err != nil {
			t.Errorf("validateProjectID(%q) = %v, want nil", ok, err)
		}
	}
	// The id becomes a directory name under .orchestrator/state/ and a column
	// value in every table, so these are not style rules.
	for _, bad := range []string{"", "-leading", "_leading", "Billing", "billing api",
		"billing/api", "billing.api", "../escape"} {
		if err := validateProjectID(bad); err == nil {
			t.Errorf("validateProjectID(%q) = nil, want an error", bad)
		}
	}
}

func TestValidateProjectRoot(t *testing.T) {
	dir := t.TempDir()
	// An existing, writable directory.
	if err := validateProjectRoot(dir); err != nil {
		t.Errorf("validateProjectRoot(%q) = %v", dir, err)
	}
	// One that does not exist yet but whose parent does — the normal case.
	if err := validateProjectRoot(filepath.Join(dir, "new-project")); err != nil {
		t.Errorf("validateProjectRoot(new) = %v", err)
	}
	// A parent that does not exist.
	if err := validateProjectRoot(filepath.Join(dir, "missing", "deeper")); err == nil {
		t.Error("a missing parent must be rejected")
	}
	// A file where a directory has to be.
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectRoot(filepath.Join(file, "child")); err == nil {
		t.Error("a file standing in for a parent directory must be rejected")
	}
}

// ---- the packaged YAML the wizard offers -------------------------------------

func TestBudgetPresetNamesComeFromTheShippedFile(t *testing.T) {
	got := budgetPresetNames()
	want := []string{"aggressive", "conservative", "shared"}
	if len(got) != len(want) {
		t.Fatalf("presets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("preset %d = %q, want %q (sorted)", i, got[i], want[i])
		}
	}
}

func TestRouterTierMapGroupsTheShippedRoutes(t *testing.T) {
	byTier := routerTierMap()
	for _, tier := range []string{"premium", "standard", "cheap"} {
		if _, ok := byTier[tier]; !ok {
			t.Errorf("the map has no %q key", tier)
		}
	}
	total := 0
	for _, tier := range []string{"premium", "standard", "cheap"} {
		total += len(byTier[tier])
		// Sorted, so the picker's default (the first) is stable between runs.
		for i := 1; i < len(byTier[tier]); i++ {
			if byTier[tier][i-1] > byTier[tier][i] {
				t.Errorf("%s tier is not sorted: %v", tier, byTier[tier])
				break
			}
		}
	}
	if total == 0 {
		t.Error("no routes were grouped — the packaged router did not parse")
	}
}

// routerTierMapNonEmpty is how many tiers the wizard will actually ask about:
// a tier with no routes is skipped rather than offered with an empty list.
func routerTierMapNonEmpty() []string {
	var out []string
	byTier := routerTierMap()
	for _, tier := range []string{"premium", "standard", "cheap"} {
		if len(byTier[tier]) > 0 {
			out = append(out, tier)
		}
	}
	return out
}

// The choice hint is inlined only when it helps.
//
// Python always writes `[{'/'.join(choices)}]`. The model keys ARE
// slash-separated, so fifteen of them joined by a slash produce a line where
// no reader can tell which slashes separate choices and which are part of one
// — and the options are already listed above it, one per line.
func TestChoiceHintIsInlinedOnlyWhenItHelps(t *testing.T) {
	if !inlineChoices([]string{"y", "n"}) {
		t.Error("a yes/no hint should be inlined")
	}
	if !inlineChoices([]string{"sqlite", "file"}) {
		t.Error("a two-word hint should be inlined")
	}
	if inlineChoices([]string{"claude/sonnet", "codex/gpt"}) {
		t.Error("choices containing slashes must not be joined by one")
	}
	if inlineChoices([]string{"a", "b", "c", "d", "e", "f", "g"}) {
		t.Error("seven choices do not belong on the prompt line")
	}
	if inlineChoices(nil) {
		t.Error("no choices, no hint")
	}
}

// The tier pickers must not put their options on the prompt line, and must
// still print them above it — the operator needs to see what they can pick.
func TestTierPickerListsOptionsAboveNotInline(t *testing.T) {
	answers := []string{"proj", "", "blank", "", "", ""}
	tiers := routerTierMapNonEmpty()
	for range tiers {
		answers = append(answers, "")
	}
	answers = append(answers, "", "n")

	s := runWizard(t, answers, Options{})
	if s.err != nil {
		t.Fatalf("Wizard: %v", s.err)
	}
	byTier := routerTierMap()
	for _, tier := range tiers {
		if !strings.Contains(s.transcript, tier+" tier options") {
			t.Errorf("the %s options were not listed", tier)
		}
		// Every option on its own line.
		for _, opt := range byTier[tier] {
			if !strings.Contains(s.transcript, "  - "+opt+"\n") {
				t.Errorf("%s option %q was not listed on its own line", tier, opt)
			}
		}
		// And not joined into the prompt.
		if len(byTier[tier]) > 1 {
			joined := strings.Join(byTier[tier], "/")
			if strings.Contains(s.transcript, "["+joined+"]") {
				t.Errorf("the %s options were inlined into the prompt", tier)
			}
		}
	}
}
