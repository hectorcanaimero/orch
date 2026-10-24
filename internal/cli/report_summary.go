package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/ci"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/receipt"
	"github.com/hectorcanaimero/orch/internal/router"
)

// summaryAITimeout bounds the --ai dispatch: one short answer, no tools.
const summaryAITimeout = 5 * time.Minute

// newReportSummaryCmd is `orch report summary` (#456): the receipt in plain
// words — what was built, what stopped and why, and the next steps with
// their commands — the same text the Now page shows under the receipt and
// `orch run` posts to the webhooks when a run ends. --ai (#461) has an agent
// explain the same facts.
func newReportSummaryCmd(flags *projectFlags) *cobra.Command {
	var (
		runID     string
		asJSON    bool
		language  string
		ai        bool
		aiBackend string
	)
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Say what a run built, what stopped and why, and what to do next",
		Long: "Print a run's summary in plain words — by default the latest one that\n" +
			"finished: what was built, why each blocked task stopped (its failure class,\n" +
			"the local verify, CI or its agent's note), and the next one to three steps\n" +
			"with the command for each. Built from the receipt, so it costs nothing.\n" +
			"Written in dashboard.language unless --language.\n\n" +
			"--ai has an agent explain the same facts, on the project's cheapest route\n" +
			"(of --backend's, when given); its spend is recorded as summary-<run>. When\n" +
			"the dispatch fails, the plain summary is printed after a warning.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkLanguage(language); err != nil {
				return err
			}
			if ai && asJSON {
				return withExitCode(2, errors.New("--ai and --json are mutually exclusive"))
			}
			if aiBackend != "" && !slices.Contains(ci.ReviewBackends, model.Backend(aiBackend)) {
				return withExitCode(2, fmt.Errorf("--backend %q: expected claude, gemini, codex or opencode", aiBackend))
			}
			rec, _, paths, cfg, st, done, err := loadRunReceipt(cmd, flags, runID)
			if err != nil {
				return err
			}
			defer done()
			if language == "" {
				language = cfg.Dashboard.SummaryLanguage
			}
			s := rec.Summary(language, cfg.GitHub.AutoMerge)
			if asJSON {
				return printCompactJSON(cmd.OutOrStdout(), s)
			}
			text := s.Text()
			if ai {
				answer, err := explainSummary(cmd, paths, engine.OneShot{State: st, Gate: newBudgetGate(paths, cfg, st)}, rec, s, aiBackend)
				if err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[warn] --ai: %v — the plain summary follows\n", err)
				} else {
					text = answer + "\n"
				}
			}
			if _, err := fmt.Fprint(cmd.OutOrStdout(), text); err != nil {
				return fmt.Errorf("write summary: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "Run id (default: the latest finished run)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the summary as JSON")
	cmd.Flags().StringVar(&language, "language", "", "Override dashboard.language (en|es|pt)")
	cmd.Flags().BoolVar(&ai, "ai", false, "Have an agent explain the summary (one dispatch, spend recorded)")
	cmd.Flags().StringVar(&aiBackend, "backend", "", "With --ai, the provider to ask (claude|gemini|codex|opencode; default: the cheapest route's)")
	return cmd
}

var languageNames = map[string]string{"en": "English", "es": "Spanish", "pt": "Portuguese"}

// explainSummary asks one agent, read-only, to explain s and the receipt it
// was built from, and returns its answer.
func explainSummary(cmd *cobra.Command, paths config.Paths, once engine.OneShot, rec *receipt.Receipt, s receipt.Summary, backend string) (string, error) {
	route, err := summaryRoute(paths, backend)
	if err != nil {
		return "", err
	}
	facts, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	prompt := "You explain the result of a run of orch, a task orchestrator, to the person who runs it. " +
		"Write in " + languageNames[s.Lang] + ", in plain prose of at most three short paragraphs: what was built, " +
		"what stopped and why. Then list the next steps, keeping each command exactly as given. " +
		"Use only the facts below; do not invent tasks, figures or commands, and do not use tools.\n\n" +
		"## Summary\n\n" + s.Text() + "\n## Receipt (JSON)\n\n" + string(facts) + "\n"
	return ci.AskOnce(cmd.Context(), once, "summary-"+rec.RunID, route, prompt, summaryAITimeout)
}

// summaryRoute is the project router's cheapest route on a backend --ai can
// run read-only, on backend's when it is set.
func summaryRoute(paths config.Paths, backend string) (model.RouteEntry, error) {
	rtr, err := router.Load(paths.RouterYAML())
	if err != nil {
		return model.RouteEntry{}, err
	}
	runs := func(b model.Backend) bool {
		return slices.Contains(ci.ReviewBackends, b) && (backend == "" || string(b) == backend)
	}
	for _, tier := range []model.Tier{model.TierCheap, model.TierStandard, model.TierPremium} {
		if k := rtr.ForTier(tier, runs); k != "" {
			return rtr[k], nil
		}
	}
	if backend != "" {
		return model.RouteEntry{}, fmt.Errorf("model_router.yaml has no route on %s", backend)
	}
	return model.RouteEntry{}, errors.New("model_router.yaml has no route on claude, gemini, codex or opencode")
}
