package vcs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// realComments is `gh api --paginate repos/hectorcanaimero/orch/issues/279/comments`
// as gh 2.100.0 printed it: the Gemini reviewer's marked comment and two of
// the maintainer's replies.
func realComments(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gh", "2.100.0", "issue-comments-no-marker.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// stubGHAPI stands in for `gh api`: the first call gets list, every later one
// gets posted. It returns the argv of each call.
func stubGHAPI(t *testing.T, list string, listErr error, posted string, postErr error) *[][]string {
	t.Helper()
	var calls [][]string
	old := ghAPI
	t.Cleanup(func() { ghAPI = old })
	ghAPI = func(args ...string) (string, error) {
		calls = append(calls, args)
		if len(calls) == 1 {
			return list, listErr
		}
		return posted, postErr
	}
	return &calls
}

// firstComment is one real comment object out of the capture: the shape the
// API answers a POST or PATCH with.
func firstComment(t *testing.T, list string) string {
	t.Helper()
	var page []json.RawMessage
	if err := json.Unmarshal([]byte(list), &page); err != nil || len(page) == 0 {
		t.Fatalf("capture: %v", err)
	}
	return string(page[0])
}

func TestParseCommentsReadsTheRealCapture(t *testing.T) {
	list := realComments(t)
	comments, err := parseComments(list)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 3 || comments[0].ID != 5677503661 ||
		!strings.HasPrefix(comments[0].Body, "<!-- orch:gemini-review -->") ||
		!strings.HasPrefix(comments[0].HTMLURL, "https://github.com/hectorcanaimero/orch/pull/279#issuecomment-") {
		t.Errorf("got %+v", comments)
	}
	// --paginate prints one array per page, back to back.
	two, err := parseComments(list + list)
	if err != nil || len(two) != 6 {
		t.Errorf("two pages: %d comments, %v", len(two), err)
	}
	if _, err := parseComments("[{"); err == nil {
		t.Error("truncated output should be an error")
	}
}

func TestUpsertCommentCreatesWhenNoCommentHasTheMarker(t *testing.T) {
	list := realComments(t)
	calls := stubGHAPI(t, list, nil, firstComment(t, list), nil)

	url, created, err := UpsertComment("hectorcanaimero/orch", 279, "<!-- orch:ci-review -->", "<!-- orch:ci-review -->\nbody")
	if err != nil {
		t.Fatal(err)
	}
	if !created || !strings.Contains(url, "#issuecomment-5677503661") {
		t.Errorf("created=%v url=%q", created, url)
	}
	want := [][]string{
		{"--paginate", "repos/hectorcanaimero/orch/issues/279/comments"},
		{"-X", "POST", "repos/hectorcanaimero/orch/issues/279/comments", "-f", "body=<!-- orch:ci-review -->\nbody"},
	}
	if !equalCalls(*calls, want) {
		t.Errorf("calls = %q\nwant    %q", *calls, want)
	}
}

func TestUpsertCommentEditsTheMarkedComment(t *testing.T) {
	list := realComments(t)
	calls := stubGHAPI(t, list, nil, firstComment(t, list), nil)

	// The capture's marked comment is the Gemini reviewer's: the same lookup,
	// with that marker.
	_, created, err := UpsertComment("hectorcanaimero/orch", 279, "<!-- orch:gemini-review -->", "new body")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("an edit reported as created")
	}
	want := []string{"-X", "PATCH", "repos/hectorcanaimero/orch/issues/comments/5677503661", "-f", "body=new body"}
	if len(*calls) != 2 || !equalCalls((*calls)[1:], [][]string{want}) {
		t.Errorf("calls = %q", *calls)
	}
}

func TestUpsertCommentErrors(t *testing.T) {
	down := errors.New("gh: HTTP 502")
	stubGHAPI(t, "", down, "", nil)
	if _, _, err := UpsertComment("o/r", 1, "m", "b"); !errors.Is(err, down) || !strings.Contains(err.Error(), "listing the comments of o/r#1") {
		t.Errorf("list failure: %v", err)
	}

	list := realComments(t)
	stubGHAPI(t, list, nil, "", down)
	if _, _, err := UpsertComment("o/r", 1, "m", "b"); !errors.Is(err, down) || !strings.Contains(err.Error(), "posting the comment") {
		t.Errorf("post failure: %v", err)
	}

	stubGHAPI(t, "not json", nil, "", nil)
	if _, _, err := UpsertComment("o/r", 1, "m", "b"); err == nil {
		t.Error("unreadable list should be an error")
	}

	stubGHAPI(t, list, nil, "not json", nil)
	if _, _, err := UpsertComment("o/r", 1, "m", "b"); err == nil || !strings.Contains(err.Error(), "reading gh's answer") {
		t.Errorf("unreadable answer: %v", err)
	}
}

func TestGHAPIReportsAMissingGh(t *testing.T) {
	withNoBin(t)
	if _, err := ghAPI("user"); err == nil {
		t.Error("want an error without gh on PATH")
	}
}

func equalCalls(got, want [][]string) bool {
	return slices.EqualFunc(got, want, func(a, b []string) bool { return slices.Equal(a, b) })
}
