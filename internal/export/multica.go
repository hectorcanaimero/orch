// Package export sends an orch project's work out to another tracker.
//
// It is `orch sync` in the other direction, and deliberately a separate verb:
// sync reads a tracker and writes tasks.json, and promises never to write to
// the tracker. Export reads tasks and writes to the tracker, and never writes
// to tasks.json or the state database.
//
// Imports model and graph. The tasks it receives are already hydrated by the
// caller (internal/cli, rule 30), so it never opens the database itself.
//
// # Multica
//
// Everything this file assumes about Multica was read from its source, not
// from its docs or guessed (github.com/multica-ai/multica):
//
//   - server/cmd/multica/cmd_issue.go: `issue create` takes --title,
//     --description-stdin, --status, --parent (an issue id or key), --stage
//     (>=1, only meaningful under a parent) and --project, and prints the
//     created issue as JSON by default (`id`, `identifier`, …). `issue list
//     --output json` prints {"issues": [...], "has_more": bool}, pages with
//     --limit (1..100) and --offset, and --fields narrows each issue to named
//     keys (id, identifier and description among them).
//   - server/internal/service/builtin_skills/multica-platform/references/
//     issues.md: a sub-issue created in `todo` enqueues its agent at once,
//     `backlog` parks it; stages are barrier groups whose completion wakes the
//     parent's assignee, and promoting the next stage is left to whoever reads
//     the sub-issues' descriptions.
//   - server/internal/service/issue.go: creating an issue whose (workspace,
//     project, parent, title) matches an active one is refused with 409, so a
//     blind re-run would fail rather than duplicate — the markers below are
//     what make a re-run skip instead.
//   - server/cmd/multica/cmd_agent.go: without a workspace the CLI says to run
//     `multica config set workspace_id <id>`; server/cmd/multica/main.go exits
//     non-zero with the message on stderr.
//
// # The mapping, and what it loses
//
// Multica has no issue dependencies, only sub-issues grouped into ordered
// stages under a parent. So each orch phase becomes one parent issue, each
// task a sub-issue of its phase's parent, and a task's stage is its depth in
// the dependency graph restricted to what this export creates in that phase.
// A stage is coarser than a DAG: stage 3 waits for all of stage 2, not only
// for the tasks it depends on. The exact dependencies go into each
// sub-issue's description, which is what Multica's own guide says to read
// before promoting one.
//
// Everything is created in `backlog`, so nothing Multica runs starts on its
// own.
package export

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
)

// MulticaBinary is the CLI export shells out to.
const MulticaBinary = "multica"

// multicaStatus is the status every exported issue is created with. Not a
// flag: `todo` would start an assigned agent the moment the issue exists
// (see the package doc), which is not a decision an export should make.
const multicaStatus = "backlog"

// multicaTimeout bounds one multica invocation (checklist rule 18). Each call
// is one HTTP request on Multica's side; two minutes is generous.
const multicaTimeout = 2 * time.Minute

// multicaPageSize is the largest page `issue list` accepts
// (issueListMaxPageSize in cmd_issue.go).
const multicaPageSize = 100

// Selection narrows which tasks are exported. The zero value exports every
// task that is not done.
type Selection struct {
	// Phases, when non-empty, keeps only tasks in these phases.
	Phases []int
	// Only, when set, is a glob on the task id — the same `*`, `?`, `[...]`
	// `orch run --only` accepts. A malformed pattern matches nothing.
	Only string
}

func (s Selection) keeps(t model.Task) bool {
	if len(s.Phases) > 0 {
		found := false
		for _, p := range s.Phases {
			if p == t.Phase {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if s.Only != "" {
		ok, err := path.Match(s.Only, t.ID)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// IssueRef is a Multica issue as the CLI names it: the UUID `--parent`
// resolves, and the human key (`MUL-123`) people read.
type IssueRef struct {
	ID  string
	Key string
}

// MulticaPlan is everything an export would create, worked out before any
// call to Multica is made that writes. `--dry-run` prints it; a real run
// prints the same thing and then walks it.
type MulticaPlan struct {
	// ProjectID namespaces the markers, so two orch projects exporting into
	// one Multica workspace (both with an F1.1.T1) never mistake each other's
	// issues for their own.
	ProjectID string
	// SpecRoot is prefixed to a task's specRef in its description.
	SpecRoot string
	Phases   []MulticaPhase
	// SkippedDone are tasks left out because orch already has them done.
	SkippedDone []string
	// Keys maps an orch task id to the Multica key of its issue, for every
	// task whose issue exists already or has been created by this run. The
	// descriptions read it to name dependencies by their Multica key.
	Keys map[string]string
}

// MulticaPhase is one parent issue and the sub-issues under it.
type MulticaPhase struct {
	Number   int
	Title    string
	Existing *IssueRef
	Tasks    []MulticaTask
}

// MulticaTask is one sub-issue.
type MulticaTask struct {
	Task  model.Task
	Stage int
	// Existing is set when an issue carrying this task's marker is already
	// in Multica; such a task is skipped, never updated.
	Existing *IssueRef
}

// Empty reports whether the plan has nothing in it at all.
func (p MulticaPlan) Empty() bool { return len(p.Phases) == 0 }

// PlanMultica works out the parents, sub-issues and stages for tasks.
//
// tasks must be hydrated (rule 30): a task's done-ness decides whether it is
// exported, and tasks.json's own status stopped being the real one at F-12.
// A dependency cycle is refused outright — no depth can be assigned to a task
// on a cycle, and Multica would be handed an order nobody can follow.
func PlanMultica(projectID, specRoot string, tasks []model.Task, sel Selection, phaseTitles map[int]string) (MulticaPlan, error) {
	if cycles := graph.FindCycles(tasks); len(cycles) > 0 {
		paths := make([]string, len(cycles))
		for i, c := range cycles {
			paths[i] = strings.Join(c, " -> ")
		}
		return MulticaPlan{}, fmt.Errorf("export: tasks.json has dependency cycles (%s); run `orch validate`",
			strings.Join(paths, "; "))
	}

	plan := MulticaPlan{ProjectID: projectID, SpecRoot: specRoot, Keys: map[string]string{}}
	byPhase := map[int][]model.Task{}
	for _, t := range tasks {
		if !sel.keeps(t) {
			continue
		}
		if t.Status == model.StatusDone {
			plan.SkippedDone = append(plan.SkippedDone, t.ID)
			continue
		}
		byPhase[t.Phase] = append(byPhase[t.Phase], t)
	}

	numbers := make([]int, 0, len(byPhase))
	for n := range byPhase {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)

	for _, n := range numbers {
		phaseTasks := byPhase[n]
		stages := stagesFor(phaseTasks)
		rows := make([]MulticaTask, len(phaseTasks))
		for i, t := range phaseTasks {
			rows[i] = MulticaTask{Task: t, Stage: stages[t.ID]}
		}
		// Stage first, declaration order within a stage: a task's
		// same-phase dependencies are always in a lower stage, so walking in
		// this order creates every dependency before the task that names it.
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Stage < rows[j].Stage })

		title := fmt.Sprintf("F%d", n)
		if name := strings.TrimSpace(phaseTitles[n]); name != "" {
			title = fmt.Sprintf("F%d — %s", n, name)
		}
		plan.Phases = append(plan.Phases, MulticaPhase{Number: n, Title: title, Tasks: rows})
	}
	return plan, nil
}

// stagesFor assigns each task in one phase its Multica stage: 1 plus the
// longest chain of dependencies inside that same set.
//
// Only a dependency that is itself in the set raises a task's stage. One in
// another phase belongs to another parent, where a stage means nothing; one
// that is done in orch, filtered out by --phase/--only, or unknown is not
// being created, so nothing in this parent waits for it. All of those are
// still named in the task's description.
//
// The caller has refused cycles already, so the recursion terminates; the
// visiting guard only keeps a bug elsewhere from becoming a stack overflow.
func stagesFor(tasks []model.Task) map[string]int {
	inSet := make(map[string]model.Task, len(tasks))
	for _, t := range tasks {
		inSet[t.ID] = t
	}
	depth := map[string]int{}
	visiting := map[string]bool{}
	var walk func(id string) int
	walk = func(id string) int {
		if d, ok := depth[id]; ok {
			return d
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		d := 0
		for _, dep := range inSet[id].Dependencies {
			if _, ok := inSet[dep]; !ok {
				continue
			}
			if dd := walk(dep) + 1; dd > d {
				d = dd
			}
		}
		visiting[id] = false
		depth[id] = d
		return d
	}
	out := make(map[string]int, len(tasks))
	for _, t := range tasks {
		out[t.ID] = walk(t.ID) + 1
	}
	return out
}

// LossNote is printed once per export: the one thing about the mapping an
// operator must know before trusting Multica's order.
const LossNote = "Multica has no issue dependencies, only ordered stages under a parent. " +
	"A task's stage is its dependency depth, so it waits for the whole previous stage, " +
	"not only for its own dependencies; the exact dependencies are in each sub-issue's description."

// taskMarker and phaseMarker are the lines an exported issue's description
// ends with. A re-run lists Multica's issues, finds these, and skips what
// exists — see Resolve.
func taskMarker(projectID, taskID string) string {
	return fmt.Sprintf("orch-task: %s/%s", projectID, taskID)
}

func phaseMarker(projectID string, phase int) string {
	return fmt.Sprintf("orch-phase: %s/%d", projectID, phase)
}

var markerLine = regexp.MustCompile(`(?m)^(orch-(?:task|phase): \S+)\s*$`)

// PhaseDescription is the body of a phase's parent issue.
func (p MulticaPlan) PhaseDescription(ph MulticaPhase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Phase F%d of orch project `%s`, exported by `orch export multica`.\n\n", ph.Number, p.ProjectID)
	b.WriteString(LossNote)
	b.WriteString(" Promote a sub-issue only once the dependencies it lists are done.\n\n")
	b.WriteString(phaseMarker(p.ProjectID, ph.Number))
	b.WriteString("\n")
	return b.String()
}

// TaskDescription is the body of a task's sub-issue: the task's own
// description, then what orch knows about it that Multica has no field for.
func (p MulticaPlan) TaskDescription(t MulticaTask) string {
	var b strings.Builder
	if d := strings.TrimSpace(t.Task.Description); d != "" {
		b.WriteString(d)
		b.WriteString("\n\n")
	}
	b.WriteString("---\n")
	if len(t.Task.Dependencies) > 0 {
		deps := make([]string, len(t.Task.Dependencies))
		for i, dep := range t.Task.Dependencies {
			if key := p.Keys[dep]; key != "" {
				deps[i] = fmt.Sprintf("%s (%s)", dep, key)
			} else {
				// Done in orch and never exported, filtered out of this
				// export, or in a later phase this run has not reached yet.
				deps[i] = fmt.Sprintf("%s (not in Multica yet)", dep)
			}
		}
		fmt.Fprintf(&b, "Depends on: %s\n", strings.Join(deps, ", "))
	} else {
		b.WriteString("Depends on: nothing\n")
	}
	if t.Task.SpecRef != "" {
		fmt.Fprintf(&b, "Spec: `%s`\n", specPath(p.SpecRoot, t.Task.SpecRef))
	}
	if t.Task.Model != "" {
		fmt.Fprintf(&b, "Model: `%s`\n", t.Task.Model)
	}
	if t.Task.EstimateHours > 0 {
		fmt.Fprintf(&b, "Estimate: %sh\n", strconv.FormatFloat(t.Task.EstimateHours, 'f', -1, 64))
	}
	b.WriteString(taskMarker(p.ProjectID, t.Task.ID))
	b.WriteString("\n")
	return b.String()
}

// TaskTitle is a sub-issue's title. The orch id leads so the two trackers can
// be matched by eye, and so Multica's duplicate guard (same title under the
// same parent) can never collide two different tasks.
func TaskTitle(t model.Task) string {
	return fmt.Sprintf("%s — %s", t.ID, t.Title)
}

func specPath(specRoot, ref string) string {
	if specRoot == "" || strings.HasPrefix(ref, specRoot+"/") {
		return ref
	}
	return specRoot + "/" + ref
}

// ---- phase titles ------------------------------------------------------

// PhaseTitles reads each phase's title from the `# F<n> — <title>` header of
// the specs its tasks point at (project.SpecOutline). tasks.json keeps no
// phase names, and "F1" in a tracker says nothing to the people reading it
// there. A phase with no title falls back to "F<n>" at the call site.
func PhaseTitles(projectRoot, specRoot string, tasks []model.Task) (map[int]string, error) {
	outline, err := project.SpecOutline(projectRoot, specRoot, tasks)
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	return outline.Phases, nil
}

// ---- talking to the CLI -----------------------------------------------

// Runner runs one multica invocation with stdin and returns its stdout. The
// default is NewExecRunner; tests substitute a function.
type Runner func(ctx context.Context, stdin string, args ...string) (string, error)

// MulticaError is a multica invocation that did not produce an answer.
type MulticaError struct {
	// Reason is "not found in PATH" or "exited non-zero".
	Reason string
	Args   []string
	Stderr string
	Err    error
}

func (e *MulticaError) Error() string {
	cmd := strings.Join(append([]string{MulticaBinary}, firstArgs(e.Args, 2)...), " ")
	switch {
	case e.Reason == reasonNotFound:
		return "export: the multica CLI is not on PATH — install it (https://multica.ai/docs/cli), " +
			"then `multica login` and `multica config set workspace_id <id>`"
	case e.Stderr != "":
		return fmt.Sprintf("export: `%s` %s: %s", cmd, e.Reason, truncate(strings.TrimSpace(e.Stderr), 600))
	case e.Err != nil:
		return fmt.Sprintf("export: `%s` %s: %v", cmd, e.Reason, e.Err)
	default:
		return fmt.Sprintf("export: `%s` %s", cmd, e.Reason)
	}
}

func (e *MulticaError) Unwrap() error { return e.Err }

const (
	reasonNotFound = "not found in PATH"
	reasonExit     = "exited non-zero"
)

// IsNotFound reports whether err is the multica CLI missing from PATH.
func IsNotFound(err error) bool {
	var me *MulticaError
	return errors.As(err, &me) && me.Reason == reasonNotFound
}

// NewExecRunner runs the multica binary for real: in dir, in its own process
// group, under a deadline (checklist rule 18).
func NewExecRunner(dir string) Runner {
	return func(ctx context.Context, stdin string, args ...string) (string, error) {
		if _, err := exec.LookPath(MulticaBinary); err != nil {
			return "", &MulticaError{Reason: reasonNotFound, Args: args, Err: err}
		}
		ctx, cancel := context.WithTimeout(ctx, multicaTimeout)
		defer cancel()
		// #nosec G204 -- a fixed binary name; args are this package's own
		// subcommands plus task titles, which exec passes as argv, not shell.
		cmd := exec.CommandContext(ctx, MulticaBinary, args...)
		cmd.Dir = dir
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return "", &MulticaError{Reason: reasonExit, Args: args, Stderr: stderr.String(), Err: err}
		}
		return stdout.String(), nil
	}
}

// Multica is one Multica destination: which project issues go into, and how
// the CLI is run.
type Multica struct {
	// Project is the Multica project (id or key) passed as --project, or ""
	// for none.
	Project string
	Run     Runner
}

// Resolve marks every phase and task in plan whose issue already exists in
// Multica, by listing the workspace's issues and reading the markers in their
// descriptions. Read-only; a dry run calls it too.
func (m Multica) Resolve(ctx context.Context, plan *MulticaPlan) error {
	existing, err := m.existing(ctx)
	if err != nil {
		return err
	}
	for i := range plan.Phases {
		ph := &plan.Phases[i]
		if ref, ok := existing[phaseMarker(plan.ProjectID, ph.Number)]; ok {
			ph.Existing = &ref
		}
		for j := range ph.Tasks {
			if ref, ok := existing[taskMarker(plan.ProjectID, ph.Tasks[j].Task.ID)]; ok {
				ph.Tasks[j].Existing = &ref
			}
		}
	}
	// Tasks this export does not include (done in orch, another phase) may
	// have been exported by an earlier run; their keys still belong in the
	// descriptions of what depends on them.
	prefix := "orch-task: " + plan.ProjectID + "/"
	for marker, ref := range existing {
		if id, ok := strings.CutPrefix(marker, prefix); ok {
			plan.Keys[id] = ref.Key
		}
	}
	return nil
}

func (m Multica) existing(ctx context.Context) (map[string]IssueRef, error) {
	found := map[string]IssueRef{}
	for offset := 0; ; {
		args := []string{"issue", "list", "--output", "json",
			"--limit", strconv.Itoa(multicaPageSize), "--offset", strconv.Itoa(offset),
			"--fields", "id,identifier,description"}
		if m.Project != "" {
			args = append(args, "--project", m.Project)
		}
		out, err := m.Run(ctx, "", args...)
		if err != nil {
			return nil, err
		}
		var page struct {
			Issues []struct {
				ID          string `json:"id"`
				Identifier  string `json:"identifier"`
				Description string `json:"description"`
			} `json:"issues"`
			HasMore bool `json:"has_more"`
		}
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			return nil, fmt.Errorf("export: parsing `multica issue list` output: %w", err)
		}
		for _, is := range page.Issues {
			for _, mm := range markerLine.FindAllStringSubmatch(is.Description, -1) {
				found[mm[1]] = IssueRef{ID: is.ID, Key: is.Identifier}
			}
		}
		if !page.HasMore || len(page.Issues) == 0 {
			return found, nil
		}
		offset += len(page.Issues)
	}
}

// Created is one issue an Apply made.
type Created struct {
	// TaskID is the orch task id, or "" for a phase parent.
	TaskID string
	Phase  int
	Stage  int
	Title  string
	Ref    IssueRef
}

// Apply creates, in order, every parent and sub-issue plan does not already
// have: each phase's parent first, then its sub-issues stage by stage, so a
// parent exists before its children and a dependency's key is known before
// the description that names it is written.
//
// It stops at the first failure. What was created before it stays, and
// onCreate has already reported it; the markers make the next run pick up
// where this one stopped rather than duplicate.
func (m Multica) Apply(ctx context.Context, plan *MulticaPlan, onCreate func(Created)) error {
	for i := range plan.Phases {
		ph := &plan.Phases[i]
		parent := ph.Existing
		if parent == nil {
			ref, err := m.create(ctx, ph.Title, plan.PhaseDescription(*ph), "", 0)
			if err != nil {
				return fmt.Errorf("creating the parent issue for phase F%d: %w", ph.Number, err)
			}
			parent = &ref
			ph.Existing = &ref
			onCreate(Created{Phase: ph.Number, Title: ph.Title, Ref: ref})
		}
		for j := range ph.Tasks {
			t := &ph.Tasks[j]
			if t.Existing != nil {
				continue
			}
			title := TaskTitle(t.Task)
			ref, err := m.create(ctx, title, plan.TaskDescription(*t), parent.ID, t.Stage)
			if err != nil {
				return fmt.Errorf("creating the sub-issue for %s: %w", t.Task.ID, err)
			}
			t.Existing = &ref
			plan.Keys[t.Task.ID] = ref.Key
			onCreate(Created{TaskID: t.Task.ID, Phase: ph.Number, Stage: t.Stage, Title: title, Ref: ref})
		}
	}
	return nil
}

func (m Multica) create(ctx context.Context, title, description, parentID string, stage int) (IssueRef, error) {
	args := []string{"issue", "create", "--title", title, "--description-stdin",
		"--status", multicaStatus, "--output", "json"}
	if parentID != "" {
		args = append(args, "--parent", parentID, "--stage", strconv.Itoa(stage))
	}
	if m.Project != "" {
		args = append(args, "--project", m.Project)
	}
	out, err := m.Run(ctx, description, args...)
	if err != nil {
		return IssueRef{}, err
	}
	var created struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		return IssueRef{}, fmt.Errorf("export: parsing `multica issue create` output: %w", err)
	}
	if created.ID == "" {
		// Without the id there is no parent to hang children off, and the
		// issue did get created — say so rather than carry on with "".
		return IssueRef{}, fmt.Errorf("export: `multica issue create` answered without an id (output: %s)",
			truncate(strings.TrimSpace(out), 200))
	}
	return IssueRef{ID: created.ID, Key: created.Identifier}, nil
}

func firstArgs(args []string, n int) []string {
	if len(args) < n {
		return args
	}
	return args[:n]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
