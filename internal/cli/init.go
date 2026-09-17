package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/scaffold"
	"github.com/hectorcanaimero/orch/internal/templates"
)

// newInitCmd ports `orch init` (orchestrator/init_cmd.py's `run_init_cli`).
//
// Two modes, and which one runs is decided the way Python decides it: passing
// any scaffolder argument — a path, `--template`, `--force`, `--sdd`,
// `--project-name` — means batch. Passing none means the operator typed
// `orch init` and wants to be asked.
//
// That rule is worth keeping rather than replacing with a `--yes` flag: the
// arguments themselves are the statement of intent. Someone who has typed out
// `--template python-api` has already answered the wizard's main question, and
// being asked it again is the tool not listening.
//
// Exit codes match Python's: 0 success or a declined confirm gate, 1 a
// conflict or a write failure.
func newInitCmd(flags *projectFlags) *cobra.Command {
	var (
		template    string
		projectName string
		force       bool
		sdd         bool
		noFindings  bool
	)

	cmd := &cobra.Command{
		Use:   "init [PATH]",
		Short: "Scaffold a new orch project",
		Long: "Scaffold a new orch project.\n\n" +
			"With no arguments, asks a short series of questions and shows " +
			"everything it is about to write before writing any of it.\n" +
			"With a path or any of the flags below, scaffolds directly.\n\n" +
			"Templates: " + strings.Join(templates.Names(), ", "),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := scaffold.Options{
				Template: template,
				Name:     projectName,
				Force:    force,
				SDD:      sdd,

				NoReportFindings: noFindings,
			}
			if len(args) == 1 {
				opts.Root = args[0]
			}

			// Python's `_is_scaffolder_flag_provided`: a path or any of the
			// flags means batch.
			batch := opts.Root != "" || template != "" || projectName != "" || force || sdd || noFindings
			if !batch {
				var confirmed bool
				var err error
				opts, confirmed, err = scaffold.Wizard(
					scaffold.NewWizardIO(cmd.InOrStdin(), cmd.OutOrStdout()), opts)
				if err != nil {
					return err
				}
				if !confirmed {
					// Declining is the gate working, not a failure. Python
					// returns 0 here too, and the wizard has already printed
					// "Aborted. Nothing written."
					return nil
				}
			}
			if opts.Root == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolving the current directory: %w", err)
				}
				opts.Root = cwd
			}

			res, err := scaffold.Run(opts)
			if err != nil {
				var conflict *scaffold.ConflictError
				if errors.As(err, &conflict) {
					// The conflict message is a list of paths and the flag
					// that overrides it — already the whole story, so it goes
					// out without a second sentence wrapped around it.
					return err
				}
				var notFound *templates.NotFoundError
				if errors.As(err, &notFound) {
					return err
				}
				return fmt.Errorf("scaffolding %s: %w", opts.Root, err)
			}

			printInitResult(cmd, res, opts)
			return nil
		},
	}

	cmd.Flags().StringVar(&template, "template", "",
		"Project template ("+strings.Join(templates.Names(), ", ")+"); omit for a blank project")
	cmd.Flags().StringVar(&projectName, "project-name", "",
		"Name used in generated files; default = the directory name")
	cmd.Flags().BoolVar(&force, "force", false,
		"Overwrite an existing project's files")
	cmd.Flags().BoolVar(&sdd, "sdd", false,
		"Also scaffold the openspec/ layout")
	cmd.Flags().BoolVar(&noFindings, "no-report-findings", false,
		"Write report_findings.enabled: false (agents do not file public issues about orch)")
	return cmd
}

// printInitResult is the banner, ported from `_print_next_steps`.
//
// The router line comes first among the notes because it is the one that
// changed: `orch init` now routes a template's own models, and saying so is
// what stops an operator from wondering whether they still need to.
func printInitResult(cmd *cobra.Command, res scaffold.Result, opts scaffold.Options) {
	out := cmd.OutOrStdout()
	// A failed write to stdout is not something this command can act on —
	// the project is already scaffolded and the banner is the last thing it
	// does. The exit code, not this text, is what a script reads.
	say := func(format string, args ...any) {
		_, _ = fmt.Fprintf(out, format+"\n", args...)
	}

	say("")
	say("✓ orch project initialized at %s", res.Root)
	if len(res.RoutesAdded) > 0 {
		say("✓ model_router.yaml: added %d route(s) for the template's tasks — %s",
			len(res.RoutesAdded), strings.Join(res.RoutesAdded, ", "))
		say("  (tier defaults to `standard`; review it, it drives the budget gate)")
	}
	for _, key := range res.RoutesUninferable {
		say("! model_router.yaml: cannot infer a backend for %q (no `backend/` prefix) — add it by hand", key)
	}
	for _, warning := range res.Warnings {
		say("! %s", warning)
	}

	say("")
	say("Next steps:")
	if opts.Template == "" {
		say("  1. Write your first spec:")
		say("       $EDITOR %s/specs/f0-foundation.md", res.Root)
		say("     (format reference: specs/README.md)")
		say("  2. Atomize it:")
		say("       orch atomize --project-root %s --file specs/f0-foundation.md --apply", res.Root)
	} else {
		say("  1. Inspect the tasks it came with:")
		say("       orch tasks --project-root %s", res.Root)
		say("  2. Preview the plan:")
		say("       orch validate --project-root %s", res.Root)
	}
	say("  3. Run it:")
	say("       orch run --project-root %s --mode semi", res.Root)
	say("")
	say("  If you change a task's `model` later, add its route:")
	say("       orch router add-missing --yes --project-root %s", res.Root)
	say("")
	say("Config: %s/.orchestrator/config.yaml", res.Root)
	say("  One file. `budgets.yaml` and `dashboard.yaml` are optional overrides")
	say("  that deep-merge on top if you drop them in the project root.")
	if !opts.NoReportFindings {
		say("")
		say("report_findings is on: agents may file public issues about orch with your gh login —")
		say("  set report_findings.enabled: false in .orchestrator/config.yaml to turn it off.")
	}
}
