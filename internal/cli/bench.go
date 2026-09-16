package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/bench"
	"github.com/hectorcanaimero/orch/internal/doctor"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/scaffold"
)

// benchTemplate is the project `orch bench` runs when --project is not given:
// the smallest packaged template, so a default bench costs as little as a
// real one can.
const benchTemplate = "python-api"

// benchCapPoll is how often a running bench re-reads its spend against
// --max-usd.
var benchCapPoll = 2 * time.Second

type benchOptions struct {
	providers []string
	project   string
	runs      int
	maxUSD    float64
	uncapped  bool
	models    map[string]string
	out       string
	markdown  bool
	dryRun    bool
	yes       bool
}

// newBenchCmd is `orch bench`: the same project run once per provider through
// `orch run`'s own code path, on throwaway copies, compared in one table.
func newBenchCmd(version string) *cobra.Command {
	var o benchOptions
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Run one project with each provider and compare done tasks, time and spend (costs real money)",
		Long: "Copies the project once per provider (and per --runs), routes every task to that provider, and runs it\n" +
			"exactly as `orch run` would: real agents, real spend. Copies never push, open PRs or touch the\n" +
			"project's own files or database. --max-usd stops a provider's run once its spend reaches the cap.\n" +
			"See docs/BENCH.md.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBench(cmd, version, o)
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&o.providers, "providers", []string{"claude", "codex", "opencode", "gemini"},
		"Providers to compare: "+strings.Join(bench.Providers, ", "))
	f.StringVar(&o.project, "project", "",
		"Project to run (a directory with tasks.json); default: a fresh copy of the "+benchTemplate+" template")
	f.IntVar(&o.runs, "runs", 1, "Runs per provider")
	f.Float64Var(&o.maxUSD, "max-usd", 0, "Stop each provider's run once it has spent this many USD (required)")
	f.BoolVar(&o.uncapped, "yes-i-know-it-costs", false, "Run without --max-usd")
	f.StringToStringVar(&o.models, "model", nil,
		"cli_model to route a provider to, e.g. --model codex=gpt-5.4 (default: the router's own route for that provider)")
	f.StringVar(&o.out, "out", "", "Also write the results as JSON to this file")
	f.BoolVar(&o.markdown, "markdown", false, "Print the results as a Markdown table instead of JSON")
	f.BoolVar(&o.dryRun, "dry-run", false, "Print what would run and exit")
	f.BoolVar(&o.yes, "yes", false, "Start without asking (required when stdin is not a terminal)")
	return cmd
}

func runBench(cmd *cobra.Command, version string, o benchOptions) error {
	if err := o.validate(); err != nil {
		return withExitCode(1, err)
	}
	ctx := cmd.Context()
	errOut := cmd.ErrOrStderr()

	src := o.project
	if src == "" {
		dir, err := os.MkdirTemp("", "orch-bench-template-")
		if err != nil {
			return withExitCode(1, fmt.Errorf("create the template directory: %w", err))
		}
		defer func() { _ = os.RemoveAll(dir) }()
		if _, err := scaffold.Run(scaffold.Options{Root: dir, Name: "bench", Template: benchTemplate}); err != nil {
			return withExitCode(1, fmt.Errorf("scaffold the %s template: %w", benchTemplate, err))
		}
		src = dir
	} else {
		abs, err := filepath.Abs(src)
		if err != nil {
			return withExitCode(1, fmt.Errorf("resolve --project: %w", err))
		}
		if _, err := os.Stat(filepath.Join(abs, "tasks.json")); err != nil {
			return withExitCode(1, fmt.Errorf("--project %s: no tasks.json: %w", o.project, err))
		}
		src = abs
	}

	_, fake := engine.FakeProviderDir()
	if !fake {
		for _, p := range o.providers {
			if _, err := exec.LookPath(p); err != nil {
				return withExitCode(1, fmt.Errorf("%s is not on PATH; install it or drop it from --providers", p))
			}
		}
	}

	plan := o.plan(src)
	if o.dryRun {
		_, err := fmt.Fprint(cmd.OutOrStdout(), plan)
		return err
	}
	_, _ = fmt.Fprint(errOut, plan)
	if !o.yes {
		if err := confirmBench(cmd.InOrStdin(), errOut); err != nil {
			return withExitCode(1, err)
		}
	}

	report := bench.Report{
		BenchVersion: bench.Version,
		OrchVersion:  version,
		Date:         time.Now().UTC().Format(time.RFC3339),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Project:      projectLabel(o.project),
		MaxUSD:       o.maxUSD,
	}
	for _, p := range o.providers {
		cli := cliVersion(p, fake)
		for run := 1; run <= o.runs; run++ {
			_, _ = fmt.Fprintf(errOut, "\n== %s, run %d of %d ==\n", p, run, o.runs)
			res, err := benchOne(ctx, cmd.InOrStdin(), errOut, src, p, o)
			if err != nil {
				res.Outcome = "error: " + err.Error()
			}
			res.Provider, res.CLIVersion, res.Run = p, cli, run
			report.Results = append(report.Results, res)
		}
	}

	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return withExitCode(1, fmt.Errorf("encode the results: %w", err))
	}
	if o.out != "" {
		if err := os.WriteFile(o.out, append(body, '\n'), 0o600); err != nil {
			return withExitCode(1, fmt.Errorf("write %s: %w", o.out, err))
		}
	}
	if o.markdown {
		_, err = fmt.Fprint(cmd.OutOrStdout(), report.Markdown())
	} else {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", body)
	}
	return err
}

func (o benchOptions) validate() error {
	if len(o.providers) == 0 {
		return errors.New("--providers is empty")
	}
	for _, p := range o.providers {
		if !slices.Contains(bench.Providers, p) {
			return fmt.Errorf("--providers: unknown provider %q (expected one of %s)", p, strings.Join(bench.Providers, ", "))
		}
	}
	for p, m := range o.models {
		if !slices.Contains(o.providers, p) {
			return fmt.Errorf("--model %s=%s: %s is not in --providers", p, m, p)
		}
	}
	switch {
	case o.runs < 1:
		return fmt.Errorf("--runs %d: must be at least 1", o.runs)
	case o.maxUSD < 0:
		return fmt.Errorf("--max-usd %g: must be positive", o.maxUSD)
	case o.maxUSD == 0 && !o.uncapped && !o.dryRun:
		return errors.New("--max-usd is required: a bench runs real agents and spends real money " +
			"(pass --yes-i-know-it-costs to run without a cap)")
	}
	return nil
}

func (o benchOptions) plan(src string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "orch bench will run %s with each of: %s (%d run(s) each, %d in total).\n",
		projectLabel(o.project), strings.Join(o.providers, ", "), o.runs, len(o.providers)*o.runs)
	fmt.Fprintf(&b, "Each run is a throwaway copy of %s: no pushes, no PRs, your files and database untouched.\n", src)
	if o.maxUSD > 0 {
		fmt.Fprintf(&b, "Each run stops once it has spent $%.2f (a dispatch already running still finishes).\n", o.maxUSD)
	} else {
		b.WriteString("No --max-usd: nothing but each project's budgets.yaml limits the spend.\n")
	}
	b.WriteString("WARNING: this dispatches real agents. Every provider bills you for its run.\n")
	return b.String()
}

// confirmBench asks on a terminal and refuses anywhere else: a bench started
// by a script must say --yes, because a pipe cannot be asked.
func confirmBench(in io.Reader, out io.Writer) error {
	f, ok := in.(*os.File)
	if !ok {
		return errors.New("not starting: pass --yes to run without a terminal")
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("not starting: pass --yes to run without a terminal")
	}
	_, _ = fmt.Fprint(out, "Type yes to start: ")
	answer, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(answer) != "yes" {
		return errors.New("not starting: the answer was not yes")
	}
	return nil
}

// benchOne prepares one copy, runs it with the cap watch, and collects it.
func benchOne(ctx context.Context, in io.Reader, errOut io.Writer, src, provider string, o benchOptions) (bench.Result, error) {
	dir, err := os.MkdirTemp("", "orch-bench-"+provider+"-")
	if err != nil {
		return bench.Result{}, fmt.Errorf("create the copy: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := bench.Prepare(src, dir, provider, o.models); err != nil {
		return bench.Result{}, err
	}

	flags := &projectFlags{root: dir}
	paths, cfg, err := loadProjectConfig(flags)
	if err != nil {
		return bench.Result{}, err
	}
	// An absolute state.sqlite_path would point the copy at the project's
	// real database.
	if db := filepath.Clean(paths.SQLitePath(cfg)); !strings.HasPrefix(db, filepath.Clean(dir)+string(filepath.Separator)) {
		return bench.Result{}, fmt.Errorf("state.sqlite_path resolves outside the copy (%s); bench only runs projects with a relative database path", db)
	}
	// Opened (and migrated) before the run, so the cap watch never races
	// the run's own open.
	backend, closeBackend, err := openBackend(ctx, paths, cfg)
	if err != nil {
		return bench.Result{}, err
	}
	defer func() { _ = closeBackend() }()
	prices := pricing.Load(dir)
	runID := providers.NewSessionID()

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	watched := make(chan error, 1)
	if o.maxUSD > 0 {
		ticker := time.NewTicker(benchCapPoll)
		defer ticker.Stop()
		go func() {
			watched <- bench.WatchCap(runCtx, ticker.C, o.maxUSD, func(c context.Context) (float64, error) {
				return bench.SpentUSD(c, backend, runID, prices)
			}, stop)
		}()
	} else {
		watched <- nil
	}

	runErr := runProject(runCtx, in, errOut, flags, runOptions{mode: string(engine.ModeAuto), runID: runID, isolated: true})
	stop()
	capErr := <-watched

	res, err := bench.Collect(ctx, backend, runID, prices)
	switch {
	case errors.Is(capErr, bench.ErrOverCap):
		res.Outcome = "stopped: max-usd"
	case runErr != nil && runErr.Error() != "":
		res.Outcome = "error: " + runErr.Error()
	case runErr != nil:
		var ee *exitError
		if errors.As(runErr, &ee) {
			res.Outcome = fmt.Sprintf("exit %d", ee.code)
		}
	default:
		res.Outcome = "finished"
	}
	return res, err
}

func cliVersion(provider string, fake bool) string {
	if fake {
		return "fake (" + engine.FakeProviderEnv + ")"
	}
	ok, v := doctor.ProbeVersion(provider)
	if !ok {
		return "unknown: " + v
	}
	return v
}

func projectLabel(project string) string {
	if project == "" {
		return "template:" + benchTemplate
	}
	return filepath.Base(filepath.Clean(project))
}
