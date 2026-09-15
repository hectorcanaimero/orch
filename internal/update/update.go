// Package update tells the operator a newer orch is out, and replaces the
// binary on request (`orch upgrade`).
//
// Releases ship daily at times, so a user has to be told. The check is one
// unauthenticated read of GitHub's releases API at most once a day, cached in
// ~/.orch; nothing about the user or the project is sent. A release whose
// notes carry CriticalMarker is critical: `orch run` and `orch dashboard`
// refuse to start on an older version until the user upgrades or says to go
// ahead anyway.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// Repo is where orch is released.
	Repo = "hectorcanaimero/orch"
	// CriticalMarker, anywhere in a release's notes, makes it critical.
	CriticalMarker = "<!-- orch:critical -->"
	// DisableEnv turns the check, the notice and the critical gate off.
	DisableEnv = "ORCH_NO_UPDATE_CHECK"
	// MaxAge is how long a check is trusted before GitHub is asked again.
	MaxAge = 24 * time.Hour

	defaultAPI = "https://api.github.com"
)

// Release is one Go release of orch, as the notice needs it.
type Release struct {
	Tag      string `json:"tag"`
	URL      string `json:"url"`
	Critical bool   `json:"critical"`
	// Highlights are the first items of the release notes, one line each.
	Highlights []string `json:"highlights"`
}

// Client reads orch's releases.
type Client struct {
	HTTP *http.Client
	// API is GitHub's API root; empty means api.github.com.
	API string
}

type apiRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Body       string `json:"body"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// Releases lists the published Go releases for this OS and architecture,
// newest first. Drafts, pre-releases and the Python line's tags are left
// out: none of them has a binary `orch upgrade` could install.
func (c Client) Releases(ctx context.Context) ([]Release, error) {
	api := c.API
	if api == "" {
		api = defaultAPI
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+Repo+"/releases?per_page=20", nil)
	if err != nil {
		return nil, fmt.Errorf("building the releases request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking GitHub for orch's releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("asking GitHub for orch's releases: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading orch's releases: %w", err)
	}
	return parseReleases(body, runtime.GOOS, runtime.GOARCH)
}

func parseReleases(body []byte, goos, goarch string) ([]Release, error) {
	var raw []apiRelease
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("reading orch's releases: %w", err)
	}
	var out []Release
	for _, r := range raw {
		if r.Draft || r.Prerelease {
			continue
		}
		if _, ok := parseVersion(r.TagName); !ok {
			continue
		}
		asset := AssetName(r.TagName, goos, goarch)
		has := false
		for _, a := range r.Assets {
			has = has || a.Name == asset
		}
		if !has {
			continue
		}
		out = append(out, Release{
			Tag:        r.TagName,
			URL:        r.HTMLURL,
			Critical:   strings.Contains(r.Body, CriticalMarker),
			Highlights: Highlights(r.Body, 3),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return Less(out[j].Tag, out[i].Tag) })
	return out, nil
}

// AssetName is the archive goreleaser publishes for one platform.
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("orch_%s_%s_%s.tar.gz", tag, goos, goarch)
}

// parseVersion reads a release tag: v<major>.<minor>.<patch> and nothing
// else, so `-py` tags, pre-releases and dev builds ("dev", git describe's
// "v0.13.0-3-gabc123") are not versions an update can be compared against.
func parseVersion(tag string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if !strings.HasPrefix(tag, "v") || len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// IsRelease reports whether version is a release tag, not a dev build.
func IsRelease(version string) bool {
	_, ok := parseVersion(version)
	return ok
}

// Less reports whether release tag a is older than b. Unparseable tags sort
// first.
func Less(a, b string) bool {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	if oka != okb {
		return !oka
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return false
}

// Newer returns the releases newer than current, newest first. A current
// that is not a release tag has nothing to compare against: nil.
func Newer(releases []Release, current string) []Release {
	if !IsRelease(current) {
		return nil
	}
	var out []Release
	for _, r := range releases {
		if Less(current, r.Tag) {
			out = append(out, r)
		}
	}
	return out
}

// Critical returns the newest critical release among newer, if any.
func Critical(newer []Release) (Release, bool) {
	for _, r := range newer {
		if r.Critical {
			return r, true
		}
	}
	return Release{}, false
}

var (
	listItem  = regexp.MustCompile(`^\s*[-*]\s+(.+)$`)
	byAuthor  = regexp.MustCompile(`\s+by @\S+ in https://github\.com/\S+/pull/(\d+)\s*$`)
	emphasis  = strings.NewReplacer("**", "", "__", "", "`", "")
	maxLength = 110
)

// Highlights returns the first max list items of release notes, as plain
// one-line text: emphasis dropped, GitHub's generated "by @x in <PR URL>"
// shortened to "(#N)", long items cut. Notes with no list give none.
func Highlights(body string, max int) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		m := listItem.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		item := byAuthor.ReplaceAllString(strings.TrimSpace(m[1]), " (#$1)")
		item = emphasis.Replace(item)
		if r := []rune(item); len(r) > maxLength {
			item = strings.TrimSpace(string(r[:maxLength-1])) + "…"
		}
		out = append(out, item)
		if len(out) == max {
			break
		}
	}
	return out
}

// cache is what the last check found, so the API is read once a day.
type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Releases  []Release `json:"releases"`
}

// CachePath is where checks are remembered: ~/.orch/update-check.json.
func CachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory: %w", err)
	}
	return filepath.Join(home, ".orch", "update-check.json"), nil
}

// Check returns orch's releases, from the cache at path while it is younger
// than MaxAge and from GitHub otherwise. When GitHub cannot be reached the
// last known releases are returned with the error, and the attempt is still
// recorded: offline, every command would otherwise wait out the timeout.
func Check(ctx context.Context, c Client, path string, now time.Time) ([]Release, error) {
	var old cache
	if data, err := os.ReadFile(path); err == nil { // #nosec G304 -- orch's own cache file
		if json.Unmarshal(data, &old) == nil && now.Sub(old.CheckedAt) < MaxAge && !now.Before(old.CheckedAt) {
			return old.Releases, nil
		}
	}
	releases, fetchErr := c.Releases(ctx)
	if fetchErr != nil {
		releases = old.Releases
	}
	if err := writeCache(path, cache{CheckedAt: now, Releases: releases}); err != nil && fetchErr == nil {
		return releases, err
	}
	return releases, fetchErr
}

func writeCache(path string, c cache) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding the update cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Notice is what a user on current reads about newer, or "" when there is
// nothing newer.
func Notice(current string, newer []Release) string {
	if len(newer) == 0 {
		return ""
	}
	latest := newer[0]
	var b strings.Builder
	if crit, ok := Critical(newer); ok {
		fmt.Fprintf(&b, "orch %s is a critical update (you have %s): orch run and orch dashboard will not start until you run: orch upgrade\n",
			crit.Tag, current)
	} else {
		fmt.Fprintf(&b, "A new orch is out: %s (you have %s). Update with: orch upgrade\n", latest.Tag, current)
	}
	shown := 0
	for _, r := range newer {
		for _, h := range r.Highlights {
			if shown == 5 {
				break
			}
			fmt.Fprintf(&b, "  • %s\n", h)
			shown++
		}
	}
	fmt.Fprintf(&b, "  Release notes: %s\n", latest.URL)
	if len(newer) > 1 {
		fmt.Fprintf(&b, "  (%d releases since yours)\n", len(newer))
	}
	return b.String()
}
