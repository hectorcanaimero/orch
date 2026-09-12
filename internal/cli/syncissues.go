package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/atomize"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

// DefaultIssuesLabel is the label `orch sync issues` reads when neither the
// flag nor config.yaml names one.
//
// Namespaced on purpose: a project's tracker already uses `bug`,
// `enhancement` and the rest for its own triage, and orch must not claim one
// of those. `orch:task` says who it belongs to.
const DefaultIssuesLabel = config.DefaultSyncIssuesLabel

// autoReportedLabel is the label orch puts on issues it FILES. Ingesting it
// would close a loop — orch turning its own bug reports into tasks for itself
// — so it is refused even when passed by hand. See errAutoReportedLabel.
const autoReportedLabel = "auto-reported"

// issueTaskIDPrefix makes a task id from an issue number.
//
// Derived from the number rather than the title: the id is what makes a second
// sync idempotent, and a title is edited all the time while a number never
// changes.
const issueTaskIDPrefix = "gh-"

// issueTaskID is the task id for an issue.
func issueTaskID(number int) string { return fmt.Sprintf("%s%d", issueTaskIDPrefix, number) }

// taskFromIssue maps one issue onto a task.
//
// Pure, and everything it cannot know is explicit rather than guessed:
//
//   - `phase` is 0. An issue tracker has no ordering, and inventing phases
//     from labels would be a convention nobody agreed to.
//   - `dependencies` is empty. Same reason, and a wrong dependency is worse
//     than none — it stops a task from ever becoming ready.
//   - `estimateHours` is 0, which the timeout falls back to a 60-second floor
//     for (engine.TimeoutFor). A made-up estimate would drive a real timeout.
//   - `model` comes from the caller, and is required. A task whose model
//     `model_router.yaml` cannot resolve makes the next `orch validate` fail
//     in a block — that is bug 14, and syncing one in would be reintroducing
//     it deliberately.
//
// The issue URL goes in the FIRST LINE of the description, not in `specRef`:
// `specRef` is joined to `spec_root` and handed to an agent as a file to read
// (bug 12 is what happens when that path is wrong), and a URL is not a file.
// The first line is where the dispatch prompt puts the description, so the
// agent sees the link before the text.
func taskFromIssue(issue vcs.Issue, modelName string) model.Task {
	description := issue.URL
	if body := strings.TrimSpace(issue.Body); body != "" {
		description += "\n\n" + body
	}
	return model.Task{
		ID:           issueTaskID(issue.Number),
		Phase:        0,
		Title:        issue.Title,
		Description:  description,
		Model:        modelName,
		Status:       model.StatusTodo,
		Dependencies: []string{},
		Files:        []string{},
	}
}

// syncPlan is what a sync would do, worked out before anything is written.
//
// Computed in full first so `--dry-run` and a real run print the same thing,
// and so a refusal (an unroutable model, say) happens before tasks.json is
// touched rather than half way through it.
type syncPlan struct {
	// Add are the tasks that would be appended, in issue-number order.
	Add []model.Task
	// SkippedExisting are the ids already in tasks.json, which a sync never
	// overwrites — a task edited locally (a real estimate, a dependency, a
	// spec ref) must survive the next sync.
	SkippedExisting []string
}

func (p syncPlan) empty() bool { return len(p.Add) == 0 }

// planSync works out the additions without writing anything.
func planSync(issues []vcs.Issue, existing []model.Task, modelName string) syncPlan {
	have := make(map[string]bool, len(existing))
	for _, t := range existing {
		have[t.ID] = true
	}

	var plan syncPlan
	sorted := append([]vcs.Issue(nil), issues...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })

	for _, issue := range sorted {
		id := issueTaskID(issue.Number)
		if have[id] {
			plan.SkippedExisting = append(plan.SkippedExisting, id)
			continue
		}
		plan.Add = append(plan.Add, taskFromIssue(issue, modelName))
	}
	return plan
}

// errAutoReportedLabel explains the loop rather than just refusing.
//
// `auto-reported` is what orch puts on issues IT files. Syncing it would mean
// orch turning its own bug reports into tasks for itself, which produces work
// nobody asked for and grows every time it runs. Refused even when passed by
// hand, because the person passing it is the one who has not seen the loop.
var errAutoReportedLabel = fmt.Errorf(
	"--label %s is refused: that label marks issues orch itself files, so "+
		"syncing it would turn orch's own reports into tasks for orch, and "+
		"every run would add more. Use a label a human puts on, e.g. %s",
	autoReportedLabel, DefaultIssuesLabel)

// newSyncCmd wires `orch sync`, a parent for the ingest verbs.
//
// **New in Go.** Python has no issues verb at all — this is not a port, and
// saying so matters: there is no Python behaviour to be faithful to and no
// parity to diff, so every decision here is ours and is written down rather
// than inherited.
func newSyncCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Bring work in from elsewhere",
		Long: "Bring work into tasks.json from somewhere else.\n\n" +
			"Read-only towards the source: `sync` never writes to the tracker.",
	}
	cmd.AddCommand(newSyncIssuesCmd(flags))
	return cmd
}

func newSyncIssuesCmd(flags *projectFlags) *cobra.Command {
	var (
		label     string
		modelName string
		state     string
		limit     int
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "issues",
		Short: "Turn labelled GitHub issues into tasks",
		Long: "Append a task per labelled GitHub issue to tasks.json.\n\n" +
			"Idempotent: a task id is `gh-<issue number>`, so a second run adds\n" +
			"only what is new and never overwrites a task you have edited.\n\n" +
			"Never writes to GitHub.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}

			if label == "" {
				label = cfg.Sync.IssuesLabel
			}
			if label == "" {
				// Reachable: config.yaml can carry `sync.issues_label: ""`,
				// which decodes over the factory default and wins. Name the
				// key, since that is where the emptiness came from.
				return withExitCode(2, fmt.Errorf(
					"no issue label to sync: pass --label, or set sync.issues_label in %s",
					paths.ConfigYAML))
			}
			if label == autoReportedLabel {
				return withExitCode(2, errAutoReportedLabel)
			}

			// The model is checked against the project's own router BEFORE
			// anything is read or written. A task whose model the router
			// cannot resolve makes the next `orch validate` fail in a block
			// (bug 14); refusing here, with the list to copy from, is the
			// difference between a clear error now and a broken project
			// later.
			// router.Load, not loadRouterTolerant: the tolerant loader turns
			// a malformed model_router.yaml into an empty one, which here
			// would report "this project resolves no models" about a file
			// that is full of them. Everywhere else that fallback degrades a
			// display column; here it is the gate, so the parse error is the
			// answer.
			rtr, err := router.Load(paths.RouterYAML())
			if err != nil {
				return withExitCode(2, err)
			}
			if err := checkSyncModel(modelName, rtr.Keys()); err != nil {
				return withExitCode(2, err)
			}

			tasksFile, err := model.LoadTasksFile(paths.TasksJSON())
			if err != nil {
				return fmt.Errorf("read %s: %w", paths.TasksJSON(), err)
			}

			issues, err := vcs.NewGitHubProvider().ListIssues(vcs.IssueQuery{
				Label: label, State: state, Limit: limit, Dir: paths.Root,
			})
			if err != nil {
				return err
			}

			plan := planSync(issues, tasksFile.Tasks, modelName)
			printSyncPlan(cmd, plan, label, dryRun)
			if dryRun || plan.empty() {
				return nil
			}

			// atomize.Apply, not model.SaveTasksFile: it writes the
			// `tasks.json.bak-<ts>` first and then renames into place. This
			// appends to a file a human maintains by hand, so the backup is
			// the same promise `orch atomize --apply` already makes.
			tasksFile.Tasks = append(tasksFile.Tasks, plan.Add...)
			backupPath, err := atomize.Apply(paths.TasksJSON(), tasksFile, true)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "\nWrote %d task(s) to %s\n",
				len(plan.Add), paths.TasksJSON()); err != nil {
				return err
			}
			if backupPath != "" {
				_, err = fmt.Fprintf(out, "Backup: %s\n", backupPath)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&label, "label", "",
		"Issue label to ingest (default: sync.issues_label, else "+DefaultIssuesLabel+")")
	cmd.Flags().StringVar(&modelName, "model", "",
		"Model for the created tasks; must be one model_router.yaml resolves (required)")
	cmd.Flags().StringVar(&state, "state", "open",
		"Which issues: open, closed or all")
	cmd.Flags().IntVar(&limit, "limit", 0,
		"Maximum issues to read (default 500)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print what would be written and write nothing")
	return cmd
}

// checkSyncModel refuses a missing or unroutable model, listing what would
// work.
//
// The list is the point. "unknown model" sends an operator to open
// model_router.yaml; the names they can copy save that trip, and there are
// rarely more than a handful.
func checkSyncModel(modelName string, known []string) error {
	known = append([]string(nil), known...) // sorted for the message, not in place
	sort.Strings(known)
	available := "this project's model_router.yaml resolves none — run `orch router add-missing --yes` first"
	if len(known) > 0 {
		available = "models this project resolves: " + strings.Join(known, ", ")
	}
	if strings.TrimSpace(modelName) == "" {
		return fmt.Errorf("--model is required: a task whose model the router "+
			"cannot resolve breaks the next `orch validate`. %s", available)
	}
	for _, k := range known {
		if k == modelName {
			return nil
		}
	}
	return fmt.Errorf("model %q is not in this project's model_router.yaml, so "+
		"the tasks would break the next `orch validate`. %s", modelName, available)
}

// printSyncPlan is what the operator reads, and it is the same text whether
// the run writes or not — `--dry-run` differs by not writing, not by
// reporting differently.
func printSyncPlan(cmd *cobra.Command, plan syncPlan, label string, dryRun bool) {
	out := cmd.OutOrStdout()
	say := func(format string, args ...any) {
		_, _ = fmt.Fprintf(out, format+"\n", args...)
	}

	verb := "Adding"
	if dryRun {
		verb = "Would add"
	}
	if plan.empty() {
		say("No new issues labelled %q — nothing to add.", label)
	} else {
		say("%s %d task(s) from issues labelled %q:", verb, len(plan.Add), label)
		for _, t := range plan.Add {
			say("  %-10s %s", t.ID, firstLineOf(t.Title))
		}
	}
	if n := len(plan.SkippedExisting); n > 0 {
		say("Leaving %d task(s) already in tasks.json alone: %s",
			n, strings.Join(plan.SkippedExisting, ", "))
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
