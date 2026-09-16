// Package demo builds the synthetic project `orch dashboard --demo` serves.
//
// It exists so the dashboard can be looked at without a real run: screenshots
// for docs and PRs, a first look before installing a provider, a fixture for
// the UI checks. Nothing here dispatches or calls a provider CLI.
//
// The project is written through the same paths a real one takes — scaffold
// for the files, state.Backend for the history — so a schema or config change
// breaks the demo loudly instead of leaving it showing a shape the product no
// longer has.
package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/scaffold"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Name is the demo project's name, also its id.
const Name = "acme-billing-api"

// RunID is the one run the demo's history belongs to.
const RunID = "demo-run"

const (
	sonnet   = "claude/claude-sonnet-4-6"
	opus     = "claude/claude-opus-4-7"
	codex    = "codex/gpt-5"
	opencode = "opencode/opencode/deepseek-v4-pro"
	gemini   = "gemini/gemini-3.7-flash-medium"
)

// what a task is doing when the demo is opened.
type fate int

const (
	finished fate = iota
	running
	failedOut // blocked by the engine after its retries ran out
	heldBack  // blocked by a person, with a reason
	queued    // todo
	parked    // backlog
)

type spec struct {
	id, title, model string
	phase            int
	deps             []string
	est              float64
	fate             fate
}

var phases = []string{"Foundation", "Accounts & auth", "Billing core", "Public API", "Launch"}

// tasks is in dependency order: every task's deps appear above it, which is
// also the order the finished ones finish in.
var tasks = []spec{
	{"F0.T1", "Repository layout and CI skeleton", sonnet, 0, nil, 1, finished},
	{"F0.T2", "Postgres schema and migrations", sonnet, 0, []string{"F0.T1"}, 2, finished},
	{"F0.T3", "Config and secrets loading", codex, 0, []string{"F0.T1"}, 1, finished},
	{"F0.T4", "Structured logging with request ids", opencode, 0, []string{"F0.T1"}, 1, finished},
	{"F1.T1", "User model and signup endpoint", sonnet, 1, []string{"F0.T2"}, 2, finished},
	{"F1.T2", "Password hashing and login", sonnet, 1, []string{"F1.T1"}, 1.5, finished},
	{"F1.T3", "Session tokens with rotation", sonnet, 1, []string{"F1.T2"}, 2, finished},
	{"F1.T4", "Organizations and team membership", sonnet, 1, []string{"F1.T1"}, 3, finished},
	{"F1.T5", "Role-based access middleware", opus, 1, []string{"F1.T3", "F1.T4"}, 2.5, finished},
	{"F1.T6", "Email verification flow", gemini, 1, []string{"F1.T1"}, 1.5, finished},
	{"F1.T7", "Password reset flow", opencode, 1, []string{"F1.T2"}, 1.5, finished},
	{"F2.T1", "Plans and prices catalog", sonnet, 2, []string{"F0.T2"}, 2, finished},
	{"F2.T2", "Stripe customer sync", sonnet, 2, []string{"F1.T4", "F2.T1"}, 3, finished},
	{"F2.T7", "Usage metering ingestion", codex, 2, []string{"F2.T1"}, 3, finished},
	{"F2.T3", "Subscription lifecycle", opus, 2, []string{"F2.T2"}, 4, finished},
	{"F3.T1", "API keys: issue and revoke", sonnet, 3, []string{"F1.T5"}, 2, finished},
	{"F2.T4", "Invoice generation", sonnet, 2, []string{"F2.T3"}, 3, running},
	{"F2.T6", "Proration on plan change", codex, 2, []string{"F2.T3"}, 2.5, running},
	{"F3.T2", "Rate limiting per API key", opencode, 3, []string{"F3.T1"}, 2, running},
	{"F2.T5", "Stripe webhook handler", sonnet, 2, []string{"F2.T2"}, 2.5, failedOut},
	{"F4.T1", "Sandbox environment for the client", sonnet, 4, []string{"F2.T3"}, 2, heldBack},
	{"F2.T8", "Dunning emails for failed payments", sonnet, 2, []string{"F2.T5"}, 2, queued},
	{"F3.T3", "OpenAPI spec and contract tests", sonnet, 3, []string{"F3.T1"}, 3, queued},
	{"F3.T4", "Cursor pagination on list endpoints", codex, 3, []string{"F3.T1"}, 1.5, queued},
	{"F3.T5", "Outbound webhooks delivery", sonnet, 3, []string{"F2.T5", "F3.T1"}, 3, queued},
	{"F4.T2", "Load test the billing endpoints", codex, 4, []string{"F2.T4", "F3.T2"}, 2, queued},
	{"F4.T3", "Status page and alerting", gemini, 4, []string{"F0.T4"}, 1.5, queued},
	{"F3.T6", "TypeScript SDK generation", sonnet, 3, []string{"F3.T3"}, 2, parked},
	{"F4.T4", "Customer docs portal", sonnet, 4, []string{"F3.T3"}, 3, parked},
	{"F4.T5", "Launch checklist and runbook", sonnet, 4, []string{"F4.T1", "F4.T2"}, 1, parked},
}

// budgetsYAML is a small, valid budgets.yaml: enough for the Budget page to
// have a preset to show, without copying the packaged file's commentary.
const budgetsYAML = `# Demo preset for orch dashboard --demo.
presets:
  conservative:
    claude:
      window_hours: 5
      token_budget: 800000
      threshold_pct: 60
    codex:
      window_hours: 5
      token_budget: 600000
      threshold_pct: 60
    opencode:
      window_hours: 5
      token_budget: 1000000
      threshold_pct: 60
`

// Build writes the demo project into root (which must be empty or absent)
// and seeds its database with a history ending at now. It returns the
// project's paths.
func Build(ctx context.Context, root string, now time.Time) (config.Paths, error) {
	if _, err := scaffold.Run(scaffold.Options{Root: root, Name: Name, Now: now}); err != nil {
		return config.Paths{}, fmt.Errorf("scaffold the demo project: %w", err)
	}
	if err := writeFiles(root, now); err != nil {
		return config.Paths{}, err
	}

	paths, err := config.ResolvePaths(root, Name, "")
	if err != nil {
		return config.Paths{}, err
	}
	res, err := config.Load(paths.ConfigYAML, paths.Root)
	if err != nil {
		return config.Paths{}, fmt.Errorf("load the demo config: %w", err)
	}
	db, _, err := state.Open(ctx, paths.SQLitePath(res.Config))
	if err != nil {
		return config.Paths{}, err
	}
	defer func() { _ = db.Close() }()

	if err := seed(ctx, state.NewSQLite(db, paths.ID, paths.Root), paths.TasksJSON(), now); err != nil {
		return config.Paths{}, fmt.Errorf("seed the demo history: %w", err)
	}
	return paths, nil
}

func writeFiles(root string, now time.Time) error {
	tf := model.TasksFile{
		Meta: model.Meta{
			Project:     Name,
			GeneratedAt: now.UTC().Format("2006-01-02"),
			Note:        "Synthetic project generated by `orch dashboard --demo`. Nothing here ran.",
		},
	}
	for i, name := range phases {
		tf.Phases = append(tf.Phases, model.Phase{ID: i, Name: name})
	}
	models := map[string]bool{}
	for _, s := range tasks {
		status := model.StatusTodo
		if s.fate == parked {
			status = model.StatusBacklog
		}
		tf.Tasks = append(tf.Tasks, model.Task{
			ID: s.id, Phase: s.phase, Title: s.title, Model: s.model,
			Status: status, Dependencies: s.deps, EstimateHours: s.est,
			Description: s.title + ".",
		})
		models[s.model] = true
	}
	if err := model.SaveTasksFile(filepath.Join(root, "tasks.json"), tf); err != nil {
		return fmt.Errorf("write the demo tasks.json: %w", err)
	}

	keys := make([]string, 0, len(models))
	for k := range models {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if _, _, err := router.AddMissing(filepath.Join(root, ".orchestrator", "model_router.yaml"), keys, model.TierStandard); err != nil {
		return fmt.Errorf("route the demo models: %w", err)
	}

	if err := os.WriteFile(filepath.Join(root, ".orchestrator", "budgets.yaml"), []byte(budgetsYAML), 0o600); err != nil {
		return fmt.Errorf("write the demo budgets.yaml: %w", err)
	}

	// The demo's content is English; the scaffolded default summary language
	// is Spanish.
	cfgPath := filepath.Join(root, ".orchestrator", "config.yaml")
	// #nosec G304 -- cfgPath is the config.yaml scaffold.Run just wrote under root
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	patched := strings.Replace(string(body), "summary_language: es", "summary_language: en", 1)
	return os.WriteFile(cfgPath, []byte(patched), 0o600)
}

// seed replays a plausible ten days of work through the backend's own writes.
func seed(ctx context.Context, b state.Backend, tasksJSON string, now time.Time) error {
	tf, err := model.LoadTasksFile(tasksJSON)
	if err != nil {
		return err
	}
	if err := b.Bootstrap(ctx, tf.Tasks); err != nil {
		return err
	}
	if err := b.StartRun(ctx, RunID, "auto"); err != nil {
		return err
	}
	s := seeder{ctx: ctx, b: b}

	var done []spec
	for _, t := range tasks {
		if t.fate == finished {
			done = append(done, t)
		}
	}
	// Finishes spread over the last ten days, oldest first, so the 7-day
	// velocity and the ETA have something to work with.
	span := 10 * 24 * time.Hour
	for i, t := range done {
		end := now.Add(-span + time.Duration(i)*span/time.Duration(len(done))).Add(time.Hour)
		dur := time.Duration(6+int(t.est*4)) * time.Minute
		s.move(t.id, model.StatusInProgress, "orch", "", end.Add(-dur))
		s.dispatch(t, end.Add(-dur), 1)
		s.succeed(t, end, dur)
		s.move(t.id, model.StatusDone, "orch", "", end)
		// Billing and API work went through pull requests.
		if t.phase >= 2 {
			url := fmt.Sprintf("https://github.com/acme/billing-api/pull/%d", 40+i)
			s.err(b.SetTaskPR(ctx, t.id, url))
			s.err(b.SetTaskCIStatus(ctx, t.id, "success"))
			s.event("pr_created", t, end, map[string]any{"pr_url": url})
			s.event("ci_success", t, end.Add(4*time.Minute), map[string]any{"pr_url": url})
			s.event("pr_merged", t, end.Add(9*time.Minute), map[string]any{"pr_url": url})
		}
	}

	for _, t := range tasks {
		switch t.fate {
		case failedOut:
			// Inside the budget window, so the Budget page shows claude in use.
			start := now.Add(-4*time.Hour - 30*time.Minute)
			reason := "tests/test_webhooks.py::test_signature_replay FAILED: signature timestamp outside tolerance"
			s.move(t.id, model.StatusInProgress, "orch", "", start)
			for attempt := 1; attempt <= 3; attempt++ {
				at := start.Add(time.Duration(attempt-1) * 40 * time.Minute)
				s.dispatch(t, at, attempt)
				s.fail(t, at.Add(18*time.Minute), 18*time.Minute, attempt, reason)
				if attempt < 3 {
					s.event("retry", t, at.Add(19*time.Minute), map[string]any{
						"attempt": attempt + 1, "reason": reason, "cli_model": cliModel(t.model), "failure_class": "tests",
					})
				}
			}
			blockedAt := start.Add(99 * time.Minute)
			why := "3 attempts failed: webhook signature tests keep failing"
			s.event("block", t, blockedAt, map[string]any{"reason": why})
			s.move(t.id, model.StatusBlocked, "orch", why, blockedAt)
		case heldBack:
			s.move(t.id, model.StatusBlocked, "operator",
				"Waiting on Stripe live-mode keys from the client", now.Add(-3*time.Hour))
		}
	}

	// Three agents working right now, one of them on its second attempt.
	ago := []time.Duration{6*time.Minute + 41*time.Second, 2*time.Minute + 10*time.Second, 37 * time.Second}
	i := 0
	for _, t := range tasks {
		if t.fate != running {
			continue
		}
		start := now.Add(-ago[i])
		attempt := 1
		if t.model == codex {
			attempt = 2
			first := start.Add(-25 * time.Minute)
			s.dispatch(t, first, 1)
			s.fail(t, first.Add(20*time.Minute), 20*time.Minute, 1, "proration test expected 1650 cents, got 1649")
			s.event("retry", t, first.Add(21*time.Minute), map[string]any{
				"attempt": 2, "reason": "proration rounding", "cli_model": cliModel(t.model), "failure_class": "tests",
			})
		}
		s.move(t.id, model.StatusInProgress, "orch", "", start)
		s.dispatch(t, start, attempt)
		s.err(b.RecordDispatch(ctx, state.Dispatch{
			RunID: RunID, TaskID: t.id, Backend: backend(t.model), PID: os.Getpid(),
			StartedAt: stamp(start), Attempt: attempt,
		}))
		i++
	}
	return s.first
}

// seeder keeps the first error so seed reads as the story it tells.
type seeder struct {
	ctx   context.Context
	b     state.Backend
	first error
}

func (s *seeder) err(err error) {
	if s.first == nil && err != nil {
		s.first = err
	}
}

func (s *seeder) move(id string, to model.Status, author, body string, at time.Time) {
	s.err(s.b.Transition(s.ctx, id, to, state.Note{Author: author, Body: body, At: at}))
}

func (s *seeder) event(kind string, t spec, at time.Time, extra map[string]any) {
	s.err(s.b.AppendEvent(s.ctx, RunID, state.Event{
		RunID: RunID, EventType: kind, TaskID: t.id, Backend: backend(t.model), TS: stamp(at), Extra: extra,
	}))
}

func (s *seeder) dispatch(t spec, at time.Time, attempt int) {
	s.event("dispatch", t, at, map[string]any{"pid": 40000 + attempt, "cli_model": cliModel(t.model), "attempt": attempt})
}

func (s *seeder) succeed(t spec, at time.Time, dur time.Duration) {
	cost := s.spend(t, at, dur)
	s.event("success", t, at, map[string]any{"attempt": 1, "cost_usd": cost, "duration_s": dur.Seconds()})
}

func (s *seeder) fail(t spec, at time.Time, dur time.Duration, attempt int, reason string) {
	s.spend(t, at, dur)
	s.event("fail", t, at, map[string]any{"attempt": attempt, "reason": reason, "exit_code": 1, "failure_class": "tests"})
}

// spend records what each provider really reports: claude a dollar figure,
// codex tokens only (the dashboard prices them), opencode an estimate,
// gemini nothing.
func (s *seeder) spend(t spec, at time.Time, dur time.Duration) float64 {
	in := 18000 + int(t.est*9000)
	out := 2500 + int(t.est*1400)
	row := state.Spend{TS: stamp(at), TaskID: t.id, Backend: backend(t.model), Model: cliModel(t.model), DurationS: dur.Seconds()}
	switch backend(t.model) {
	case "claude":
		row.TokensIn, row.TokensOut = in, out
		row.CostUSD = float64(in)*3/1e6 + float64(out)*15/1e6
		if t.model == opus {
			row.CostUSD *= 5
		}
	case "codex":
		row.TokensIn, row.TokensOut = in, out
	case "opencode":
		row.TokensIn, row.TokensOut, row.Estimated = in, out, true
	}
	s.err(s.b.RecordSpend(s.ctx, row))
	return row.CostUSD
}

func backend(key string) string {
	b, _, _ := strings.Cut(key, "/")
	return b
}

func cliModel(key string) string {
	_, m, _ := strings.Cut(key, "/")
	return m
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }
