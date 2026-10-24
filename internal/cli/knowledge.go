package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/knowledge"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newKnowledgeCmd wires `orch knowledge` — the project's lessons under
// .orch/knowledge/ (#451), for the operator.
func newKnowledgeCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "knowledge",
		Short: "The project's lessons (.orch/knowledge/) that later tasks get in their prompt",
	}
	root := func() (string, error) {
		paths, err := resolveAndValidate(flags)
		return paths.Root, err
	}

	var l knowledge.Lesson
	add := &cobra.Command{
		Use:   "add --title TEXT --body TEXT|- [--area PATH]... [--tags TAG]... [--task ID] [--source operator]",
		Short: "Write a lesson as a file under .orch/knowledge/; commit it like any other file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := root()
			if err != nil {
				return err
			}
			if l.Body == "-" {
				raw, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read the body from stdin: %w", err)
				}
				l.Body = string(raw)
			}
			written, err := knowledge.Add(dir, l, time.Now())
			if err != nil {
				return withExitCode(2, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", written.Path)
			return err
		},
	}
	add.Flags().StringVar(&l.Title, "title", "", "One line a later task would search for")
	add.Flags().StringVar(&l.Body, "body", "", "What happened and what worked, a few lines; - reads it from stdin")
	add.Flags().StringSliceVar(&l.Area, "area", nil, "Paths or globs it applies to (repeatable)")
	add.Flags().StringSliceVar(&l.Tags, "tags", nil, "Keywords (repeatable)")
	add.Flags().StringVar(&l.Task, "task", "", "The task it came from")
	add.Flags().StringVar(&l.Source, "source", "operator", "Who learned it: "+strings.Join(knowledge.Sources, " | "))

	var asJSON bool
	list := &cobra.Command{
		Use:   "list [--json]",
		Short: "List the lessons: id, source, area, title",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := root()
			if err != nil {
				return err
			}
			lessons, err := knowledge.Load(dir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(append([]knowledge.Lesson{}, lessons...))
			}
			if len(lessons) == 0 {
				_, err := fmt.Fprintln(out, "no lessons yet (.orch/knowledge/ is empty)")
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "ID\tSOURCE\tAREA\tTITLE")
			for _, l := range lessons {
				area := strings.Join(l.Area, ",")
				if area == "" {
					area = "-"
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", l.ID, l.Source, area, l.Title)
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "One JSON array of lessons")

	show := &cobra.Command{
		Use:   "show ID",
		Short: "Print one lesson's file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := root()
			if err != nil {
				return err
			}
			lessons, err := knowledge.Load(dir)
			if err != nil {
				return err
			}
			for _, l := range lessons {
				if l.ID == args[0] {
					l.Path = ""
					_, err := cmd.OutOrStdout().Write(knowledge.Marshal(l))
					return err
				}
			}
			return withExitCode(2, fmt.Errorf("no lesson %q in %s (orch knowledge list shows them)", args[0], knowledge.Dir))
		},
	}

	rm := &cobra.Command{
		Use:   "rm ID",
		Short: "Delete a lesson's file; commit the deletion like any other change",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := root()
			if err != nil {
				return err
			}
			if err := knowledge.Remove(dir, args[0]); err != nil {
				return withExitCode(2, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return err
		},
	}

	var statsJSON bool
	stats := &cobra.Command{
		Use:   "stats [--json]",
		Short: "Lessons by source, and first-try success and retries per task before and since the first lesson",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			lessons, err := knowledge.Load(paths.Root)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(cmd.Context(), paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()
			done, err := backend.Tasks(cmd.Context(), state.TaskFilter{Statuses: []model.Status{model.StatusDone}})
			if err != nil {
				return err
			}
			s := knowledgeStats(lessons, done)
			if statsJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(s)
			}
			return printKnowledgeStats(cmd.OutOrStdout(), s)
		},
	}
	stats.Flags().BoolVar(&statsJSON, "json", false, "One JSON object")

	cmd.AddCommand(add, list, show, rm, stats, newKnowledgeCurateCmd(flags))
	return cmd
}

type taskOutcomes struct {
	Tasks         int     `json:"tasks"`
	FirstTry      int     `json:"first_try"`
	FirstTryRate  float64 `json:"first_try_rate"`
	RetriesPerTsk float64 `json:"retries_per_task"`
}

type knowledgeStatsOut struct {
	Lessons     int            `json:"lessons"`
	BySource    map[string]int `json:"by_source"`
	FirstLesson string         `json:"first_lesson,omitempty"`
	Before      taskOutcomes   `json:"before"`
	Since       taskOutcomes   `json:"since"`
}

// knowledgeStats splits the done tasks at the first lesson's date: a task
// that finished on its first dispatch with no CI retry is a first-try
// success, and every further dispatch or CI retry is a retry.
func knowledgeStats(lessons []knowledge.Lesson, done []state.TaskRuntime) knowledgeStatsOut {
	out := knowledgeStatsOut{Lessons: len(lessons), BySource: map[string]int{}}
	var first time.Time
	for _, l := range lessons {
		out.BySource[l.Source]++
		if t, err := time.Parse("2006-01-02", l.Created); err == nil && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	if !first.IsZero() {
		out.FirstLesson = first.Format("2006-01-02")
	}
	retries := [2]int{}
	for _, t := range done {
		finished, ok := state.ParseTS(t.FinishedAt)
		bucket, i := &out.Before, 0
		if !first.IsZero() && ok && !finished.Before(first) {
			bucket, i = &out.Since, 1
		}
		bucket.Tasks++
		r := max(t.Attempts-1, 0) + t.CIAttempts
		if r == 0 {
			bucket.FirstTry++
		}
		retries[i] += r
	}
	for i, b := range []*taskOutcomes{&out.Before, &out.Since} {
		if b.Tasks > 0 {
			b.FirstTryRate = float64(b.FirstTry) / float64(b.Tasks)
			b.RetriesPerTsk = float64(retries[i]) / float64(b.Tasks)
		}
	}
	return out
}

func printKnowledgeStats(w io.Writer, s knowledgeStatsOut) error {
	var src []string
	for _, name := range knowledge.Sources {
		if n := s.BySource[name]; n > 0 {
			src = append(src, fmt.Sprintf("%s %d", name, n))
		}
	}
	first := "no lessons yet"
	if s.FirstLesson != "" {
		first = "first lesson " + s.FirstLesson
	}
	_, _ = fmt.Fprintf(w, "lessons: %d (%s); %s\n\n", s.Lessons, strings.Join(src, ", "), first)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DONE TASKS\tTASKS\tFIRST TRY\tRETRIES/TASK")
	for _, row := range []struct {
		name string
		o    taskOutcomes
	}{{"before lessons", s.Before}, {"since lessons", s.Since}} {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%.0f%%\t%.2f\n", row.name, row.o.Tasks, 100*row.o.FirstTryRate, row.o.RetriesPerTsk)
	}
	return tw.Flush()
}
