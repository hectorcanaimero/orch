package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/export"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
)

func newExportClickUpCmd(flags *projectFlags) *cobra.Command {
	var (
		dryRun    bool
		folder    string
		phases    []int
		only      string
		statusMap []string
		tokenFile string
		language  string
	)

	cmd := &cobra.Command{
		Use:   "clickup",
		Short: "Mirror the DAG into a ClickUp Folder: a List per phase, a task per task, statuses kept in sync",
		Long: "Mirror this project into one ClickUp Folder through ClickUp's REST API: a List\n" +
			"per phase and a task per orch task, with a readable description (what to do,\n" +
			"how we know it is done, dependencies, model, estimate, files), its time\n" +
			"estimate, ClickUp dependencies, and the custom fields it finds by name\n" +
			"(orch ID, Model/Modelo, Package/Paquete, Cost USD/Costo USD).\n\n" +
			"Re-running it is how the mirror stays current: it creates what is missing,\n" +
			"moves each task to orch's status with a comment saying what happened, refreshes\n" +
			"those fields and adds missing dependencies. It never deletes anything and never\n" +
			"writes to tasks.json or the state database.\n\n" +
			"The token is a ClickUp personal API token, read from --token-file or $CLICKUP_TOKEN.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if strings.TrimSpace(folder) == "" {
				return withExitCode(2, errors.New("export: --folder is required (the ClickUp Folder id, from its URL)"))
			}
			statuses, err := parseStatusMap(statusMap)
			if err != nil {
				return withExitCode(2, err)
			}
			token, err := clickUpToken(tokenFile)
			if err != nil {
				return withExitCode(2, err)
			}

			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()

			// Hydrated (rule 30): the status is exactly what gets mirrored.
			tasks, err := project.Load(ctx, backend, paths.TasksJSON())
			if err != nil {
				return err
			}
			outline, err := project.SpecOutline(paths.Root, cfg.SpecRoot, tasks)
			if err != nil {
				return fmt.Errorf("export: %w", err)
			}
			plan, err := export.PlanClickUp(paths.ID, cfg.SpecRoot, tasks,
				export.Selection{Phases: phases, Only: only}, outline)
			if err != nil {
				return withExitCode(2, err)
			}
			plan.TimeoutMultiplier = cfg.DefaultTimeoutMult
			plan.Language = language
			if plan.Language == "" {
				plan.Language = cfg.Dashboard.SummaryLanguage
			}
			spend, err := backend.AllSpend(ctx, time.Time{})
			if err != nil {
				return err
			}
			plan.Spend = export.SumSpend(spend)

			out := cmd.OutOrStdout()
			if plan.Empty() {
				_, err := fmt.Fprintf(out, "Nothing to export from project %s.\n", paths.ID)
				return err
			}

			dest := export.ClickUp{
				FolderID: folder,
				Client:   export.NewClickUpClient(os.Getenv(export.ClickUpAPIURLEnv), token),
				Statuses: statuses,
				DryRun:   dryRun,
			}
			if dryRun {
				_, _ = fmt.Fprintf(out, "Dry run: reading ClickUp, changing nothing.\n\n")
			}
			sum, syncErr := dest.Sync(ctx, plan, func(a export.Action) { printClickUpAction(out, a, dryRun) })
			if err := printClickUpSummary(out, sum, dryRun); err != nil {
				return err
			}
			if syncErr != nil {
				// What was done stays and is printed above; the next run finds
				// every created task again by its marker and continues.
				return fmt.Errorf("%w (re-running continues from here)", syncErr)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&folder, "folder", "", "ClickUp Folder id to mirror into (required; it is in the Folder's URL)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Read ClickUp and print what would change, changing nothing")
	cmd.Flags().IntSliceVar(&phases, "phase", nil, "Mirror only these phases (repeatable, or comma-separated)")
	cmd.Flags().StringVar(&only, "only", "", "Mirror only tasks whose id matches this glob, like `orch run --only`")
	cmd.Flags().StringSliceVar(&statusMap, "status-map", nil,
		"Map an orch status to a ClickUp status name, e.g. done=closed (repeatable; defaults: backlog=backlog, "+
			"todo=to do, in-progress=in progress, blocked=blocked, done=complete)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "File holding the ClickUp personal API token (default: $CLICKUP_TOKEN)")
	cmd.Flags().StringVar(&language, "language", "", "Language of descriptions and comments: en, es or pt (default: dashboard.summary_language)")
	return cmd
}

func parseStatusMap(pairs []string) (map[model.Status]string, error) {
	out := make(map[model.Status]string, len(export.DefaultClickUpStatuses))
	for k, v := range export.DefaultClickUpStatuses {
		out[k] = v
	}
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("export: --status-map %q: want <orch status>=<ClickUp status>", pair)
		}
		st, err := model.ParseStatus(strings.ReplaceAll(strings.TrimSpace(k), "_", "-"))
		if err != nil {
			return nil, fmt.Errorf("export: --status-map %q: %w", pair, err)
		}
		out[st] = strings.TrimSpace(v)
	}
	return out, nil
}

func clickUpToken(file string) (string, error) {
	if file != "" {
		data, err := os.ReadFile(file) // #nosec G304 -- a path the operator passed.
		if err != nil {
			return "", fmt.Errorf("export: reading --token-file: %w", err)
		}
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
		return "", fmt.Errorf("export: --token-file %s is empty", file)
	}
	if tok := strings.TrimSpace(os.Getenv(export.ClickUpTokenEnv)); tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("export: no ClickUp token — create a personal one (ClickUp → Settings → Apps → API Token) "+
		"and pass --token-file or set $%s", export.ClickUpTokenEnv)
}

func printClickUpAction(w io.Writer, a export.Action, dryRun bool) {
	would := ""
	if dryRun {
		would = "would "
	}
	switch a.Kind {
	case export.ActionCreateList:
		_, _ = fmt.Fprintf(w, "%screate List  %s\n", would, a.Detail)
	case export.ActionCreateTask:
		_, _ = fmt.Fprintf(w, "%screate       %-12s status %q\n", would, a.TaskID, a.Detail)
	case export.ActionMoveStatus:
		_, _ = fmt.Fprintf(w, "%smove         %-12s %s\n", would, a.TaskID, a.Detail)
	case export.ActionSetField:
		_, _ = fmt.Fprintf(w, "%sset field    %-12s %s\n", would, a.TaskID, a.Detail)
	case export.ActionDependency:
		_, _ = fmt.Fprintf(w, "%sadd dep      %-12s waits on %s\n", would, a.TaskID, a.Detail)
	}
}

func printClickUpSummary(w io.Writer, s export.Summary, dryRun bool) error {
	verb := "Mirrored into ClickUp"
	if dryRun {
		verb = "Would mirror into ClickUp"
	}
	_, err := fmt.Fprintf(w, "\n%s: %d List(s) created, %d task(s) created, %d status change(s), %d field update(s), %d dependency(ies) added, %d task(s) already up to date.\n",
		verb, s.ListsCreated, s.TasksCreated, s.StatusesMoved, s.FieldsSet, s.DependenciesAdded, s.Unchanged)
	return err
}
