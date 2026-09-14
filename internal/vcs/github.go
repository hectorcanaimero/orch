package vcs

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// gh reports a check that has not started as EXPECTED, which Python's
	// list does not name. Grouped with the rest of "not finished yet"
	// rather than falling through to the conclusion map, where an unknown
	// key already means pending — same answer, said on purpose.
	"expected": true,
}

// normalizeCheckState lowercases gh's UPPERCASE `state` so the two maps
// above — which are Python's vocabulary, lowercase, and are GitHub's own —
// can be looked up with it.
//
// The maps are not rewritten in uppercase because they are the ported
// tables: `_CI_STATE_MAP` in orchestrator/vcs/github.py is the reference,
// and a reader comparing the two files should see the same keys.
func normalizeCheckState(state string) string {
	return strings.ToLower(strings.TrimSpace(state))
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

// githubChecksRow is one entry of `gh pr checks --json name,state,bucket`.
//
// There is NO `conclusion` field, and asking for one is a usage error that
// makes gh exit non-zero — which is how this went unnoticed: both binaries
// asked for `state,conclusion`, gh refused the whole command, and the
// refusal was swallowed into "pending". See CIStatus.
//
// `state` is UPPERCASE (`SUCCESS`, `NEUTRAL`, `IN_PROGRESS`) — it carries the
// check's conclusion once it has one and its status before that, which is why
// one field answers both questions the maps below ask. `bucket` is gh's own
// lowercase normalisation (`pass`, `fail`, `pending`, `skipping`, `cancel`);
// captured but not mapped on, because the vocabulary this package ports is
// GitHub's own and `bucket` is gh's editorial summary of it.
//
// Captured from gh 2.100.0 — testdata/gh/2.100.0/.
type githubChecksRow struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Bucket string `json:"bucket"`
}

func (p *GitHubProvider) CIStatus(prURL string) (CIState, error) {
	if err := checkBinary("gh"); err != nil {
		return "", err
	}
	// `state` only. `conclusion` is not a field gh offers, and asking for it
	// fails the whole command — see githubChecksRow.
	out, err := run("gh", nil, "pr", "checks", prURL, "--json", "name,state,bucket")
	var re *runError
	if errors.As(err, &re) && strings.Contains(re.stderr, "no checks reported") {
		// gh's own spelling of an empty list: exit 1 and this on stderr.
		return p.noChecks(prURL)
	}
	if err != nil {
		// Reported, not swallowed. Python returns "pending" here and so did
		// this, which is exactly what hid a command that could never succeed:
		// a run whose CI never resolves looks the same as one still waiting.
		// The poller logs this and carries on to the next task (cipoll.go),
		// so surfacing it costs nothing and buys the operator the one line
		// that says why nothing is finishing.
		return CIPending, fmt.Errorf("gh pr checks %s: %w", prURL, err)
	}

	var checks []githubChecksRow
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return CIPending, fmt.Errorf("parsing gh pr checks output for %s: %w", prURL, err)
	}
	if len(checks) == 0 {
		return p.noChecks(prURL)
	}

	for _, c := range checks {
		if githubInProgressStates[normalizeCheckState(c.State)] {
			return CIPending, nil
		}
	}

	sawFailure := false
	allSuccess := true
	for _, c := range checks {
		mapped, ok := githubConclusionMap[normalizeCheckState(c.State)]
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

// noChecks tells a PR that has no checks because it conflicts with its base
// from one that simply has none. An unreadable answer is pending with the
// error, never none: none can end in the task being finished.
func (p *GitHubProvider) noChecks(prURL string) (CIState, error) {
	out, err := run("gh", nil, "pr", "view", prURL, "--json", "mergeable")
	if err != nil {
		return CIPending, fmt.Errorf("gh pr view %s --json mergeable: %w", prURL, err)
	}
	var view struct {
		Mergeable string `json:"mergeable"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return CIPending, fmt.Errorf("parsing gh pr view output for %s: %w", prURL, err)
	}
	if view.Mergeable == "CONFLICTING" {
		return CIConflict, nil
	}
	return CINone, nil
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
