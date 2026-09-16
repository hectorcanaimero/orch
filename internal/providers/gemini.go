package providers

import (
	"fmt"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
)

// GeminiProvider adapts the `gemini` CLI. Port of dispatcher.py's
// GeminiBackend.
//
// gemini prints plain text: there is no structured envelope, so cost and both
// token counts are always zero and the exit code is the only success signal.
// Spend for a gemini route is therefore always estimated downstream from the
// dashboard's pricing.yaml, exactly as in Python.
type GeminiProvider struct{}

func (GeminiProvider) Name() model.Backend { return model.BackendGemini }

// PromptDelivery is PromptArg: gemini takes the prompt as the value of `-p`,
// and the engine closes the child's stdin.
func (GeminiProvider) PromptDelivery() PromptDelivery { return PromptArg }

// Argv returns the argv for `gemini -p <prompt> --model <model>`.
//
// There is no working-directory flag. Python sets `PWD` in the child's
// environment on top of the usual working directory; that is the engine's
// half of the contract, not the argv's.
func (GeminiProvider) Argv(req Request) []string {
	return []string{
		"gemini",
		"-p", req.PromptText,
		"--model", req.Route.CLIModel,
	}
}

// Parse reports success purely from the exit code, and quotes the last
// non-empty line as the failure reason.
//
// That last line is what carries the diagnosis in practice: an
// unauthenticated gemini 0.59.0 exits 41 with a single line naming the three
// environment variables it would accept
// (testdata/gemini/0.59.0/auth-error.log).
func (GeminiProvider) Parse(exitCode int, output []byte) Result {
	text := string(output)
	success := exitCode == 0

	var errMsg, answer string
	if success {
		answer = strings.TrimSpace(text)
	} else if last := lastNonEmptyLine(text); last != "" {
		errMsg = last
	} else {
		errMsg = fmt.Sprintf("gemini exited with code %d", exitCode)
	}

	return Result{
		ExitCode:     exitCode,
		Success:      success,
		Stdout:       text,
		Text:         answer,
		ErrorMessage: errMsg,
		// The zeros above are "not reported", not "free": the spend row says
		// so, and the budget gate weighs it as a typical dispatch.
		Estimated: true,
	}
}

// ExtractCost is always zero for gemini: the CLI reports no usage at all.
func (GeminiProvider) ExtractCost([]byte) (cost float64, tokensIn, tokensOut int) {
	return 0, 0, 0
}

// lastNonEmptyLine returns the final line of s that is not blank once
// trimmed, mirroring Python's `[ln.strip() for ln in text.splitlines() if
// ln.strip()][-1]`.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
