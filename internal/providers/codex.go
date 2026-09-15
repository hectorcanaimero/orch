package providers

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
)

// CodexProvider adapts the `codex` CLI. Port of dispatcher.py's CodexBackend.
//
// codex >= 0.148 streams JSONL events to **stdout** under `--json`; the `-o`
// file holds only the last message, in markdown. So Parse reads the captured
// log, not the artefact file — dispatcher.py says the same in a comment above
// CodexBackend.wait_result, and the G2.4 brief's "`exec --json -o` with JSONL"
// describes the older schema.
//
// A run succeeds when the process exited 0, no event is an error, and the last
// `turn.*` event is `turn.completed`. The pre-0.148 schema (`task_complete`,
// `step_finish`) is gone and is not accepted.
type CodexProvider struct{}

func (CodexProvider) Name() model.Backend { return model.BackendCodex }

func (CodexProvider) PromptDelivery() PromptDelivery { return PromptStdin }

// Argv returns the argv for `codex exec --skip-git-repo-check --json ...`.
//
// `-C .` is the literal dot, as with claude's `--add-dir`: the engine sets the
// child's working directory.
//
// `-s` / `--sandbox` is deliberately absent. `--approve-for-me` already
// implies the workspace-write sandbox, and codex >= 0.148 rejects the two
// together ("cannot be used with --approve-for-me").
//
// Python writes a `__OUTPUT__<task id>.codex.json` placeholder here that its
// spawn step rewrites in place. Go takes the resolved path in the Request
// instead; DefaultOutputPath builds the value Python's spawn would have
// produced.
func (CodexProvider) Argv(req Request) []string {
	return []string{
		"codex",
		"exec",
		"--skip-git-repo-check",
		"--json",
		"-o", req.OutputPath,
		"-C", ".",
		"--approve-for-me",
		"-m", req.Route.CLIModel,
	}
}

// DefaultOutputPath is where codex's `-o` artefact goes: beside the dispatch
// log, one file per dispatch. logsDir is the engine's `state/logs`.
func (CodexProvider) DefaultOutputPath(logsDir, taskID string) string {
	return filepath.Join(logsDir, taskID+".codex.json")
}

// codexNonFatalWarnings are messages codex reports as `item.type == "error"`
// that are not failures — the model carries on normally afterwards. Matched as
// substrings, case-sensitively, as in Python.
var codexNonFatalWarnings = []string{
	"Skill descriptions were shortened",
}

// Parse reads the JSONL event stream from a finished run's captured output.
func (CodexProvider) Parse(exitCode int, output []byte) Result {
	text := string(output)
	events := jsonlEvents(output)

	hasError := false
	for _, ev := range events {
		if isCodexError(ev) {
			hasError = true
			break
		}
	}

	// The terminal event is the last `turn.*`: turn.completed passes,
	// turn.failed fails, and neither present also fails.
	terminalOK := false
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].eventType() {
		case "turn.completed":
			terminalOK = true
		case "turn.failed":
			terminalOK = false
		default:
			continue
		}
		break
	}

	success := exitCode == 0 && !hasError && terminalOK
	cost, tokensIn, tokensOut := sumCodexUsage(events)

	var errMsg string
	if !success {
		errMsg = codexFailureMessage(events, exitCode)
	}

	return Result{
		ExitCode:     exitCode,
		Success:      success,
		CostUSD:      cost,
		TokensIn:     tokensIn,
		TokensOut:    tokensOut,
		Stdout:       text,
		Text:         codexText(events),
		ErrorMessage: errMsg,
	}
}

// codexText is the text of the last `item.completed` whose item is an
// `agent_message`: the answer codex also writes to its `-o` file.
func codexText(events []event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].eventType() != "item.completed" {
			continue
		}
		if item, ok := asObject(events[i].obj["item"]); ok && asString(item["type"]) == "agent_message" {
			return asString(item["text"])
		}
	}
	return ""
}

// ExtractCost reports the token counts in a captured codex log. Port of
// extract_cost; see sumCodexUsage on why the USD figure is always zero.
func (CodexProvider) ExtractCost(output []byte) (cost float64, tokensIn, tokensOut int) {
	return sumCodexUsage(jsonlEvents(output))
}

func codexFailureMessage(events []event, exitCode int) string {
	for _, ev := range events {
		if isCodexError(ev) {
			return codexErrorMessage(ev)
		}
	}
	if len(events) == 0 {
		return "codex produced no JSONL events"
	}
	return fmt.Sprintf("codex terminal event was not success (exit=%d)", exitCode)
}

// isCodexError reports whether an event is a fatal codex error: a top-level
// `type: "error"`, or an `item.completed` whose item is itself an error (that
// is how model_not_found arrives). Known non-fatal warnings are excluded.
func isCodexError(ev event) bool {
	switch ev.eventType() {
	case "error":
		return !isCodexNonFatalWarning(pyStr(ev.obj["message"]))
	case "item.completed":
		item, ok := asObject(ev.obj["item"])
		if !ok || asString(item["type"]) != "error" {
			return false
		}
		return !isCodexNonFatalWarning(pyStr(item["message"]))
	default:
		return false
	}
}

func isCodexNonFatalWarning(message string) bool {
	for _, w := range codexNonFatalWarnings {
		if strings.Contains(message, w) {
			return true
		}
	}
	return false
}

// codexErrorMessage renders a readable message from a codex error event.
//
// Python's final fallback is `str(ev)` — a CPython dict repr. This returns the
// raw JSONL line instead: it is what the CLI actually wrote, it is stable, and
// nothing keys on the string (Classify matches substrings that a dict repr
// would not contain either way). Noted in
// docs/brainstorm/go-migration-notes.md.
func codexErrorMessage(ev event) string {
	if item, ok := asObject(ev.obj["item"]); ok {
		if msg := item["message"]; pyTruthy(msg) {
			return pyStr(msg)
		}
	}
	if msg := ev.obj["message"]; pyTruthy(msg) {
		return pyStr(msg)
	}
	if errObj, ok := asObject(ev.obj["error"]); ok {
		if msg := errObj["message"]; pyTruthy(msg) {
			return pyStr(msg)
		}
		return pyStr(errObj)
	}
	if err := ev.obj["error"]; pyTruthy(err) {
		return pyStr(err)
	}
	return ev.raw
}

// sumCodexUsage sums the token counts on every `turn.completed` event.
//
// cost is always 0: codex's JSONL reports tokens but no USD figure, so the
// per-dispatch budget gate does not apply to it. Estimating a price from
// tokens would need the model's rate card, which lives in the dashboard's
// pricing.yaml — the same arrangement Python leaves a note about.
func sumCodexUsage(events []event) (cost float64, tokensIn, tokensOut int) {
	for _, ev := range events {
		if ev.eventType() != "turn.completed" {
			continue
		}
		usage, ok := asObject(ev.obj["usage"])
		if !ok {
			continue
		}
		tokensIn += toInt(usage["input_tokens"])
		tokensOut += toInt(usage["output_tokens"])
	}
	return 0, tokensIn, tokensOut
}
