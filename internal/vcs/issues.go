package vcs

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Issue is one GitHub issue, in the shape `gh issue list --json …` returns.
//
// Captured, not guessed: internal/vcs/testdata/gh/2.100.0/issue-list.json is a
// real payload from this repository. The field set is gh's to change, which is
// why the fixture carries its version in the path — and why the tests read it
// rather than a literal written from this struct (see the README there, and
// bug 27, which was a field nobody had ever seen gh emit).
type Issue struct {
	Number    int          `json:"number"`
	Title     string       `json:"title"`
	Body      string       `json:"body"`
	State     string       `json:"state"`
	URL       string       `json:"url"`
	CreatedAt string       `json:"createdAt"`
	Labels    []IssueLabel `json:"labels"`
}

// IssueLabel is one label on an issue. gh returns `id`, `name`, `description`
// and `color`; only the name is load-bearing here, and the rest are left off
// rather than carried unused.
type IssueLabel struct {
	Name string `json:"name"`
}

// LabelNames is the labels as plain strings, in gh's order.
func (i Issue) LabelNames() []string {
	out := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		out = append(out, l.Name)
	}
	return out
}

// IssueQuery narrows what ListIssues asks for.
type IssueQuery struct {
	// Label is required. A sync with no label would ingest an entire issue
	// tracker, which is never what anyone means.
	Label string
	// State is "open" (the default), "closed" or "all", matching gh's own
	// vocabulary so an operator who knows gh needs no translation.
	State string
	// Limit caps how many gh returns. gh's own default is 30, which would
	// silently truncate a real backlog.
	Limit int
	// Dir is the working directory gh runs in, which is how gh decides
	// which repository it is reading. Should be the project root, not the
	// shell's cwd — see runIn. Empty means the process cwd.
	Dir string
}

// issueListFields is what ListIssues asks gh for.
//
// Named as a constant next to the struct it fills, because the two have to
// agree and bug 27 was exactly the case where they did not — a field asked
// for that gh does not have makes the whole command a usage error.
const issueListFields = "number,title,body,state,url,createdAt,labels"

// ListIssues returns the issues carrying a label.
//
// Filtered by gh server-side rather than here: `--label` is gh's own flag, an
// unmatched label returns `[]` rather than an error, and filtering client-side
// would mean paging the whole tracker to throw most of it away.
func (p *GitHubProvider) ListIssues(q IssueQuery) ([]Issue, error) {
	if err := checkBinary("gh"); err != nil {
		return nil, err
	}
	if q.Label == "" {
		return nil, fmt.Errorf("vcs: listing issues needs a label")
	}
	state := q.State
	if state == "" {
		state = "open"
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultIssueLimit
	}

	out, err := runIn(q.Dir, "gh", nil, "issue", "list",
		"--label", q.Label,
		"--state", state,
		"--limit", fmt.Sprintf("%d", limit),
		"--json", issueListFields)
	if err != nil {
		// Naming the directory is not decoration: gh reads the repository
		// from the git remote of its working directory, so "not a git
		// repository" and "no such label" are both answers ABOUT that
		// directory, and the operator's cwd is usually a different one.
		where := q.Dir
		if where == "" {
			where = "the current directory"
		}
		return nil, fmt.Errorf("gh issue list --label %q in %s: %w", q.Label, where, err)
	}

	var issues []Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parsing gh issue list output: %w", err)
	}
	return issues, nil
}

// defaultIssueLimit is how many issues one sync reads.
//
// gh's own default is 30 and it truncates silently; a tracker with more
// labelled issues than that would sync a third of itself and say nothing.
// 500 is well past any backlog someone would hand to one orch project, and
// `--limit` moves it.
const defaultIssueLimit = 500

// SearchIssues returns up to limit issues in repo carrying label whose title
// matches the words of query, open and closed alike. gh's `--search` is
// GitHub's issue search, so the match is fuzzy by design: it is a list of
// candidates for a human or a model to judge, not a verdict.
func (p *GitHubProvider) SearchIssues(repo, label, query string, limit int) ([]Issue, error) {
	if err := checkBinary("gh"); err != nil {
		return nil, err
	}
	out, err := run("gh", nil, "issue", "list", "--repo", repo,
		"--label", label, "--state", "all",
		"--search", query+" in:title",
		"--limit", fmt.Sprintf("%d", limit),
		"--json", issueListFields)
	if err != nil {
		return nil, fmt.Errorf("gh issue list --repo %s --search %q: %w", repo, query, err)
	}
	var issues []Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parsing gh issue list output: %w", err)
	}
	return issues, nil
}

// CreateIssue files an issue in repo and returns its URL, which is what
// `gh issue create` prints.
func (p *GitHubProvider) CreateIssue(repo, title, body string, labels []string) (string, error) {
	if err := checkBinary("gh"); err != nil {
		return "", err
	}
	args := []string{"issue", "create", "--repo", repo, "--title", title, "--body", body}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	out, err := run("gh", nil, args...)
	if err != nil {
		if authErr := checkAuth("gh", nil); authErr != nil {
			return "", authErr
		}
		return "", fmt.Errorf("gh issue create --repo %s: %w", repo, err)
	}
	url := strings.TrimSpace(out)
	if url == "" {
		return "", fmt.Errorf("gh issue create --repo %s printed no URL", repo)
	}
	return url, nil
}
