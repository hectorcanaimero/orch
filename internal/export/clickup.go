package export

// # ClickUp
//
// `orch export clickup` mirrors the project into one ClickUp Folder: a List
// per phase, a task per orch task. Unlike the Multica export it is a
// *mirror*, not a one-shot: re-running it creates what is missing, moves
// each task to the status orch has, refreshes the custom fields orch owns and
// adds missing dependencies. It still writes only to ClickUp.
//
// It talks to ClickUp's REST API v2 with a personal token, not through the
// ClickUp MCP server: the MCP is capped at 100 calls a day, and a first
// export of a few hundred tasks with their dependencies is several hundred
// calls. The REST API is limited per minute instead, which clickUpClient
// honours by reading ClickUp's rate-limit headers.
//
// What this file assumes about ClickUp, from its public API reference
// (https://clickup.com/api):
//
//   - `Authorization: <personal token>` (pk_…), no Bearer prefix.
//   - GET /folder/{id}/list → {"lists":[{id,name}]}; POST /folder/{id}/list
//     with {name, content} creates one.
//   - GET /list/{id} → {statuses:[{status,type}]}; statuses are managed in
//     ClickUp's UI (the API cannot create them), which is why a status orch
//     needs and the List lacks is an error that names it.
//   - GET /list/{id}/field → {fields:[{id,name,type,type_config:{options}}]};
//     a drop_down value is an option id when written, an orderindex when read.
//   - GET /list/{id}/task?page=N&include_closed=true&subtasks=true
//     &include_markdown_description=true → {tasks:[…], last_page}.
//   - POST /list/{id}/task with {name, markdown_content, status,
//     time_estimate (ms), custom_fields:[{id,value}]}.
//   - PUT /task/{id} with {status}; POST /task/{id}/field/{field} with
//     {value}; POST /task/{id}/dependency with {depends_on};
//     POST /task/{id}/comment with {comment_text, notify_all}.
//   - 429 carries X-RateLimit-Reset (unix seconds); every answer carries
//     X-RateLimit-Remaining.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/state"
)

// ClickUpAPIURLEnv overrides ClickUp's API base URL. Tests point it at a fake.
const ClickUpAPIURLEnv = "ORCH_CLICKUP_API_URL"

// ClickUpTokenEnv holds the personal API token when no --token-file is given.
const ClickUpTokenEnv = "CLICKUP_TOKEN"

// DefaultClickUpAPIURL is ClickUp's REST API v2.
const DefaultClickUpAPIURL = "https://api.clickup.com/api/v2"

// clickUpTimeout bounds one HTTP request (checklist rule 18).
const clickUpTimeout = 60 * time.Second

// DefaultClickUpStatuses is the status each orch status is mirrored as. The
// names are ClickUp's defaults plus the two orch needs that ClickUp does not
// ship (backlog, blocked); --status-map overrides any of them.
var DefaultClickUpStatuses = map[model.Status]string{
	model.StatusBacklog:    "backlog",
	model.StatusTodo:       "to do",
	model.StatusInProgress: "in progress",
	model.StatusBlocked:    "blocked",
	model.StatusDone:       "complete",
}

// ---- plan ----------------------------------------------------------------

// ClickUpPlan is the project as it will be mirrored: every selected task,
// grouped by phase, with what the descriptions and comments need to know.
type ClickUpPlan struct {
	ProjectID string
	SpecRoot  string
	// Language is the language descriptions and comments are written in:
	// en, es or pt (anything else falls back to en).
	Language string
	// TimeoutMultiplier turns an estimate into the time orch lets an attempt
	// run, which the description states.
	TimeoutMultiplier float64
	Phases            []ClickUpPhase
	// Packages maps "<phase>.<package>" to the package title from the specs
	// (project.SpecOutline), for the Package field and the description.
	Packages map[string]string
	// All is every task in tasks.json, selected or not, by id: descriptions
	// name a dependency by its title even when it is outside the selection.
	All map[string]model.Task
	// Spend is the summed spend per task id.
	Spend map[string]TaskSpend
}

// ClickUpPhase is one List and the tasks in it.
type ClickUpPhase struct {
	Number int
	Title  string
	Tasks  []model.Task
}

// TaskSpend is what running a task has cost so far.
type TaskSpend struct {
	CostUSD   float64
	TokensIn  int
	TokensOut int
	DurationS float64
	Attempts  int
}

// SumSpend folds spend rows into one TaskSpend per task.
func SumSpend(rows []state.Spend) map[string]TaskSpend {
	out := map[string]TaskSpend{}
	for _, r := range rows {
		if r.TaskID == "" {
			continue
		}
		s := out[r.TaskID]
		s.CostUSD += r.CostUSD
		s.TokensIn += r.TokensIn
		s.TokensOut += r.TokensOut
		s.DurationS += r.DurationS
		s.Attempts++
		out[r.TaskID] = s
	}
	return out
}

// PlanClickUp groups the selected tasks by phase. Done tasks are kept: a
// mirror shows finished work too. tasks must be hydrated (rule 30), since the
// status is what gets mirrored.
func PlanClickUp(projectID, specRoot string, tasks []model.Task, sel Selection, outline project.Outline) (ClickUpPlan, error) {
	if cycles := graph.FindCycles(tasks); len(cycles) > 0 {
		paths := make([]string, len(cycles))
		for i, c := range cycles {
			paths[i] = strings.Join(c, " -> ")
		}
		return ClickUpPlan{}, fmt.Errorf("export: tasks.json has dependency cycles (%s); run `orch validate`",
			strings.Join(paths, "; "))
	}
	plan := ClickUpPlan{ProjectID: projectID, SpecRoot: specRoot, Packages: outline.Packages,
		All: map[string]model.Task{}, Spend: map[string]TaskSpend{}}
	byPhase := map[int][]model.Task{}
	for _, t := range tasks {
		plan.All[t.ID] = t
		if sel.keeps(t) {
			byPhase[t.Phase] = append(byPhase[t.Phase], t)
		}
	}
	numbers := make([]int, 0, len(byPhase))
	for n := range byPhase {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	for _, n := range numbers {
		title := fmt.Sprintf("F%d", n)
		if name := strings.TrimSpace(outline.Phases[n]); name != "" {
			title = fmt.Sprintf("F%d — %s", n, name)
		}
		plan.Phases = append(plan.Phases, ClickUpPhase{Number: n, Title: title, Tasks: byPhase[n]})
	}
	return plan, nil
}

// Empty reports whether the plan selects nothing.
func (p ClickUpPlan) Empty() bool { return len(p.Phases) == 0 }

// ---- the client ------------------------------------------------------------

// ClickUpClient is a minimal ClickUp REST client.
type ClickUpClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// Sleep waits; tests replace it so a rate-limit wait costs nothing.
	Sleep func(context.Context, time.Duration) error
	// Now is the clock rate-limit resets are measured against.
	Now func() time.Time
}

// NewClickUpClient builds a client for baseURL ("" means ClickUp's own).
func NewClickUpClient(baseURL, token string) *ClickUpClient {
	if baseURL == "" {
		baseURL = DefaultClickUpAPIURL
	}
	return &ClickUpClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: clickUpTimeout},
		Sleep:   sleepCtx,
		Now:     time.Now,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ClickUpError is an answer from ClickUp that was not a success.
type ClickUpError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *ClickUpError) Error() string {
	msg := fmt.Sprintf("export: ClickUp answered %d to %s %s", e.Status, e.Method, e.Path)
	if b := strings.TrimSpace(e.Body); b != "" {
		msg += ": " + truncate(b, 400)
	}
	if e.Status == http.StatusUnauthorized {
		msg += " — check the personal API token (ClickUp → Settings → Apps → API Token)"
	}
	return msg
}

// maxRateLimitRetries bounds how often one request waits out a 429.
const maxRateLimitRetries = 5

// lowRemaining is how few requests left in the window make the client wait
// for the reset before sending the next one, instead of running into a 429.
const lowRemaining = 3

func (c *ClickUpClient) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.Token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("export: %s %s: %w", method, path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("export: reading ClickUp's answer to %s %s: %w", method, path, readErr)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRateLimitRetries {
			if err := c.Sleep(ctx, c.untilReset(resp.Header, time.Minute)); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return &ClickUpError{Method: method, Path: path, Status: resp.StatusCode, Body: string(data)}
		}
		if rem, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil && rem <= lowRemaining {
			if err := c.Sleep(ctx, c.untilReset(resp.Header, 0)); err != nil {
				return err
			}
		}
		if out == nil || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("export: parsing ClickUp's answer to %s %s: %w", method, path, err)
		}
		return nil
	}
}

// untilReset is how long until X-RateLimit-Reset, or fallback when the header
// is missing or unreadable.
func (c *ClickUpClient) untilReset(h http.Header, fallback time.Duration) time.Duration {
	reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return fallback
	}
	d := time.Until(time.Unix(reset, 0))
	if c.Now != nil {
		d = time.Unix(reset, 0).Sub(c.Now())
	}
	if d < 0 {
		return 0
	}
	return d + time.Second
}

// ---- ClickUp's shapes --------------------------------------------------------

type cuList struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Statuses []cuStatus `json:"statuses"`
}

type cuStatus struct {
	Status string `json:"status"`
}

type cuField struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	TypeConfig struct {
		Options []cuOption `json:"options"`
	} `json:"type_config"`
}

type cuOption struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	OrderIndex any    `json:"orderindex"`
}

type cuTask struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	URL                 string `json:"url"`
	Description         string `json:"description"`
	MarkdownDescription string `json:"markdown_description"`
	Status              struct {
		Status string `json:"status"`
	} `json:"status"`
	CustomFields []struct {
		ID    string `json:"id"`
		Value any    `json:"value"`
	} `json:"custom_fields"`
	Dependencies []struct {
		TaskID    string `json:"task_id"`
		DependsOn string `json:"depends_on"`
	} `json:"dependencies"`
}

// ---- the mirror ----------------------------------------------------------------

// ClickUp is one destination: a Folder, and how orch statuses are named there.
type ClickUp struct {
	FolderID string
	Client   *ClickUpClient
	// Statuses maps each orch status to a ClickUp status name.
	Statuses map[model.Status]string
	DryRun   bool
}

// ActionKind names what the mirror did (or, in a dry run, would do).
type ActionKind string

const (
	ActionCreateList   ActionKind = "create-list"
	ActionCreateTask   ActionKind = "create-task"
	ActionMoveStatus   ActionKind = "status"
	ActionSetField     ActionKind = "field"
	ActionDependency   ActionKind = "dependency"
	ActionUnchanged    ActionKind = "unchanged"
	ActionListUnknown  ActionKind = "list-pending"
	ActionMissingState ActionKind = "missing-status"
)

// Action is one step, reported as it happens.
type Action struct {
	Kind   ActionKind
	Phase  int
	TaskID string
	// Detail is the human part: a list name, "to do → in progress", a field
	// name, a dependency id.
	Detail string
	URL    string
}

// Summary counts what a Sync did.
type Summary struct {
	ListsCreated, TasksCreated, StatusesMoved, FieldsSet, DependenciesAdded, Unchanged int
}

// fieldSet is the custom fields orch fills, found by name on a List. Each is
// optional: a List without one simply does not get that value.
type fieldSet struct {
	orchID, model, pkg, cost *cuField
}

var fieldNames = map[string][]string{
	"orchID": {"orch id"},
	"model":  {"model", "modelo"},
	"pkg":    {"package", "paquete", "pacote"},
	"cost":   {"cost usd", "costo usd", "custo usd", "cost", "costo", "custo"},
}

func findFields(fields []cuField) fieldSet {
	var fs fieldSet
	pick := func(key string) *cuField {
		for _, want := range fieldNames[key] {
			for i := range fields {
				if strings.EqualFold(strings.TrimSpace(fields[i].Name), want) {
					return &fields[i]
				}
			}
		}
		return nil
	}
	fs.orchID, fs.model, fs.pkg, fs.cost = pick("orchID"), pick("model"), pick("pkg"), pick("cost")
	return fs
}

var phaseListName = regexp.MustCompile(`^F(\d+)(?:\s|$|[—–-])`)

// clickUpTaskMarker is the line a task description ends with, and how a
// re-run finds the task. It is namespaced by project like Multica's.
func clickUpTaskMarker(projectID, taskID string) string {
	return fmt.Sprintf("orch-task: %s/%s", projectID, taskID)
}

// Sync mirrors plan into the Folder. It stops at the first failure: what was
// done stays, and the next run picks up from there because every task is
// found again by its marker or its orch ID field.
func (c ClickUp) Sync(ctx context.Context, plan ClickUpPlan, report func(Action)) (Summary, error) {
	var sum Summary
	var lists struct {
		Lists []cuList `json:"lists"`
	}
	if err := c.Client.do(ctx, http.MethodGet, "/folder/"+url.PathEscape(c.FolderID)+"/list?archived=false", nil, &lists); err != nil {
		return sum, err
	}
	listByPhase := map[int]cuList{}
	for _, l := range lists.Lists {
		if m := phaseListName.FindStringSubmatch(strings.TrimSpace(l.Name)); m != nil {
			n, _ := strconv.Atoi(m[1])
			if _, dup := listByPhase[n]; !dup {
				listByPhase[n] = l
			}
		}
	}

	// Every task already in any phase List, by orch id: a dependency may live
	// in a phase this run does not select.
	ids := map[string]cuTask{}
	for _, l := range listByPhase {
		tasks, err := c.listTasks(ctx, l.ID)
		if err != nil {
			return sum, err
		}
		fields, err := c.fields(ctx, l.ID)
		if err != nil {
			return sum, err
		}
		fs := findFields(fields)
		marker := regexp.MustCompile(`orch-task: ` + regexp.QuoteMeta(plan.ProjectID) + `/(\S+)`)
		for _, t := range tasks {
			if m := marker.FindStringSubmatch(t.MarkdownDescription + "\n" + t.Description); m != nil {
				ids[m[1]] = t
				continue
			}
			if fs.orchID != nil {
				if v, ok := fieldValue(t, fs.orchID.ID).(string); ok && strings.TrimSpace(v) != "" {
					ids[strings.TrimSpace(v)] = t
				}
			}
		}
	}

	for _, ph := range plan.Phases {
		list, ok := listByPhase[ph.Number]
		if !ok {
			report(Action{Kind: ActionCreateList, Phase: ph.Number, Detail: ph.Title})
			sum.ListsCreated++
			if c.DryRun {
				for _, t := range ph.Tasks {
					report(Action{Kind: ActionCreateTask, Phase: ph.Number, TaskID: t.ID, Detail: c.Statuses[normStatus(t.Status)]})
					sum.TasksCreated++
				}
				continue
			}
			if err := c.Client.do(ctx, http.MethodPost, "/folder/"+url.PathEscape(c.FolderID)+"/list",
				map[string]string{"name": ph.Title, "content": plan.PhaseContent(ph)}, &list); err != nil {
				return sum, fmt.Errorf("creating the List for phase F%d: %w", ph.Number, err)
			}
			listByPhase[ph.Number] = list
		}

		var detail cuList
		if err := c.Client.do(ctx, http.MethodGet, "/list/"+url.PathEscape(list.ID), nil, &detail); err != nil {
			return sum, err
		}
		if missing := c.missingStatuses(detail.Statuses, ph.Tasks); len(missing) > 0 {
			return sum, fmt.Errorf("export: the ClickUp List %q has no status %s; add them in ClickUp "+
				"(Space or List settings → Statuses) or map orch's statuses to existing ones with --status-map",
				list.Name, quoteJoin(missing))
		}
		fields, err := c.fields(ctx, list.ID)
		if err != nil {
			return sum, err
		}
		fs := findFields(fields)

		for _, t := range ph.Tasks {
			want := c.Statuses[normStatus(t.Status)]
			existing, found := ids[t.ID]
			if !found {
				report(Action{Kind: ActionCreateTask, Phase: ph.Number, TaskID: t.ID, Detail: want})
				sum.TasksCreated++
				if c.DryRun {
					continue
				}
				created, err := c.createTask(ctx, list.ID, plan, t, want, fs)
				if err != nil {
					return sum, fmt.Errorf("creating the ClickUp task for %s: %w", t.ID, err)
				}
				ids[t.ID] = created
				continue
			}

			changed := false
			if have := existing.Status.Status; !strings.EqualFold(have, want) {
				changed = true
				report(Action{Kind: ActionMoveStatus, Phase: ph.Number, TaskID: t.ID, Detail: have + " → " + want, URL: existing.URL})
				sum.StatusesMoved++
				if !c.DryRun {
					if err := c.Client.do(ctx, http.MethodPut, "/task/"+url.PathEscape(existing.ID), map[string]string{"status": want}, nil); err != nil {
						return sum, fmt.Errorf("moving %s to %q: %w", t.ID, want, err)
					}
					comment := map[string]any{"comment_text": plan.StatusComment(t, have, want), "notify_all": false}
					if err := c.Client.do(ctx, http.MethodPost, "/task/"+url.PathEscape(existing.ID)+"/comment", comment, nil); err != nil {
						return sum, fmt.Errorf("commenting on %s: %w", t.ID, err)
					}
				}
			}
			for _, fv := range plan.fieldValues(t, fs) {
				if sameFieldValue(fieldValue(existing, fv.field.ID), fv) {
					continue
				}
				changed = true
				report(Action{Kind: ActionSetField, Phase: ph.Number, TaskID: t.ID, Detail: fv.field.Name})
				sum.FieldsSet++
				if !c.DryRun {
					if err := c.Client.do(ctx, http.MethodPost, "/task/"+url.PathEscape(existing.ID)+"/field/"+url.PathEscape(fv.field.ID),
						map[string]any{"value": fv.value}, nil); err != nil {
						return sum, fmt.Errorf("setting %q on %s: %w", fv.field.Name, t.ID, err)
					}
				}
			}
			if !changed {
				sum.Unchanged++
				report(Action{Kind: ActionUnchanged, Phase: ph.Number, TaskID: t.ID})
			}
		}
	}

	// Dependencies last, once every task has an id to point at.
	for _, ph := range plan.Phases {
		for _, t := range ph.Tasks {
			cu, ok := ids[t.ID]
			if !ok {
				continue // dry run: not created
			}
			for _, dep := range t.Dependencies {
				target, ok := ids[dep]
				if !ok || hasDependency(cu, target.ID) {
					continue
				}
				report(Action{Kind: ActionDependency, Phase: ph.Number, TaskID: t.ID, Detail: dep})
				sum.DependenciesAdded++
				if c.DryRun {
					continue
				}
				if err := c.Client.do(ctx, http.MethodPost, "/task/"+url.PathEscape(cu.ID)+"/dependency",
					map[string]string{"depends_on": target.ID}, nil); err != nil {
					return sum, fmt.Errorf("making %s wait on %s: %w", t.ID, dep, err)
				}
			}
		}
	}
	return sum, nil
}

func (c ClickUp) listTasks(ctx context.Context, listID string) ([]cuTask, error) {
	var all []cuTask
	for page := 0; ; page++ {
		var resp struct {
			Tasks    []cuTask `json:"tasks"`
			LastPage *bool    `json:"last_page"`
		}
		path := fmt.Sprintf("/list/%s/task?page=%d&include_closed=true&subtasks=true&include_markdown_description=true",
			url.PathEscape(listID), page)
		if err := c.Client.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Tasks...)
		if len(resp.Tasks) == 0 || resp.LastPage == nil || *resp.LastPage {
			return all, nil
		}
	}
}

func (c ClickUp) fields(ctx context.Context, listID string) ([]cuField, error) {
	var resp struct {
		Fields []cuField `json:"fields"`
	}
	if err := c.Client.do(ctx, http.MethodGet, "/list/"+url.PathEscape(listID)+"/field", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Fields, nil
}

func (c ClickUp) missingStatuses(have []cuStatus, tasks []model.Task) []string {
	names := map[string]bool{}
	for _, s := range have {
		names[strings.ToLower(s.Status)] = true
	}
	seen := map[string]bool{}
	var missing []string
	for _, t := range tasks {
		want := c.Statuses[normStatus(t.Status)]
		if !names[strings.ToLower(want)] && !seen[want] {
			seen[want] = true
			missing = append(missing, want)
		}
	}
	sort.Strings(missing)
	return missing
}

func (c ClickUp) createTask(ctx context.Context, listID string, plan ClickUpPlan, t model.Task, status string, fs fieldSet) (cuTask, error) {
	body := map[string]any{
		"name":             TaskTitle(t),
		"markdown_content": plan.TaskDescription(t),
		"status":           status,
	}
	if t.EstimateHours > 0 {
		body["time_estimate"] = int64(math.Round(t.EstimateHours * 3600 * 1000))
	}
	var custom []map[string]any
	for _, fv := range plan.fieldValues(t, fs) {
		custom = append(custom, map[string]any{"id": fv.field.ID, "value": fv.value})
	}
	if len(custom) > 0 {
		body["custom_fields"] = custom
	}
	var created cuTask
	if err := c.Client.do(ctx, http.MethodPost, "/list/"+url.PathEscape(listID)+"/task", body, &created); err != nil {
		return cuTask{}, err
	}
	if created.ID == "" {
		return cuTask{}, errors.New("export: ClickUp created the task but answered without an id")
	}
	return created, nil
}

// normStatus folds the spellings of in-progress into one.
func normStatus(s model.Status) model.Status {
	if s == "in_progress" {
		return model.StatusInProgress
	}
	if s == "" {
		return model.StatusBacklog
	}
	return s
}

func hasDependency(t cuTask, dependsOn string) bool {
	for _, d := range t.Dependencies {
		if d.TaskID == t.ID && d.DependsOn == dependsOn {
			return true
		}
	}
	return false
}

func fieldValue(t cuTask, fieldID string) any {
	for _, f := range t.CustomFields {
		if f.ID == fieldID {
			return f.Value
		}
	}
	return nil
}

type fieldWrite struct {
	field *cuField
	value any
	// option is set for a drop_down: ClickUp reads its value back as the
	// option's orderindex, not its id.
	option *cuOption
}

// fieldValues is every custom field orch has a value for on this List.
func (p ClickUpPlan) fieldValues(t model.Task, fs fieldSet) []fieldWrite {
	var out []fieldWrite
	if fs.orchID != nil {
		out = append(out, fieldWrite{field: fs.orchID, value: t.ID})
	}
	if fs.pkg != nil {
		if pkg := p.PackageLabel(t.ID); pkg != "" {
			out = append(out, fieldWrite{field: fs.pkg, value: pkg})
		}
	}
	if fs.model != nil && t.Model != "" {
		if fs.model.Type == "drop_down" {
			if opt := matchModelOption(fs.model.TypeConfig.Options, t.Model); opt != nil {
				out = append(out, fieldWrite{field: fs.model, value: opt.ID, option: opt})
			}
		} else {
			out = append(out, fieldWrite{field: fs.model, value: t.Model})
		}
	}
	if fs.cost != nil {
		if s, ok := p.Spend[t.ID]; ok && s.CostUSD > 0 {
			out = append(out, fieldWrite{field: fs.cost, value: math.Round(s.CostUSD*100) / 100})
		}
	}
	return out
}

func sameFieldValue(have any, w fieldWrite) bool {
	if have == nil {
		return false
	}
	if w.option != nil {
		return fmt.Sprint(have) == fmt.Sprint(w.option.OrderIndex) || fmt.Sprint(have) == w.option.ID
	}
	switch v := w.value.(type) {
	case float64:
		f, err := strconv.ParseFloat(fmt.Sprint(have), 64)
		return err == nil && math.Abs(f-v) < 0.005
	default:
		return fmt.Sprint(have) == fmt.Sprint(w.value)
	}
}

// matchModelOption finds the drop-down option naming model: "Sonnet 5" for
// claude/claude-sonnet-5, "Haiku 4.5" for claude/claude-haiku-4-5. An option
// whose name is the whole model string also matches.
func matchModelOption(opts []cuOption, modelName string) *cuOption {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		return strings.NewReplacer(" ", "-", ".", "-", "_", "-").Replace(s)
	}
	m := norm(modelName)
	var best *cuOption
	for i := range opts {
		n := norm(opts[i].Name)
		if n == "" {
			continue
		}
		if m == n || strings.HasSuffix(m, "/"+n) || strings.HasSuffix(m, "-"+n) {
			if best == nil || len(n) > len(norm(best.Name)) {
				best = &opts[i]
			}
		}
	}
	return best
}

// PackageLabel is "F0.6 — core identity" for F0.6.T2, "F0.6" when the spec
// names no title, and "" for an id outside the atomizer's scheme.
func (p ClickUpPlan) PackageLabel(taskID string) string {
	key := project.PackageKey(taskID)
	if key == "" {
		return ""
	}
	label := "F" + key
	if title := strings.TrimSpace(p.Packages[key]); title != "" {
		label += " — " + title
	}
	return label
}

func quoteJoin(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}
