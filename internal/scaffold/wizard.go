package scaffold

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hectorcanaimero/orch/internal/templates"
	"gopkg.in/yaml.v3"
)

// The interactive half of `orch init`, ported from `run_wizard`.
//
// # Nothing is written until the operator says yes
//
// Every question has a default and none of them touch the disk. The H-7
// confirm gate then shows the whole set of answers in one screen, so an
// operator who got one of them wrong can say "n" instead of discovering it
// after the files exist and cleaning up by hand. That ordering is the feature:
// a wizard that wrote as it went would make the summary a report instead of a
// decision.

// Wizard asks the questions and returns the Options they describe.
//
// `confirmed` is false when the operator declined at the gate — not an error:
// answering "no" to "shall I write this?" is the gate working. The caller
// exits 0 and says nothing was written.
func Wizard(term *WizardIO, args Options) (opts Options, confirmed bool, err error) {
	if term == nil {
		return Options{}, false, errors.New("wizard: no input or output")
	}
	q := &asker{io: term}

	term.say("orch init — interactive setup")
	term.say("Press Enter to accept defaults. Ctrl-C to abort.")
	term.say("")

	opts = args

	projectID, err := q.ask("project id", "", nil, validateProjectID)
	if err != nil {
		return Options{}, false, err
	}
	opts.Name = projectID

	cwd, err := os.Getwd()
	if err != nil {
		// Not recoverable by asking again: without a working directory there
		// is no sensible default to offer, and a relative one would scaffold
		// somewhere the operator did not choose.
		return Options{}, false, fmt.Errorf("resolving the current directory for the default project root: %w", err)
	}
	rootDefault := filepath.Join(cwd, projectID)
	root, err := q.ask("project root", rootDefault, nil, validateProjectRoot)
	if err != nil {
		return Options{}, false, err
	}
	opts.Root = root

	// An explicit --template already answered this; do not ask twice.
	if opts.Template == "" {
		picked, perr := q.template()
		if perr != nil {
			return Options{}, false, perr
		}
		opts.Template = picked
	}

	// Informational only. A missing backend is something `orch doctor`
	// reports and the operator installs later; refusing to scaffold over it
	// would block a perfectly good project setup on a tool not needed yet.
	term.say("")
	term.say("Detected backends on PATH:")
	for _, name := range []string{"claude", "codex", "opencode"} {
		if path, lookErr := exec.LookPath(name); lookErr == nil {
			term.say(fmt.Sprintf("  %-10s ✓ %s", name, path))
		} else {
			term.say(fmt.Sprintf("  %-10s ✗ not on PATH", name))
		}
	}
	term.say("(missing backends can be installed later — `orch doctor` verifies)")
	term.say("")

	backend, err := q.ask("state backend", "sqlite", []string{"sqlite", "file"}, nil)
	if err != nil {
		return Options{}, false, err
	}
	opts.StateBackend = backend
	if backend == "file" {
		term.say("  → file backend: JSONL + JSON in state/. Legacy — kept for")
		term.say("    pre-v0.11 projects and removed in the next major.")
	} else {
		term.say("  → sqlite backend selected — single orch.db file with WAL journaling.")
	}
	term.say("")

	presets := budgetPresetNames()
	preset, err := q.ask("budget preset", presets[0], presets, nil)
	if err != nil {
		return Options{}, false, err
	}

	opts.BudgetPreset = preset

	specRoot, err := q.ask("spec root (relative to project root)", "specs", nil, nil)
	if err != nil {
		return Options{}, false, err
	}
	opts.SpecRoot = specRoot

	tiers, err := q.tiers()
	if err != nil {
		return Options{}, false, err
	}
	opts.TierDefaults = tiers

	sdd, err := q.ask("scaffold openspec/ layout for SDD?", "n", []string{"y", "n"}, nil)
	if err != nil {
		return Options{}, false, err
	}
	opts.SDD = sdd == "y"

	// H-7: everything in one screen, before anything is written.
	//
	// The VCS flags are read out of the config the scaffold will actually
	// write, not restated here. A summary that lists what someone believed
	// the defaults were is worse than no summary: it is wrong exactly when it
	// matters, which is after somebody edits a template.
	wt, autoPR, autoMerge := vcsFlagsFor(opts.Template)
	term.say("")
	term.say("About to scaffold:")
	term.say(fmt.Sprintf("  project id       %s", projectID))
	term.say(fmt.Sprintf("  project root     %s", opts.Root))
	term.say(fmt.Sprintf("  template         %s", orBlank(opts.Template)))
	term.say(fmt.Sprintf("  state backend    %s", backend))
	term.say(fmt.Sprintf("  budget preset    %s", preset))
	term.say(fmt.Sprintf("  spec root        %s", specRoot))
	term.say(fmt.Sprintf("  worktree mode    %s", yesNo(wt)))
	term.say(fmt.Sprintf("  auto PR          %s", yesNo(autoPR)))
	term.say(fmt.Sprintf("  auto merge       %s", yesNo(autoMerge)))
	for _, tier := range []string{"premium", "standard", "cheap"} {
		if m := tiers[tier]; m != "" {
			term.say(fmt.Sprintf("  %-9s model  %s", tier, m))
		}
	}
	term.say(fmt.Sprintf("  openspec layout  %s", yesNo(opts.SDD)))
	term.say("")

	proceed, err := q.ask("Proceed with scaffolding?", "y", []string{"y", "n"}, nil)
	if err != nil {
		return Options{}, false, err
	}
	if proceed != "y" {
		term.say("")
		term.say("Aborted. Nothing written.")
		return opts, false, nil
	}
	return opts, true, nil
}

// WizardIO is the wizard's terminal: a line source and a place to print.
//
// An explicit type rather than reading os.Stdin inside, so a test drives the
// whole flow with a scripted string and reads back exactly what an operator
// would have seen. Python takes an `input_fn` for the same reason.
type WizardIO struct {
	in  *bufio.Scanner
	out io.Writer
}

// NewWizardIO wires the wizard to a reader and a writer.
func NewWizardIO(r io.Reader, w io.Writer) *WizardIO {
	return &WizardIO{in: bufio.NewScanner(r), out: w}
}

// say writes one line to the wizard's output.
//
// The write error is dropped deliberately, and this is the one place it is:
// the destination is a terminal, and a wizard that abandoned a scaffold
// because a line failed to print would be reacting to the least important
// thing that can go wrong. A real failure — the operator closing the pipe —
// shows up at the next read, as EOF, where it stops the flow properly.
func (w *WizardIO) say(msg string) {
	_, _ = fmt.Fprintln(w.out, msg)
}

// readLine returns the next line, or io.EOF when there are none left.
//
// EOF is an error rather than an empty answer on purpose. A wizard whose input
// has run out would otherwise accept every remaining default and scaffold a
// project nobody confirmed — which is the one outcome the confirm gate exists
// to prevent.
func (w *WizardIO) readLine() (string, error) {
	if !w.in.Scan() {
		if err := w.in.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return w.in.Text(), nil
}

// asker loops one question until the answer is acceptable.
type asker struct{ io *WizardIO }

func (a *asker) ask(message, def string, choices []string, validate func(string) error) (string, error) {
	var b strings.Builder
	b.WriteString(message)
	if inlineChoices(choices) {
		b.WriteString(" [" + strings.Join(choices, "/") + "]")
	}
	if def != "" {
		b.WriteString(" (" + def + ")")
	}
	b.WriteString(": ")
	label := b.String()

	for {
		_, _ = fmt.Fprint(a.io.out, label) // see WizardIO.say
		raw, err := a.io.readLine()
		if err != nil {
			return "", fmt.Errorf("reading the answer to %q: %w", message, err)
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			value = def
		}
		if value == "" {
			a.io.say("  → value required, please try again.")
			continue
		}
		if len(choices) > 0 && !contains(choices, value) {
			a.io.say("  → must be one of: " + strings.Join(choices, ", "))
			continue
		}
		if validate != nil {
			if verr := validate(value); verr != nil {
				a.io.say("  → " + verr.Error())
				continue
			}
		}
		return value, nil
	}
}

// inlineChoices decides whether the choices fit in the prompt's `[a/b]` hint.
//
// # Divergence: Python always inlines them
//
// `prompt` in init_cmd.py writes `[{'/'.join(choices)}]` unconditionally. For
// yes/no and sqlite/file that reads well. For the model tier picker it does
// not: the keys ARE slash-separated, so fifteen of them joined by a slash
// produce a line like
//
//	[agy/flash/claude/claude-haiku-4-5/gemini/gemini-2.5-flash/opencode-go/…]
//
// where no reader can tell which slashes separate choices and which are part
// of one. The options are already printed above it, one per line, so the
// bracket adds nothing but the confusion.
//
// Inlined when every choice is slash-free and there are at most six. `orch
// init` is interactive and its output is not compared by scripts/parity.sh,
// so this is a difference a user sees and nothing else depends on.
func inlineChoices(choices []string) bool {
	if len(choices) == 0 || len(choices) > 6 {
		return false
	}
	for _, c := range choices {
		if strings.Contains(c, "/") {
			return false
		}
	}
	return true
}

// template offers blank plus every shipped template, with its description.
func (a *asker) template() (string, error) {
	list := templates.List()
	if len(list) == 0 {
		return "", nil
	}
	a.io.say("")
	a.io.say("Project templates:")
	a.io.say("  blank          Empty tasks.json — you fill it in yourself.")
	choices := []string{"blank"}
	for _, t := range list {
		a.io.say(fmt.Sprintf("  %-14s %s", t.Name, t.Description))
		choices = append(choices, t.Name)
	}
	picked, err := a.ask("template", "blank", choices, nil)
	if err != nil {
		return "", err
	}
	a.io.say("")
	if picked == "blank" {
		return "", nil
	}
	return picked, nil
}

// tiers asks for a default model per tier, offering the routes the packaged
// router already groups that way.
//
// Informational: the answers go into tasks.json's meta block, not into the
// router. A tier with no routes is skipped rather than asked about with an
// empty list.
func (a *asker) tiers() (map[string]string, error) {
	byTier := routerTierMap()
	out := map[string]string{}
	for _, tier := range []string{"premium", "standard", "cheap"} {
		options := byTier[tier]
		if len(options) == 0 {
			continue
		}
		a.io.say("")
		a.io.say(fmt.Sprintf("%s tier options (pick one for meta.default_%s_model):", tier, tier))
		for _, opt := range options {
			a.io.say("  - " + opt)
		}
		picked, err := a.ask(fmt.Sprintf("  default %s model", tier), options[0], options, nil)
		if err != nil {
			return nil, err
		}
		out[tier] = picked
	}
	a.io.say("")
	return out, nil
}

// ---- validation ------------------------------------------------------------

var projectIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// validateProjectID enforces the shape the project id has to have.
//
// It is not cosmetic: the id becomes a directory name under
// `.orchestrator/state/` and a column value in every table, so a space or a
// slash in it produces a path nobody intended.
func validateProjectID(v string) error {
	if !projectIDRe.MatchString(v) {
		return errors.New("project id must start with [a-z0-9] and contain only " +
			"lowercase letters, digits, _ and -")
	}
	return nil
}

// validateProjectRoot checks the destination can be written before the
// operator answers six more questions and finds out at the end.
func validateProjectRoot(v string) error {
	path, err := filepath.Abs(os.ExpandEnv(v))
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", v, err)
	}
	parent := path
	if _, err := os.Stat(path); err != nil {
		parent = filepath.Dir(path)
	}
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("parent dir %s is not usable: %w", parent, err)
	}
	if !info.IsDir() {
		// No underlying error to wrap: the stat succeeded and the answer is
		// simply "that is a file".
		return fmt.Errorf("%s is not a directory", parent)
	}
	// A write probe rather than a permission-bit check: the bits do not
	// account for the effective user, and root ignores them entirely.
	probe, err := os.CreateTemp(parent, ".orch-init-probe-*")
	if err != nil {
		return fmt.Errorf("parent dir %s is not writable: %w", parent, err)
	}
	// The probe answered the only question — can this directory be written —
	// so a failure to close or remove it changes nothing the caller can act
	// on. Worst case it leaves a zero-byte dotfile the next scaffold ignores.
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return nil
}

// ---- the packaged YAML the wizard offers -----------------------------------

// budgetPresetNames reads the preset keys out of the shipped budgets.yaml.
//
// Read rather than listed, so a preset added to the YAML shows up in the
// wizard without a second edit here. The fallback covers a broken packaged
// copy: offering the three that have always existed beats failing to start.
func budgetPresetNames() []string {
	fallback := []string{"conservative", "aggressive", "shared"}
	raw, err := packagedDefault("budgets.yaml")
	if err != nil {
		return fallback
	}
	var file struct {
		Presets map[string]any `yaml:"presets"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil || len(file.Presets) == 0 {
		return fallback
	}
	names := make([]string, 0, len(file.Presets))
	for name := range file.Presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// routerTierMap groups the packaged router's keys by tier, for the picker.
func routerTierMap() map[string][]string {
	out := map[string][]string{"premium": {}, "standard": {}, "cheap": {}}
	raw, err := packagedDefault("model_router.yaml")
	if err != nil {
		return out
	}
	var entries map[string]struct {
		Tier string `yaml:"tier"`
	}
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		return out
	}
	for key, e := range entries {
		if _, known := out[e.Tier]; known {
			out[e.Tier] = append(out[e.Tier], key)
		}
	}
	for tier := range out {
		sort.Strings(out[tier])
	}
	return out
}

// vcsFlagsFor reads worktree_mode, auto_pr and auto_merge out of the config
// the scaffold will actually write.
//
// Read, not restated. The summary's whole job is to be true, and a hardcoded
// copy of what someone believed the defaults were would be wrong exactly when
// it matters: after a template's config.yaml is edited.
func vcsFlagsFor(template string) (worktree, autoPR, autoMerge bool) {
	var raw []byte
	if template != "" {
		if dir, err := templates.Project(template); err == nil {
			if body, rerr := fs.ReadFile(dir, "config.yaml.tmpl"); rerr == nil {
				raw = body
			}
		}
	}
	if raw == nil {
		body, err := packagedDefault("config.yaml")
		if err != nil {
			return true, true, false
		}
		raw = body
	}

	var cfg struct {
		Dispatch struct {
			WorktreeMode bool `yaml:"worktree_mode"`
		} `yaml:"dispatch"`
		VCS struct {
			AutoPR bool `yaml:"auto_pr"`
		} `yaml:"vcs"`
		GitHub struct {
			AutoMerge bool `yaml:"auto_merge"`
		} `yaml:"github"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return true, true, false
	}
	return cfg.Dispatch.WorktreeMode, cfg.VCS.AutoPR, cfg.GitHub.AutoMerge
}

// ---- small helpers ---------------------------------------------------------

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orBlank(s string) string {
	if s == "" {
		return "blank"
	}
	return s
}
