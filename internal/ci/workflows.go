package ci

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Workflow summarises one GitHub Actions workflow file: what triggers it and
// what its jobs run, by name. The operator's CI page shows it as is; the
// client portal reads which checks it amounts to (Gates).
type Workflow struct {
	File     string        `json:"file"`
	Name     string        `json:"name"`
	Triggers []string      `json:"triggers"`
	Jobs     []WorkflowJob `json:"jobs"`
	OnPRs    bool          `json:"runs_on_pull_requests"`
	// ParseFailed says why the file is not YAML GitHub can read. The file is
	// still listed: a broken workflow is exactly what a reader needs to see.
	ParseFailed string `json:"parse_error,omitempty"`
}

// WorkflowJob is one job, with its steps labelled by name, action or the
// first line of its command.
type WorkflowJob struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Needs []string `json:"needs"`
	Steps []string `json:"steps"`
	// ParseFailed says why a job's body could not be read, e.g. a job that is
	// a string instead of a mapping. The job is still listed by its id.
	ParseFailed string `json:"parse_error,omitempty"`
}

// ReadWorkflows summarises every *.yml and *.yaml file in dir, sorted by
// name. A missing directory is no workflows, not an error.
func ReadWorkflows(dir string) ([]Workflow, error) {
	var files []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", dir, err)
		}
		files = append(files, m...)
	}
	sort.Strings(files)

	out := []Workflow{}
	for _, f := range files {
		wf := Workflow{File: filepath.Base(f), Triggers: []string{}, Jobs: []WorkflowJob{}}
		raw, err := os.ReadFile(f) // #nosec G304 -- a workflow file of the project being read
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
		wf.Triggers = nodeNames(&doc.On)
		for _, t := range wf.Triggers {
			wf.OnPRs = wf.OnPRs || t == "pull_request" || t == "pull_request_target"
		}
		wf.Jobs = workflowJobs(&doc.Jobs)
		out = append(out, wf)
	}
	return out, nil
}

// nodeNames reads a name, a list of names, or a mapping's keys: the three
// spellings of `on:` and of `needs:`.
func nodeNames(n *yaml.Node) []string {
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

// workflowJobs keeps the file's job order, which is how people read it.
func workflowJobs(n *yaml.Node) []WorkflowJob {
	out := []WorkflowJob{}
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
		job := WorkflowJob{ID: n.Content[i].Value, Needs: []string{}, Steps: []string{}}
		if err := n.Content[i+1].Decode(&body); err != nil {
			job.ParseFailed = fmt.Errorf("job %q: %w", job.ID, err).Error()
		} else {
			job.Name = body.Name
			job.Needs = nodeNames(&body.Needs)
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

// The kinds of check a pull request can go through, in the order a client
// reads them.
const (
	GateTests     = "tests"
	GateTypecheck = "typecheck"
	GateLint      = "lint"
	GateBuild     = "build"
	GateReview    = "review"
)

var gateOrder = []string{GateTests, GateTypecheck, GateLint, GateBuild, GateReview}

// gateWords are what a step label says when it runs that kind of check.
var gateWords = map[string][]string{
	GateTypecheck: {"typecheck", "type-check", "type check", "tsc", "mypy"},
	GateLint:      {"lint", "go vet", "clippy", "ruff", "eslint", "oxlint", "golangci"},
	GateTests:     {"test", "pytest", "vitest", "jest"},
	GateBuild:     {"build"},
}

// Gates names the kinds of check the workflows that run on pull requests
// amount to, from their jobs' and steps' names. A job whose name or id says
// review is a review, whatever its steps are called ("Build review input"
// is not a build). Workflows that do not run on pull requests check nothing
// a delivery goes through, and are ignored.
//
// ponytail: word matching on labels; a step named oddly is missed rather
// than misread, and the operator's CI page still shows it by name.
func Gates(workflows []Workflow) []string {
	found := map[string]bool{}
	for _, w := range workflows {
		if !w.OnPRs || w.ParseFailed != "" {
			continue
		}
		for _, j := range w.Jobs {
			jobLabel := strings.ToLower(j.ID + " " + j.Name)
			if strings.Contains(jobLabel, "review") {
				found[GateReview] = true
				continue
			}
			for _, step := range j.Steps {
				label := strings.ToLower(step)
				if strings.Contains(label, "orch ci review") {
					found[GateReview] = true
					continue
				}
				for gate, words := range gateWords {
					for _, word := range words {
						if strings.Contains(label, word) {
							found[gate] = true
						}
					}
				}
			}
		}
	}
	out := []string{}
	for _, g := range gateOrder {
		if found[g] {
			out = append(out, g)
		}
	}
	return out
}
