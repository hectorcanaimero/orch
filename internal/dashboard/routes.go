package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/hectorcanaimero/orch/internal/config"
)

// The routes this phase wires. G5.2(b) adds the read endpoints; tunnel is
// G5.6.
//
// The names are Python's route names, verbatim, because the stakeholder
// allow-list matches on them — `DEFAULT_STAKEHOLDER_ROUTES` lists
// `api_whoami`, not `/api/whoami`. A renamed route here silently changes who
// can reach it, which is why they are written next to the pattern rather than
// derived from it.
func (s *Server) routes() []route {
	out := []route{
		{pattern: "GET /api/whoami", name: "api_whoami", handler: s.handleWhoami},
		{pattern: "GET /api/config/status", name: "api_config_status", handler: s.handleConfigStatus},
	}
	out = append(out, s.readRoutes()...)
	out = append(out, s.metricRoutes()...)
	out = append(out, s.ciRoutes()...)
	out = append(out, s.sprintRoutes()...)
	out = append(out, s.stakeholderRoutes()...)
	out = append(out, s.portalRoutes()...)
	out = append(out, s.tunnelRoutes()...)
	return append(out, s.streamRoutes()...)
}

// whoamiPayload is `/api/whoami`'s body.
//
// One field, and deliberately not more. Python's comment is explicit that this
// is NOT part of `/api/config`: that endpoint is operator-only and its payload
// carries budgets, the findings repo and the state backend, none of which a
// stakeholder session should see. The profile itself is not a secret — the
// token is — so the SPA reads it to hide operator-only navigation.
type whoamiPayload struct {
	Profile string `json:"profile"`
	// Routes is the stakeholder allow-list, by route name, and only for the
	// stakeholder profile: the SPA shows exactly the pages whose data route
	// is on it. Omitted for operator and both, whose SPA surface is ungated.
	Routes []string `json:"routes,omitempty"`
}

func (s *Server) handleWhoami(w http.ResponseWriter, _ *http.Request) {
	payload := whoamiPayload{Profile: string(s.cfg.Profile)}
	if s.cfg.Profile == ProfileStakeholder {
		payload.Routes = s.cfg.StakeholderRoutes
	}
	writeJSON(w, http.StatusOK, payload)
}

// configStatusPayload is `/api/config/status`'s body.
//
// The SPA asks one question — does this project still need the setup wizard —
// and the other fields are there so it can say WHICH part is missing rather
// than only that something is.
type configStatusPayload struct {
	IsSetup      bool   `json:"is_setup"`
	ProjectID    string `json:"project_id"`
	SpecRoot     string `json:"spec_root"`
	Backend      string `json:"backend"`
	BudgetPreset string `json:"budget_preset"`
	// Error carries a load failure as text, matching Python, which returns
	// `{"is_setup": false, "error": ...}` with a 200 rather than a 5xx. The
	// SPA renders the wizard either way, and a project too broken to read is
	// the strongest possible case for showing it.
	Error string `json:"error,omitempty"`
}

func (s *Server) handleConfigStatus(w http.ResponseWriter, _ *http.Request) {
	payload, err := s.configStatus()
	if err != nil {
		writeJSON(w, http.StatusOK, configStatusPayload{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// configStatus reads the four things that make a project "set up".
//
// All four, and the AND is the point: a project with a config but no
// `tasks.json` meta, or with a spec root and no backend, is half-scaffolded
// and the wizard is the right answer. Python checks the same four.
func (s *Server) configStatus() (configStatusPayload, error) {
	var out configStatusPayload

	loaded, err := config.Load(s.paths.ConfigYAML, s.paths.Root)
	if err != nil {
		return out, fmt.Errorf("reading %s: %w", s.paths.ConfigYAML, err)
	}
	out.SpecRoot = loaded.Config.SpecRoot
	out.Backend = loaded.Config.State.Backend
	out.BudgetPreset = loaded.Config.BudgetsPreset

	raw, err := os.ReadFile(s.paths.TasksJSON()) // #nosec G304 -- the project's own tasks.json
	if err != nil {
		return out, fmt.Errorf("reading %s: %w", s.paths.TasksJSON(), err)
	}
	var doc struct {
		Meta struct {
			Project string `json:"project"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return out, fmt.Errorf("parsing %s: %w", s.paths.TasksJSON(), err)
	}
	out.ProjectID = doc.Meta.Project

	out.IsSetup = out.ProjectID != "" && out.SpecRoot != "" &&
		out.Backend != "" && out.BudgetPreset != ""
	return out, nil
}
