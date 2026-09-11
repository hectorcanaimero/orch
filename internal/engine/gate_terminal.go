package engine

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// TerminalGate is the real semi-mode gate: it prints a summary and blocks on
// one keystroke. Port of checkpoints.py's SemiModeGate.prompt_operator.
//
// It is the only thing in the engine that reads stdin, which is why it is
// behind the Gate interface and in its own file — the scheduler never has to
// care that a human exists.
type TerminalGate struct {
	In  io.Reader
	Out io.Writer
}

// Ask shows the task and decodes the answer.
//
//	y            → dispatch
//	Enter or n   → defer   (offered again on the next run)
//	s            → skip    (blocked permanently)
//	q            → quit    (drain what is in flight, then stop)
//
// Anything else re-prompts, because an operator can fat-finger a key and the
// wrong default here blocks a task or ends a run. End of input is read as
// quit, matching Python's handling of EOF and Ctrl-C: a gate with nobody at
// the keyboard must drain cleanly rather than dispatch unattended.
func (g TerminalGate) Ask(task model.Task, reason string) Decision {
	files := "[]"
	if len(task.Files) > 0 {
		files = strings.Join(task.Files, ", ")
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "\n=== SEMI GATE — %s [phase %d] ===\n", task.ID, task.Phase)
	fmt.Fprintf(&summary, "  %s\n", task.Title)
	fmt.Fprintf(&summary, "  model: %s\n", task.Model)
	fmt.Fprintf(&summary, "  files: %s\n", files)
	fmt.Fprintf(&summary, "  est: %sh\n", pyfmt.Float(task.EstimateHours))
	fmt.Fprintf(&summary, "  reason: %s\n", reason)

	// Write errors are ignored throughout: the operator's terminal is the
	// only destination, there is nothing useful to do when it cannot be
	// written to, and failing a dispatch decision over a broken stdout would
	// be worse than the broken stdout.
	_, _ = io.WriteString(g.Out, summary.String())

	scanner := bufio.NewScanner(g.In)
	for {
		_, _ = io.WriteString(g.Out, "[y]dispatch / [N]defer / [s]skip-permanently / [q]uit > ")
		if !scanner.Scan() {
			// EOF or a read error: nobody is answering. Python treats EOF
			// and Ctrl-C the same way — drain cleanly rather than dispatch
			// unattended.
			_, _ = io.WriteString(g.Out, "\n")
			return DecisionQuit
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "y":
			return DecisionDispatch
		case "", "n":
			return DecisionDefer
		case "s":
			return DecisionSkip
		case "q":
			return DecisionQuit
		}
	}
}
