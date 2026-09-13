// Package providers holds one thin adapter per external coding-agent CLI.
//
// A provider knows three things and nothing else:
//
//   - how to build the argv for its CLI (Argv),
//   - how the prompt reaches that CLI (PromptDelivery),
//   - how to read a finished run's captured output back into a Result (Parse).
//
// It does not spawn processes, touch the filesystem, or record state — that is
// internal/engine's job (G2.5). Keeping the parsers pure is what lets every one
// of them be pinned against captured CLI bytes in testdata/ (checklist rule 21).
//
// Ported from orchestrator/dispatcher.py. Where this package and that file
// disagree, the Python is the specification and the divergence is written down
// both here and in docs/brainstorm/go-migration-notes.md.
package providers

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hectorcanaimero/orch/internal/model"
)

// PromptDelivery says how a provider's CLI wants the prompt.
type PromptDelivery int

const (
	// PromptStdin pipes the prompt file's bytes into the child's stdin.
	// claude, codex and opencode all take the prompt this way.
	PromptStdin PromptDelivery = iota
	// PromptArg passes the prompt text as a CLI argument. gemini and agy
	// need this; neither is ported yet.
	PromptArg
)

// Request is everything a provider needs to build an argv for one dispatch.
//
// Python's `Backend.build_cmd(task, route)` takes the whole Task and reaches
// into module state for the rest (ClaudeBackend stores a config dict on the
// instance; CodexBackend writes a `__OUTPUT__` placeholder into the argv that
// `spawn` rewrites afterwards). A struct of plain values instead keeps Argv
// pure and keeps this package off internal/config (checklist rule 14, and the
// stricter rule in the G2.4 brief: providers import model only).
type Request struct {
	// TaskID is the id of the task being dispatched. Only used to name
	// per-dispatch artefacts; no provider puts it in the argv today.
	TaskID string

	// Route is the resolved model_router.yaml entry for this task.
	Route model.RouteEntry

	// Cwd is the absolute directory the CLI runs in. Providers that take an
	// explicit directory flag read it from here; the engine also passes it
	// to the child process itself.
	Cwd string

	// OutputPath is where a provider that writes a separate artefact file
	// should point its flag (codex's `-o`). The engine builds it with the
	// provider's own DefaultOutputPath helper. Empty means "no artefact".
	OutputPath string

	// PromptText is only read by providers whose PromptDelivery is
	// PromptArg. Providers on PromptStdin ignore it.
	PromptText string

	// SessionID correlates retries in the CLI's own session store. Only
	// claude takes one. nil means "let the CLI pick"; see ClaudeProvider.Argv.
	SessionID string

	// BudgetUSD is the per-dispatch spend cap to hand the CLI, when the
	// operator configured one. nil omits the flag entirely.
	BudgetUSD *float64
}

// Result is the terminal state of one dispatch. Ported from
// dispatcher.py's DispatchResult.
//
// Two of Python's fields are deliberately absent: `files_touched` (the
// dispatcher leaves it empty and the main loop fills it from `git diff`, so it
// belongs to the engine, not here) and `should_retry_with_fallback` (the reap
// loop derives it from Classify, so storing it invites the two disagreeing).
type Result struct {
	ExitCode  int
	Success   bool
	CostUSD   float64
	TokensIn  int
	TokensOut int

	// Stdout is the captured log — stdout and stderr merged, because that is
	// how the engine spawns children (dispatcher.py: `stderr=STDOUT`).
	Stdout string
	// Stderr is populated only when the engine captures stderr separately.
	// Both Python and the providers here leave it empty; Classify still
	// reads it so the engine can fill it in without touching this package.
	Stderr string

	// ErrorMessage is the operator-facing reason the dispatch failed, and is
	// the first thing Classify looks at. Python types it `str | None`; the
	// empty string is the None.
	ErrorMessage string

	// Estimated marks a run whose backend reported activity but no token
	// usage, so downstream spend rows can tell missing telemetry apart from
	// genuinely zero-cost work. Only opencode sets it (Issue #8).
	Estimated bool
}

// Provider is the adapter contract. One implementation per external CLI.
//
// The G2.4 brief sketched this as `Argv(prompt, route, budgetUSD) []string`
// and `Parse(stdout []byte) (Result, error)`; both changed to match what
// dispatcher.py actually does. See Request (argv inputs are not just a prompt,
// a route and a budget) and Parse (the exit code decides success, and a parse
// failure is a Result, not an error).
type Provider interface {
	// Name is the model_router.yaml `backend:` literal this adapter serves.
	Name() model.Backend

	// PromptDelivery says whether Argv expects Request.PromptText to be set.
	PromptDelivery() PromptDelivery

	// Argv returns the argv to exec. Pure: same Request, same argv. Never
	// contains shell metacharacters — the engine execs directly, no shell.
	Argv(req Request) []string

	// Parse turns a finished run's exit code and captured output into a
	// Result. Pure, total, and never returns an error: unreadable output is
	// itself a Result, carrying the ErrorMessage that Classify maps to
	// FailureParser. Cost and tokens are still extracted from output that
	// failed, because a failed run has usually already been paid for.
	Parse(exitCode int, output []byte) Result
}

// ErrNotPorted marks a backend the Python orch supports but the Go rewrite has
// not reached yet. Distinguished from an unknown name so the engine can tell
// "you asked for something that does not exist" from "not yet", and so a
// half-finished rewrite fails loudly rather than silently doing nothing.
//
// Empty since G3.4 ported the last four backends. Kept, with its test, because
// the distinction it draws is what the engine's error message depends on; a
// sixth backend added to model.Backend before its adapter exists lands here.
var ErrNotPorted = errors.New("provider not implemented yet")

// notPorted are the Python backends still awaiting their Go adapter.
var notPorted = map[model.Backend]bool{}

// Get returns the adapter for a router entry's backend.
//
// Python's get_backend raises ValueError for an unknown name; the split
// between ErrNotPorted and "unknown backend" is new, and exists only while the
// rewrite is mid-flight.
func Get(name model.Backend) (Provider, error) {
	switch name {
	case model.BackendClaude:
		return ClaudeProvider{}, nil
	case model.BackendCodex:
		return CodexProvider{}, nil
	case model.BackendOpencode:
		return OpencodeProvider{}, nil
	case model.BackendGemini:
		return GeminiProvider{}, nil
	case model.BackendAgy:
		return AgyProvider{}, nil
	}
	if notPorted[name] {
		return nil, fmt.Errorf("backend %q: %w", name, ErrNotPorted)
	}
	return nil, fmt.Errorf(
		"unknown backend %q; expected one of [claude codex opencode gemini agy]", name)
}

// NewSessionID mints the per-dispatch session id claude correlates retries by.
//
// Python generates this inside ClaudeBackend.build_cmd and then digs it back
// out of the argv with `_extract_session_id`. Go moves generation to the
// caller so Argv stays pure and testable; the engine calls this once per
// dispatch and puts the result in Request.SessionID.
func NewSessionID() string { return uuid.NewString() }
