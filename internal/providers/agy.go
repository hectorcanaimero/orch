package providers

import (
	"fmt"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
)

// AgyProvider adapts the `agy` CLI (Antigravity). Port of dispatcher.py's
// AgyBackend.
//
// agy emits a single JSON object on stdout under `--output-format json`, not
// JSONL. It reports token counts but no USD figure, so cost is always zero and
// the dashboard's pricing.yaml estimates spend from the tokens.
type AgyProvider struct{}

func (AgyProvider) Name() model.Backend { return model.BackendAgy }

// PromptDelivery is PromptArg: the prompt is the value of `--print`.
func (AgyProvider) PromptDelivery() PromptDelivery { return PromptArg }

// agyDefaultAgent is the persona a route gets when it names none.
//
// Issue #88: agy's own default agent is the coordinator, which delegates to a
// sub-agent instead of executing in-process. It returns a "subagent launched"
// message, never calls task-finish.sh, and orch marks every dispatch failed.
// `executor` runs the prompt directly, which is what the orch protocol wants.
const agyDefaultAgent = "executor"

// Argv returns the argv for `agy --output-format json --agent <a> --model <m>
// [--effort <e>] --print <prompt>`.
//
// `--print` MUST stay last: its value is the prompt, parsed positionally, so
// any flag after it is swallowed into the prompt text.
//
// `--print-timeout` is deliberately absent — the engine supervises the child
// with its own SIGTERM/SIGKILL deadline, and a second timeout owned by the CLI
// would race it.
//
// `--effort` is emitted only when the route sets it (issue #87): base-name
// Gemini models require an effort tier, while suffix-baked ones
// (`gemini-3.7-flash-high`) reject it.
func (AgyProvider) Argv(req Request) []string {
	agent := agyDefaultAgent
	if req.Route.Agent != nil && *req.Route.Agent != "" {
		agent = *req.Route.Agent
	}

	argv := []string{
		"agy",
		"--output-format", "json",
		"--agent", agent,
		"--model", req.Route.CLIModel,
	}
	if req.Route.Effort != nil && *req.Route.Effort != "" {
		argv = append(argv, "--effort", *req.Route.Effort)
	}
	return append(argv, "--print", req.PromptText)
}

// Parse reads agy's JSON envelope. A run succeeds only when the process exited
// 0 AND the envelope says `status: "SUCCESS"`.
func (AgyProvider) Parse(exitCode int, output []byte) Result {
	text := string(output)
	payload, havePayload := agyEnvelope(output)

	status := asString(payload["status"])
	response := asString(payload["response"])
	var tokensIn, tokensOut, thinking int
	if usage, ok := asObject(payload["usage"]); ok {
		tokensIn = toInt(usage["input_tokens"])
		tokensOut = toInt(usage["output_tokens"])
		thinking = toInt(usage["thinking_tokens"])
	}

	success := exitCode == 0 && status == "SUCCESS"

	// Issue #86, thinking-mode absorption: a Gemini model with thinking on
	// can report SUCCESS while every emitted token went to the internal
	// reasoning buffer, leaving `response` empty. Left alone, orch retries
	// and escalates in silence, burning ~1.5k thinking tokens per attempt.
	const thinkingAbsorbed = "EMPTY_RESPONSE_THINKING_MODE"
	if success && strings.TrimSpace(response) == "" && thinking > 0 {
		success = false
		status = thinkingAbsorbed
	}

	var errMsg string
	if !success {
		switch {
		case status == thinkingAbsorbed:
			errMsg = fmt.Sprintf(
				"agy returned SUCCESS but response is empty — model emitted %d "+
					"thinking token(s) with no visible reply (thinking-mode "+
					"absorption). Use a non-thinking cli_model variant or lower "+
					"`effort:` on this route (see issue #86).", thinking)
		case status != "" && status != "SUCCESS":
			errMsg = "agy status=" + status
		case !havePayload && exitCode == 0:
			if last := lastNonEmptyLine(text); last != "" {
				errMsg = last
			} else {
				errMsg = "agy produced no parseable JSON output"
			}
		default:
			if last := lastNonEmptyLine(text); last != "" {
				errMsg = last
			} else {
				errMsg = fmt.Sprintf("agy exited with code %d", exitCode)
			}
		}
	}

	return Result{
		ExitCode:     exitCode,
		Success:      success,
		TokensIn:     tokensIn,
		TokensOut:    tokensOut,
		Stdout:       text,
		ErrorMessage: errMsg,
	}
}

// ExtractCost reports the token counts in a captured agy log; the USD figure
// is always zero because agy does not price the turn.
func (AgyProvider) ExtractCost(output []byte) (cost float64, tokensIn, tokensOut int) {
	payload, ok := agyEnvelope(output)
	if !ok {
		return 0, 0, 0
	}
	usage, ok := asObject(payload["usage"])
	if !ok {
		return 0, 0, 0
	}
	return 0, toInt(usage["input_tokens"]), toInt(usage["output_tokens"])
}

// agyEnvelope decodes agy's JSON object out of a captured run.
//
// Divergence from Python, deliberate. dispatcher.py runs `json.loads` over the
// whole log and gives up when that fails, which means any line agy prints
// before the envelope costs the parser everything: status, response and the
// token counts. That is not hypothetical — an unauthenticated agy 1.2.1 writes
// an OAuth prompt first and the envelope last
// (testdata/agy/1.2.1/auth-error.json), so Python reads status as absent and
// the usage numbers as zero on every run that ever printed a warning. Falling
// back to the last line that decodes as a JSON object is the same last-line
// rule parseClaudeEnvelope already applies. Logged as bug 29 in
// docs/brainstorm/go-migration-notes/sonnet-2.md.
func agyEnvelope(output []byte) (map[string]any, bool) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil, false
	}
	if obj, ok := decodeObject([]byte(trimmed)); ok {
		return obj, true
	}
	lines := strings.Split(trimmed, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if obj, ok := decodeObject([]byte(line)); ok {
			return obj, true
		}
	}
	return nil, false
}
