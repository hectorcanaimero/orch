package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
	"github.com/hectorcanaimero/orch/internal/report"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newReportCmd is `orch report`, whose only subcommand today is `pdf`.
//
// A parent with one child rather than `orch report-pdf`, because the shape it
// is reserving is real: a report is a rendering of one snapshot, and the next
// format (html, a one-line digest) is a sibling and not a new command.
func newReportCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render the stakeholder snapshot as a document",
	}
	cmd.AddCommand(newReportPDFCmd(flags))
	return cmd
}

// defaultReportName is where the PDF lands when `--out` is not given.
const defaultReportName = "orch-report.pdf"

func newReportPDFCmd(flags *projectFlags) *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:   "pdf",
		Short: "Write a one-page A4 progress report",
		Long: "Render the stakeholder snapshot as a one-page A4 PDF: progress, " +
			"milestones, what is blocked and why, and — only if\n" +
			"`dashboard.show_spend_to_stakeholder` is on — what it has cost.\n\n" +
			"The page carries the same figures the stakeholder web snapshot " +
			"does, from the same builder, so the two cannot disagree.",
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
			// Reported rather than discarded, unlike the read-only commands:
			// this one WRITES a file, and a database handle that fails to
			// close is the kind of thing worth knowing about next to an
			// artefact somebody is about to email. It does not fail the
			// command — the PDF on disk is already correct — so it goes to
			// stderr and the exit code stays 0.
			defer func() {
				if cerr := closeDB(); cerr != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"[warn] closing the database: %v\n", cerr)
				}
			}()

			snap, err := buildStakeholderSnapshot(ctx, paths, cfg, backend, time.Now())
			if err != nil {
				return err
			}

			target := out
			if target == "" {
				target = filepath.Join(paths.Root, defaultReportName)
			}
			// #nosec G304 -- the operator names the output file
			f, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("creating %s: %w", target, err)
			}
			// Compression on: this is the artefact somebody emails, not the
			// one a test reads back.
			renderErr := report.PDF(f, snap, report.Options{Compress: true, Now: time.Now()})
			closeErr := f.Close()
			if renderErr != nil {
				return renderErr
			}
			if closeErr != nil {
				// A close that fails after a successful write is a write that
				// did not land — unlike a read-only handle, this one has to
				// be reported.
				return fmt.Errorf("closing %s: %w", target, closeErr)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", target)
			return nil
		},
	}

	cmd.Flags().StringVar(&out, "out", "",
		"Where to write the PDF; default = <project-root>/"+defaultReportName)
	return cmd
}

// brandingLogoPath resolves a relative logo against the PROJECT ROOT, the way
// `budgets_config` already resolves.
//
// Not against the working directory: `orch report pdf --project-root ../other`
// is a normal thing to type, and a logo that only resolves when you happen to
// be standing in the project is a logo that works on the operator's machine
// and not in CI. A `data:` URI and an absolute path are left alone.
func brandingLogoPath(paths config.Paths, logo string) string {
	if logo == "" || strings.HasPrefix(logo, "data:") || filepath.IsAbs(logo) {
		return logo
	}
	return filepath.Join(paths.Root, logo)
}

// buildStakeholderSnapshot assembles the stakeholder snapshot from the
// project. Named for its audience rather than `buildSnapshot`, because
// `status.go` already has one of those and they are different documents: that
// one is the operator's full task table, this one is the id-free summary a
// client sees.
//
// This is the FIRST caller of `snapshot.Build` in the tree — the package
// landed with G6.1 and nothing had assembled its Input yet. Worth saying out
// loud, because it is the same gap this migration keeps finding from the other
// side: a builder nobody calls is as untested in practice as a column nobody
// reads.
func buildStakeholderSnapshot(ctx context.Context, paths config.Paths, cfg config.Config,
	backend state.Backend, now time.Time) (snapshot.Snapshot, error) {
	f, err := model.LoadTasksFile(paths.TasksJSON())
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	tasks, err := project.Hydrate(ctx, backend, f.Tasks)
	if err != nil {
		return snapshot.Snapshot{}, err
	}

	// Every blocked task's reason comes from its last event, and the spend
	// figure from every row: both are reads the snapshot does not do for
	// itself, by design — it takes data and shapes it.
	events, err := backend.AllEvents(ctx, 0)
	if err != nil {
		return snapshot.Snapshot{}, fmt.Errorf("reading the event log: %w", err)
	}
	spends, err := backend.AllSpend(ctx, time.Time{})
	if err != nil {
		return snapshot.Snapshot{}, fmt.Errorf("reading the spend log: %w", err)
	}
	// Names the specs give that tasks.json does not, and when each task
	// finished: the portal's roadmap and "since your last visit".
	outline, err := project.SpecOutline(paths.Root, cfg.SpecRoot, tasks)
	if err != nil {
		return snapshot.Snapshot{}, fmt.Errorf("reading phase and package names from the specs: %w", err)
	}
	finishedAt, err := project.FinishedAt(ctx, backend)
	if err != nil {
		return snapshot.Snapshot{}, fmt.Errorf("reading when tasks finished: %w", err)
	}

	// The pace behind the finish date, counted as the Sprint page counts it.
	doneInWindow, err := backend.CountDoneLastNDays(ctx, snapshot.VelocityWindowDays)
	if err != nil {
		return snapshot.Snapshot{}, fmt.Errorf("counting recent completions: %w", err)
	}

	name := f.Meta.Project
	if name == "" {
		name = paths.ID
	}

	// The logo is read HERE, not in the snapshot package: reading a file is a
	// caller's job, and the document's own rule is that it must stand alone —
	// by the time it reaches Build it is a data URI or nothing.
	branding := snapshot.Branding{
		Name:        cfg.Presentation.Branding.Name,
		AccentColor: cfg.Presentation.Branding.AccentColor,
		Footer:      cfg.Presentation.Branding.Footer,
	}
	logo, err := snapshot.ResolveLogo(brandingLogoPath(paths, cfg.Presentation.Branding.Logo))
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	branding.Logo = logo

	return snapshot.Build(snapshot.Input{
		Tasks:       tasks,
		Phases:      f.Phases,
		Events:      events,
		Spends:      spends,
		ProjectName: name,
		Language:    cfg.Dashboard.SummaryLanguage,
		// The flag is read here and nowhere else. `snapshot.Build` is what
		// leaves spend out when it is off, and `internal/report` renders
		// whatever the snapshot carries — one decision, one place.
		ShowSpend: cfg.Dashboard.ShowSpendToStakeholder,
		Branding:  branding,
		Now:       now,

		DoneInVelocityWindow: doneInWindow,
		PhaseTitles:          outline.Phases,
		PackageTitles:        outline.Packages,
		FinishedAt:           finishedAt,
	}), nil
}
