package cli

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
)

// validateProjectJSON is `validate --json`'s `project` object.
type validateProjectJSON struct {
	ID   string `json:"id"`
	Root string `json:"root"`
}

// validateSummaryJSON is `validate --json`'s `summary` object.
type validateSummaryJSON struct {
	Total    int           `json:"total"`
	ByKind   *orderedCount `json:"by_kind"`
	Errors   int           `json:"errors"`
	Warnings int           `json:"warnings"`
}

// validatePayload is the full `validate --json` payload, field for field
// against `_run_validate_subcommand`'s dict (orchestrator/orch.py) for the
// checks Go actually runs. See newValidateCmd's doc comment for what is
// deliberately NOT ported.
type validatePayload struct {
	Project  validateProjectJSON `json:"project"`
	Errors   []graph.Problem     `json:"errors"`
	Summary  validateSummaryJSON `json:"summary"`
	ExitCode int                 `json:"exit_code"`
}

// newValidateCmd ports `orch validate` (orchestrator/orch.py's
// _run_validate_subcommand) — static checks with no dispatch and no
// subprocess probes, so it works on a project that has never been run.
//
// Ported: config load, router load, tasks load — each tolerant, each
// producing its own error entry on failure rather than aborting — then
// `graph.Validate` (schema, dependencies, cycles, unresolved routes) once
// tasks parse. Same exit-code convention as Python: 0 clean, 2 any error
// (`graph.Validate` never emits a warning today, so 1 "warnings only" is
// currently unreachable in Go — see below).
//
// Deliberately NOT ported, all Python-only checks with no Go home yet:
// `preflight.validate_config_shape` (config.yaml key/type shape),
// `preflight.validate_preset_sanity` (budgets preset vs typical dispatch
// size — no internal/budget preset-sanity check exists), and
// `preflight.validate_files_writable` (the `--files` flag). `--files` is
// still registered, so passing it is a clear error rather than cobra's
// opaque "unknown flag" — matching the project's own precedent (`task set
// --model`). See docs/brainstorm/go-migration-notes.md.
func newValidateCmd(flags *projectFlags) *cobra.Command {
	var asJSON, checkFiles bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Static validation of tasks.json + routing (schema, deps, cycles, routes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkFiles {
				return fmt.Errorf(
					"--files is not implemented yet — preflight.validate_files_writable " +
						"has no Go port (see docs/brainstorm/go-migration-notes.md)")
			}

			paths, err := config.ResolvePaths(flags.root, flags.id, flags.configPath)
			if err != nil {
				return withExitCode(2, err)
			}

			var problems []graph.Problem

			if _, cfgErr := config.Load(paths.ConfigYAML, paths.Root); cfgErr != nil {
				problems = append(problems, graph.Problem{
					Field:    "config",
					Kind:     "schema.config",
					Message:  fmt.Sprintf("config load failed: %v", cfgErr),
					Severity: graph.SeverityError,
				})
			}

			var routerKeys []string
			rtr, routerErr := router.Load(paths.RouterYAML())
			switch {
			case routerErr == nil:
				routerKeys = rtr.Keys()
			case errors.Is(routerErr, os.ErrNotExist):
				problems = append(problems, graph.Problem{
					Field:       "model_router.yaml",
					Kind:        "router.missing",
					Message:     fmt.Sprintf("router file not found: %s", paths.RouterYAML()),
					Remediation: strPtr("Run `orch init` to scaffold, or create the file by hand."),
					Severity:    graph.SeverityError,
				})
			default:
				problems = append(problems, graph.Problem{
					Field:    "model_router.yaml",
					Kind:     "router.parse",
					Message:  fmt.Sprintf("router load failed: %v", routerErr),
					Severity: graph.SeverityError,
				})
			}

			tf, tasksErr := model.LoadTasksFile(paths.TasksJSON())
			switch {
			case tasksErr == nil:
				var routes []string
				if len(routerKeys) > 0 {
					routes = routerKeys
				}
				problems = append(problems, graph.Validate(tf.Tasks, routes)...)
			case errors.Is(tasksErr, os.ErrNotExist):
				problems = append(problems, graph.Problem{
					Field:       "tasks.json",
					Kind:        "tasks.missing",
					Message:     fmt.Sprintf("tasks file not found: %s", paths.TasksJSON()),
					Remediation: strPtr("Run `orch init` to scaffold, or create tasks.json by hand."),
					Severity:    graph.SeverityError,
				})
			default:
				problems = append(problems, graph.Problem{
					Field:    "tasks.json",
					Kind:     "tasks.parse",
					Message:  fmt.Sprintf("tasks load failed: %v", tasksErr),
					Severity: graph.SeverityError,
				})
			}

			payload := buildValidatePayload(paths, problems)

			if asJSON {
				if err := printCompactJSON(cmd.OutOrStdout(), payload); err != nil {
					return err
				}
			} else if err := renderValidateReport(cmd, payload); err != nil {
				return err
			}
			if payload.ExitCode != 0 {
				return withSilentExitCode(payload.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the full validation report as JSON on stdout")
	cmd.Flags().BoolVar(&checkFiles, "files", false,
		"Also check that parent dirs of each task.files[] entry exist + are writable (not implemented yet)")
	return cmd
}

// buildValidatePayload groups problems by kind and computes the exit code,
// mirroring Python's `exit_code_for_errors`: 0 none, 2 any "error" severity,
// 1 warnings only.
func buildValidatePayload(paths config.Paths, problems []graph.Problem) validatePayload {
	if problems == nil {
		problems = []graph.Problem{}
	}
	byKind := newOrderedCount()
	errCount, warnCount := 0, 0
	for _, p := range problems {
		byKind.Add(p.Kind, 1)
		if p.Severity == graph.SeverityError {
			errCount++
		} else {
			warnCount++
		}
	}
	exitCode := 0
	switch {
	case errCount > 0:
		exitCode = 2
	case warnCount > 0:
		exitCode = 1
	}
	return validatePayload{
		Project: validateProjectJSON{ID: paths.ID, Root: paths.Root},
		Errors:  problems,
		Summary: validateSummaryJSON{
			Total: len(problems), ByKind: byKind,
			Errors: errCount, Warnings: warnCount,
		},
		ExitCode: exitCode,
	}
}

// renderValidateReport is the human (non-JSON) renderer — plain tabular
// output, not a port of Python's rich table (no byte-parity requirement for
// human mode, same as every other command here).
func renderValidateReport(cmd *cobra.Command, payload validatePayload) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "orch validate · project=%s\n", payload.Project.ID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "%d error(s) · %d warning(s) · %d total\n",
		payload.Summary.Errors, payload.Summary.Warnings, payload.Summary.Total); err != nil {
		return err
	}
	if len(payload.Errors) == 0 {
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "SEVERITY\tKIND\tTASK\tFIELD\tMESSAGE"); err != nil {
		return err
	}
	for _, p := range payload.Errors {
		taskID := "—"
		if p.TaskID != nil {
			taskID = *p.TaskID
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			p.Severity, p.Kind, taskID, p.Field, p.Message); err != nil {
			return err
		}
	}
	return w.Flush()
}

// strPtr is the address of a string — Problem.Remediation is a pointer so
// "no remediation" crosses the wire as `null`, matching Python; a Go string
// literal isn't addressable directly, so this is the ergonomic way to take
// its address at a call site.
func strPtr(s string) *string { return &s }
