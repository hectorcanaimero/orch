package vcs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// IssueComment is one entry of `gh api repos/{repo}/issues/{n}/comments`.
// Pull request conversation comments are issue comments to the API. Captured,
// not guessed: see testdata/gh/2.100.0/issue-comments-*.json.
type IssueComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

// ghAPI runs `gh api <args>` and returns its stdout. A variable so tests can
// stand in for gh.
var ghAPI = func(args ...string) (string, error) {
	if err := checkBinary("gh"); err != nil {
		return "", err
	}
	return run("gh", nil, append([]string{"api"}, args...)...)
}

// UpsertComment makes body the one comment on issue or pull request number
// whose body contains marker: it edits that comment when there is one and
// creates it otherwise. gh authenticates itself (GH_TOKEN on a runner). It
// returns the comment's URL and whether it was created.
func UpsertComment(repo string, number int, marker, body string) (url string, created bool, err error) {
	list := fmt.Sprintf("repos/%s/issues/%d/comments", repo, number)
	out, err := ghAPI("--paginate", list)
	if err != nil {
		return "", false, fmt.Errorf("listing the comments of %s#%d: %w", repo, number, err)
	}
	comments, err := parseComments(out)
	if err != nil {
		return "", false, fmt.Errorf("listing the comments of %s#%d: %w", repo, number, err)
	}

	method, path := "POST", list
	if c, ok := markedComment(comments, marker); ok {
		method, path = "PATCH", fmt.Sprintf("repos/%s/issues/comments/%d", repo, c.ID)
	}
	out, err = ghAPI("-X", method, path, "-f", "body="+body)
	if err != nil {
		return "", false, fmt.Errorf("posting the comment on %s#%d: %w", repo, number, err)
	}
	var posted IssueComment
	if err := json.Unmarshal([]byte(out), &posted); err != nil {
		return "", false, fmt.Errorf("reading gh's answer to the comment on %s#%d: %w", repo, number, err)
	}
	return posted.HTMLURL, method == "POST", nil
}

// parseComments reads `gh api --paginate` output: one JSON array per page,
// written one after another.
func parseComments(out string) ([]IssueComment, error) {
	dec := json.NewDecoder(strings.NewReader(out))
	var all []IssueComment
	for {
		var page []IssueComment
		err := dec.Decode(&page)
		if errors.Is(err, io.EOF) {
			return all, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decoding gh output: %w", err)
		}
		all = append(all, page...)
	}
}

// markedComment is the first comment carrying marker: the oldest, which is
// the one a previous run created.
func markedComment(comments []IssueComment, marker string) (IssueComment, bool) {
	for _, c := range comments {
		if strings.Contains(c.Body, marker) {
			return c, true
		}
	}
	return IssueComment{}, false
}
