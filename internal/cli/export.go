package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/export"
	"github.com/hectorcanaimero/orch/internal/project"
)

// newExportCmd wires `orch export`, the parent for verbs that send work out.
//
// **New in Go**, and a separate verb from `orch sync` on purpose: `sync` is
// documented as never writing to the tracker it reads, and the moment one of
// its subcommands did, that promise would stop being something an operator
// could rely on without reading each subcommand. Export is the direction that
// writes outward, and it never writes to tasks.json or the state database.
func newExportCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Send this project's work out to another tracker",
		Long: "Create or mirror this project's tasks in another tracker.\n\n" +
			"Writes only to the destination: never to tasks.json or the state database.",
	}
	cmd.AddCommand(newExportMulticaCmd(flags))
	cmd.AddCommand(newExportClickUpCmd(flags))
	return cmd
}

func newExportMulticaCmd(flags *projectFlags) *cobra.Command {
	var (
		dryRun      bool
		projectFlag string
		phases      []int
		only        string
	)

	cmd := &cobra.Command{
		Use:   "multica",
		Short: "Create the DAG as Multica issues: a parent per phase, staged sub-issues per task",
		Long: "Create one Multica parent issue per phase and one sub-issue per task, through the\n" +
			"`multica` CLI (installed, logged in, with a workspace selected).\n\n" +
			"A task's stage is its dependency depth inside its phase; Multica stages are\n" +
			"coarser than the DAG, so each sub-issue's description names its exact\n" +
			"dependencies. Everything is created in backlog, so nothing starts on its own.\n" +
			"Tasks orch has done are left out.\n\n" +
			"Idempotent: every issue carries an `orch-task:` / `orch-phase:` marker, and a\n" +
			"second run skips what exists. It never updates or deletes an issue.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			// Discarded like every other read-only command's: the process is
			// exiting and nothing was written through this handle.
			defer func() { _ = closeDB() }()

			// Hydrated (rule 30): whether a task is done decides whether it
			// is exported, and tasks.json's status froze at F-12.
			tasks, err := project.Load(ctx, backend, paths.TasksJSON())
			if err != nil {
				return err
			}
			titles, err := export.PhaseTitles(paths.Root, cfg.SpecRoot, tasks)
			if err != nil {
				return err
			}
			plan, err := export.PlanMultica(paths.ID, cfg.SpecRoot, tasks,
				export.Selection{Phases: phases, Only: only}, titles)
			if err != nil {
				return withExitCode(2, err)
			}

			out := cmd.OutOrStdout()
			if plan.Empty() {
				_, err := fmt.Fprintf(out, "Nothing to export from project %s%s.\n", paths.ID, skippedSuffix(plan))
				return err
			}

			dest := export.Multica{Project: projectFlag, Run: export.NewExecRunner(paths.Root)}
			if err := dest.Resolve(ctx, &plan); err != nil {
				// A dry run without the CLI still shows the plan: that is how
				// someone decides whether to install it. It cannot know what
				// already exists, and says so; a real run stops here.
				if !dryRun || !export.IsNotFound(err) {
					return err
				}
				if _, werr := fmt.Fprintf(out, "Note: %v\nShowing the plan as if Multica had none of it yet.\n\n", err); werr != nil {
					return werr
				}
			}

			if err := printMulticaPlan(out, plan, dryRun); err != nil {
				return err
			}
			if dryRun {
				return nil
			}

			created := 0
			applyErr := dest.Apply(ctx, &plan, func(c export.Created) {
				created++
				if c.TaskID == "" {
					_, _ = fmt.Fprintf(out, "created %-10s parent  %s\n", c.Ref.Key, c.Title)
					return
				}
				_, _ = fmt.Fprintf(out, "created %-10s stage %d %s\n", c.Ref.Key, c.Stage, c.Title)
			})
			if applyErr != nil {
				// What was created stays, and has been printed above; the
				// markers make the next run continue instead of duplicating.
				return fmt.Errorf("%w (created %d issue(s) before this; re-running skips them)", applyErr, created)
			}
			_, err = fmt.Fprintf(out, "\nCreated %d issue(s) in Multica.\n", created)
			return err
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print what would be created and create nothing (still lists Multica's issues to find what exists)")
	cmd.Flags().StringVar(&projectFlag, "project", "",
		"Multica project to create the issues in (id or key; default: none)")
	cmd.Flags().IntSliceVar(&phases, "phase", nil,
		"Export only these phases (repeatable, or comma-separated)")
	cmd.Flags().StringVar(&only, "only", "",
		"Export only tasks whose id matches this glob, like `orch run --only`")
	return cmd
}

// printMulticaPlan is the same text for a dry run and a real one — a dry run
// differs by not creating, not by reporting differently.
func printMulticaPlan(w io.Writer, plan export.MulticaPlan, dryRun bool) error {
	verb := "Creating"
	if dryRun {
		verb = "Would create"
	}
	newParents, newTasks, existing := 0, 0, 0
	for _, ph := range plan.Phases {
		if ph.Existing == nil {
			newParents++
		} else {
			existing++
		}
		for _, t := range ph.Tasks {
			if t.Existing == nil {
				newTasks++
			} else {
				existing++
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %d parent issue(s) and %d sub-issue(s) in Multica, all in backlog", verb, newParents, newTasks)
	if existing > 0 {
		fmt.Fprintf(&b, "; %d already there", existing)
	}
	b.WriteString(":\n")
	for _, ph := range plan.Phases {
		fmt.Fprintf(&b, "\n  %s  %s\n", existingOrNew(ph.Existing), ph.Title)
		for _, t := range ph.Tasks {
			deps := "—"
			if len(t.Task.Dependencies) > 0 {
				deps = strings.Join(t.Task.Dependencies, ", ")
			}
			fmt.Fprintf(&b, "    %s  stage %d  %-10s %s  (deps: %s)\n",
				existingOrNew(t.Existing), t.Stage, t.Task.ID, firstLineOf(t.Task.Title), deps)
		}
	}
	fmt.Fprintf(&b, "\n%s\n", export.LossNote)
	if len(plan.SkippedDone) > 0 {
		fmt.Fprintf(&b, "Left out, done in orch: %s\n", strings.Join(plan.SkippedDone, ", "))
	}
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func existingOrNew(ref *export.IssueRef) string {
	if ref == nil {
		return "new       "
	}
	return fmt.Sprintf("%-10s", "="+ref.Key)
}

func skippedSuffix(plan export.MulticaPlan) string {
	if len(plan.SkippedDone) == 0 {
		return ""
	}
	return fmt.Sprintf(" (left out, done in orch: %s)", strings.Join(plan.SkippedDone, ", "))
}
