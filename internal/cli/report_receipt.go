package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/receipt"
)

// newReportReceiptCmd is `orch report receipt`: one run's summary, the same
// one the dashboard's Now page shows, as Markdown to paste or JSON to script.
func newReportReceiptCmd(flags *projectFlags) *cobra.Command {
	var (
		runID       string
		asJSON      bool
		asMarkdown  bool
		noBuiltWith bool
	)
	cmd := &cobra.Command{
		Use:   "receipt",
		Short: "Summarise a run: tasks done and blocked, time, spend per provider, PRs",
		Long: "Print the receipt of a run — by default the latest one that finished — as\n" +
			"Markdown (the default) or JSON. Spend is labelled reported, estimated\n" +
			"(pricing.yaml) or no data, per provider. The Markdown ends with a\n" +
			"\"Built with orch\" line unless --no-built-with.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON && asMarkdown {
				return withExitCode(2, errors.New("--json and --markdown are mutually exclusive"))
			}
			ctx := cmd.Context()
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			// A failed close loses nothing on a read, so it warns on stderr
			// rather than changing an exit code the receipt already earned.
			defer func() {
				if cerr := closeDB(); cerr != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[warn] closing the database: %v\n", cerr)
				}
			}()

			rec, err := receipt.Load(ctx, backend, runID, receipt.Titles(loadDAG(paths)), pricing.Load(paths.Root))
			if err != nil {
				return err
			}
			if rec == nil {
				if runID != "" {
					return withExitCode(1, fmt.Errorf("no events for run %q", runID))
				}
				return withExitCode(1, errors.New("no run has finished yet — `orch run` writes a receipt when its loop ends"))
			}
			if asJSON {
				return printCompactJSON(cmd.OutOrStdout(), rec)
			}
			md := rec.Markdown()
			if !noBuiltWith {
				md += "\n" + receipt.BuiltWith + "\n"
			}
			if _, err := fmt.Fprint(cmd.OutOrStdout(), md); err != nil {
				return fmt.Errorf("write receipt: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "Run id (default: the latest finished run)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the receipt as JSON")
	cmd.Flags().BoolVar(&asMarkdown, "markdown", false, "Print the receipt as Markdown (the default)")
	cmd.Flags().BoolVar(&noBuiltWith, "no-built-with", false, "Leave out the \"Built with orch\" line")
	return cmd
}
