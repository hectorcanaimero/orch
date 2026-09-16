// Package mcp serves orch's state to an agent as MCP tools over stdio.
//
// # Why this exists
//
// Until now an agent reported back by shelling out: `scripts/task-finish.sh`
// runs `orch task-status … done`, and the dispatch prompt says so in as many
// words. That works, but it is a one-way pipe — the agent can *write* a status
// and nothing else. It cannot ask what its dependencies decided, what its own
// spec ref is, how much budget is left, or what the run has been doing. Every
// one of those answers already exists behind `state.Backend`; the scripts just
// have no way to ask for them.
//
// These tools are that surface. The three the scripts already cover
// (`orch_set_status`, `orch_block`) keep exactly the shape the scripts have, so
// G6.6 can offer MCP first and fall back to the scripts without either side
// learning a new vocabulary:
//
//	scripts/task-start.sh  ID                      -> orch_set_status{task_id, status:"in-progress"}
//	scripts/task-finish.sh ID "summary" "author"   -> orch_set_status{task_id, status:"done", note, author}
//	scripts/task-block.sh  ID "reason"  "author"   -> orch_block{task_id, reason, author}
//
// The other five (`orch_list_tasks`, `orch_get_task`, `orch_budget`,
// `orch_events`, `orch_context`) are read-only and have no script equivalent
// at all. `orch_report_finding` writes nothing local: it files a GitHub issue
// about orch itself, and only when the project opted in.
//
// # One backend, no second source of truth
//
// The server shares `state.Backend` with the engine rather than opening its
// own view of the project. It has to: an agent asking "am I still
// in-progress?" while the run loop is reaping dispatches must get the run
// loop's answer. Every write goes through `Backend.Transition`, which is the
// same single-writer path `orch task-status` uses and the same one that
// enforces the legal-transition table.
//
// # Errors an agent can act on
//
// A refused transition is not a protocol error. It is a tool result with
// `IsError` set, carrying a structured payload that names the current status
// and every status it may legally move to — because the caller here is a model
// that can retry, and "illegal transition in-progress -> backlog" without the
// list is a dead end. See setStatusOut.
package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hectorcanaimero/orch/internal/explain"
	"github.com/hectorcanaimero/orch/internal/state"
)

// ServerName is the name the server reports in `initialize`. It is what an
// agent's tool list groups these tools under, so it matches the binary.
const ServerName = "orch"

// BudgetReporter is the slice of *budget.Gate that `orch_budget` and
// `orch_context` need. Aliased to explain's so there is one definition of
// "what this server needs from the guardrail" rather than two that must be
// kept identical by hand.
//
// An interface rather than the concrete gate so a project with no
// budgets.yaml can pass nil and the tool can answer "not configured" instead
// of the package having to build a disabled gate to ask.
type BudgetReporter = explain.BudgetReporter

// Options is everything the tools read.
//
// Backend is the only required field. The rest degrade: no TasksJSON means
// the DAG-shaped fields (title, description, files, spec ref, dependencies)
// come back empty rather than the server refusing to start, which is the
// right call for a tool whose whole job is answering questions about a
// project that may be mid-scaffold.
type Options struct {
	// Backend is the project's state, shared with the engine.
	Backend state.Backend
	// TasksJSON is the path to tasks.json, read for the DAG's shape.
	TasksJSON string
	// ProjectID and ProjectRoot identify the project in `orch_context`.
	ProjectID   string
	ProjectRoot string
	// SpecRoot prefixes a task's specRef. Empty means prompt.DefaultSpecRoot.
	SpecRoot string
	// Budget answers `orch_budget`. Nil means no budgets.yaml, which the
	// tool reports as disabled.
	Budget BudgetReporter
	// Version is reported in `initialize`, so an agent can tell which orch
	// it is talking to.
	Version string
	// Findings files orch_report_finding's issues. Nil means
	// `report_findings.enabled` is off: the tool is still listed, and says so.
	Findings FindingsReporter
}

// server holds the options every tool handler closes over.
type server struct {
	opts Options
}

// NewServer builds the MCP server with every tool registered.
func NewServer(opts Options) (*mcpsdk.Server, error) {
	if opts.Backend == nil {
		return nil, fmt.Errorf("mcp: Options.Backend is required")
	}
	s := &server{opts: opts}

	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    ServerName,
		Title:   "orch",
		Version: opts.Version,
	}, nil)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "orch_list_tasks",
		Description: "List the project's tasks with their live status. " +
			"Filter by status, milestone (a phase: 2 or F2) or id, or ask for `ready` to get " +
			"only the tasks whose dependencies are all done.",
	}, s.listTasks)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "orch_get_task",
		Description: "One task in full: its description, the files it may " +
			"write, its spec ref, its dependencies, its comment trail, and " +
			"the statuses it may legally move to next.",
	}, s.getTask)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "orch_set_status",
		Description: "Move a task to a new status, recording a note. This is " +
			"how you report that you started (in-progress) or finished " +
			"(done) — the equivalent of scripts/task-start.sh and " +
			"scripts/task-finish.sh. A move the transition table forbids is " +
			"refused with the list of statuses that are legal from here.",
	}, s.setStatus)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "orch_block",
		Description: "Mark a task blocked, recording why. The equivalent of " +
			"scripts/task-block.sh: use it when you cannot finish, and stop.",
	}, s.block)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "orch_budget",
		Description: "The rolling-window spend guardrail, per provider: " +
			"tokens used against the window's budget, and when a capped " +
			"provider is estimated to free up.",
	}, s.budget)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "orch_events",
		Description: "The run's event log — dispatches, finishes, retries, " +
			"CI results. For one task, or across every task.",
	}, s.events)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "orch_context",
		Description: "Where you are: the project's root, spec root and task " +
			"counts, plus — with a task_id — everything that task's dispatch " +
			"prompt carries, including what its finished dependencies said.",
	}, s.taskContext)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "orch_report_finding",
		Description: "Report something about orch itself as a GitHub issue on " +
			FindingsRepo + ": a bug (orch did something wrong or got in your way — " +
			"a wrong status, a refused tool, a confusing message), an improvement " +
			"(it works but could be clearer, faster or take fewer steps), or a " +
			"feature (a capability orch lacks — a command or flag you looked for, a " +
			"step you had to do by hand that orch could do). Only about orch, never " +
			"the project you are working on: no secrets, no project code, no " +
			"customer or project names. It searches first: a same-titled issue is " +
			"returned instead of filed, and similar ones stop the filing until you " +
			"call again with confirm_new. Report once per finding, then carry on.",
	}, s.reportFinding)

	return srv, nil
}

// Serve runs the server on stdio until ctx is cancelled or stdin closes.
func Serve(ctx context.Context, opts Options) error {
	srv, err := NewServer(opts)
	if err != nil {
		return err
	}
	if err := srv.Run(ctx, &mcpsdk.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp: serving on stdio: %w", err)
	}
	return nil
}
