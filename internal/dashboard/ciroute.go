package dashboard

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

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
	Config    ciConfig     `json:"config"`
	Workflows []ciWorkflow `json:"workflows"`
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

type ciWorkflow struct {
	File        string   `json:"file"`
	Name        string   `json:"name"`
	Triggers    []string `json:"triggers"`
	Jobs        []ciJob  `json:"jobs"`
	OnPRs       bool     `json:"runs_on_pull_requests"`
	ParseFailed string   `json:"parse_error,omitempty"`
}

type ciJob struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Needs []string `json:"needs"`
	Steps []string `json:"steps"`
	// ParseFailed says why a job's body could not be read, e.g. a job that is
	// a string instead of a mapping. The job is still listed by its id.
	ParseFailed string `json:"parse_error,omitempty"`
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
		Workflows: []ciWorkflow{},
		Warnings:  []string{},
	}

	workflows, err := readWorkflows(filepath.Join(s.paths.Root, ".github", "workflows"))
	if err != nil {
		return ciPayload{}, err
	}
	out.Workflows = workflows
	out.Warnings = ciWarnings(out.Config, workflows)
	return out, nil
}

// readWorkflows summarises every workflow file: its triggers and its jobs'
// steps, by name. A file that is not valid YAML is listed with the error
// rather than hidden: a broken workflow is exactly what this page is for.
func readWorkflows(dir string) ([]ciWorkflow, error) {
	var files []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", dir, err)
		}
		files = append(files, m...)
	}
	sort.Strings(files)

	out := []ciWorkflow{}
	for _, f := range files {
		wf := ciWorkflow{File: filepath.Base(f), Triggers: []string{}, Jobs: []ciJob{}}
		raw, err := os.ReadFile(f) // #nosec G304 -- a workflow file of the project being served
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}
		var doc struct {
			Name string    `yaml:"name"`
			On   yaml.Node `yaml:"on"`
			Jobs yaml.Node `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			wf.ParseFailed = err.Error()
			out = append(out, wf)
			continue
		}
		wf.Name = doc.Name
		wf.Triggers = triggers(&doc.On)
		for _, t := range wf.Triggers {
			wf.OnPRs = wf.OnPRs || t == "pull_request" || t == "pull_request_target"
		}
		wf.Jobs = jobs(&doc.Jobs)
		out = append(out, wf)
	}
	return out, nil
}

// triggers reads `on:` in its three spellings: a name, a list, or a map.
func triggers(n *yaml.Node) []string {
	out := []string{}
	switch n.Kind {
	case yaml.ScalarNode:
		out = append(out, n.Value)
	case yaml.SequenceNode:
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
	}
	return out
}

// jobs keeps the file's job order, which is how people read a workflow.
func jobs(n *yaml.Node) []ciJob {
	out := []ciJob{}
	if n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		var body struct {
			Name  string    `yaml:"name"`
			Needs yaml.Node `yaml:"needs"`
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		}
		job := ciJob{ID: n.Content[i].Value, Needs: []string{}, Steps: []string{}}
		if err := n.Content[i+1].Decode(&body); err != nil {
			job.ParseFailed = fmt.Errorf("job %q: %w", job.ID, err).Error()
		} else {
			job.Name = body.Name
			job.Needs = triggers(&body.Needs)
			for _, st := range body.Steps {
				label := st.Name
				if label == "" {
					label = st.Uses
				}
				if label == "" {
					label, _, _ = strings.Cut(strings.TrimSpace(st.Run), "\n")
				}
				job.Steps = append(job.Steps, label)
			}
		}
		out = append(out, job)
	}
	return out
}

// ciWarnings are the combinations that silently do nothing, or silently do
// something else, said the way the fix reads.
func ciWarnings(c ciConfig, workflows []ciWorkflow) []string {
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
