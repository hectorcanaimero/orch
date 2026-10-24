package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

// newTaskCmd is the `orch task` parent. `set` is Python's; `note` and `stop`
// steer a task from outside the run (#356).
func newTaskCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manual task mutations",
	}
	cmd.AddCommand(newTaskSetCmd(flags), newTaskAddCmd(flags), newTaskEditCmd(flags),
		newTaskNoteCmd(flags), newTaskStopCmd(flags), newTaskSalvageCmd(flags), newTaskAcceptCmd(flags))
	return cmd
}

// openSteer opens the project's backend for `orch task note|stop`.
func openSteer(cmd *cobra.Command, flags *projectFlags) (project.Steer, func() error, error) {
	paths, cfg, err := loadProjectConfig(flags)
	if err != nil {
		return project.Steer{}, nil, err
	}
	backend, closeDB, err := openBackend(cmd.Context(), paths, cfg)
	if err != nil {
		return project.Steer{}, nil, err
	}
	if err := backend.Bootstrap(cmd.Context(), loadDAG(paths)); err != nil {
		_ = closeDB()
		return project.Steer{}, nil, fmt.Errorf("bootstrap: %w", err)
	}
	return project.Steer{Backend: backend, StateDir: paths.StateDir(), Source: "cli",
		VCS:  vcs.Config{Provider: cfg.VCS.Provider, Host: cfg.VCS.Host},
		Root: paths.Root, BaseBranch: cfg.Dispatch.BaseBranch, TasksJSON: paths.TasksJSON()}, closeDB, nil
}

// steerErr maps the backend's errors onto `task set`'s exit codes.
func steerErr(id string, err error) error {
	switch {
	case errors.Is(err, state.ErrTaskNotFound):
		return withExitCode(1, fmt.Errorf("%s: %w", id, err))
	case errors.Is(err, state.ErrIllegalTransition):
		return withExitCode(3, fmt.Errorf("illegal transition: %w", err))
	}
	return err
}

func newTaskNoteCmd(flags *projectFlags) *cobra.Command {
	var id string
	var retry bool
	cmd := &cobra.Command{
		Use:   "note --id ID [--retry] TEXT",
		Short: "Leave a note for a task's agent; it reaches the prompt of the task's next attempt",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			steer, closeDB, err := openSteer(cmd, flags)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()
			if retry {
				res, err := steer.SendBack(cmd.Context(), id, strings.Join(args, " "))
				if err != nil {
					return steerErr(id, err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "task %s: sent back with the note (%s)\n", id, res.Message)
				return err
			}
			if err := steer.Note(cmd.Context(), id, strings.Join(args, " ")); err != nil {
				return steerErr(id, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "task %s: note added; its next attempt's prompt carries it\n", id)
			return err
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Task ID, e.g. F1.1.T3")
	cmd.Flags().BoolVar(&retry, "retry", false, "Also send the task back to todo, so orch run dispatches it again with the note")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// newTaskAcceptCmd is `orch task accept` (#454): merge a task's reviewed
// work — its PR, or with vcs.provider local its branch into the base — the
// way the dashboard's Changes tab does.
func newTaskAcceptCmd(flags *projectFlags) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "accept --id ID",
		Short: "Accept a task's work: squash-merge its PR now (vcs.provider local: its branch into the base)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			steer, closeDB, err := openSteer(cmd, flags)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()
			msg, err := steer.Accept(cmd.Context(), id)
			if err != nil {
				return steerErr(id, err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), msg)
			return err
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Task ID, e.g. F1.1.T3")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// stopWait is how long `orch task stop` waits for the run to answer.
var stopWait = 15 * time.Second

func newTaskStopCmd(flags *projectFlags) *cobra.Command {
	var id, note string
	var hold bool
	cmd := &cobra.Command{
		Use:   "stop --id ID [--hold] [--note TEXT]",
		Short: "End a running task's attempt: hold it, or dispatch it again with your note",
		Long: "Ask the running `orch run` to end the task's attempt now. With --hold the task\n" +
			"is blocked (\"held by the operator\") until `orch task set --status todo`;\n" +
			"without it the task is dispatched again at once, and the stopped attempt does\n" +
			"not count against max_attempts. --note reaches that next attempt's prompt.\n" +
			"With no `orch run` running it is a plain status change, and says so.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			steer, closeDB, err := openSteer(cmd, flags)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()
			res, err := steer.Stop(cmd.Context(), id, hold, note)
			if err != nil {
				return steerErr(id, err)
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintln(out, res.Message); err != nil || !res.Requested {
				return err
			}
			ev, ok, err := steer.WaitStopped(cmd.Context(), res, stopWait)
			if err != nil {
				return err
			}
			if !ok {
				_, err = fmt.Fprintf(out, "the run has not answered yet; `orch events %s` shows it when it does\n", id)
				return err
			}
			_, err = fmt.Fprintln(out, stopAnswer(id, ev))
			return err
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Task ID, e.g. F1.1.T3")
	cmd.Flags().BoolVar(&hold, "hold", false, "Keep the task from being dispatched again (blocked)")
	cmd.Flags().StringVar(&note, "note", "", "A note for the task's next attempt")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// stopAnswer says what the run did with a stop request.
func stopAnswer(id string, ev state.Event) string {
	hold, _ := ev.Extra["hold"].(bool)
	switch ev.Extra["stopped"] {
	case "attempt":
		if hold {
			return fmt.Sprintf("stopped: %s's attempt ended; the task is held (blocked) — `orch task set --id %s --status todo` releases it", id, id)
		}
		return fmt.Sprintf("stopped: %s's attempt ended; it is dispatched again", id)
	case "held":
		return fmt.Sprintf("%s was not running; it is held (blocked)", id)
	case "retry_now":
		return fmt.Sprintf("%s was waiting to retry; it is dispatched now", id)
	case "already_done":
		return fmt.Sprintf("%s is already done; nothing to stop", id)
	}
	return fmt.Sprintf("%s was not running; nothing to stop", id)
}

// newTaskSetCmd ports `orch task set` (orchestrator/orch.py's
// _run_task_set_subcommand). Only `--status` is wired to
// state.Backend.Transition today — `--model`/`--backend` would write to
// `tasks_definition`, and the Backend interface has no method for
// that yet (see go-migration-notes.md). The flags are still registered, so
// a caller gets a clear "not implemented" error instead of cobra's opaque
// "unknown flag" — the CLI surface matches Python's even where the
// implementation behind it doesn't yet.
//
// `--milestone` is gone rather than stubbed: a milestone is a phase (see
// snapshot.PhaseMilestones), which a task already carries in tasks.json, so
// there is nothing for the flag to ever write.
func newTaskSetCmd(flags *projectFlags) *cobra.Command {
	var id, status, modelFlag string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set a task's status, or its model",
		RunE: func(cmd *cobra.Command, args []string) error {
			if modelFlag == "" && status == "" {
				return withExitCode(1, errors.New("at least one of --model, --status is required"))
			}
			// The model is the task's definition, in tasks.json, changed the
			// way `task edit` changes it (#390). The route names the backend.
			if modelFlag != "" {
				e, closeDB, err := openTaskEditor(cmd, flags)
				if err != nil {
					return err
				}
				_, err = e.Update(cmd.Context(), id, project.TaskPatch{Model: &modelFlag})
				_ = closeDB()
				if err != nil {
					return editErr(id, err)
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "task %s: model -> %s\n", id, modelFlag); err != nil {
					return err
				}
				if status == "" {
					return nil
				}
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
			defer func() { _ = closeDB() }()
			if err := backend.Bootstrap(ctx, loadDAG(paths)); err != nil {
				return fmt.Errorf("bootstrap: %w", err)
			}

			st, err := model.ParseStatus(status)
			if err != nil {
				return withExitCode(1, err)
			}
			note := state.Note{Author: "operator", Body: "manual set via orch task set"}
			err = backend.Transition(ctx, id, st, note)
			switch {
			case err == nil:
				_, ferr := fmt.Fprintf(cmd.OutOrStdout(), "task %s: status -> %s\n", id, status)
				return ferr
			case errors.Is(err, state.ErrTaskNotFound):
				return withExitCode(1, fmt.Errorf("%s: %w", id, err))
			case errors.Is(err, state.ErrIllegalTransition):
				return withExitCode(3, fmt.Errorf("illegal transition: %w", err))
			default:
				return err
			}
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Task ID, e.g. F1.1.T3")
	cmd.Flags().StringVar(&modelFlag, "model", "", "Set the task's model in tasks.json: a model_router.yaml key, which names the backend")
	cmd.Flags().StringVar(&status, "status", "", "Set the task status (e.g. done, in-progress, blocked)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
