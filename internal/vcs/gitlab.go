package vcs

import (
	"encoding/json"
	"strconv"
	"strings"
)

// gitlabPipelineStatusMap ports _CI_STATE_MAP from orchestrator/vcs/gitlab.py.
var gitlabPipelineStatusMap = map[string]CIState{
	"success":              CISuccess,
	"failed":               CIFailure,
	"canceled":             CIFailure,
	"skipped":              CISuccess,
	"running":              CIPending,
	"pending":              CIPending,
	"created":              CIPending,
	"waiting_for_resource": CIPending,
	"preparing":            CIPending,
	"scheduled":            CIPending,
	"manual":               CIPending,
}

// GitLabProvider drives MR creation, CI polling, and merges through the
// `glab` CLI. Ports GitLabProvider (orchestrator/vcs/gitlab.py).
type GitLabProvider struct {
	host string
}

// NewGitLabProvider builds a GitLabProvider talking to host (e.g.
// "gitlab.com" or a self-hosted instance).
func NewGitLabProvider(host string) *GitLabProvider {
	return &GitLabProvider{host: host}
}

func (p *GitLabProvider) env() []string {
	return []string{"GITLAB_HOST=" + p.host}
}

func (p *GitLabProvider) CreatePR(head, base, title, body string) (string, error) {
	if err := checkBinary("glab"); err != nil {
		return "", err
	}
	out, err := run("glab", p.env(),
		"mr", "create",
		"--title", title,
		"--description", body,
		"--source-branch", head,
		"--target-branch", base,
		"--yes",
	)
	if err != nil {
		if authErr := checkAuth("glab", p.env()); authErr != nil {
			return "", authErr
		}
		return "", nil
	}
	url := lastNonEmptyLine(out)
	if url == "" {
		return "", nil
	}
	return url, nil
}

type gitlabMRView struct {
	HasConflicts bool `json:"has_conflicts"`
	HeadPipeline struct {
		ID     json.Number `json:"id"`
		Status string      `json:"status"`
	} `json:"head_pipeline"`
}

func (p *GitLabProvider) CIStatus(prURL string) (CIState, error) {
	if err := checkBinary("glab"); err != nil {
		return "", err
	}
	iid := iidFromURL(prURL)
	if iid == "" {
		return CIPending, nil
	}

	out, err := run("glab", p.env(), "mr", "view", iid, "--output", "json")
	if err != nil {
		return CIPending, nil
	}

	var view gitlabMRView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return CIPending, nil
	}
	if view.HeadPipeline.Status == "" {
		// No pipeline at all: see CINone and CIConflict.
		if view.HasConflicts {
			return CIConflict, nil
		}
		return CINone, nil
	}
	if mapped, ok := gitlabPipelineStatusMap[view.HeadPipeline.Status]; ok {
		return mapped, nil
	}
	return CIPending, nil
}

type gitlabJob struct {
	ID     json.Number `json:"id"`
	Status string      `json:"status"`
}

func (p *GitLabProvider) CILogs(prURL string) (string, error) {
	if err := checkBinary("glab"); err != nil {
		return "", err
	}
	iid := iidFromURL(prURL)
	if iid == "" {
		return "", nil
	}

	viewOut, err := run("glab", p.env(), "mr", "view", iid, "--output", "json")
	if err != nil {
		return "", nil
	}
	var view gitlabMRView
	if err := json.Unmarshal([]byte(viewOut), &view); err != nil {
		return "", nil
	}
	pipelineID := view.HeadPipeline.ID.String()
	if pipelineID == "" {
		return "", nil
	}

	jobsOut, err := run("glab", p.env(), "pipeline", "jobs", pipelineID, "--output", "json")
	if err != nil {
		return "", nil
	}
	var jobs []gitlabJob
	if err := json.Unmarshal([]byte(jobsOut), &jobs); err != nil {
		return "", nil
	}

	var failedIDs []string
	for _, j := range jobs {
		if j.Status == "failed" {
			failedIDs = append(failedIDs, j.ID.String())
		}
	}
	if len(failedIDs) > 3 {
		failedIDs = failedIDs[:3]
	}

	var logs []string
	for _, jobID := range failedIDs {
		traceOut, err := run("glab", p.env(), "pipeline", "trace", jobID)
		if err == nil {
			logs = append(logs, traceOut)
		}
	}
	combined := strings.Join(logs, "\n---\n")
	return truncate(combined, maxLogChars), nil
}

func (p *GitLabProvider) MergePR(prURL string, squash, auto bool) error {
	if err := checkBinary("glab"); err != nil {
		return err
	}
	iid := iidFromURL(prURL)
	if iid == "" {
		return ErrMergeFailed
	}

	args := []string{"mr", "merge", iid, "--yes"}
	if squash {
		args = append(args, "--squash")
	}
	if auto {
		args = append(args, "--when-pipeline-succeeds")
	}
	if _, err := run("glab", p.env(), args...); err != nil {
		if authErr := checkAuth("glab", p.env()); authErr != nil {
			return authErr
		}
		return ErrMergeFailed
	}
	return nil
}

// iidFromURL extracts the numeric MR IID from ".../merge_requests/<iid>".
// Ports GitLabProvider._iid_from_url.
func iidFromURL(url string) string {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	for i, part := range parts {
		if part == "merge_requests" && i+1 < len(parts) {
			candidate := parts[i+1]
			if _, err := strconv.Atoi(candidate); err == nil {
				return candidate
			}
			return ""
		}
	}
	return ""
}
