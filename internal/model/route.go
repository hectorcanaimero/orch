package model

// Backend names a supported CLI coding agent. Ported from the Python
// `Backend` literal in orchestrator/models.py.
type Backend string

const (
	BackendClaude   Backend = "claude"
	BackendCodex    Backend = "codex"
	BackendOpencode Backend = "opencode"
	BackendGemini   Backend = "gemini"
	BackendAgy      Backend = "agy"
)

// Tier is a route's cost/capability class — drives the semi-auto gate and
// the budget guardrail's premium-spend accounting.
type Tier string

const (
	TierPremium  Tier = "premium"
	TierStandard Tier = "standard"
	TierCheap    Tier = "cheap"
)

// RouteEntry is one row from model_router.yaml, ported from RouteEntry in
// orchestrator/models.py. Field names/tags mirror the YAML keys the Python
// router reads (see orchestrator/model_router.yaml's header comment) so
// internal/router can decode straight into this struct with yaml.v3.
//
// FallbackCLIModel, EscalationModel, Agent and Effort are nil when the
// route doesn't set them, matching Python's `| None` optional fields —
// IsPremium is NOT optional: Python precomputes it at load time from Tier
// so the dispatch hot path never string-compares Tier, and Go keeps that
// contract rather than deriving it lazily.
type RouteEntry struct {
	Backend   Backend `yaml:"backend"`
	CLIModel  string  `yaml:"cli_model"`
	Tier      Tier    `yaml:"tier"`
	IsPremium bool    `yaml:"is_premium"`

	FallbackCLIModel *string `yaml:"fallback_cli_model,omitempty"`
	// EscalationModel names another ROUTE KEY (not a raw CLIModel) that the
	// reap loop promotes to on attempt 3 after a retryable failure on
	// attempt 2. A non-nil value that doesn't correspond to a real route
	// entry is a fail-fast startup error in internal/router, not here.
	EscalationModel *string `yaml:"escalation_model,omitempty"`

	// Backend-specific extras (agy — issues #87/#88). Agent is agy's
	// `--agent <role>` selector; internal/providers/agy.go defaults it to
	// "executor" when the route leaves it unset (the default coordinator
	// persona delegates instead of executing). Effort is agy's
	// `--effort {low,medium,high}` for base-name Gemini models that
	// require it; left nil when the cli_model already encodes effort as a
	// suffix (e.g. "gemini-3.7-flash-high").
	Agent  *string `yaml:"agent,omitempty"`
	Effort *string `yaml:"effort,omitempty"`
}
