package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
)

// ReviewTaskID names the review's dispatch. Under ORCH_FAKE_PROVIDER it is the
// canned response's file name: <dir>/<backend>/ci-review.out.
const ReviewTaskID = "ci-review"

// readOnly says how each provider's argv is turned read-only for a review.
// The adapters' argv is built for an agent that edits a worktree; a review
// only reads its prompt. Each entry swaps the flag that grants tools for the
// one that withholds them, and the review runs in an empty scratch directory
// besides, so even a read tool finds nothing of the repository.
//
//   - claude: `--tools ""` offers no built-in tool and `--strict-mcp-config`
//     no MCP server; the permission mode goes back to the default.
//   - codex: the read-only sandbox instead of `--approve-for-me`'s
//     workspace-write one.
//   - opencode: the built-in `plan` agent, which may not edit or run
//     commands, instead of `--auto`'s approve-everything.
//   - gemini: `--approval-mode plan` is its read-only mode, and `--skip-trust`
//     trusts the empty scratch directory, which gemini otherwise refuses to
//     run in (exit 55). The prompt goes on stdin, which gemini prepends to
//     `-p`: a diff near --max-diff-bytes would not fit in one argument.
var readOnly = map[model.Backend]struct{ grant, instead []string }{
	model.BackendClaude: {
		grant:   []string{"--permission-mode", "acceptEdits"},
		instead: []string{"--permission-mode", "default", "--tools", "", "--strict-mcp-config"},
	},
	model.BackendCodex: {
		grant:   []string{"--approve-for-me"},
		instead: []string{"--sandbox", "read-only"},
	},
	model.BackendOpencode: {
		grant:   []string{"--auto"},
		instead: []string{"--agent", "plan"},
	},
	model.BackendGemini: {
		instead: []string{"--approval-mode", "plan", "--skip-trust"},
	},
}

// ReviewBackends are the providers `orch ci review` runs.
var ReviewBackends = []model.Backend{
	model.BackendClaude, model.BackendGemini, model.BackendCodex, model.BackendOpencode,
}

// geminiPrompt is gemini's `-p`, appended to the real prompt it reads on stdin.
const geminiPrompt = "Answer the review request above."

// ReadOnlyArgv is the provider's own argv with its tool grant swapped for the
// read-only flags in readOnly. An adapter whose argv no longer has the flag
// being swapped is an error rather than a review that silently keeps it.
func ReadOnlyArgv(p providers.Provider, req providers.Request) ([]string, error) {
	rule, ok := readOnly[p.Name()]
	if !ok {
		return nil, fmt.Errorf("orch ci review does not run %s", p.Name())
	}
	argv := p.Argv(req)
	if rule.grant == nil {
		return append(argv, rule.instead...), nil
	}
	for i := 0; i+len(rule.grant) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(rule.grant)], rule.grant) {
			return slices.Concat(argv[:i], rule.instead, argv[i+len(rule.grant):]), nil
		}
	}
	return nil, fmt.Errorf("the %s argv no longer contains %q, so orch ci review cannot make it read-only",
		p.Name(), strings.Join(rule.grant, " "))
}

// fixedArgv runs a provider with an argv computed beforehand, prompt on stdin.
type fixedArgv struct {
	providers.Provider
	argv []string
}

func (f fixedArgv) Argv(providers.Request) []string { return f.argv }

func (fixedArgv) PromptDelivery() providers.PromptDelivery { return providers.PromptStdin }

// Ask runs one provider on prompt, read-only, and returns its final answer.
// It uses the engine's own spawn and supervision (a process group, the
// timeout's SIGTERM then SIGKILL), and so honours ORCH_FAKE_PROVIDER.
func Ask(ctx context.Context, backend model.Backend, cliModel, prompt string, timeout time.Duration) (answer string, err error) {
	p, err := providers.Get(backend)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "orch-ci-review-")
	if err != nil {
		return "", fmt.Errorf("creating a scratch directory: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil && err == nil {
			err = fmt.Errorf("removing the scratch directory: %w", rmErr)
		}
	}()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		return "", fmt.Errorf("creating a scratch directory: %w", err)
	}
	promptPath := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return "", fmt.Errorf("writing the prompt: %w", err)
	}

	req := providers.Request{
		TaskID:     ReviewTaskID,
		Route:      model.RouteEntry{Backend: backend, CLIModel: cliModel},
		Cwd:        work,
		OutputPath: filepath.Join(dir, "last-message.md"),
		PromptText: geminiPrompt,
	}
	argv, err := ReadOnlyArgv(p, req)
	if err != nil {
		return "", err
	}
	out, err := engine.Run(ctx, engine.Dispatch{
		Provider:   fixedArgv{Provider: p, argv: argv},
		Req:        req,
		PromptPath: promptPath,
		LogPath:    filepath.Join(dir, "review.log"),
		Cwd:        work,
		Timeout:    timeout,
	})
	if err != nil {
		return "", fmt.Errorf("running %s: %w", backend, err)
	}
	if !out.Result.Success {
		return "", fmt.Errorf("%s failed: %s", backend, out.Result.ErrorMessage)
	}
	if strings.TrimSpace(out.Result.Text) == "" {
		return "", fmt.Errorf("%s ran but gave no answer", backend)
	}
	return out.Result.Text, nil
}

// gitTimeout bounds `git diff`, which reads only the local clone.
const gitTimeout = 2 * time.Minute

// Diff is `git diff --merge-base <base>` in dir: what the pull request
// changes, against the point it branched from.
func Diff(ctx context.Context, dir, base string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	// #nosec G204 -- fixed git subcommand; base is a ref from a flag or
	// GITHUB_BASE_REF, passed as one argument before `--`, never to a shell.
	cmd := exec.CommandContext(ctx, "git", "diff", "--merge-base", base, "--")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff --merge-base %s: %w: %s", base, err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// PRFromEvent reads the pull request number from a GitHub Actions event
// payload, the file GITHUB_EVENT_PATH names.
func PRFromEvent(path string) (int, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the event file the Actions runner names
	if err != nil {
		return 0, fmt.Errorf("reading the event payload: %w", err)
	}
	var ev struct {
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		return 0, fmt.Errorf("reading the event payload %s: %w", path, err)
	}
	if ev.PullRequest.Number == 0 {
		return 0, errors.New("the event payload has no pull_request.number: run on a pull_request event, or pass --pr")
	}
	return ev.PullRequest.Number, nil
}
