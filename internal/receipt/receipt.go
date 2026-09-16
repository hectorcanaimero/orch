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
	"fmt"
	"math"
	"sort"
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
	// order they reached it.
	Done    []TaskRef `json:"done"`
	Blocked []TaskRef `json:"blocked"`

	Providers        []Provider `json:"providers"`
	TotalCostUSD     float64    `json:"total_cost_usd"`
	EstimatedCostUSD float64    `json:"estimated_cost_usd"`
	PRs              []PR       `json:"prs"`
}

// TaskRef names a task.
type TaskRef struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
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
	r := &Receipt{RunID: runID, StartedAt: mine[0].TS, Done: []TaskRef{}, Blocked: []TaskRef{}, Providers: []Provider{}, PRs: []PR{}}
	start, end := at(mine[0]), at(mine[len(mine)-1])

	outcome := map[string]string{}
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
		case "fail", "timeout":
			r.FailedAttempts++
		case "retry":
			r.Retries++
		case "success", "block", "ci_blocked":
			if _, ok := outcome[e.TaskID]; !ok {
				order = append(order, e.TaskID)
			}
			outcome[e.TaskID] = e.EventType
		case "pr_created":
			url, _ := e.Extra["pr_url"].(string)
			if url == "" || seenPR[url] {
				continue
			}
			seenPR[url] = true
			status := ci[e.TaskID].CIStatus
			r.PRs = append(r.PRs, PR{TaskID: e.TaskID, Title: titles[e.TaskID], URL: url, CIStatus: status})
		case "sprint_done":
			r.Finished, r.FinishedAt, end = true, e.TS, at(e)
		}
	}
	for _, id := range order {
		if outcome[id] == "success" {
			r.Done = append(r.Done, ref(id))
		} else {
			r.Blocked = append(r.Blocked, ref(id))
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
