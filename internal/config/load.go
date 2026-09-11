package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrFileBackend is returned when a config still asks for the JSONL file
// backend. It is the one key whose removal is an error rather than a warning:
// the file backend is gone (ADR-G4), and quietly treating it as sqlite would
// open an empty database and report a project with no history.
var ErrFileBackend = errors.New(
	"state.backend: file is no longer supported — the JSONL backend was " +
		"removed. Import the existing state with `orch migrate`, then set " +
		"state.backend: sqlite (or drop the key: sqlite is the default)")

// Result is a loaded config plus everything worth telling the operator once.
type Result struct {
	Config Config
	// Warnings are one line per ignored key, already de-duplicated and
	// sorted. The caller prints them once at startup; they are never fatal.
	Warnings []string
	// Sources lists the files that contributed, in the order they were
	// merged. Useful in `orch config show` and when a value is surprising.
	Sources []string
}

// Defaults returns the config a project gets with an empty `config.yaml`.
//
// These mirror `config_loader._apply_defaults` in the Python tree, with three
// deliberate differences, each of which has a reason:
//
//   - state.backend is "sqlite". Python has no default at all for it, and
//     every read site falls back to "file". Go has one backend.
//   - findings.* is absent. The feature is dropped.
//   - publish.* is new, and nothing reads it yet.
func Defaults() Config {
	return Config{
		Concurrency: Concurrency{
			GlobalMax:   6,
			PerProvider: map[string]int{"claude": 3, "codex": 2, "opencode": 3},
			PerFile:     0,
		},
		StrictFilesPhases:  []int{},
		DefaultTimeoutMult: 1.5,
		Budget:             Budget{PerDispatchUSD: 5.0},
		Retry: Retry{
			MaxAttempts:             2,
			BackoffSeconds:          5.0,
			RateLimitBackoffSeconds: 60.0,
		},
		SpecRoot:             "specs",
		State:                State{Backend: "sqlite"},
		BudgetsConfig:        "budgets.yaml",
		BudgetsPreset:        "conservative",
		TypicalDispatchToken: 200000,
		Dashboard: Dashboard{
			Profile:         "operator",
			SummaryLanguage: "es",
		},
		Dispatch: Dispatch{WorktreeMode: false, BaseBranch: "main"},
		VCS: VCS{
			Provider:        "github",
			Host:            "github.com",
			AutoPR:          false,
			CIMaxRetries:    1,
			CIPollIntervalS: 30,
		},
		GitHub:        GitHub{TestCommand: "pytest", AutoMerge: false},
		Notifications: Notifications{TimeoutS: 5},
		Presentation: Presentation{StatusLabels: map[string]string{
			"backlog":     "Planificado",
			"todo":        "Por hacer",
			"in_progress": "En progreso",
			"in-progress": "En progreso",
			"done":        "Entregado",
			"blocked":     "Bloqueado",
			"skipped":     "Omitido",
		}},
		Publish: Publish{IntervalS: 60, To: "dir", Dir: "public", GitBranch: "gh-pages"},
	}
}

// Load reads `path`, deep-merges a project-root `dashboard.yaml` over it when
// one exists (the H-2 compatibility path), applies defaults, and validates.
//
// projectRoot may be empty, in which case the directory containing `path` is
// used — matching `config_loader.load_config`.
//
// A missing file is not an error: a project with no config gets the defaults,
// which is what `orch init` without a template produces anyway.
func Load(path, projectRoot string) (Result, error) {
	var res Result

	if projectRoot == "" {
		projectRoot = filepath.Dir(path)
	}

	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- caller-supplied config path is the point
		parsed, dupWarnings, perr := decodeYAML(b, path)
		if perr != nil {
			return res, perr
		}
		raw = parsed
		res.Warnings = append(res.Warnings, dupWarnings...)
		res.Sources = append(res.Sources, path)
	} else if !os.IsNotExist(err) {
		return res, fmt.Errorf("read %s: %w", path, err)
	}

	// H-2 compatibility: a `dashboard.yaml` at the project root deep-merges
	// on top. `orch init` stopped scaffolding one, but projects created
	// before H-2 still have it and it still has to win.
	overridePath := filepath.Join(projectRoot, "dashboard.yaml")
	if b, err := os.ReadFile(overridePath); err == nil { // #nosec G304 -- fixed filename under the project root
		override, dupWarnings, perr := decodeYAML(b, overridePath)
		if perr != nil {
			return res, perr
		}
		raw = deepMerge(raw, override)
		res.Warnings = append(res.Warnings, dupWarnings...)
		res.Sources = append(res.Sources, overridePath)
	} else if !os.IsNotExist(err) {
		return res, fmt.Errorf("read %s: %w", overridePath, err)
	}

	res.Warnings = append(res.Warnings, unknownKeyWarnings(raw)...)
	sort.Strings(res.Warnings)

	// Decode over the defaults so an absent key keeps its default and a
	// present one wins. yaml.v3 leaves untouched fields alone, which is
	// exactly the merge semantics we want and the reason for not decoding
	// into a zero value and merging afterwards.
	cfg := Defaults()
	reencoded, err := yaml.Marshal(raw)
	if err != nil {
		return res, fmt.Errorf("re-encode config: %w", err)
	}
	if err := yaml.Unmarshal(reencoded, &cfg); err != nil {
		return res, fmt.Errorf("decode %s: %w", path, err)
	}

	if err := validate(&cfg); err != nil {
		return res, err
	}
	res.Config = cfg
	return res, nil
}

// validate rejects the one value that cannot be honoured and normalises the
// rest. Everything it fixes silently is a value with exactly one sensible
// reading; anything ambiguous is an error.
func validate(cfg *Config) error {
	switch strings.ToLower(strings.TrimSpace(cfg.State.Backend)) {
	case "", "sqlite":
		cfg.State.Backend = "sqlite"
	case "file":
		return ErrFileBackend
	default:
		return fmt.Errorf("state.backend: %q is not a backend orch knows "+
			"(the only value is `sqlite`)", cfg.State.Backend)
	}

	if cfg.Dashboard.Profile == "" {
		cfg.Dashboard.Profile = "operator"
	}
	switch cfg.Dashboard.Profile {
	case "operator", "stakeholder", "both":
	default:
		return fmt.Errorf("dashboard.profile: %q is not one of "+
			"operator, stakeholder, both", cfg.Dashboard.Profile)
	}
	// A stakeholder dashboard with no token serves nothing — the middleware
	// 401s every request. Catching it here turns a confusing runtime symptom
	// into a startup error.
	if cfg.Dashboard.Profile != "operator" && cfg.Dashboard.Token == "" {
		return fmt.Errorf("dashboard.profile is %q but dashboard.token is "+
			"empty — the stakeholder view would reject every request",
			cfg.Dashboard.Profile)
	}

	if cfg.SpecRoot == "" {
		cfg.SpecRoot = "specs"
	}
	if cfg.Dispatch.BaseBranch == "" {
		cfg.Dispatch.BaseBranch = "main"
	}
	return nil
}

// deepMerge returns a copy of base with override applied. Maps merge
// recursively; every other type is replaced wholesale, including slices —
// a config that lists three phases means those three, not those three
// appended to the defaults.
func deepMerge(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		bv, inBase := out[k]
		bm, baseIsMap := bv.(map[string]any)
		om, overIsMap := v.(map[string]any)
		if inBase && baseIsMap && overIsMap {
			out[k] = deepMerge(bm, om)
			continue
		}
		out[k] = v
	}
	return out
}

// knownKeys is every path the Go tree reads, as `section.key` (or bare
// `key` at the top level). A `section.*` entry means "anything under here",
// used where the shape is a free map rather than a struct.
var knownKeys = map[string]bool{
	"concurrency.global_max": true, "concurrency.per_provider.*": true,
	"concurrency.per_file": true,
	"strict_files_phases":  true, "default_timeout_multiplier": true,
	"budget.per_dispatch_usd": true,
	"retry.max_attempts":      true, "retry.backoff_seconds": true,
	"retry.rate_limit_backoff_seconds": true,
	"retry.transient.*":                true, "retry.timeout.*": true,
	"retry.rate_limit.*": true, "retry.version_drift.*": true,
	"retry.ci_failed.*": true,
	"spec_root":         true,
	"state.backend":     true, "state.sqlite_path": true,
	"budgets_config": true, "budgets_preset": true,
	"typical_dispatch_tokens": true,
	"dashboard.profile":       true, "dashboard.token": true,
	"dashboard.show_spend_to_stakeholder": true,
	"dashboard.summary_language":          true,
	"dispatch.worktree_mode":              true, "dispatch.base_branch": true,
	"vcs.provider": true, "vcs.host": true, "vcs.auto_pr": true,
	"vcs.ci_max_retries": true, "vcs.ci_poll_interval_s": true,
	"github.test_command": true, "github.auto_merge": true,
	"notifications.slack_webhook": true, "notifications.discord_webhook": true,
	"notifications.timeout_s":      true,
	"presentation.status_labels.*": true,
	"publish.interval_s":           true, "publish.to": true,
	"publish.dir": true, "publish.git_branch": true,
}

// droppedKeys explains the keys a pre-Go config is most likely to carry, so
// the warning says what happened rather than just "unknown".
var droppedKeys = map[string]string{
	"findings":            "the findings feature was removed",
	"dashboard.board_url": "the ExcaliDash board embed was removed",
	"dashboard.kanban":    "the Kanban defaults moved into the SPA",
	"dashboard.tunnel":    "only pinggy and cloudflared remain; configure them under `tunnel`",
	"dashboard.server":    "host and port are CLI flags",
	"providers":           "provider settings moved to model_router.yaml",
}

// unknownKeyWarnings walks the raw tree and returns one line per key the Go
// tree does not read — sorted, de-duplicated, and never fatal. Reporting the
// deepest known ancestor keeps a dropped section to one line instead of one
// per leaf.
func unknownKeyWarnings(raw map[string]any) []string {
	seen := map[string]bool{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if why, dropped := droppedKeys[path]; dropped {
				seen[fmt.Sprintf("ignoring `%s`: %s", path, why)] = true
				continue
			}
			if knownKeys[path] {
				continue
			}
			if knownKeys[path+".*"] {
				continue // a free-form map: its children are all fine
			}
			if child, ok := v.(map[string]any); ok && hasKnownDescendant(path) {
				walk(path, child)
				continue
			}
			seen[fmt.Sprintf("ignoring `%s`: orch does not read this key", path)] = true
		}
	}
	walk("", raw)

	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// hasKnownDescendant reports whether any known key sits under path, which is
// how a section like `vcs` is recognised as worth descending into even though
// `vcs` itself is not a leaf we read.
func hasKnownDescendant(path string) bool {
	prefix := path + "."
	for k := range knownKeys {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
