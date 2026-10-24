package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/vcs"
	"github.com/hectorcanaimero/orch/internal/worktree"
)

// The operator's review of a task's work (#454): accept it, or send it back
// to its agent. Blocking is Move's. The CLI (`orch task accept`, `orch task
// note --retry`) and the dashboard's Changes tab go through these.

// EventPRMerged is the event an accepted task's merge is written as: the
// engine's own name for a PR merged outside the run.
const EventPRMerged = "pr_merged"

// EventMergedLocally is a task squash-merged into the base here, in place of
// a PR (vcs.provider: local, #455): by the run, or by the operator's Accept.
const EventMergedLocally = "merged_locally"

// CIFeedbackFile is where the failing CI logs and the verify's findings are
// left for the agent, inside its worktree; it is never committed. The name is
// Python's, and a contract with whatever the agent is told to read.
const CIFeedbackFile = ".orch-ci-feedback.md"

// ErrNoPR is Accept's answer for a task with no PR to merge.
var ErrNoPR = errors.New("the task has no PR to merge")

// Accept merges the task's work. With a forge (vcs.provider github or
// gitlab) that is its PR, squashed, now: the operator has read the change.
// Which merge it is depends on the provider, and only here.
func (s Steer) Accept(ctx context.Context, taskID string) (string, error) {
	t, err := s.Backend.Task(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", taskID, err)
	}
	if s.VCS.Provider == "local" {
		return s.acceptLocally(ctx, t)
	}
	if t.PRURL == "" {
		return "", fmt.Errorf("%s: %w", taskID, ErrNoPR)
	}
	p := s.Provider
	if p == nil {
		p = vcs.NewProvider(s.VCS)
	}
	if err := p.MergePR(t.PRURL, true, false); err != nil {
		return "", fmt.Errorf("merging %s: %w", t.PRURL, err)
	}
	if err := s.Backend.AppendEvent(ctx, "", state.Event{
		EventType: EventPRMerged,
		TaskID:    taskID,
		TS:        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Extra:     map[string]any{"pr_url": t.PRURL, "accepted_by": s.Source, "pid": os.Getpid()},
	}); err != nil {
		return "", fmt.Errorf("the PR is merged, but recording it failed: %w", err)
	}
	return fmt.Sprintf("%s accepted: %s merged", taskID, t.PRURL), nil
}

// acceptLocally lands the task as the run does in local mode (#455): its
// pending work committed, then orch/<id> squash-merged into the base with
// worktree.LandLocally. A conflict changes nothing: it is the operator's
// error, naming the files, and Send back hands them to the agent.
func (s Steer) acceptLocally(ctx context.Context, t state.TaskRuntime) (string, error) {
	if t.Status == model.StatusInProgress {
		return "", fmt.Errorf("%s is running: %w; stop it first", t.ID, ErrRunOwnsTask)
	}
	task := model.Task{ID: t.ID}
	if s.TasksJSON != "" {
		tf, err := model.LoadTasksFile(s.TasksJSON)
		if err != nil {
			return "", err
		}
		if i := slices.IndexFunc(tf.Tasks, func(d model.Task) bool { return d.ID == t.ID }); i >= 0 {
			task = tf.Tasks[i]
		}
	}
	base := s.BaseBranch
	if base == "" {
		base = "main"
	}
	m := worktree.NewManager(s.Root, false)
	if m.Exists(t.ID) {
		if err := os.Remove(filepath.Join(m.WorktreePath(t.ID), CIFeedbackFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("removing the feedback file: %w", err)
		}
		if _, err := m.CommitPending(t.ID, t.ID+": orch auto-commit"); err != nil {
			return "", fmt.Errorf("committing %s's pending work: %w", t.ID, err)
		}
	}
	title, body := PRText(task)
	res, err := m.LandLocally(t.ID, base, title+"\n\n"+body)
	if err != nil {
		return "", fmt.Errorf("merging %s into %s: %w", m.BranchName(t.ID), base, err)
	}
	if len(res.Conflicts) > 0 {
		return "", fmt.Errorf("%s conflicts with %s in %s; nothing was merged: send it back for its agent to resolve",
			m.BranchName(t.ID), base, strings.Join(res.Conflicts, ", "))
	}
	if t.Status != model.StatusDone {
		if err := s.Backend.Transition(ctx, t.ID, model.StatusDone, state.Note{Author: OperatorAuthor, Body: "accepted"}); err != nil {
			return "", fmt.Errorf("the work is on %s, but marking %s done failed: %w", base, t.ID, err)
		}
	}
	if err := s.Backend.AppendEvent(ctx, "", state.Event{
		EventType: EventMergedLocally,
		TaskID:    t.ID,
		TS:        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Extra:     map[string]any{"base": base, "commit": res.Commit, "accepted_by": s.Source, "pid": os.Getpid()},
	}); err != nil {
		return "", fmt.Errorf("the work is on %s, but recording it failed: %w", base, err)
	}
	if res.Commit == "" {
		return fmt.Sprintf("%s accepted: nothing to merge, %s already has its work", t.ID, base), nil
	}
	return fmt.Sprintf("%s accepted: merged into %s as %.7s", t.ID, base, res.Commit), nil
}

// PRText is a task's PR title and body, and its local squash commit's
// message: the run's and Accept's one spelling.
func PRText(task model.Task) (title, body string) {
	spec := task.SpecRef
	if spec == "" {
		spec = "n/a"
	}
	title = task.Title
	if title == "" {
		title = task.ID
	}
	return title, strings.TrimSpace(fmt.Sprintf("Task: `%s`\nSpec: %s\n\n%s", task.ID, spec, task.Reason))
}

// SendBack returns a finished or blocked task to its agent: the note reaches
// its next attempt's prompt, and the task goes back to todo for `orch run` to
// dispatch again.
func (s Steer) SendBack(ctx context.Context, taskID, note string) (StopResult, error) {
	if err := s.Note(ctx, taskID, note); err != nil {
		return StopResult{}, err
	}
	return s.Move(ctx, taskID, model.StatusTodo, "sent back by the operator")
}
