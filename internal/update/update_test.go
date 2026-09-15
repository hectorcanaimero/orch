package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "releases.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The real API answer mixes Go releases with the Python line's. Only a
// release carrying this platform's archive can be offered: a wheel is not
// something `orch upgrade` can install.
func TestParseReleasesKeepsOnlyInstallableGoReleases(t *testing.T) {
	got, err := parseReleases(fixture(t), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, r := range got {
		tags = append(tags, r.Tag)
	}
	if strings.Join(tags, " ") != "v0.13.0 v0.12.1 v0.12.0" {
		t.Errorf("tags = %v, want the three Go releases, newest first", tags)
	}
	if got[0].URL != "https://github.com/hectorcanaimero/orch/releases/tag/v0.13.0" {
		t.Errorf("URL = %q", got[0].URL)
	}
	if len(got[0].Highlights) != 3 || !strings.HasPrefix(got[0].Highlights[0], "Overview: a status line") {
		t.Errorf("highlights = %q, want the first three items with emphasis dropped", got[0].Highlights)
	}
	if len(got[1].Highlights) != 0 {
		t.Errorf("v0.12.1 has empty notes, got highlights %q", got[1].Highlights)
	}

	none, err := parseReleases(fixture(t), "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("offered %d releases for a platform nothing is built for", len(none))
	}
}

func TestVersionsCompareNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		less bool
	}{
		{"v0.9.0", "v0.10.0", true},
		{"v0.13.0", "v0.13.1", true},
		{"v0.13.1", "v0.13.0", false},
		{"v1.0.0", "v1.0.0", false},
		{"dev", "v0.1.0", true},
	}
	for _, c := range cases {
		if got := Less(c.a, c.b); got != c.less {
			t.Errorf("Less(%s, %s) = %v, want %v", c.a, c.b, got, c.less)
		}
	}
	for _, v := range []string{"dev", "v0.11.0-py", "v0.13.0-3-gabc1234", "0.13.0", "v0.13"} {
		if IsRelease(v) {
			t.Errorf("IsRelease(%q) = true, want false", v)
		}
	}
}

func TestNewerAndCritical(t *testing.T) {
	releases := []Release{
		{Tag: "v0.14.0"},
		{Tag: "v0.13.1", Critical: true},
		{Tag: "v0.13.0"},
	}
	newer := Newer(releases, "v0.13.0")
	if len(newer) != 2 {
		t.Fatalf("newer = %v, want v0.14.0 and v0.13.1", newer)
	}
	if crit, ok := Critical(newer); !ok || crit.Tag != "v0.13.1" {
		t.Errorf("critical = %v %v, want v0.13.1", crit, ok)
	}
	// Already past the critical one: nothing blocks.
	if _, ok := Critical(Newer(releases, "v0.13.1")); ok {
		t.Errorf("a user on v0.13.1 is blocked by v0.13.1 itself")
	}
	if Newer(releases, "dev") != nil {
		t.Errorf("a dev build was offered releases")
	}
}

func TestHighlights(t *testing.T) {
	body := "## What's Changed\n" +
		"* fix(engine): a CI retry keeps its PR by @someone in https://github.com/hectorcanaimero/orch/pull/277\n" +
		"- **Live**: `orch dashboard` serves the portal\n" +
		"\n1. not a bullet\n"
	got := Highlights(body, 5)
	want := []string{
		"fix(engine): a CI retry keeps its PR (#277)",
		"Live: orch dashboard serves the portal",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Highlights = %q, want %q", got, want)
	}
	long := "- " + strings.Repeat("x", 300)
	if r := []rune(Highlights(long, 1)[0]); len(r) != maxLength {
		t.Errorf("a long item is %d runes, want it cut to %d", len(r), maxLength)
	}
}

func TestCheckReadsGitHubOnceADay(t *testing.T) {
	var hits atomic.Int32
	body := fixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/hectorcanaimero/orch/releases" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := Client{API: srv.URL}
	path := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	for _, at := range []time.Time{now, now.Add(time.Hour), now.Add(23 * time.Hour)} {
		if _, err := Check(context.Background(), c, path, at); err != nil {
			t.Fatalf("Check at %v: %v", at, err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("GitHub was asked %d times within a day, want once", hits.Load())
	}
	if _, err := Check(context.Background(), c, path, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Errorf("GitHub was asked %d times after a day, want twice", hits.Load())
	}
}

// Offline, the last known releases still answer, and the failed attempt is
// remembered so the next command does not wait on the network again.
func TestCheckOfflineKeepsTheLastAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	if err := writeCache(path, cache{CheckedAt: now.Add(-48 * time.Hour), Releases: []Release{{Tag: "v0.13.0"}}}); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := Client{API: srv.URL}

	got, err := Check(context.Background(), c, path, now)
	if err == nil || len(got) != 1 || got[0].Tag != "v0.13.0" {
		t.Fatalf("Check = %v, %v; want the cached release and the error", got, err)
	}
	if _, err := Check(context.Background(), c, path, now.Add(time.Minute)); err != nil {
		t.Errorf("second check: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("GitHub was asked %d times, want once", hits.Load())
	}
}

func TestNotice(t *testing.T) {
	if Notice("v0.13.0", nil) != "" {
		t.Errorf("a notice with nothing newer")
	}
	newer := []Release{
		{Tag: "v0.13.2", URL: "https://example.test/v0.13.2", Highlights: []string{"a", "b"}},
		{Tag: "v0.13.1", URL: "https://example.test/v0.13.1", Highlights: []string{"c"}},
	}
	got := Notice("v0.13.0", newer)
	for _, want := range []string{"A new orch is out: v0.13.2 (you have v0.13.0)", "orch upgrade", "• a", "• c", "https://example.test/v0.13.2", "2 releases since yours"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice is missing %q:\n%s", want, got)
		}
	}
	newer[1].Critical = true
	if got := Notice("v0.13.0", newer); !strings.Contains(got, "v0.13.1 is a critical update") {
		t.Errorf("critical notice:\n%s", got)
	}
}
