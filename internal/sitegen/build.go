package sitegen

import (
	"fmt"

	"github.com/hectorcanaimero/orch/internal/atomize"
	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
)

// Result is what the playground shows for one spec.
type Result struct {
	// Summary is `orch validate`'s own count line.
	Summary string `json:"summary"`
	// Warnings are the parser's, as `orch atomize --list` prints them.
	Warnings []string `json:"warnings"`
	// Problems are `orch validate`'s findings, routes not checked: the
	// playground has no model_router.yaml.
	Problems     []graph.Problem `json:"problems"`
	Tasks        []TaskRow       `json:"tasks"`
	TotalHours   float64         `json:"total_hours"`
	CriticalPath []string        `json:"critical_path"`
	SVG          string          `json:"svg"`
}

// TaskRow is one line of `orch atomize --list`'s table.
type TaskRow struct {
	ID            string   `json:"id"`
	Phase         int      `json:"phase"`
	Title         string   `json:"title"`
	Model         string   `json:"model"`
	EstimateHours float64  `json:"estimate_hours"`
	Dependencies  []string `json:"dependencies"`
}

// Build runs a spec through the same code `orch atomize` and `orch validate`
// run: parse, merge into an empty tasks.json, validate the graph.
func Build(spec string) Result {
	parse := atomize.ParseText(spec, "spec.md", ".", "")
	merged, _ := atomize.MergeTasks(model.TasksFile{}, parse.Tasks)
	tasks := merged.Tasks

	res := Result{
		Warnings: nonNil(parse.Warnings),
		Problems: graph.Validate(tasks, nil),
		Tasks:    []TaskRow{},
		SVG:      DAGSVG(tasks),
	}
	if res.Problems == nil {
		res.Problems = []graph.Problem{}
	}
	errs := 0
	for _, p := range res.Problems {
		if p.Severity == graph.SeverityError {
			errs++
		}
	}
	res.Summary = fmt.Sprintf("%d error(s) · %d warning(s) · %d total", errs, len(res.Problems)-errs, len(res.Problems))

	for _, t := range tasks {
		res.TotalHours += t.EstimateHours
		res.Tasks = append(res.Tasks, TaskRow{
			ID: t.ID, Phase: t.Phase, Title: t.Title, Model: t.Model,
			EstimateHours: t.EstimateHours, Dependencies: nonNil(t.Dependencies),
		})
	}

	res.CriticalPath = []string{}
	if order, err := graph.TopoOrder(tasks); err == nil {
		critical := graph.CriticalPath(tasks)
		for _, id := range order {
			if critical[id] {
				res.CriticalPath = append(res.CriticalPath, id)
			}
		}
	}
	return res
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SampleSpec is the playground's starting text: the nextjs-saas template's
// six tasks written as the spec `orch atomize` would turn into them.
const SampleSpec = `# F0 — Foundation

## F0.1 — Package: scaffold

### F0.1.T1 — Next.js 14 App Router scaffold + Tailwind + shadcn/ui

- **Model**: claude/claude-sonnet-4-6
- **Estimate**: 30m
- **Reason**: Scaffolding — deterministic.

# F1 — Clerk auth

## F1.1 — Package: auth

### F1.1.T1 — Clerk auth: middleware + sign-in/sign-up routes

- **Model**: claude/claude-sonnet-4-6
- **Estimate**: 36m
- **Reason**: SDK integration — Sonnet handles it.
- **Dependencies**: F0.1.T1

### F1.1.T2 — Protected /dashboard route + server-side user lookup

- **Model**: claude/claude-sonnet-4-6
- **Estimate**: 30m
- **Reason**: One route + one Playwright test.
- **Dependencies**: F1.1.T1

# F2 — Supabase schema

## F2.1 — Package: data

### F2.1.T1 — Supabase Postgres schema + Drizzle client

- **Model**: claude/claude-sonnet-4-6
- **Estimate**: 48m
- **Reason**: SDK glue + schema — deterministic.
- **Dependencies**: F0.1.T1

### F2.1.T2 — Clerk webhook syncs users into Supabase

- **Model**: claude/claude-sonnet-4-6
- **Estimate**: 1h
- **Reason**: Webhook + DB write + one integration test.
- **Dependencies**: F1.1.T1, F2.1.T1

# F3 — Vercel deploy pipeline

## F3.1 — Package: deploy

### F3.1.T1 — Vercel deploy config + preview-on-PR

- **Model**: claude/claude-sonnet-4-6
- **Estimate**: 36m
- **Reason**: CI wiring — small.
- **Dependencies**: F0.1.T1, F2.1.T1
`
