package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
)

// newRouterCmd is the `orch router` parent. `add-missing` ports Python's
// only subcommand (`orch.py`'s `_run_router_subcommand` dispatches nothing
// else); `validate` has no Python counterpart — `internal/router.Router.
// Validate` already exists (AS-06, checked before dispatch) and exposing it
// as its own subcommand lets an operator ask the question without needing a
// full `orch validate` run or a live dispatch to trigger it.
func newRouterCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "router",
		Short: "Inspect and maintain model_router.yaml",
	}
	cmd.AddCommand(newRouterValidateCmd(flags))
	cmd.AddCommand(newRouterAddMissingCmd(flags))
	return cmd
}

// routerOffenderJSON mirrors router.Offender with JSON tags — the router
// package's own type has none, since crossing the wire was never its job.
type routerOffenderJSON struct {
	TaskID string `json:"task_id"`
	Model  string `json:"model"`
}

// routerValidatePayload is `router validate --json`'s payload. New-in-Go
// shape (no Python source), kept consistent with `orch validate`'s: a
// project header, the offenders found (empty when clean), and an explicit
// `ok` rather than making the caller infer it from an empty array.
type routerValidatePayload struct {
	Project   validateProjectJSON  `json:"project"`
	Offenders []routerOffenderJSON `json:"offenders"`
	OK        bool                 `json:"ok"`
}

// newRouterValidateCmd checks that every tasks.json `model` resolves to a
// model_router.yaml entry, without loading a Backend or dispatching
// anything. Exit 0 clean, 2 any unrouted task (same convention `orch
// validate` uses for `route.unresolved`) or a project/router/tasks load
// failure.
func newRouterValidateCmd(flags *projectFlags) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check that every task.model resolves to a model_router.yaml entry",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveAndValidate(flags)
			if err != nil {
				return withExitCode(2, err)
			}
			rtr, err := router.Load(paths.RouterYAML())
			if err != nil {
				return withExitCode(2, fmt.Errorf("load %s: %w", paths.RouterYAML(), err))
			}
			tf, err := model.LoadTasksFile(paths.TasksJSON())
			if err != nil {
				return withExitCode(2, fmt.Errorf("load %s: %w", paths.TasksJSON(), err))
			}

			verr := rtr.Validate(tf.Tasks)
			offenders := []routerOffenderJSON{}
			var unrouted *router.UnroutedError
			if errors.As(verr, &unrouted) {
				for _, o := range unrouted.Offenders {
					offenders = append(offenders, routerOffenderJSON{TaskID: o.TaskID, Model: o.Model})
				}
			}
			payload := routerValidatePayload{
				Project:   validateProjectJSON{ID: paths.ID, Root: paths.Root},
				Offenders: offenders,
				OK:        verr == nil,
			}

			if asJSON {
				if err := printCompactJSON(cmd.OutOrStdout(), payload); err != nil {
					return err
				}
			} else if verr != nil {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), verr); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(cmd.OutOrStdout(),
				"model_router.yaml covers every task model."); err != nil {
				return err
			}

			if verr != nil {
				return withSilentExitCode(2)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the result as JSON")
	return cmd
}

// newRouterAddMissingCmd ports `orch router add-missing` (orchestrator/
// orch.py's `_run_router_add_missing_subcommand`): infer and append routes
// for every model tasks.json references that model_router.yaml lacks.
//
// Same plan/confirm/apply shape as Python, same exit codes (0 success or
// nothing to do, 1 aborted at the prompt, 2 missing/unparseable router or
// tasks file, or nothing inferable), same message wording where Python's is
// user-facing contract rather than incidental phrasing — `--yes` skips the
// prompt for scripted use exactly like Python's.
func newRouterAddMissingCmd(flags *projectFlags) *cobra.Command {
	var tier string
	var yes bool
	cmd := &cobra.Command{
		Use:   "add-missing",
		Short: "Append inferred model_router.yaml entries for every unrouted task model",
		RunE: func(cmd *cobra.Command, args []string) error {
			tierVal := model.Tier(tier)
			if tierVal != model.TierPremium && tierVal != model.TierStandard && tierVal != model.TierCheap {
				return withExitCode(2, fmt.Errorf(
					"--tier must be one of premium, standard, cheap (got %q)", tier))
			}

			paths, err := resolveAndValidate(flags)
			if err != nil {
				return withExitCode(2, err)
			}

			rtr, err := router.Load(paths.RouterYAML())
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					if _, err := fmt.Fprintf(cmd.ErrOrStderr(),
						"error: router file not found: %s\n", paths.RouterYAML()); err != nil {
						return err
					}
					if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Run `orch init` to scaffold it first."); err != nil {
						return err
					}
					return withSilentExitCode(2)
				}
				return withExitCode(2, err)
			}
			tf, err := model.LoadTasksFile(paths.TasksJSON())
			if err != nil {
				return withExitCode(2, fmt.Errorf("error: tasks file not found: %s: %w", paths.TasksJSON(), err))
			}

			out := cmd.OutOrStdout()
			missing := rtr.MissingModels(tf.Tasks)
			if len(missing) == 0 {
				_, err := fmt.Fprintln(out, "model_router.yaml already covers every task model. Nothing to add.")
				return err
			}

			taskCounts := map[string]int{}
			missingSet := make(map[string]bool, len(missing))
			for _, m := range missing {
				missingSet[m] = true
			}
			for _, t := range tf.Tasks {
				if missingSet[t.Model] {
					taskCounts[t.Model]++
				}
			}

			inferable := map[string]model.RouteEntry{}
			var skipped []string
			for _, m := range missing {
				entry, ierr := router.InferEntry(m, tierVal)
				if ierr != nil {
					skipped = append(skipped, m)
				} else {
					inferable[m] = entry
				}
			}

			totalTasks := 0
			for _, c := range taskCounts {
				totalTasks += c
			}
			if _, err := fmt.Fprintf(out, "Found %d missing model(s) across %d task(s):\n",
				len(missing), totalTasks); err != nil {
				return err
			}
			for _, m := range missing {
				if e, ok := inferable[m]; ok {
					if _, err := fmt.Fprintf(out, "  + %s  →  backend=%s, cli_model=%s, tier=%s  (%d task(s))\n",
						m, e.Backend, e.CLIModel, e.Tier, taskCounts[m]); err != nil {
						return err
					}
				}
			}
			for _, m := range skipped {
				if _, err := fmt.Fprintf(out,
					"  ! %s  →  cannot infer backend (no `backend/` prefix) — add by hand  (%d task(s))\n",
					m, taskCounts[m]); err != nil {
					return err
				}
			}

			if len(inferable) == 0 {
				_, err := fmt.Fprintln(cmd.ErrOrStderr(),
					"\nNothing can be auto-added — every missing model lacks a "+
						"recognizable `backend/` prefix.")
				if err != nil {
					return err
				}
				return withSilentExitCode(2)
			}

			if !yes {
				if _, err := fmt.Fprintf(out, "\nAppend %d entry(ies) to %s with tier '%s'? [y/N] ",
					len(inferable), paths.RouterYAML(), tier); err != nil {
					return err
				}
				reply, err := readLine(cmd.InOrStdin())
				if err != nil {
					return err
				}
				reply = strings.ToLower(strings.TrimSpace(reply))
				if reply != "y" && reply != "yes" {
					_, err := fmt.Fprintln(out, "Aborted. No changes written.")
					if err != nil {
						return err
					}
					return withSilentExitCode(1)
				}
			}

			added, stillSkipped, err := router.AddMissing(paths.RouterYAML(), missing, tierVal)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "\nAdded %d entry(ies) to %s.\n",
				len(added), paths.RouterYAML()); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out,
				"→ Review the `tier` of each new entry (defaulted to '%s') — "+
					"it drives budget + the semi gate.\n", tier); err != nil {
				return err
			}
			if len(stillSkipped) > 0 {
				if _, err := fmt.Fprintf(out,
					"→ %d model(s) still need manual entries (no backend prefix).\n",
					len(stillSkipped)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tier, "tier", "standard",
		"Tier assigned to every added entry (premium, standard, cheap)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

// readLine reads one line. An unattended stdin hitting EOF immediately
// (Python's `input()` catching EOFError) returns an empty string with no
// error — the caller treats that as "not yes", the same fail-closed
// behavior Python has. EOF after some text (no trailing newline on the
// last line) still returns that text, for the same reason: the reply was
// typed, it just wasn't followed by a newline before the pipe closed. Any
// other read error is real and gets reported rather than silently treated
// as an empty reply.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read confirmation: %w", err)
	}
	return line, nil
}
