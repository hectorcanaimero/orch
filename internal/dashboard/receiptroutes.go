package dashboard

import (
	"net/http"
	"strings"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/doctor"
	"github.com/hectorcanaimero/orch/internal/receipt"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/tunnel"
)

// The two cards the Now page shows when nothing is running: the last run's
// receipt, and — before any run has finished — the checklist that gets a new
// project to its first one. Both read only (CHECKLIST rule 13): the checklist
// names the command that fixes an item, it does not run it.

func (s *Server) receiptRoutes() []route {
	return []route{
		{pattern: "GET /api/receipt", name: "api_receipt", handler: s.handleReceipt},
		{pattern: "GET /api/onboarding", name: "api_onboarding", handler: s.handleOnboarding},
	}
}

// receiptPayload wraps the receipt so "no finished run yet" is a null, not a
// 404 the page has to tell apart from a broken route. Markdown is the same
// text `orch report receipt` prints, without the BuiltWith footer, which the
// page appends unless its author unticked it.
type receiptPayload struct {
	Receipt   *receipt.Receipt `json:"receipt"`
	Markdown  string           `json:"markdown"`
	BuiltWith string           `json:"built_with"`
}

func (s *Server) handleReceipt(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "receipt", errNoBackend)
		return
	}
	view, err := s.loadView(r.Context())
	if err != nil {
		s.failRead(w, "receipt", err)
		return
	}
	runID := r.URL.Query().Get("run")
	rec, err := receipt.Load(r.Context(), s.state, runID, receipt.Titles(view.Tasks), s.pricing())
	if err != nil {
		s.failRead(w, "receipt", err)
		return
	}
	if rec == nil && runID != "" {
		writeJSON(w, http.StatusNotFound, errorPayload{Detail: "run not found: " + runID})
		return
	}
	out := receiptPayload{Receipt: rec, BuiltWith: receipt.BuiltWith}
	if rec != nil {
		out.Markdown = rec.Markdown()
	}
	writeJSON(w, http.StatusOK, out)
}

// onboardingItem is one step to a project's first run. Command is a shell
// command that fixes it, when there is one; Link is a page of the dashboard
// that explains it.
type onboardingItem struct {
	ID       string `json:"id"`
	Done     bool   `json:"done"`
	Optional bool   `json:"optional"`
	Detail   string `json:"detail"`
	Command  string `json:"command,omitempty"`
	Link     string `json:"link,omitempty"`
}

// onboardingPayload's Complete is "a run has finished": the page stops
// showing the checklist then, whatever the items say.
type onboardingPayload struct {
	Complete bool             `json:"complete"`
	Items    []onboardingItem `json:"items"`
}

// checkBackends is doctor.CheckBackends, which runs every provider CLI with
// a timeout; tests replace it so they do not depend on what is on PATH.
var checkBackends = doctor.CheckBackends

func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "onboarding", errNoBackend)
		return
	}
	events, err := s.state.AllEvents(r.Context(), 0)
	if err != nil {
		s.failRead(w, "onboarding", err)
		return
	}
	out := onboardingPayload{Items: []onboardingItem{}}
	dispatched := false
	for _, e := range events {
		switch e.EventType {
		case "sprint_done":
			out.Complete = true
		case "dispatch":
			dispatched = true
		}
	}

	// A broken tasks.json or config.yaml is itself an unmet item, not a 500:
	// this card exists for exactly the project that is not set up yet.
	view, viewErr := s.loadView(r.Context())
	cfg := config.Defaults()
	loaded, cfgErr := config.Load(s.paths.ConfigYAML, s.paths.Root)
	if cfgErr == nil {
		cfg = loaded.Config
	}
	providers := onboardingItem{ID: "providers", Done: true, Command: "orch doctor"}
	// Like `orch doctor`, a router that does not load probes the known
	// backends — and is itself the first thing to fix.
	rtr, rErr := router.Load(s.paths.RouterYAML())
	if rErr != nil {
		providers.Done = false
		providers.Detail = "model_router.yaml: " + rErr.Error()
	}
	for _, c := range checkBackends(doctor.ReferencedBackends(view.Tasks, rtr)) {
		if c.Status == doctor.StatusError {
			providers.Done = false
			providers.Detail = joinDetail(providers.Detail, c.Detail)
		}
	}
	out.Items = append(out.Items, providers)

	budgetItem := onboardingItem{ID: "budget", Done: true, Link: "/cost/budget"}
	// The budget is config.yaml's to name; a config.yaml that does not load
	// is the unmet item, with the defaults standing in for the other checks.
	if cfgErr != nil {
		budgetItem.Done = false
		budgetItem.Detail = "config.yaml: " + cfgErr.Error()
	}
	for _, c := range doctor.CheckBudgetPreset(budget.ResolvePath(s.paths.Root, s.paths.ConfigYAML, cfg.BudgetsConfig), cfg.BudgetsPreset, cfg.TypicalDispatchToken) {
		if c.Status != doctor.StatusOK {
			budgetItem.Done = false
			budgetItem.Detail = joinDetail(budgetItem.Detail, c.Detail)
		}
	}
	out.Items = append(out.Items, budgetItem)

	tasksItem := onboardingItem{ID: "tasks", Done: viewErr == nil && len(view.Tasks) > 0, Command: "orch validate", Link: "/work/list"}
	if viewErr != nil {
		tasksItem.Detail = viewErr.Error()
	}
	out.Items = append(out.Items, tasksItem)

	vcsItem := onboardingItem{ID: "vcs", Done: true, Optional: true, Link: "/delivery/ci"}
	for _, c := range doctor.CheckVCSReadiness(s.paths.Root, doctor.VCSConfig{
		WorktreeMode: cfg.Dispatch.WorktreeMode, AutoPR: cfg.VCS.AutoPR,
		Provider: cfg.VCS.Provider, Host: cfg.VCS.Host,
	}) {
		if c.Status == doctor.StatusWarn || c.Status == doctor.StatusError {
			vcsItem.Done = false
			vcsItem.Detail = joinDetail(vcsItem.Detail, c.Detail)
			if vcsItem.Command == "" {
				vcsItem.Command = c.Remediation
			}
		}
	}
	out.Items = append(out.Items, vcsItem)

	tun := doctor.CheckTunnel(cfg.Tunnel.Enabled, tunnel.LookupBinary(""), tunnel.ConfigBlocker())
	out.Items = append(out.Items, onboardingItem{
		ID: "tunnel", Optional: true, Link: "/delivery/share",
		Done: tun.Status != doctor.StatusWarn && tun.Status != doctor.StatusError, Detail: tun.Detail,
	})

	out.Items = append(out.Items, onboardingItem{ID: "first_run", Done: dispatched, Command: "orch run"})
	writeJSON(w, http.StatusOK, out)
}

func joinDetail(have, add string) string {
	if have == "" {
		return add
	}
	return strings.Join([]string{have, add}, "; ")
}
