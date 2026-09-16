// Package bench runs one project once per provider and compares the runs.
//
// Each run happens on a throwaway copy of the project whose router sends every
// task to one provider, so the user's files and database are never touched.
// The dispatch itself is the caller's (`orch bench` hands in `orch run`'s own
// code path); this package prepares the copies, watches the spend cap while a
// run is going, and reads the finished copy's database into a Result.
package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Version is the results schema's version. Bump it when a field changes
// meaning or goes away; adding one does not need it.
const Version = 1

// Providers are the backends a bench can run, in the order the flag help
// lists them.
var Providers = []string{
	string(model.BackendClaude), string(model.BackendCodex), string(model.BackendOpencode),
	string(model.BackendGemini), string(model.BackendAgy),
}

// Report is the whole bench: where and when it ran, and one Result per run.
type Report struct {
	BenchVersion int      `json:"bench_version"`
	OrchVersion  string   `json:"orch_version"`
	Date         string   `json:"date"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Project      string   `json:"project"`
	MaxUSD       float64  `json:"max_usd"`
	Results      []Result `json:"results"`
}

// Result is one provider's run.
type Result struct {
	Provider   string `json:"provider"`
	CLIVersion string `json:"cli_version"`
	Run        int    `json:"run"`
	// Outcome is "finished", "stopped: max-usd" or "error: <message>".
	Outcome string `json:"outcome"`

	Tasks      int `json:"tasks"`
	Done       int `json:"done"`
	Blocked    int `json:"blocked"`
	Unfinished int `json:"unfinished"`
	Dispatches int `json:"dispatches"`
	// Retries counts attempts beyond each task's first.
	Retries int `json:"retries"`

	WallS  float64 `json:"wall_s"`
	AgentS float64 `json:"agent_s"`

	// CostUSD is what the CLI reported plus the pricing.yaml estimate for
	// rows it did not price; CostSource says which of the two it is.
	CostUSD        float64 `json:"cost_usd"`
	CostSource     string  `json:"cost_source"`
	TokensIn       int     `json:"tokens_in"`
	TokensOut      int     `json:"tokens_out"`
	CacheRead      int     `json:"cache_read_tokens"`
	CacheCreation  int     `json:"cache_creation_tokens"`
	WeightedTokens int     `json:"weighted_tokens"`
}

// Prepare copies the project at src to dst for one provider's run: without
// its runtime state, every task not in the backlog back to todo, and the
// router rewritten so each route dispatches to provider.
//
// models maps a provider to the cli_model every route should use; without an
// entry, each route takes the cli_model of a route that already targets the
// provider, same tier first.
func Prepare(src, dst, provider string, models map[string]string) error {
	if err := copyProject(src, dst); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := resetTasks(filepath.Join(dst, "tasks.json")); err != nil {
		return err
	}
	return routeAll(filepath.Join(dst, ".orchestrator", "model_router.yaml"), provider, models[provider])
}

// skipDirs are never copied: runtime state, git history, and what a build
// leaves behind. A benchmark starts from the project's definition, not from
// what a previous run did to it.
var skipDirs = []string{".git", "node_modules", filepath.Join(".orchestrator", "state"), filepath.Join(".orchestrator", "worktrees")}

// copyProject reads and writes through os.Root on both sides, so a path in
// the walk can never resolve outside the project or outside the copy, even
// if a directory is swapped for a symlink mid-walk.
func copyProject(src, dst string) error {
	from, err := os.OpenRoot(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = from.Close() }()
	to, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = to.Close() }()

	return fs.WalkDir(from.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && slices.Contains(skipDirs, filepath.FromSlash(rel)) {
			return fs.SkipDir
		}
		switch {
		case d.IsDir():
			if err := to.MkdirAll(rel, 0o750); err != nil {
				return fmt.Errorf("create %s: %w", rel, err)
			}
			return nil
		case !d.Type().IsRegular():
			// ponytail: symlinks and devices are skipped; copy them when a
			// project turns out to need one to run.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", rel, err)
		}
		body, err := from.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		// The source file's mode, so scripts stay executable.
		if err := to.WriteFile(rel, body, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		return nil
	})
}

// resetTasks rewrites each task's seed status to todo, except backlog, which
// `orch run` never dispatches either. Every other field is kept as written.
func resetTasks(path string) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- the benchmark copy's own tasks.json
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	tasks, _ := doc["tasks"].([]any)
	for _, t := range tasks {
		task, ok := t.(map[string]any)
		if !ok || task["status"] == string(model.StatusBacklog) {
			continue
		}
		task["status"] = string(model.StatusTodo)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return os.WriteFile(path, out, 0o600)
}

// routeAll points every route at provider, keeping the route keys (tasks and
// escalation_model name them) and each route's tier.
func routeAll(path, provider, cliModel string) error {
	rtr, err := router.Load(path)
	if err != nil {
		return fmt.Errorf("load the router: %w", err)
	}
	target := model.Backend(provider)
	out := make(map[string]model.RouteEntry, len(rtr))
	for _, key := range rtr.Keys() {
		entry := rtr[key]
		if entry.Backend != target {
			m := cliModel
			if m == "" {
				m = modelFor(rtr, target, entry.Tier)
			}
			if m == "" {
				return fmt.Errorf("route %s: the router has no %s route to take a model from; pass --model %s=<cli_model>",
					key, provider, provider)
			}
			entry.Backend = target
			entry.CLIModel = m
			entry.FallbackCLIModel = nil
			entry.Agent, entry.Effort = nil, nil
		} else if cliModel != "" {
			entry.CLIModel = cliModel
		}
		out[key] = entry
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("encode the router: %w", err)
	}
	return os.WriteFile(path, body, 0o600)
}

// modelFor is the cli_model of a route already on backend: the same tier if
// there is one, else the first in key order.
func modelFor(rtr router.Router, backend model.Backend, tier model.Tier) string {
	fallback := ""
	for _, key := range rtr.Keys() {
		e := rtr[key]
		if e.Backend != backend {
			continue
		}
		if e.Tier == tier {
			return e.CLIModel
		}
		if fallback == "" {
			fallback = e.CLIModel
		}
	}
	return fallback
}

// Spend is what Collect and the cap watch read from a run's database.
type Spend interface {
	Tasks(ctx context.Context, filter state.TaskFilter) ([]state.TaskRuntime, error)
	AllSpend(ctx context.Context, since time.Time) ([]state.Spend, error)
}

// Collect reads a finished copy's database into a Result. Provider,
// CLIVersion, Run, Outcome and WallS are the caller's to fill.
//
// It reads the rows directly; the run receipt (`orch report receipt`) sums the
// same tables, and this can call it once both are on main.
func Collect(ctx context.Context, db Spend, prices pricing.Table) (Result, error) {
	var r Result
	tasks, err := db.Tasks(ctx, state.TaskFilter{})
	if err != nil {
		return r, fmt.Errorf("read tasks: %w", err)
	}
	for _, t := range tasks {
		r.Tasks++
		switch t.Status {
		case model.StatusDone:
			r.Done++
		case model.StatusBlocked:
			r.Blocked++
		default:
			r.Unfinished++
		}
		if t.Attempts > 1 {
			r.Retries += t.Attempts - 1
		}
	}
	rows, err := db.AllSpend(ctx, time.Time{})
	if err != nil {
		return r, fmt.Errorf("read spend: %w", err)
	}
	var reported, estimated, weighted float64
	for _, s := range rows {
		r.Dispatches++
		r.AgentS += s.DurationS
		r.TokensIn += s.TokensIn
		r.TokensOut += s.TokensOut
		r.CacheRead += s.CacheReadTokens
		r.CacheCreation += s.CacheCreationTokens
		weighted += budget.WeightedTokens(s)
		if s.CostUSD > 0 {
			reported += s.CostUSD
		} else {
			estimated += prices.ResolveCost(0, s.Model, s.TokensIn, s.TokensOut)
		}
	}
	r.WeightedTokens = int(weighted + 0.5)
	r.CostUSD = reported + estimated
	r.CostSource = costSource(len(rows), reported, estimated)
	return r, nil
}

// costSource labels a cost the way the dashboard's Budget page does
// (internal/dashboard/metricroutes.go): reported when the CLI priced
// anything, estimated when only pricing.yaml did, no_data when neither.
func costSource(rows int, reported, estimated float64) string {
	switch {
	case rows == 0:
		return "none"
	case reported > 0 && estimated > 0:
		return "reported+estimated"
	case reported > 0:
		return "reported"
	case estimated > 0:
		return "estimated"
	default:
		return "no_data"
	}
}

// SpentUSD is the cap watch's running total: the same sum as Collect's cost.
func SpentUSD(ctx context.Context, db Spend, prices pricing.Table) (float64, error) {
	rows, err := db.AllSpend(ctx, time.Time{})
	if err != nil {
		return 0, fmt.Errorf("read spend: %w", err)
	}
	total := 0.0
	for _, s := range rows {
		total += prices.ResolveCost(s.CostUSD, s.Model, s.TokensIn, s.TokensOut)
	}
	return total, nil
}

// ErrOverCap is what WatchCap reports when a run crossed its cap.
var ErrOverCap = errors.New("max-usd reached")

// WatchCap calls spent on every tick and calls stop once it reaches max,
// then returns ErrOverCap. It returns nil when ctx ends first. A read error is
// not a reason to stop a run; the next tick tries again.
//
// The cap is checked between dispatches, not inside one: a dispatch already
// running when the total crosses it finishes, so a run can end a dispatch's
// cost above max.
func WatchCap(ctx context.Context, ticks <-chan time.Time, max float64, spent func(context.Context) (float64, error), stop func()) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			total, err := spent(ctx)
			if err == nil && total >= max {
				stop()
				return ErrOverCap
			}
		}
	}
}

// Markdown renders the results as one table, a row per run.
func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# orch bench\n\n%s · orch %s · %s/%s · project `%s`\n\n", r.Date, r.OrchVersion, r.OS, r.Arch, r.Project)
	b.WriteString("| provider | run | outcome | done | blocked | unfinished | dispatches | retries | wall | agent time | cost (USD) | cost source | tokens (raw) | tokens (weighted) | CLI |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, x := range r.Results {
		fmt.Fprintf(&b, "| %s | %d | %s | %d/%d | %d | %d | %d | %d | %s | %s | %.2f | %s | %d | %d | %s |\n",
			x.Provider, x.Run, x.Outcome, x.Done, x.Tasks, x.Blocked, x.Unfinished, x.Dispatches, x.Retries,
			seconds(x.WallS), seconds(x.AgentS), x.CostUSD, x.CostSource,
			x.TokensIn+x.TokensOut, x.WeightedTokens, x.CLIVersion)
	}
	return b.String()
}

func seconds(s float64) string {
	return (time.Duration(s * float64(time.Second))).Round(time.Second).String()
}
