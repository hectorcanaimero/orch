package engine

import (
	"fmt"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// nonRetryable are the failure classes a retry cannot help with.
//
// TIMEOUT: the task outran its estimate; running it again just burns the same
// wall clock. PERMISSION: credentials do not fix themselves. BUDGET: retrying
// spends more of the thing that ran out. ID_SPOOF: the agent called
// task-finish.sh with someone else's id, and a retry invites it to try again.
var nonRetryable = map[providers.Failure]bool{
	providers.FailureTimeout:    true,
	providers.FailurePermission: true,
	providers.FailureBudget:     true,
	providers.FailureIDSpoof:    true,
}

// RetryItem is one task waiting to be re-dispatched.
type RetryItem struct {
	Task    model.Task
	Route   model.RouteEntry
	Attempt int
	// EarliestAt is when the backoff expires. The refill loop skips an item
	// whose time has not come rather than sleeping — the loop already ticks.
	EarliestAt time.Time
}

// RetryDecision is what the reaper concluded about a failed dispatch.
type RetryDecision struct {
	// Retry is false when the task is terminal and should be blocked.
	Retry bool
	// Route is what the retry should use: the same one, the version-drift
	// fallback, or the escalation route.
	Route model.RouteEntry
	// Attempt is the number the retry will run as.
	Attempt int
	// Backoff is how long to wait first.
	Backoff time.Duration
	// Reason is the operator-facing explanation, already composed.
	Reason string
	// Escalated marks an attempt-3 promotion to another route, which emits
	// its own event before the retry event.
	Escalated bool
	// ToRouteKey is the escalation target's key in the router, for the
	// `escalate` event. Empty unless Escalated.
	ToRouteKey string
}

// RetryInput is everything the policy needs to decide.
type RetryInput struct {
	Task    model.Task
	Route   model.RouteEntry
	Attempt int
	Failure providers.Failure
	// Reason is the failure's message, already truncated by the caller.
	Reason string
	// SpentUSD is what this task has cost across every attempt so far.
	SpentUSD float64
	// Routes is the router, for resolving an escalation key.
	Routes map[string]model.RouteEntry
	Cfg    config.Config
}

// DecideRetry applies the retry policy to one failed dispatch. Pure: no
// clocks, no I/O — Backoff is a duration, not a deadline, so the caller
// stamps it against its own clock and a test does not have to wait.
//
// Port of the Fase B / FR-D-4 / FR-D-8 branch of orch.py's reap loop.
func DecideRetry(in RetryInput) RetryDecision {
	no := RetryDecision{Route: in.Route, Attempt: in.Attempt}

	if nonRetryable[in.Failure] || in.Failure == "" {
		return no
	}

	// FR-D-8: a third attempt exists only when the route names an escalation
	// target that actually resolves, and only for classes where promoting to
	// another model is the right move. VERSION_DRIFT is excluded because it
	// already has the fallback path — escalating as well would double-swap.
	escalationKey := ""
	if in.Route.EscalationModel != nil {
		escalationKey = *in.Route.EscalationModel
	}
	_, escalationResolves := in.Routes[escalationKey]
	hasEscalation := escalationKey != "" && escalationResolves &&
		in.Failure != providers.FailureVersionDrift

	// The third attempt still counts against budget.per_dispatch_usd. A task
	// that has already spent its cap does not get promoted to a pricier
	// model to spend more.
	escalationAllowed := hasEscalation && budget.AllowEscalation(in.SpentUSD, in.Cfg.Budget.PerDispatchUSD)

	rule := retryRuleFor(in.Cfg.Retry, in.Failure)
	base := rule.Attempts(defaultMaxAttempts(in.Cfg.Retry))
	maxAttempts := base
	if escalationAllowed {
		maxAttempts = base + 1
	}
	if in.Attempt >= maxAttempts {
		return no
	}

	next := in.Attempt + 1
	decision := RetryDecision{
		Retry:   true,
		Route:   in.Route,
		Attempt: next,
		Reason:  in.Reason,
	}

	switch {
	case next == 3 && escalationAllowed:
		decision.Route = in.Routes[escalationKey]
		decision.Escalated = true
		decision.ToRouteKey = escalationKey
		decision.Reason = fmt.Sprintf("attempt-3 escalation: %s -> %s (%s)",
			pyfmt.Quote(in.Task.Model), pyfmt.Quote(escalationKey), in.Reason)
	case in.Failure == providers.FailureVersionDrift && in.Route.FallbackCLIModel != nil:
		fallback := *in.Route.FallbackCLIModel
		decision.Route.CLIModel = fallback
		decision.Reason = fmt.Sprintf("version-drift fallback: %s -> %s (%s)",
			pyfmt.Quote(in.Route.CLIModel), pyfmt.Quote(fallback), in.Reason)
	}

	decision.Backoff = retryBackoff(in.Cfg.Retry, in.Failure, decision.Escalated, rule)
	return decision
}

// retryBackoff is how long a retry waits before it may be dispatched.
//
//   - an escalation waits 0: it goes to a different route entirely, so the
//     failing one's rate limit or hiccup is behind it;
//   - VERSION_DRIFT waits 0: the fallback is a different model, and the
//     original failure was a rejection, not a rate limit;
//   - RATE_LIMIT waits rate_limit_backoff_seconds, to let the window reset;
//   - everything else waits backoff_seconds.
func retryBackoff(r config.Retry, f providers.Failure, escalated bool, rule config.RetryRule) time.Duration {
	if escalated || f == providers.FailureVersionDrift {
		return 0
	}
	fallback := r.BackoffSeconds
	if fallback == 0 {
		fallback = defaultBackoffSeconds
	}
	if f == providers.FailureRateLimit {
		fallback = r.RateLimitBackoffSeconds
		if fallback == 0 {
			fallback = defaultRateLimitBackoffSeconds
		}
	}
	return time.Duration(rule.Backoff(fallback) * float64(time.Second))
}

// Defaults, matching config_loader.py's setdefault calls. They are applied
// here as well as at load time so a Config built in code — a test, or a
// caller that skipped the loader — behaves like a loaded one rather than
// retrying zero times with no backoff.
const (
	defaultMaxAttemptsValue        = 2
	defaultBackoffSeconds          = 5.0
	defaultRateLimitBackoffSeconds = 60.0
)

func defaultMaxAttempts(r config.Retry) int {
	if r.MaxAttempts == 0 {
		return defaultMaxAttemptsValue
	}
	return r.MaxAttempts
}

// retryRuleFor picks the per-class rule.
//
// Python reads only the three scalars (`max_attempts`, `backoff_seconds`,
// `rate_limit_backoff_seconds`); the per-class blocks are modelled in
// internal/config but no shipped config.yaml carries them. With none present
// every RetryRule is empty and its Attempts/Backoff helpers return the
// scalars, so behaviour is identical to Python for every existing project —
// and a project that opts into a per-class rule gets something Python has no
// way to express. Noted in docs/brainstorm/go-migration-notes.md.
func retryRuleFor(r config.Retry, f providers.Failure) config.RetryRule {
	switch f {
	case providers.FailureTransient:
		return r.Transient
	case providers.FailureTimeout:
		return r.Timeout
	case providers.FailureRateLimit:
		return r.RateLimit
	case providers.FailureVersionDrift:
		return r.VersionDrift
	default:
		// PARSER and OTHER have no block of their own; they take the
		// scalars, as they do in Python.
		return config.RetryRule{}
	}
}
