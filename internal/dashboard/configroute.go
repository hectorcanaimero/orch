package dashboard

import (
	"net/http"
	"os"

	"gopkg.in/yaml.v3"
)

// `/api/config` — a WHITELISTED view of the project's config.yaml.
//
// The list is written out key by key, and it is not style. Python's comment
// is explicit: never `dict(**raw)`, so that a config key added later — one
// holding a token, a webhook URL, a path nobody meant to publish — cannot
// reach a client just by existing. Adding a key here is a decision somebody
// makes on purpose; forgetting to add one costs a field in a panel, which is
// the failure worth having.
//
// Two keys are deliberately absent and must stay absent: `dashboard.profile`
// and `dashboard.token`. The gate already knows the profile, and echoing the
// token would hand it to exactly the caller the token exists to stop.

// configPayload is the shape the SPA reads. `any` rather than concrete types
// because Python emits `null` for a key the file omits, and the SPA
// distinguishes null from a zero value — "no per-file limit" is not "a limit
// of 0".
type configPayload struct {
	Concurrency struct {
		GlobalMax   any            `json:"global_max"`
		PerProvider map[string]any `json:"per_provider"`
		PerFile     any            `json:"per_file"`
	} `json:"concurrency"`
	Budget struct {
		PerDispatchUSD any `json:"per_dispatch_usd"`
	} `json:"budget"`
	State struct {
		Backend    any `json:"backend"`
		SQLitePath any `json:"sqlite_path"`
	} `json:"state"`
	Retry struct {
		BackoffSeconds          any `json:"backoff_seconds"`
		RateLimitBackoffSeconds any `json:"rate_limit_backoff_seconds"`
	} `json:"retry"`
	SpecRoot any `json:"spec_root"`
	Budgets  struct {
		ConfigPath            any `json:"config_path"`
		Preset                any `json:"preset"`
		TypicalDispatchTokens any `json:"typical_dispatch_tokens"`
	} `json:"budgets"`
	Findings struct {
		PublishRepo             any `json:"publish_repo"`
		PublishRateLimitPerHour any `json:"publish_rate_limit_per_hour"`
		Label                   any `json:"label"`
		MinPublishConfidence    any `json:"min_publish_confidence"`
	} `json:"findings"`
	Dashboard struct {
		// The one dashboard key that travels, and it is a boolean by force:
		// it decides whether a STAKEHOLDER sees spend, so a string or a null
		// arriving from a hand-edited file must read as "no", not as truthy.
		ShowSpendToStakeholder bool `json:"show_spend_to_stakeholder"`
	} `json:"dashboard"`
	Presentation struct {
		StatusLabels map[string]any `json:"status_labels"`
	} `json:"presentation"`
	StrictFilesPhases  []any `json:"strict_files_phases"`
	DefaultTimeoutMult any   `json:"default_timeout_multiplier"`
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.projectConfigView())
}

// projectConfigView reads config.yaml and picks.
//
// An unreadable or malformed file yields the empty view rather than an error,
// which is Python's behaviour and the right one here: the SPA renders a
// settings panel of nulls, which is a truthful picture of a project whose
// config cannot be read, and `/api/config/status` is the endpoint whose job is
// to say so out loud.
func (s *Server) projectConfigView() configPayload {
	var out configPayload
	// These four are maps and slices the SPA iterates, so they are empty
	// rather than null even when the file has nothing to say.
	out.Concurrency.PerProvider = map[string]any{}
	out.Presentation.StatusLabels = map[string]any{}
	out.StrictFilesPhases = []any{}

	// #nosec G304 -- the project's own config.yaml, resolved from its paths
	body, err := os.ReadFile(s.paths.ConfigYAML)
	if err != nil {
		return out
	}
	var raw map[string]any
	if err := yaml.Unmarshal(body, &raw); err != nil || raw == nil {
		return out
	}

	concurrency := subMap(raw, "concurrency")
	out.Concurrency.GlobalMax = concurrency["global_max"]
	if perProvider := toMap(concurrency["per_provider"]); perProvider != nil {
		out.Concurrency.PerProvider = perProvider
	}
	out.Concurrency.PerFile = concurrency["per_file"]

	out.Budget.PerDispatchUSD = subMap(raw, "budget")["per_dispatch_usd"]

	stateRaw := subMap(raw, "state")
	out.State.Backend = stateRaw["backend"]
	out.State.SQLitePath = stateRaw["sqlite_path"]

	retry := subMap(raw, "retry")
	out.Retry.BackoffSeconds = retry["backoff_seconds"]
	out.Retry.RateLimitBackoffSeconds = retry["rate_limit_backoff_seconds"]

	out.SpecRoot = raw["spec_root"]

	// Note the flattening: three TOP-LEVEL keys are reported under `budgets`.
	// That is Python's shape and the SPA's, not a nesting in the file.
	out.Budgets.ConfigPath = raw["budgets_config"]
	out.Budgets.Preset = raw["budgets_preset"]
	out.Budgets.TypicalDispatchTokens = raw["typical_dispatch_tokens"]

	findings := subMap(raw, "findings")
	out.Findings.PublishRepo = findings["publish_repo"]
	out.Findings.PublishRateLimitPerHour = findings["publish_rate_limit_per_hour"]
	out.Findings.Label = findings["label"]
	out.Findings.MinPublishConfidence = findings["min_publish_confidence"]

	show, _ := subMap(raw, "dashboard")["show_spend_to_stakeholder"].(bool)
	out.Dashboard.ShowSpendToStakeholder = show

	if labels := toMap(subMap(raw, "presentation")["status_labels"]); labels != nil {
		out.Presentation.StatusLabels = labels
	}

	if phases, ok := raw["strict_files_phases"].([]any); ok {
		out.StrictFilesPhases = phases
	}
	out.DefaultTimeoutMult = raw["default_timeout_multiplier"]
	return out
}

// subMap reads a nested mapping, answering an empty one for a key that is
// missing, null, or holds something that is not a mapping. Python's
// `raw.get(k) or {}` in one place so eight call sites do not each get it
// slightly wrong.
func subMap(raw map[string]any, key string) map[string]any {
	if m := toMap(raw[key]); m != nil {
		return m
	}
	return map[string]any{}
}

// toMap converts what yaml.v3 decodes a mapping into. Keys are already
// strings for any YAML a human wrote; a mapping with non-string keys — `1:
// true` — is not a config this endpoint has an opinion about, and is dropped.
func toMap(raw any) map[string]any {
	switch m := raw.(type) {
	case map[string]any:
		return m
	default:
		return nil
	}
}
