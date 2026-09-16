package providers

import (
	"fmt"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// ClaudeProvider adapts the `claude` CLI. Port of dispatcher.py's
// ClaudeBackend.
//
// The CLI emits one JSON envelope on stdout under `--output-format json`.
// A run counts as a success only when the process exited 0 AND the envelope
// says `is_error: false` AND `subtype: "success"` — all three, because 2.1.269
// will happily exit non-zero with `subtype: "success"` on an API error (see
// testdata/claude/2.1.269/unrecognized-model.log).
type ClaudeProvider struct{}

func (ClaudeProvider) Name() model.Backend { return model.BackendClaude }

func (ClaudeProvider) PromptDelivery() PromptDelivery { return PromptStdin }

// Argv returns the argv for `claude -p --output-format json ...`.
//
// The order is the Python's, flag for flag. `--add-dir .` is the literal dot:
// the engine passes the real directory as the child's working directory, and
// the flag grants explicit write access to it as defence in depth.
//
// Two departures from build_cmd, both because Argv is pure:
//
//   - The session id comes from the Request instead of being minted here and
//     dug back out of the argv afterwards. An empty SessionID omits the flag
//     and lets the CLI pick its own, so a caller that forgets loses retry
//     correlation rather than emitting a broken `--session-id ""`.
//   - The budget comes from the Request instead of a config dict stored on the
//     adapter. Python looks for `claude_max_budget_usd` and then
//     `budget.per_dispatch_usd`; resolving which one wins is the engine's job
//     now, and this package never sees internal/config.
func (ClaudeProvider) Argv(req Request) []string {
	argv := []string{
		"claude",
		"-p",
		"--output-format", "json",
		"--model", req.Route.CLIModel,
	}
	if req.SessionID != "" {
		argv = append(argv, "--session-id", req.SessionID)
	}
	argv = append(argv,
		"--add-dir", ".",
		"--permission-mode", "acceptEdits",
	)
	if req.Settings != "" {
		argv = append(argv, "--settings", req.Settings)
	}
	if req.BudgetUSD != nil {
		argv = append(argv, "--max-budget-usd", pyfmt.Float(*req.BudgetUSD))
	}
	return argv
}

// Parse reads the JSON envelope out of a finished run's captured output.
func (c ClaudeProvider) Parse(exitCode int, output []byte) Result {
	text := string(output)

	env, ok := parseClaudeEnvelope(output)
	if !ok {
		return Result{
			ExitCode:     exitCode,
			Success:      false,
			Stdout:       text,
			ErrorMessage: "could not parse claude JSON envelope",
		}
	}

	isError := pyTruthy(env["is_error"])
	subtype := asString(env["subtype"])
	success := exitCode == 0 && !isError && subtype == "success"

	cost, tokensIn, tokensOut := claudeCost(env)

	var errMsg string
	if !success {
		errMsg = claudeErrorMessage(env, isError)
	}

	return Result{
		ExitCode:          exitCode,
		Success:           success,
		CostUSD:           cost,
		TokensIn:          tokensIn,
		TokensOut:         tokensOut,
		Stdout:            text,
		Text:              asString(env["result"]),
		ErrorMessage:      errMsg,
		PermissionDenials: claudeDenials(env),
	}
}

// claudeDenials reads `permission_denials`: one object per refused call, with
// `tool_name` and the `tool_input` it was refused. A Bash call names its
// command, because "Bash" alone does not say what to allow.
func claudeDenials(env map[string]any) []string {
	list, _ := env["permission_denials"].([]any)
	var out []string
	for _, item := range list {
		d, ok := asObject(item)
		if !ok {
			continue
		}
		name := asString(d["tool_name"])
		if input, ok := asObject(d["tool_input"]); ok {
			if cmd := asString(input["command"]); cmd != "" {
				name += "(" + cmd + ")"
			}
		}
		out = append(out, name)
	}
	return out
}

// claudeErrorMessage ports the `err = api_error_status or terminal_reason or ""`
// chain. api_error_status is an object on some failures (a code/message pair)
// and a bare HTTP status number on others — 2.1.269 sends `404` for an
// unrecognised model, and the long-standing auth fixture sends an object.
func claudeErrorMessage(env map[string]any, isError bool) string {
	var err any
	switch {
	case pyTruthy(env["api_error_status"]):
		err = env["api_error_status"]
	case pyTruthy(env["terminal_reason"]):
		err = env["terminal_reason"]
	default:
		err = ""
	}

	if obj, ok := asObject(err); ok {
		if msg := obj["message"]; pyTruthy(msg) {
			return pyStr(msg)
		}
		return pyStr(obj)
	}

	if s := pyStr(err); s != "" {
		return s
	}
	// Python formats the bool here, so the string reads "is_error=True".
	return fmt.Sprintf("claude reported is_error=%s", pyStr(isError))
}

// claudeCost pulls `total_cost_usd` and the `usage` token counts out of the
// envelope. Port of ClaudeBackend.extract_cost, which re-parses the text;
// taking the already-decoded envelope saves a second pass and cannot disagree
// with the one Parse used.
//
// # Divergence: cache tokens are input
//
// Python read `usage.input_tokens` alone. With prompt caching that is the
// handful of tokens after the last cache breakpoint: the 2.1.269 capture
// reports 19 of the 60,132 input tokens the request processed, the rest being
// `cache_creation_input_tokens` and `cache_read_input_tokens`. The budget
// window sums these rows, so it saw a few hundred tokens per dispatch and the
// claude cap practically never tripped. All three count.
func claudeCost(env map[string]any) (cost float64, tokensIn, tokensOut int) {
	cost = toFloat(env["total_cost_usd"])
	usage, ok := asObject(env["usage"])
	if !ok {
		return cost, 0, 0
	}
	tokensIn = toInt(usage["input_tokens"]) +
		toInt(usage["cache_creation_input_tokens"]) +
		toInt(usage["cache_read_input_tokens"])
	return cost, tokensIn, toInt(usage["output_tokens"])
}

// ExtractCost reports the spend and token counts in a captured claude log,
// without deciding whether the run succeeded. Port of extract_cost, kept
// exported because the engine's budget gate reads a log it did not parse.
func (c ClaudeProvider) ExtractCost(output []byte) (cost float64, tokensIn, tokensOut int) {
	env, ok := parseClaudeEnvelope(output)
	if !ok {
		return 0, 0, 0
	}
	return claudeCost(env)
}

// parseClaudeEnvelope extracts the top-level JSON object from claude's output.
//
// The whole capture is the envelope on a clean run. When the CLI also wrote to
// stderr — which the engine merges into the same stream — the blob as a whole
// is not JSON, and the envelope is the last line that parses. Port of
// _parse_claude_envelope, including that fallback.
func parseClaudeEnvelope(output []byte) (map[string]any, bool) {
	stripped := strings.TrimSpace(string(output))
	if stripped == "" {
		return nil, false
	}
	if obj, ok := decodeObject([]byte(stripped)); ok {
		return obj, true
	}
	lines := strings.Split(stripped, "\n")
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
