package explain

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// Text renders the overview for a person: what this project is, where the DAG
// stands, what could run right now, what the guardrail thinks, and the
// commands that are safe to type next.
//
// It is deliberately about thirty lines. The point of `orch explain` is to be
// read in full by somebody who has just walked in — an operator returning to a
// project after a week, or an agent that has been handed a repository and no
// context. A screen that scrolls is a screen that gets skimmed, so anything
// that only matters once you have a question is left to the command that
// answers that question.
func (o Overview) Text(w io.Writer) error {
	var b strings.Builder

	fmt.Fprintf(&b, "Project %s\n", o.Project.ID)
	fmt.Fprintf(&b, "  root:  %s\n", o.Project.Root)
	fmt.Fprintf(&b, "  specs: %s\n", o.Project.SpecRoot)
	b.WriteString("\n")

	o.writeTasks(&b)
	b.WriteString("\n")
	o.writeReady(&b)
	b.WriteString("\n")
	o.writeBudget(&b)
	b.WriteString("\n")
	o.writeCommands(&b)

	_, err := io.WriteString(w, b.String())
	return err
}

func (o Overview) writeTasks(b *strings.Builder) {
	done := o.Project.Counts["done"]
	pct := 0
	if o.Project.Total > 0 {
		pct = done * 100 / o.Project.Total
	}
	fmt.Fprintf(b, "Tasks: %d total, %d done (%d%%)\n", o.Project.Total, done, pct)

	// In the order work moves through, not alphabetically: a reader scanning
	// this is following a task's life, and `backlog, blocked, done,
	// in-progress, todo` reads as a list of words rather than as a pipeline.
	for _, st := range []string{"backlog", "todo", "in-progress", "blocked", "done"} {
		n := o.Project.Counts[st]
		if n == 0 && st != "blocked" {
			continue
		}
		// `blocked` prints even at zero. "Nothing is blocked" is one of the
		// two things a person opens this to find out, and a line that
		// disappears when the answer is good makes them go looking.
		label := st
		if st == "blocked" && n > 0 {
			label = st + "  <-- needs a decision"
		}
		fmt.Fprintf(b, "  %-12s %s\n", label, pyfmt.Commas(int64(n)))
	}
}

// readyShown is how many ready tasks the text lists before summarising. Enough
// to choose from, few enough to keep the whole page on one screen.
const readyShown = 5

func (o Overview) writeReady(b *strings.Builder) {
	if len(o.Ready) == 0 {
		b.WriteString("Ready to dispatch: none\n")
		blocked := o.Project.Counts["blocked"]
		inProgress := o.Project.Counts["in-progress"]
		switch {
		case o.Project.Total == 0:
			b.WriteString("  (no tasks yet — write a spec and run `orch atomize`)\n")
		case o.Project.Total == o.Project.Counts["done"]:
			b.WriteString("  (everything is done)\n")
		case inProgress > 0:
			// Not "the rest are waiting on them": a task with an unfinished
			// dependency is waiting on THAT dependency, which may be any
			// not-done task and not the one in flight. Saying so would be
			// asserting a cause this has not checked.
			fmt.Fprintf(b, "  (%d in progress; everything else is waiting on a dependency)\n", inProgress)
		case blocked > 0:
			fmt.Fprintf(b, "  (%d blocked — `orch tasks --status blocked` says why)\n", blocked)
		default:
			b.WriteString("  (every remaining task is waiting on a dependency)\n")
		}
		return
	}

	fmt.Fprintf(b, "Ready to dispatch: %d\n", len(o.Ready))
	for i, t := range o.Ready {
		if i == readyShown {
			fmt.Fprintf(b, "  ... and %d more (`orch tasks --ready`)\n", len(o.Ready)-readyShown)
			break
		}
		title := t.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(b, "  %-10s %s\n", t.ID, title)
	}
}

func (o Overview) writeBudget(b *strings.Builder) {
	if !o.Budget.Enabled {
		b.WriteString("Budget: not configured (no budgets.yaml)\n")
		return
	}
	if len(o.Budget.Providers) == 0 {
		b.WriteString("Budget: configured, no providers\n")
		return
	}

	b.WriteString("Budget (rolling window):\n")
	names := make([]string, 0, len(o.Budget.Providers))
	for name := range o.Budget.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := o.Budget.Providers[name]
		state := ""
		if p.Capped {
			// The one line here that changes what somebody does next.
			state = "  CAPPED"
			if p.ResetAt != nil {
				state = "  CAPPED until " + *p.ResetAt
			}
		}
		fmt.Fprintf(b, "  %-10s %s / %s tokens (%d%%)%s\n",
			name, pyfmt.Commas(int64(p.TokensUsed)), pyfmt.Commas(int64(p.TokenBudget)),
			int(p.UsagePct), state)
	}
}

// writeCommands lists what is safe to type next.
//
// Safe means READ-ONLY: every command here answers a question and changes
// nothing. `orch run` is the obvious next step and is deliberately not in this
// list — it dispatches work to an AI CLI that edits files and opens PRs, which
// is not something to put under a heading somebody is skimming.
func (o Overview) writeCommands(b *strings.Builder) {
	b.WriteString("Safe to run (these only read):\n")
	b.WriteString("  orch status            what each task is doing\n")
	b.WriteString("  orch validate          is the DAG dispatchable\n")
	b.WriteString("  orch dashboard         the same thing in a browser\n")

	switch {
	case o.Project.Total == 0:
		b.WriteString("  orch atomize --file specs/<your-spec>.md\n" +
			"                         turn a spec into tasks (add --apply to write)\n")
	case o.Project.Counts["blocked"] > 0:
		b.WriteString("  orch tasks --status blocked\n" +
			"                         what is stuck, and the note that says why\n")
	case len(o.Ready) > 0:
		fmt.Fprintf(b, "  orch events %-10s the last thing that happened to it\n", o.Ready[0].ID)
	case o.Project.Counts["in-progress"] > 0:
		// Nothing to start and something in flight: the useful next question
		// is what that one is doing, not what to launch.
		b.WriteString("  orch tasks --status in-progress\n" +
			"                         what is running, and since when\n")
	}
}
