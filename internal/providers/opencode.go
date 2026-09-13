package providers

import (
	"fmt"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
)

// OpencodeProvider adapts the `opencode` CLI. Port of dispatcher.py's
// OpencodeBackend.
//
// opencode streams JSONL events to stdout under `--format json`. A run
// succeeds when the process exited 0, no event is a `type:"error"`, and the
// last `step_finish` carries a reason outside opencodeFailureReasons — any
// other reason ("stop", "length", provider-specific values) is a completed
// run.
type OpencodeProvider struct{}

func (OpencodeProvider) Name() model.Backend { return model.BackendOpencode }

func (OpencodeProvider) PromptDelivery() PromptDelivery { return PromptStdin }

// Argv returns the argv for `opencode run --format json --auto --dir <abs>`.
//
// `--dir` takes Request.Cwd rather than the literal dot claude and codex use:
// opencode resolves file writes against $PWD or its own project detection and
// ignores the child's working directory, so the value has to be absolute.
//
// Route.CLIModel goes in verbatim, prefix included. Router entries embed the
// provider prefix themselves (`google/gemini-2.5-pro`); there is no separate
// provider field to join. See the note in
// docs/brainstorm/go-migration-notes/sonnet-2.md about that prefix not
// resolving against opencode 1.18.30 on an unauthenticated machine.
func (OpencodeProvider) Argv(req Request) []string {
	return []string{
		"opencode",
		"run",
		"--format", "json",
		"--model", req.Route.CLIModel,
		"--auto",
		"--dir", req.Cwd,
	}
}

// opencodeFailureReasons are the terminal `step_finish` reasons that mean the
// run did not complete. Ordered as Python's `sorted(...)` renders them, because
// the failure message quotes the set.
var opencodeFailureReasons = []string{"aborted", "canceled", "cancelled", "error"}

func isOpencodeFailureReason(reason string) bool {
	for _, r := range opencodeFailureReasons {
		if reason == r {
			return true
		}
	}
	return false
}

// Parse reads the JSONL event stream from a finished run's captured output.
func (OpencodeProvider) Parse(exitCode int, output []byte) Result {
	text := string(output)
	events := jsonlEvents(output)

	hasError := false
	var errEvent *event
	for i := range events {
		if events[i].eventType() == "error" {
			hasError = true
			errEvent = &events[i]
			break
		}
	}

	// The terminal event is the last step_finish. Absent entirely, the run
	// failed: Python distinguishes that from a failure reason in its message.
	terminalOK := false
	haveTerminal := false
	terminalReason := ""
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].eventType() != "step_finish" {
			continue
		}
		haveTerminal = true
		terminalReason = asString(stepFinishPayload(events[i])["reason"])
		terminalOK = !isOpencodeFailureReason(terminalReason)
		break
	}

	success := exitCode == 0 && !hasError && terminalOK
	cost, tokensIn, tokensOut := sumStepFinishCosts(events)

	// Issue #8: a stream with step_finish events but no usage numbers is a
	// provider that does not report tokens, not a run that did no work.
	// Marking it estimated is what keeps the budget guardrail from reading
	// free work into a zero spend. Python also warns once per (backend,
	// model) here; the warning is the engine's to emit, since a pure parser
	// has nowhere to keep the "once" and no logger to write to.
	estimated := hasAnyStepFinish(events) && tokensIn == 0 && tokensOut == 0

	var errMsg string
	if !success {
		switch {
		case errEvent != nil:
			errMsg = opencodeErrorMessage(*errEvent)
		case len(events) == 0:
			errMsg = "opencode produced no JSONL events"
		case !haveTerminal:
			errMsg = fmt.Sprintf(
				"opencode did not emit a terminal step_finish event (exit=%d)",
				exitCode)
		default:
			errMsg = fmt.Sprintf(
				"opencode step_finish reason=%q is in failure set [%s] (exit=%d)",
				terminalReason, strings.Join(opencodeFailureReasons, " "), exitCode)
		}
	}

	return Result{
		ExitCode:     exitCode,
		Success:      success,
		CostUSD:      cost,
		TokensIn:     tokensIn,
		TokensOut:    tokensOut,
		Stdout:       text,
		ErrorMessage: errMsg,
		Estimated:    estimated,
	}
}

// ExtractCost reports cost and token counts in a captured opencode log.
func (OpencodeProvider) ExtractCost(output []byte) (cost float64, tokensIn, tokensOut int) {
	return sumStepFinishCosts(jsonlEvents(output))
}

// opencodeErrorMessage renders the operator-facing reason from an error event.
//
// Divergence from Python, deliberate. dispatcher.py reads `error.message` and
// falls back to `str(error)` — a CPython dict repr of the whole object. Real
// opencode 1.18.30 puts the human sentence one level deeper, in
// `error.data.message` (testdata/opencode/1.18.30/unknown-model.json), so
// Python's first branch never hits and every opencode error reaches the
// operator as a dict repr. Reading `data.message` first fixes that; the
// Python-shaped `message` is still honoured, and the raw line is the last
// resort for the same reason codexErrorMessage uses it. Logged as bug 28 in
// docs/brainstorm/go-migration-notes/sonnet-2.md.
func opencodeErrorMessage(ev event) string {
	errObj, ok := asObject(ev.obj["error"])
	if !ok {
		if err := ev.obj["error"]; pyTruthy(err) {
			return pyStr(err)
		}
		return ev.raw
	}
	if data, ok := asObject(errObj["data"]); ok {
		if msg := data["message"]; pyTruthy(msg) {
			return pyStr(msg)
		}
	}
	if msg := errObj["message"]; pyTruthy(msg) {
		return pyStr(msg)
	}
	return pyStr(errObj)
}

// stepFinishPayload returns the level of a step_finish event that holds the
// payload keys, tolerating the schema drift Python documents.
//
// Real opencode wraps `cost`, `tokens` and `reason` in an inner `part` object;
// older fixtures put them at the top level. `part` wins when both exist.
func stepFinishPayload(ev event) map[string]any {
	part, ok := asObject(ev.obj["part"])
	if !ok {
		return ev.obj
	}
	for _, k := range []string{"cost", "tokens", "reason"} {
		if _, present := part[k]; present {
			return part
		}
	}
	return ev.obj
}

// sumStepFinishCosts sums cost and token counts over every step_finish event.
//
// Python shares this helper between codex and opencode; in Go only opencode
// uses it, because codex 0.154 reports usage on `turn.completed` instead (see
// sumCodexUsage).
func sumStepFinishCosts(events []event) (cost float64, tokensIn, tokensOut int) {
	for _, ev := range events {
		if ev.eventType() != "step_finish" {
			continue
		}
		payload := stepFinishPayload(ev)
		cost += toFloat(payload["cost"])
		tokens, ok := asObject(payload["tokens"])
		if !ok {
			continue
		}
		tokensIn += toInt(tokens["input"])
		tokensOut += toInt(tokens["output"])
	}
	return cost, tokensIn, tokensOut
}

func hasAnyStepFinish(events []event) bool {
	for _, ev := range events {
		if ev.eventType() == "step_finish" {
			return true
		}
	}
	return false
}
