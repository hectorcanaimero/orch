// Package receipt summarises one run of `orch run`: what got done, what got
// stuck, how long it took and what it cost. `orch report receipt` prints it and
// the dashboard's Now page shows it, from this one builder, so the two cannot
// disagree.
//
// It is computed from the event log and the spend table rather than stored:
// the `runs` row never records an end (Go writes no `status = 'done'`), and the
// `sprint_done` event the runner writes when its loop ends is the one reliable
// "this run finished" signal.
package receipt

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/state"
)

// BuiltWith is the footer a shared receipt carries unless its author opts out.
const BuiltWith = "Built with [orch](https://github.com/hectorcanaimero/orch)"

// Receipt is one run's summary. Its JSON is what `/api/receipt` and
// `orch report receipt --json` return.
type Receipt struct {
	RunID     string `json:"run_id"`
	StartedAt string `json:"started_at"`
	// FinishedAt is the run's sprint_done event; empty while it has none.
	FinishedAt string `json:"finished_at"`
	Finished   bool   `json:"finished"`
	// WallSeconds runs from the run's first event to its sprint_done (or its
	// last event, for an unfinished run). AgentSeconds adds up the durations
	// of the dispatches that finished inside that span.
	WallSeconds  float64 `json:"wall_seconds"`
	AgentSeconds float64 `json:"agent_seconds"`

	Dispatches int `json:"dispatches"`
	// FailedAttempts counts `fail` and `timeout` events: attempts, not tasks.
	FailedAttempts int `json:"failed_attempts"`
	Retries        int `json:"retries"`
	// Done and Blocked are tasks by their last outcome in this run, in the
	// order they first reached one. Blocked counts a terminal failure (one
	// no retry followed) and a task its agent blocked, not only `block` and
	// `ci_blocked` events; a task tried again and never decided is in neither.
	Done    []TaskRef `json:"done"`
	Blocked []TaskRef `json:"blocked"`

	Providers        []Provider `json:"providers"`
	TotalCostUSD     float64    `json:"total_cost_usd"`
	EstimatedCostUSD float64    `json:"estimated_cost_usd"`
	PRs              []PR       `json:"prs"`
	// MergedLocally is the tasks squash-merged into the base here, in place
	// of a PR (vcs.provider: local, #455).
	MergedLocally []Merge `json:"merged_locally"`
}

// Merge is a task the run merged into the base here.
type Merge struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	Base   string `json:"base"`
	Commit string `json:"commit"`
}

// TaskRef names a task. On a blocked task, Cause says why it stopped:
// "verify" (the local verify kept rejecting its work), "failure" (its agent
// failed; FailureClass is the classifier's verdict), "ci" (its PR kept
// failing CI), "not_started" (orch could not start it) or "agent" (its agent
// blocked it). Reason is the event's own text, or the agent's note.
type TaskRef struct {
	TaskID       string `json:"task_id"`
	Title        string `json:"title"`
	Cause        string `json:"cause,omitempty"`
	FailureClass string `json:"failure_class,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// Provider is one backend's share of the run.
type Provider struct {
	Provider   string `json:"provider"`
	Dispatches int    `json:"dispatches"`
	// TokensIn/TokensOut are as the CLI reported them, cache included;
	// WeightedTokens is what the budget gate counts (cache reads at 10%,
	// writes at 125%).
	TokensIn       int `json:"tokens_in"`
	TokensOut      int `json:"tokens_out"`
	WeightedTokens int `json:"weighted_tokens"`
	// CostUSD is reported dollars plus, for rows with tokens and no price,
	// the pricing.yaml estimate. CostSource says which it is: "reported" when
	// the CLI reported any dollars, "estimated" when all of it is the
	// estimate, "no_data" when the provider reported neither.
	CostUSD          float64 `json:"cost_usd"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	CostSource       string  `json:"cost_source"`
}

// PR is a pull request the run opened, with its CI state now.
type PR struct {
	TaskID   string `json:"task_id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	CIStatus string `json:"ci_status"`
}

// Reader is what Load reads; state.Backend and the dashboard's StateReader
// both satisfy it.
type Reader interface {
	AllEvents(ctx context.Context, n int) ([]state.Event, error)
	AllSpend(ctx context.Context, since time.Time) ([]state.Spend, error)
	Tasks(ctx context.Context, filter state.TaskFilter) ([]state.TaskRuntime, error)
}

// Titles maps task id to title, for Build.
func Titles(tasks []model.Task) map[string]string {
	out := make(map[string]string, len(tasks))
	for _, t := range tasks {
		out[t.ID] = t.Title
	}
	return out
}

// Load reads everything Build needs. runID "" means the latest finished run;
// nil with no error means there is no such run.
func Load(ctx context.Context, r Reader, runID string, titles map[string]string, table pricing.Table) (*Receipt, error) {
	events, err := r.AllEvents(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	spend, err := r.AllSpend(ctx, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("read spend: %w", err)
	}
	tasks, err := r.Tasks(ctx, state.TaskFilter{})
	if err != nil {
		return nil, fmt.Errorf("read tasks: %w", err)
	}
	return Build(events, spend, tasks, titles, table, runID), nil
}

// Build computes the receipt for runID ("" = the latest finished run) from
// the whole event log and spend table. nil when there is no such run.
func Build(events []state.Event, spend []state.Spend, tasks []state.TaskRuntime, titles map[string]string, table pricing.Table, runID string) *Receipt {
	if runID == "" {
		runID = latestFinished(events)
	}
	var mine []state.Event
	for _, e := range events {
		if runID != "" && e.RunID == runID {
			mine = append(mine, e)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	sort.SliceStable(mine, func(i, j int) bool { return at(mine[i]).Before(at(mine[j])) })

	ref := func(id string) TaskRef { return TaskRef{TaskID: id, Title: titles[id]} }
	r := &Receipt{RunID: runID, StartedAt: mine[0].TS, Done: []TaskRef{}, Blocked: []TaskRef{}, Providers: []Provider{}, PRs: []PR{}, MergedLocally: []Merge{}}
	start, end := at(mine[0]), at(mine[len(mine)-1])

	// outcome is each task's latest verdict in the run; why is what its
	// latest stop said. A dispatch or retry after a verdict makes it pending
	// again, so a task failed and retried is neither done nor blocked until
	// a later verdict says which.
	outcome := map[string]string{}
	why := map[string]TaskRef{}
	verifyFailed := map[string]bool{}
	// reviewSaid is the latest verify_failed's findings, in one line (#463).
	reviewSaid := map[string]string{}
	var order []string
	byProvider := map[string]*Provider{}
	provider := func(name string) *Provider {
		if byProvider[name] == nil {
			byProvider[name] = &Provider{Provider: name}
		}
		return byProvider[name]
	}
	ci := map[string]state.TaskRuntime{}
	for _, t := range tasks {
		ci[t.ID] = t
	}
	seenPR := map[string]bool{}

	for _, e := range mine {
		switch e.EventType {
		case "dispatch":
			r.Dispatches++
			provider(e.Backend).Dispatches++
			pending(outcome, e.TaskID)
		case "retry":
			r.Retries++
			pending(outcome, e.TaskID)
		case "verify_failed":
			verifyFailed[e.TaskID] = true
			reviewSaid[e.TaskID] = findingsReason(e.Extra["findings"])
			pending(outcome, e.TaskID)
		case "success", "block", "ci_blocked", "fail", "timeout":
			if e.EventType == "fail" || e.EventType == "timeout" {
				r.FailedAttempts++
			}
			if _, ok := outcome[e.TaskID]; !ok {
				order = append(order, e.TaskID)
			}
			outcome[e.TaskID] = e.EventType
			why[e.TaskID] = stopCause(e, verifyFailed[e.TaskID])
			if c := why[e.TaskID]; c.Cause == "verify" && reviewSaid[e.TaskID] != "" {
				c.Reason = reviewSaid[e.TaskID]
				why[e.TaskID] = c
			}
		case "pr_created":
			url, _ := e.Extra["pr_url"].(string)
			if url == "" || seenPR[url] {
				continue
			}
			seenPR[url] = true
			status := ci[e.TaskID].CIStatus
			r.PRs = append(r.PRs, PR{TaskID: e.TaskID, Title: titles[e.TaskID], URL: url, CIStatus: status})
		case "merged_locally":
			base, _ := e.Extra["base"].(string)
			commit, _ := e.Extra["commit"].(string)
			r.MergedLocally = append(r.MergedLocally, Merge{TaskID: e.TaskID, Title: titles[e.TaskID], Base: base, Commit: commit})
		case "sprint_done":
			r.Finished, r.FinishedAt, end = true, e.TS, at(e)
		}
	}
	for _, id := range order {
		switch outcome[id] {
		case "pending":
		case "success":
			// An agent that blocks its task exits cleanly, so its outcome
			// event says success; the task's status, moved inside the run,
			// and its last note say otherwise.
			if t := ci[id]; t.Status == model.StatusBlocked && within(t.UpdatedAt, start, end) {
				b := ref(id)
				b.Cause, b.Reason = "agent", lastNote(t.Comments)
				r.Blocked = append(r.Blocked, b)
				continue
			}
			r.Done = append(r.Done, ref(id))
		default:
			b := why[id]
			b.TaskID, b.Title = id, titles[id]
			r.Blocked = append(r.Blocked, b)
		}
	}
	if !start.IsZero() && !end.IsZero() {
		r.WallSeconds = end.Sub(start).Seconds()
	}

	// ponytail: spend rows carry no run id, so a run owns the rows dated
	// inside its span; two runs overlapping in time would share them. Add a
	// run_id column to spend if concurrent runs ever become a thing.
	for _, s := range spend {
		ts, ok := state.ParseTS(s.TS)
		if !ok || ts.Before(start) || ts.After(end) {
			continue
		}
		p := provider(s.Backend)
		r.AgentSeconds += s.DurationS
		p.TokensIn += s.TokensIn
		p.TokensOut += s.TokensOut
		p.WeightedTokens += int(math.Round(budget.WeightedTokens(s)))
		switch {
		case s.CostUSD > 0:
			p.CostUSD += s.CostUSD
		case s.TokensIn+s.TokensOut > 0:
			est := table.EstimateCost(s.Model, s.TokensIn, s.TokensOut)
			p.CostUSD += est
			p.EstimatedCostUSD += est
		}
	}
	names := make([]string, 0, len(byProvider))
	for name := range byProvider {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := byProvider[name]
		switch {
		case p.CostUSD > p.EstimatedCostUSD:
			p.CostSource = "reported"
		case p.EstimatedCostUSD > 0 || p.TokensIn+p.TokensOut > 0:
			p.CostSource = "estimated"
		default:
			p.CostSource = "no_data"
		}
		p.CostUSD, p.EstimatedCostUSD = round4(p.CostUSD), round4(p.EstimatedCostUSD)
		r.TotalCostUSD += p.CostUSD
		r.EstimatedCostUSD += p.EstimatedCostUSD
		r.Providers = append(r.Providers, *p)
	}
	r.TotalCostUSD, r.EstimatedCostUSD = round4(r.TotalCostUSD), round4(r.EstimatedCostUSD)
	return r
}

// pending reopens a task's verdict: it is being tried again.
func pending(outcome map[string]string, id string) {
	if _, ok := outcome[id]; ok {
		outcome[id] = "pending"
	}
}

// stopCause is why a stop event stopped its task. A terminal failure with no
// failure class after a verify_failed is the local verify's block: the agent
// exited cleanly every time, its work did not pass.
func stopCause(e state.Event, verifyFailed bool) TaskRef {
	reason, _ := e.Extra["reason"].(string)
	class, _ := e.Extra["failure_class"].(string)
	c := TaskRef{Reason: reason}
	switch e.EventType {
	case "ci_blocked":
		c.Cause = "ci"
	case "block":
		c.Cause = "not_started"
	case "fail", "timeout":
		c.Cause, c.FailureClass = "failure", class
		if class == "" && e.EventType == "timeout" {
			c.FailureClass = "timeout"
		}
		if class == "" && verifyFailed {
			c.Cause = "verify"
			_, c.Reason, _ = strings.Cut(reason, ": ")
		}
	}
	return c
}

// findingsReason is a verify_failed's structured findings (#463) in one
// line, "" for an event without them: the reason's own first line only says
// a review asked for changes.
func findingsReason(raw any) string {
	findings, _ := raw.([]any)
	var titles []string
	for _, f := range findings {
		m, _ := f.(map[string]any)
		title, _ := m["title"].(string)
		if file, _ := m["file"].(string); file != "" {
			if line, ok := m["line"].(float64); ok && line > 0 {
				file += ":" + strconv.Itoa(int(line))
			}
			title = file + " " + title
		}
		titles = append(titles, title)
	}
	if len(titles) == 0 {
		return ""
	}
	return fmt.Sprintf("%d review findings: %s", len(titles), strings.Join(titles, "; "))
}

func within(ts string, start, end time.Time) bool {
	t, ok := state.ParseTS(ts)
	return ok && !t.Before(start) && !t.After(end)
}

// lastNote is the body of a task's newest comment.
func lastNote(comments []json.RawMessage) string {
	for i := len(comments) - 1; i >= 0; i-- {
		var c struct {
			Body string `json:"body"`
		}
		if json.Unmarshal(comments[i], &c) == nil && strings.TrimSpace(c.Body) != "" {
			return strings.TrimSpace(c.Body)
		}
	}
	return ""
}

// latestFinished is the run whose sprint_done is newest.
func latestFinished(events []state.Event) string {
	var id string
	var newest time.Time
	for _, e := range events {
		if e.EventType != "sprint_done" {
			continue
		}
		if t := at(e); id == "" || t.After(newest) {
			id, newest = e.RunID, t
		}
	}
	return id
}

func at(e state.Event) time.Time {
	t, _ := state.ParseTS(e.TS)
	return t
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// Markdown renders the receipt for pasting into a PR, a chat or an email. It
// does not carry BuiltWith: the caller appends it unless its author opted out.
func (r *Receipt) Markdown() string {
	var b strings.Builder
	status := "finished"
	if !r.Finished {
		status = "not finished"
	}
	fmt.Fprintf(&b, "## orch run `%s` — %s\n\n", r.RunID, status)
	fmt.Fprintf(&b, "%d done · %d blocked · %s · %s · %s wall time · %s agent time\n",
		len(r.Done), len(r.Blocked), plural(r.FailedAttempts, "failed attempt"), plural(r.Dispatches, "dispatch"),
		Duration(r.WallSeconds), Duration(r.AgentSeconds))

	if len(r.Providers) > 0 {
		b.WriteString("\n| Provider | Dispatches | Tokens (in / out) | Cost |\n|---|---:|---:|---:|\n")
		for _, p := range r.Providers {
			tokens := "—"
			if p.TokensIn+p.TokensOut > 0 {
				tokens = thousands(p.TokensIn) + " / " + thousands(p.TokensOut)
				if p.WeightedTokens != p.TokensIn+p.TokensOut {
					tokens += " (" + thousands(p.WeightedTokens) + " weighted)"
				}
			}
			cost := "no data"
			switch p.CostSource {
			case "reported":
				cost = fmt.Sprintf("$%.2f reported", p.CostUSD)
			case "estimated":
				cost = fmt.Sprintf("~$%.2f estimated", p.CostUSD)
			}
			fmt.Fprintf(&b, "| %s | %d | %s | %s |\n", p.Provider, p.Dispatches, tokens, cost)
		}
	}
	list := func(title string, refs []TaskRef) {
		if len(refs) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n**%s**\n\n", title)
		for _, t := range refs {
			fmt.Fprintf(&b, "- %s %s\n", t.TaskID, t.Title)
		}
	}
	list("Done", r.Done)
	list("Blocked", r.Blocked)
	if len(r.PRs) > 0 {
		b.WriteString("\n**Pull requests**\n\n")
		for _, p := range r.PRs {
			num := p.URL[strings.LastIndex(p.URL, "/")+1:]
			ciState := "CI " + p.CIStatus
			if p.CIStatus == "" {
				ciState = "no CI result"
			}
			fmt.Fprintf(&b, "- [#%s](%s) %s %s — %s\n", num, p.URL, p.TaskID, p.Title, ciState)
		}
	}
	if len(r.MergedLocally) > 0 {
		b.WriteString("\n**Merged locally**\n\n")
		for _, m := range r.MergedLocally {
			fmt.Fprintf(&b, "- %s %s — `%.7s` on %s\n", m.TaskID, m.Title, m.Commit, m.Base)
		}
	}
	return b.String()
}

// Duration formats seconds the way the receipt shows them: "2d 3h", "2h 5m",
// "50m", "40s".
func Duration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if strings.HasSuffix(word, "ch") {
		return fmt.Sprintf("%d %ses", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
