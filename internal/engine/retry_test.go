package engine

import (
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
)

func intp(v int) *int         { return &v }
func f64p(v float64) *float64 { return &v }
func strp(v string) *string   { return &v }

// baseRetryCfg is the shipped config: three scalars, no per-class blocks.
func baseRetryCfg() config.Config {
	return config.Config{
		Retry: config.Retry{
			MaxAttempts:             2,
			BackoffSeconds:          5,
			RateLimitBackoffSeconds: 60,
		},
	}
}

func retryInput(f providers.Failure, attempt int) RetryInput {
	return RetryInput{
		Task:    task("T-1", 1, "claude/opus"),
		Route:   route(model.BackendClaude, "opus", false),
		Attempt: attempt,
		Failure: f,
		Reason:  "something went wrong",
		Routes:  map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)},
		Cfg:     baseRetryCfg(),
	}
}

// TestDecideRetryByClass is the policy table. The four terminal classes are
// terminal for a reason each: a timeout outran its estimate, credentials do
// not fix themselves, a budget retry spends more of what ran out, and
// retrying an id spoof invites the agent to spoof again.
func TestDecideRetryByClass(t *testing.T) {
	tests := []struct {
		name      string
		failure   providers.Failure
		attempt   int
		wantRetry bool
		wantWait  time.Duration
	}{
		{name: "transient retries", failure: providers.FailureTransient, attempt: 1, wantRetry: true, wantWait: 5 * time.Second},
		{name: "parser retries", failure: providers.FailureParser, attempt: 1, wantRetry: true, wantWait: 5 * time.Second},
		{name: "other retries", failure: providers.FailureOther, attempt: 1, wantRetry: true, wantWait: 5 * time.Second},
		{
			name:    "a rate limit waits its own, longer backoff",
			failure: providers.FailureRateLimit, attempt: 1,
			wantRetry: true, wantWait: 60 * time.Second,
		},
		{
			// The fallback is a different model, and the failure was a
			// rejection rather than a rate limit, so there is nothing to
			// wait for.
			name:    "version drift retries immediately",
			failure: providers.FailureVersionDrift, attempt: 1,
			wantRetry: true, wantWait: 0,
		},
		{name: "a timeout is terminal", failure: providers.FailureTimeout, attempt: 1},
		{name: "a permission failure is terminal", failure: providers.FailurePermission, attempt: 1},
		{name: "a budget stop is terminal", failure: providers.FailureBudget, attempt: 1},
		{name: "an id spoof is terminal", failure: providers.FailureIDSpoof, attempt: 1},
		{name: "no failure class at all", failure: "", attempt: 1},
		{name: "attempts exhausted", failure: providers.FailureTransient, attempt: 2},
		{name: "past the limit", failure: providers.FailureTransient, attempt: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideRetry(retryInput(tt.failure, tt.attempt))
			if got.Retry != tt.wantRetry {
				t.Fatalf("Retry = %v, want %v", got.Retry, tt.wantRetry)
			}
			if !tt.wantRetry {
				return
			}
			if got.Attempt != tt.attempt+1 {
				t.Errorf("Attempt = %d, want %d", got.Attempt, tt.attempt+1)
			}
			if got.Backoff != tt.wantWait {
				t.Errorf("Backoff = %v, want %v", got.Backoff, tt.wantWait)
			}
		})
	}
}

// TestRetryRuleOverridesTheScalar is the behaviour orch-98 asked to be
// observable: changing retry.rate_limit.max_attempts changes what happens.
//
// With no per-class block, every RetryRule is empty and the scalars apply —
// which is what every shipped config has, so parity with Python holds.
func TestRetryRuleOverridesTheScalar(t *testing.T) {
	t.Run("no per-class block behaves like Python", func(t *testing.T) {
		in := retryInput(providers.FailureRateLimit, 1)
		if got := DecideRetry(in); !got.Retry {
			t.Error("want a retry at attempt 1 of 2")
		}
		in.Attempt = 2
		if got := DecideRetry(in); got.Retry {
			t.Error("want no retry once the scalar's attempts are used")
		}
	})

	t.Run("rate_limit.max_attempts of 0 never retries a rate limit", func(t *testing.T) {
		in := retryInput(providers.FailureRateLimit, 1)
		in.Cfg.Retry.RateLimit = config.RetryRule{MaxAttempts: intp(0)}
		if got := DecideRetry(in); got.Retry {
			t.Error("max_attempts: 0 means never retry this class")
		}
		// And it is scoped to that class: transient still retries.
		in.Failure = providers.FailureTransient
		if got := DecideRetry(in); !got.Retry {
			t.Error("the rate_limit rule must not affect transient")
		}
	})

	t.Run("rate_limit.max_attempts of 4 retries further", func(t *testing.T) {
		in := retryInput(providers.FailureRateLimit, 3)
		in.Cfg.Retry.RateLimit = config.RetryRule{MaxAttempts: intp(4)}
		if got := DecideRetry(in); !got.Retry {
			t.Error("want a retry at attempt 3 of 4")
		}
	})

	t.Run("a per-class backoff overrides the scalar", func(t *testing.T) {
		in := retryInput(providers.FailureTransient, 1)
		in.Cfg.Retry.Transient = config.RetryRule{BackoffSeconds: f64p(0.25)}
		if got := DecideRetry(in); got.Backoff != 250*time.Millisecond {
			t.Errorf("Backoff = %v, want 250ms", got.Backoff)
		}
	})
}

// TestDecideRetryVersionDriftSwapsTheModel, with Python's message.
func TestDecideRetryVersionDriftSwapsTheModel(t *testing.T) {
	in := retryInput(providers.FailureVersionDrift, 1)
	in.Route.FallbackCLIModel = strp("claude-sonnet-4-6")
	in.Reason = "model not found"

	got := DecideRetry(in)
	if !got.Retry {
		t.Fatal("want a retry")
	}
	if got.Route.CLIModel != "claude-sonnet-4-6" {
		t.Errorf("CLIModel = %q, want the fallback", got.Route.CLIModel)
	}
	want := `version-drift fallback: 'opus' -> 'claude-sonnet-4-6' (model not found)`
	if got.Reason != want {
		t.Errorf("Reason =\n  %q\nwant\n  %q", got.Reason, want)
	}
	if got.Escalated {
		t.Error("a fallback swap is not an escalation")
	}
}

// TestDecideRetryVersionDriftWithNoFallback: the class still retries, on the
// same model, because Python's condition is `should_retry_with_fallback AND
// fallback_cli_model` — without one it falls through to a plain retry.
func TestDecideRetryVersionDriftWithNoFallback(t *testing.T) {
	in := retryInput(providers.FailureVersionDrift, 1)
	got := DecideRetry(in)
	if !got.Retry {
		t.Fatal("want a retry")
	}
	if got.Route.CLIModel != "opus" {
		t.Errorf("CLIModel = %q, want the original", got.Route.CLIModel)
	}
	if got.Reason != "something went wrong" {
		t.Errorf("Reason = %q, want it unchanged", got.Reason)
	}
}

// ---- Escalation (FR-D-8) -------------------------------------------------

func escalationInput(attempt int) RetryInput {
	in := retryInput(providers.FailureTransient, attempt)
	in.Route.EscalationModel = strp("claude/bigger")
	in.Routes["claude/bigger"] = route(model.BackendClaude, "opus-4-8", true)
	return in
}

func TestEscalationOnAttemptThree(t *testing.T) {
	in := escalationInput(2)
	in.Reason = "500 internal server error"

	got := DecideRetry(in)
	if !got.Retry {
		t.Fatal("want a third attempt when the route has an escalation target")
	}
	if got.Attempt != 3 {
		t.Errorf("Attempt = %d, want 3", got.Attempt)
	}
	if !got.Escalated {
		t.Error("want Escalated")
	}
	if got.Route.CLIModel != "opus-4-8" {
		t.Errorf("CLIModel = %q, want the escalation route's", got.Route.CLIModel)
	}
	if got.ToRouteKey != "claude/bigger" {
		t.Errorf("ToRouteKey = %q", got.ToRouteKey)
	}
	// An escalation goes to a different route, so the failing one's hiccup
	// is behind it — no wait.
	if got.Backoff != 0 {
		t.Errorf("Backoff = %v, want 0 for an escalation", got.Backoff)
	}
	want := `attempt-3 escalation: 'claude/opus' -> 'claude/bigger' (500 internal server error)`
	if got.Reason != want {
		t.Errorf("Reason =\n  %q\nwant\n  %q", got.Reason, want)
	}
}

func TestEscalationPreconditions(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(in *RetryInput)
		want    bool
	}{
		{
			name:    "the happy case",
			prepare: func(*RetryInput) {},
			want:    true,
		},
		{
			name:    "no escalation target on the route",
			prepare: func(in *RetryInput) { in.Route.EscalationModel = nil },
		},
		{
			name: "a target that does not resolve in the router",
			prepare: func(in *RetryInput) {
				in.Route.EscalationModel = strp("claude/ghost")
			},
		},
		{
			// Drift already has the fallback path; escalating too would
			// double-swap.
			name:    "version drift does not escalate",
			prepare: func(in *RetryInput) { in.Failure = providers.FailureVersionDrift },
		},
		{
			name: "a task that already spent its per-dispatch cap",
			prepare: func(in *RetryInput) {
				in.Cfg.Budget.PerDispatchUSD = 1.0
				in.SpentUSD = 1.5
			},
		},
		{
			name: "under the cap still escalates",
			prepare: func(in *RetryInput) {
				in.Cfg.Budget.PerDispatchUSD = 1.0
				in.SpentUSD = 0.25
			},
			want: true,
		},
		{
			name: "no cap configured means the cap does not block it",
			prepare: func(in *RetryInput) {
				in.Cfg.Budget.PerDispatchUSD = 0
				in.SpentUSD = 99
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := escalationInput(2)
			tt.prepare(&in)
			got := DecideRetry(in)
			if got.Retry != tt.want {
				t.Errorf("Retry = %v, want %v", got.Retry, tt.want)
			}
			if tt.want && !got.Escalated && in.Failure != providers.FailureVersionDrift {
				t.Error("want Escalated on the third attempt")
			}
		})
	}
}

// TestNoThirdAttemptWithoutEscalation: without an escalation target the limit
// is the configured max_attempts, full stop.
func TestNoThirdAttemptWithoutEscalation(t *testing.T) {
	in := retryInput(providers.FailureTransient, 2)
	if got := DecideRetry(in); got.Retry {
		t.Error("want no third attempt when the route names no escalation target")
	}
}

// TestRetryDefaultsMatchPython covers a Config built in code rather than
// loaded, which the loader's setdefault calls would otherwise have filled in.
func TestRetryDefaultsMatchPython(t *testing.T) {
	in := retryInput(providers.FailureTransient, 1)
	in.Cfg = config.Config{} // nothing set at all

	got := DecideRetry(in)
	if !got.Retry {
		t.Fatal("want a retry — max_attempts defaults to 2, not 0")
	}
	if got.Backoff != 5*time.Second {
		t.Errorf("Backoff = %v, want the 5s default", got.Backoff)
	}

	in.Failure = providers.FailureRateLimit
	if got := DecideRetry(in); got.Backoff != 60*time.Second {
		t.Errorf("rate-limit Backoff = %v, want the 60s default", got.Backoff)
	}
}

// TestDecideRetryIsPure: no clock, no I/O, so a backoff can be asserted
// without waiting and two calls agree.
func TestDecideRetryIsPure(t *testing.T) {
	in := retryInput(providers.FailureTransient, 1)
	first := DecideRetry(in)
	second := DecideRetry(in)
	if first != second {
		t.Errorf("not deterministic:\n  %+v\n  %+v", first, second)
	}
	if in.Attempt != 1 {
		t.Error("DecideRetry mutated its input")
	}
}
