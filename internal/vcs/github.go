package vcs

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// githubConclusionMap ports _CI_STATE_MAP from orchestrator/vcs/github.py.
//
// "skipped" mapping to success (not to a distinct skipped state) is the
// actual Python behavior, not an oversight worth "fixing" here — see the
// go-migration-notes.md entry on this package for the discrepancy between
// that and the coordination brief's mention of a distinct skipped state.
var githubConclusionMap = map[string]CIState{
	"success":         CISuccess,
	"completed":       CISuccess,
	"failure":         CIFailure,
	"timed_out":       CIFailure,
	"action_required": CIFailure,
	"cancelled":       CIFailure,
	"neutral":         CISuccess,
	"skipped":         CISuccess,
	"stale":           CIFailure,
}

var githubInProgressStates = map[string]bool{
	"in_progress": true, "queued": true, "waiting": true, "requested": true, "pending": true,
}

// GitHubProvider drives PR creation, CI polling, and merges through the
// `gh` CLI. Ports GitHubProvider (orchestrator/vcs/github.py).
type GitHubProvider struct{}

// NewGitHubProvider builds a GitHubProvider. gh reads its own
// authentication from its config directory; no host/token is threaded
// through here, matching Python (GitHubProvider takes no constructor args).
func NewGitHubProvider() *GitHubProvider { return &GitHubProvider{} }

func (p *GitHubProvider) CreatePR(head, base, title, body string) (string, error) {
	if err := checkBinary("gh"); err != nil {
		return "", err
	}
	out, err := run("gh", nil, "pr", "create", "--title", title, "--body", body, "--head", head, "--base", base)
	if err != nil {
		if authErr := checkAuth("gh", nil); authErr != nil {
			return "", authErr
		}
		return "", nil
	}
	url := strings.TrimSpace(out)
	if url == "" {
		return "", nil
	}
	return url, nil
}

// githubChecksRow is one entry of `gh pr checks --json state,conclusion`.
type githubChecksRow struct {
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
}

func (p *GitHubProvider) CIStatus(prURL string) (CIState, error) {
	if err := checkBinary("gh"); err != nil {
		return "", err
	}
	out, err := run("gh", nil, "pr", "checks", prURL, "--json", "state,conclusion")
	if err != nil {
		return CIPending, nil
	}

	var checks []githubChecksRow
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return CIPending, nil
	}
	if len(checks) == 0 {
		return CIPending, nil
	}

	for _, c := range checks {
		if githubInProgressStates[c.State] {
			return CIPending, nil
		}
	}

	sawFailure := false
	allSuccess := true
	for _, c := range checks {
		mapped, ok := githubConclusionMap[c.Conclusion]
		if !ok {
			mapped = CIPending
		}
		if mapped == CIFailure {
			sawFailure = true
		}
		if mapped != CISuccess {
			allSuccess = false
		}
	}
	if sawFailure {
		return CIFailure, nil
	}
	if allSuccess {
		return CISuccess, nil
	}
	return CIPending, nil
}

type githubPRView struct {
	StatusCheckRollup []struct {
		Conclusion string `json:"conclusion"`
		DetailsURL string `json:"detailsUrl"`
	} `json:"statusCheckRollup"`
}

var reTrailingDigits = regexp.MustCompile(`(\d+)$`)

func (p *GitHubProvider) CILogs(prURL string) (string, error) {
	if err := checkBinary("gh"); err != nil {
		return "", err
	}
	viewOut, err := run("gh", nil, "pr", "view", prURL, "--json", "statusCheckRollup")
	if err != nil {
		return "", nil
	}

	var view githubPRView
	if err := json.Unmarshal([]byte(viewOut), &view); err != nil {
		return "", nil
	}

	var runURL string
	for _, c := range view.StatusCheckRollup {
		if c.Conclusion == "failure" || c.Conclusion == "timed_out" {
			runURL = c.DetailsURL
			break
		}
	}
	if runURL == "" {
		return "", nil
	}

	runID := runIDFromURL(runURL)
	if runID == "" {
		return "", nil
	}

	logOut, err := run("gh", nil, "run", "view", runID, "--log-failed")
	if err != nil {
		return "", nil
	}
	return truncate(logOut, maxLogChars), nil
}

// runIDFromURL extracts the trailing numeric run ID from a GitHub Actions
// URL (".../runs/<id>" or ".../runs/<id>/jobs/<job-id>"). Ports the
// Python's `next(p for p in reversed(parts) if p.isdigit())` — the
// rightmost purely-numeric path segment.
func runIDFromURL(url string) string {
	for _, part := range reversedSplit(strings.TrimRight(url, "/"), "/") {
		if isAllDigits(part) {
			return part
		}
	}
	return ""
}

func reversedSplit(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, len(parts))
	for i, p := range parts {
		out[len(parts)-1-i] = p
	}
	return out
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.Atoi(s)
	return err == nil
}

func (p *GitHubProvider) MergePR(prURL string, squash, auto bool) error {
	if err := checkBinary("gh"); err != nil {
		return err
	}
	args := []string{"pr", "merge", prURL}
	if squash {
		args = append(args, "--squash")
	}
	if auto {
		args = append(args, "--auto")
	}
	if _, err := run("gh", nil, args...); err != nil {
		if authErr := checkAuth("gh", nil); authErr != nil {
			return authErr
		}
		return ErrMergeFailed
	}
	return nil
}
