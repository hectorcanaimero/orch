package dashboard

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/hectorcanaimero/orch/internal/ci"
	"github.com/hectorcanaimero/orch/internal/config"
)

// The operator's CI page: how the project's pipeline is set up, the orch half
// (config.yaml: PRs, retries, merges) and the repo half (its GitHub Actions
// workflows). The live half, each task's PR and CI state, rides on /api/tasks.
// Operator only: workflow files and repository settings are not a client's.

func (s *Server) ciRoutes() []route {
	return []route{
		{pattern: "GET /api/ci", name: "api_ci", handler: s.handleCI},
	}
}

type ciPayload struct {
	Config    ciConfig      `json:"config"`
	Workflows []ci.Workflow `json:"workflows"`
	// Warnings name settings that cannot work together, in words.
	Warnings []string `json:"warnings"`
}

type ciConfig struct {
	WorktreeMode  bool   `json:"worktree_mode"`
	BaseBranch    string `json:"base_branch"`
	Provider      string `json:"provider"`
	Host          string `json:"host"`
	AutoPR        bool   `json:"auto_pr"`
	MaxRetries    int    `json:"ci_max_retries"`
	PollIntervalS int    `json:"ci_poll_interval_s"`
	AutoMerge     bool   `json:"auto_merge"`
	TestCommand   string `json:"test_command"`
}

func (s *Server) handleCI(w http.ResponseWriter, _ *http.Request) {
	payload, err := s.ciView()
	if err != nil {
		s.failRead(w, "ci", err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) ciView() (ciPayload, error) {
	loaded, err := config.Load(s.paths.ConfigYAML, s.paths.Root)
	if err != nil {
		return ciPayload{}, fmt.Errorf("reading %s: %w", s.paths.ConfigYAML, err)
	}
	c := loaded.Config
	out := ciPayload{
		Config: ciConfig{
			WorktreeMode:  c.Dispatch.WorktreeMode,
			BaseBranch:    c.Dispatch.BaseBranch,
			Provider:      c.VCS.Provider,
			Host:          c.VCS.Host,
			AutoPR:        c.VCS.AutoPR,
			MaxRetries:    c.VCS.CIMaxRetries,
			PollIntervalS: c.VCS.CIPollIntervalS,
			AutoMerge:     c.GitHub.AutoMerge,
			TestCommand:   c.GitHub.TestCommand,
		},
		Workflows: []ci.Workflow{},
		Warnings:  []string{},
	}

	workflows, err := ci.ReadWorkflows(filepath.Join(s.paths.Root, ".github", "workflows"))
	if err != nil {
		return ciPayload{}, fmt.Errorf("reading the project's CI workflows: %w", err)
	}
	out.Workflows = workflows
	out.Warnings = ciWarnings(out.Config, workflows)
	return out, nil
}

// ciWarnings are the combinations that silently do nothing, or silently do
// something else, said the way the fix reads.
func ciWarnings(c ciConfig, workflows []ci.Workflow) []string {
	out := []string{}
	if c.AutoPR && !c.WorktreeMode {
		out = append(out, "vcs.auto_pr is on but dispatch.worktree_mode is off: orch run refuses to start, because there is no branch to open a PR from.")
	}
	onPRs := false
	for _, w := range workflows {
		onPRs = onPRs || w.OnPRs
	}
	if c.AutoPR && !onPRs {
		out = append(out, "No workflow runs on pull_request: orch waits for checks that never come, then finishes each PR without CI (and without merging it).")
	}
	if c.AutoMerge && !c.AutoPR {
		out = append(out, "github.auto_merge is on but vcs.auto_pr is off: there is never a PR to merge.")
	}
	return out
}
