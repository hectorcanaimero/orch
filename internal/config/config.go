// Package config loads a project's `.orchestrator/config.yaml` into a typed
// struct with defaults filled in.
//
// The file format is a compatibility contract: an existing project must keep
// working when its owner swaps the Python orch for the Go one. Two rules
// follow from that, and they pull in opposite directions:
//
//   - Keys the Go tree no longer implements are IGNORED, with one warning
//     each, once. Failing on `findings.publish_repo` would break a project
//     whose config was written by a version that had findings.
//   - `state.backend: file` is the single exception. The file backend is gone
//     (ADR-G4), so silently treating it as sqlite would point orch at an
//     empty database and report a project with no history. That is an error
//     with the migration command in it.
//
// Every key, its default and the ignored-key list are documented in
// docs/CONFIG.md — keep the two in step.
//
// Defaults come from three places that must agree, and did not:
// `orchestrator/config.yaml`, `config_loader._apply_defaults`, and the four
// templates. Where they disagreed the packaged file won, and the disagreement
// was fixed in Python first (PR #102). `testdata/*.effective.yaml` holds what
// Python actually produces for eight inputs; `TestMatchesPythonGoldens` is
// the check that the two loaders still agree.
package config

// Config is the whole of `config.yaml` after defaults are applied.
//
// Every field carries a yaml tag even when it matches the field name, so the
// wire format is legible from the struct alone rather than from Go's
// lowercasing rules.
type Config struct {
	Concurrency          Concurrency    `yaml:"concurrency"`
	StrictFilesPhases    []int          `yaml:"strict_files_phases"`
	DefaultTimeoutMult   float64        `yaml:"default_timeout_multiplier"`
	Budget               Budget         `yaml:"budget"`
	Retry                Retry          `yaml:"retry"`
	SpecRoot             string         `yaml:"spec_root"`
	State                State          `yaml:"state"`
	BudgetsConfig        string         `yaml:"budgets_config"`
	BudgetsPreset        string         `yaml:"budgets_preset"`
	TypicalDispatchToken int            `yaml:"typical_dispatch_tokens"`
	Dashboard            Dashboard      `yaml:"dashboard"`
	Dispatch             Dispatch       `yaml:"dispatch"`
	VCS                  VCS            `yaml:"vcs"`
	GitHub               GitHub         `yaml:"github"`
	Notifications        Notifications  `yaml:"notifications"`
	Presentation         Presentation   `yaml:"presentation"`
	Publish              Publish        `yaml:"publish"`
	Tunnel               Tunnel         `yaml:"tunnel"`
	Telemetry            Telemetry      `yaml:"telemetry"`
	Sync                 Sync           `yaml:"sync"`
	ReportFindings       ReportFindings `yaml:"report_findings"`
	Portal               Portal         `yaml:"portal"`
}

// Portal configures what the client portal (`orch publish`) shows beyond the
// project's status.
type Portal struct {
	// Documents are globs, relative to the project root, of the markdown
	// files a client may read — by default what the orch-plan pipeline
	// writes. An empty list publishes no documents.
	Documents []string `yaml:"documents"`
}

// DefaultPortalDocuments is what `portal.documents` lists when a project says
// nothing: the PRDs, architectures and specs the planning pipeline writes.
// `specs/f*.md` rather than `specs/*.md`: orch-spec names specs fN-slug.md,
// and the folder's README is about the folder, not the project.
var DefaultPortalDocuments = []string{"docs/prd/*.md", "docs/arch/*.md", "specs/f*.md"}

// ReportFindings lets the MCP tool orch_report_finding file GitHub issues
// about orch itself on hectorcanaimero/orch, with the operator's own `gh`.
//
// Off by default: a public issue under someone's account is not something a
// project should do because an agent decided to. Go only, no Python source —
// the Python line's `findings:` block was a different, dropped feature.
type ReportFindings struct {
	Enabled bool `yaml:"enabled"`
}

// Concurrency caps in-flight dispatches. `global_max` is the hard ceiling
// across every backend; `per_provider` is a second gate per backend name.
type Concurrency struct {
	GlobalMax   int            `yaml:"global_max"`
	PerProvider map[string]int `yaml:"per_provider"`
	PerFile     int            `yaml:"per_file"`
}

// Budget is the per-dispatch cap handed to the provider CLI. The rolling
// window guardrail lives in budgets.yaml, not here.
type Budget struct {
	PerDispatchUSD float64 `yaml:"per_dispatch_usd"`
	// PerDispatchExplicit reports whether config.yaml (or dashboard.yaml)
	// wrote `budget.per_dispatch_usd` itself, read from the parsed YAML
	// mapping rather than inferred from the value. Only an explicit value is
	// sent to claude as --max-budget-usd; the default only limits escalation.
	PerDispatchExplicit bool `yaml:"-"`
}

// Retry is the declarative policy F4.6 asks for: one rule per failure class,
// replacing the reaper's hardcoded numbers.
//
// The two scalars Python ships (`backoff_seconds`, `rate_limit_backoff_seconds`)
// are still read, and seed the per-class defaults — an existing config keeps
// behaving the same way without being rewritten. A per-class block always
// wins over the scalar that seeded it.
//
// Class names match `FailureClass` in dispatcher.py, lowercased. `ci_failed`
// has no enum member there because CI failure is decided by the VCS poller,
// not by classifying a subprocess; it is a retry class all the same.
type Retry struct {
	// Legacy scalars. Kept for compatibility, not deprecated: they are the
	// concise way to say "the same everywhere".
	MaxAttempts             int     `yaml:"max_attempts"`
	BackoffSeconds          float64 `yaml:"backoff_seconds"`
	RateLimitBackoffSeconds float64 `yaml:"rate_limit_backoff_seconds"`

	Transient    RetryRule `yaml:"transient"`
	Timeout      RetryRule `yaml:"timeout"`
	RateLimit    RetryRule `yaml:"rate_limit"`
	VersionDrift RetryRule `yaml:"version_drift"`
	CIFailed     RetryRule `yaml:"ci_failed"`
}

// RetryRule is one failure class's policy. Pointers because "absent" and
// "explicitly zero" mean different things: `max_attempts: 0` is "never retry
// this class", which is not the same as "inherit the default".
type RetryRule struct {
	MaxAttempts    *int     `yaml:"max_attempts,omitempty"`
	BackoffSeconds *float64 `yaml:"backoff_seconds,omitempty"`
	// EscalateModel re-dispatches on the next tier up instead of the same
	// model. Meaningful for version_drift, where retrying the same CLI with
	// the same model reproduces the same failure.
	EscalateModel *bool `yaml:"escalate_model,omitempty"`
}

// Attempts returns the rule's max attempts, falling back to the policy-wide
// scalar.
func (r RetryRule) Attempts(fallback int) int {
	if r.MaxAttempts == nil {
		return fallback
	}
	return *r.MaxAttempts
}

// Backoff returns the rule's backoff, falling back to the given default.
func (r RetryRule) Backoff(fallback float64) float64 {
	if r.BackoffSeconds == nil {
		return fallback
	}
	return *r.BackoffSeconds
}

// Escalate reports whether a retry of this class should step up a model tier.
func (r RetryRule) Escalate() bool {
	return r.EscalateModel != nil && *r.EscalateModel
}

// State points at the SQLite database. `backend` is accepted only as
// "sqlite" or absent — see the package comment.
type State struct {
	Backend    string `yaml:"backend"`
	SQLitePath string `yaml:"sqlite_path"`
}

// Dashboard is the subset of the Python dashboard config the Go tree keeps.
// Kanban defaults are dropped; a config carrying them loads fine and they
// are ignored. The legacy nested `dashboard.tunnel` block is dropped too
// (see droppedKeys in load.go) — it moved to the top-level Tunnel type
// below (G5.6).
type Dashboard struct {
	Profile                string `yaml:"profile"`
	Token                  string `yaml:"token"`
	ShowSpendToStakeholder bool   `yaml:"show_spend_to_stakeholder"`
	SummaryLanguage        string `yaml:"summary_language"`
	// Language is the client-facing language (en, es or pt): the executive
	// summary, the portal and the PDF report. summary_language is its older
	// name; validate resolves both into the same value.
	Language string `yaml:"language"`
}

// Dispatch controls per-task git isolation.
type Dispatch struct {
	WorktreeMode bool   `yaml:"worktree_mode"`
	BaseBranch   string `yaml:"base_branch"`
}

// VCS drives PR creation and CI polling through the `gh` / `glab` CLIs.
type VCS struct {
	Provider        string `yaml:"provider"`
	Host            string `yaml:"host"`
	AutoPR          bool   `yaml:"auto_pr"`
	CIMaxRetries    int    `yaml:"ci_max_retries"`
	CIPollIntervalS int    `yaml:"ci_poll_interval_s"`
}

// GitHub configures the CI workflow `orch init` generates.
type GitHub struct {
	TestCommand string `yaml:"test_command"`
	AutoMerge   bool   `yaml:"auto_merge"`
}

// Notifications are fire-and-forget webhooks. An empty URL disables that
// channel; a failing webhook never surfaces into the dispatch loop.
type Notifications struct {
	SlackWebhook   string `yaml:"slack_webhook"`
	DiscordWebhook string `yaml:"discord_webhook"`
	TimeoutS       int    `yaml:"timeout_s"`
}

// Presentation renames statuses for display. Internal status values never
// change — this is the label the client reads.
type Presentation struct {
	StatusLabels map[string]string `yaml:"status_labels"`
	Branding     Branding          `yaml:"branding"`
}

// Branding is the white-label block (F3.5): what an agency puts on the
// snapshot, the stakeholder bundle and the PDF so a client sees their own
// name rather than ours.
//
// Every field is optional and the zero value changes nothing — a project
// without this block produces the same bytes it produced before the block
// existed, which is the property that makes it safe to add to a document with
// a frozen schema.
type Branding struct {
	// Name replaces the project name in a client-facing header. The project's
	// own `meta.project` stays what it is; this is what the client reads.
	Name string `yaml:"name"`
	// Logo is a path to a local image file, or a `data:` URI already. A path
	// is read and embedded at build time — the document must stand alone, so
	// nothing in it may point at a file the viewer cannot reach.
	Logo string `yaml:"logo"`
	// AccentColor is `#rgb` or `#rrggbb`. Anything else is a config error
	// rather than a silently ignored value: a colour that does not apply is
	// invisible, and the operator would go looking in the wrong place.
	AccentColor string `yaml:"accent_color"`
	// Footer is one line at the bottom of the page — an agency's name, a
	// confidentiality note.
	Footer string `yaml:"footer"`
}

// Configured reports whether anything was set. The zero block is the "no
// branding" case every caller has to distinguish, so it has a name rather
// than four comparisons repeated at each site.
func (b Branding) Configured() bool {
	return b.Name != "" || b.Logo != "" || b.AccentColor != "" || b.Footer != ""
}

// Publish is new in Go (blueprint F2). Nothing reads it yet; it ships now so
// a project's config does not need rewriting when `orch publish` lands, and
// so the key is reserved against a name collision.
type Publish struct {
	// IntervalS is how often `orch publish --watch` re-exports while a run
	// is in flight.
	IntervalS int `yaml:"interval_s"`
	// To is the destination: "dir", "git" or "cloud".
	To string `yaml:"to"`
	// Dir is the output directory for `to: dir`.
	Dir string `yaml:"dir"`
	// GitBranch is the branch pushed for `to: git` — typically gh-pages.
	GitBranch string `yaml:"git_branch"`
}

// DefaultSyncIssuesLabel is the label `orch sync issues` reads when neither
// --label nor `sync.issues_label` says otherwise.
//
// Namespaced on purpose: `orch:task` is a label a human adds to say "this
// issue is work for orch", and the prefix keeps it from colliding with the
// `bug` / `enhancement` labels a repo already uses for its own triage.
const DefaultSyncIssuesLabel = "orch:task"

// Sync configures `orch sync`, which brings work in from a tracker.
//
// New in Go: Python has no `sync` verb, so this key is not a port and no
// Python config file has ever contained it.
type Sync struct {
	// IssuesLabel is the GitHub issue label `orch sync issues` ingests
	// when --label is not passed.
	IssuesLabel string `yaml:"issues_label"`
}

// Tunnel configures the dashboard's optional public URL, a Cloudflare quick
// tunnel (internal/tunnel). There is nothing to choose: `enabled: true` is
// the whole configuration, and the removed provider keys are refused at load
// (removedTunnelKeys in load.go).
type Tunnel struct {
	Enabled bool `yaml:"enabled"`
	// URLParseTimeoutS is how long to wait for cloudflared's URL line before
	// reporting url_parse_timeout; 0 means 30.
	URLParseTimeoutS int `yaml:"url_parse_timeout_s"`
}

// Telemetry controls the anonymous, opt-in usage ping G8.6 (F4.9) adds —
// see internal/telemetry's own package doc for exactly what one event
// carries (never a path, a prompt, a task id, or a project id/name) and
// docs/TELEMETRY.md for the field list as shipped documentation.
//
// Off by default (Enabled's zero value). The standard `DO_NOT_TRACK`
// environment variable always overrides Enabled — see
// internal/telemetry.DoNotTrack — so there is no config knob for it: an
// environment variable a project's own config.yaml cannot see is the
// point.
type Telemetry struct {
	Enabled bool `yaml:"enabled"`
	// Endpoint overrides internal/telemetry's own default (empty until a
	// real collector exists — see that package's defaultEndpoint). Most
	// projects never set this; it exists for a self-hosted collector or a
	// future default once one is chosen.
	Endpoint string `yaml:"endpoint"`
}
