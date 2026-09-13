package publish

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/publish/cloudfake"
)

// writeSite lays out a directory shaped like an export.
func writeSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func sampleSite(t *testing.T) string {
	return writeSite(t, map[string]string{
		"index.html":       "<!doctype html><title>client</title>",
		"assets/app-1.js":  "console.log(1)",
		"data.json":        `{"project_name":"Cliente"}`,
		"data.js":          `window.__ORCH_SNAPSHOT__ = {};`,
		"robots.txt":       "User-agent: *\nDisallow: /\n",
		"favicon.svg":      "<svg/>",
		"assets/style.css": "body{}",
	})
}

// loggedIn starts a fake Worker and logs in to it, returning the options a
// publish needs.
func loggedIn(t *testing.T) (*cloudfake.Worker, CloudOptions) {
	t.Helper()
	w := cloudfake.New()
	t.Cleanup(w.Close)
	cred := filepath.Join(t.TempDir(), ".orch", "credentials")
	if _, err := CloudLogin(context.Background(), cred, w.URL, w.AdminToken); err != nil {
		t.Fatalf("CloudLogin: %v", err)
	}
	return w, CloudOptions{CredentialsPath: cred, ProjectID: "cliente", Getenv: noEnv}
}

func noEnv(string) string { return "" }

func mustLoad(t *testing.T, path string) CredentialsFile {
	t.Helper()
	cf, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	return cf
}

func getStatus(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) // #nosec G107 -- a test server URL
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// The acceptance path, client side: the first publish registers the project
// and stores its tokens, the page is served at the printed link, a second
// publish of the same bytes changes nothing, and a changed file is a new
// version.
func TestPublishToCloudCreatesOnceThenReusesTheTokens(t *testing.T) {
	w, opts := loggedIn(t)
	dir := sampleSite(t)
	ctx := context.Background()

	first, err := PublishToCloud(ctx, dir, opts)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if !first.Created || !first.Upload.Changed || first.Upload.Version != 1 {
		t.Errorf("first publish = %+v, want created, changed, version 1", first)
	}
	stored := mustLoad(t, opts.CredentialsPath).Cloud.Projects["cliente"]
	if stored.PublishToken == "" || stored.ViewToken == "" {
		t.Fatalf("tokens not stored after the first publish: %+v", stored)
	}
	if first.ViewerURL != w.URL+"/v/"+stored.ViewToken+"/" {
		t.Errorf("viewer URL = %q, want the stored view token's link", first.ViewerURL)
	}
	if code, body := getStatus(t, first.ViewerURL); code != 200 || !strings.Contains(body, "<title>client</title>") {
		t.Errorf("GET viewer = %d %q, want the uploaded index.html", code, body)
	}
	if len(w.SiteFiles("cliente")) != 7 {
		t.Errorf("the Worker holds %v, want the 7 files of the export", w.SiteFiles("cliente"))
	}
	if w.StoresToken(stored.PublishToken) || w.StoresToken(stored.ViewToken) {
		t.Error("the fake stored a token in the clear — the client test would be testing the wrong contract")
	}

	second, err := PublishToCloud(ctx, dir, opts)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if second.Created || second.Upload.Changed || second.Upload.Version != 1 {
		t.Errorf("second publish of the same bytes = %+v, want not created, unchanged, version 1", second)
	}
	if again := mustLoad(t, opts.CredentialsPath).Cloud.Projects["cliente"]; again != stored {
		t.Error("a second publish replaced the stored tokens")
	}

	if err := os.WriteFile(filepath.Join(dir, "assets", "app-1.js"), []byte("console.log(2)"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := PublishToCloud(ctx, dir, opts)
	if err != nil {
		t.Fatalf("third publish: %v", err)
	}
	if !third.Upload.Changed || third.Upload.Version != 2 {
		t.Errorf("publish after changing a bundle file = %+v, want changed, version 2", third)
	}
}

func TestRotateViewTokenKillsTheOldLinkAndStoresTheNewOne(t *testing.T) {
	_, opts := loggedIn(t)
	ctx := context.Background()
	first, err := PublishToCloud(ctx, sampleSite(t), opts)
	if err != nil {
		t.Fatal(err)
	}

	newURL, err := RotateCloudViewToken(ctx, opts)
	if err != nil {
		t.Fatalf("RotateCloudViewToken: %v", err)
	}
	if newURL == first.ViewerURL {
		t.Fatal("rotation returned the same link")
	}
	if code, _ := getStatus(t, first.ViewerURL); code != 404 {
		t.Errorf("old link answers %d after rotation, want 404", code)
	}
	if code, _ := getStatus(t, newURL); code != 200 {
		t.Errorf("new link answers %d, want 200", code)
	}
	stored := mustLoad(t, opts.CredentialsPath).Cloud.Projects["cliente"]
	if !strings.Contains(newURL, "/v/"+stored.ViewToken+"/") {
		t.Error("the stored view token is not the one in the new link")
	}
}

func TestRotatePublishTokenLocksOutTheOldOne(t *testing.T) {
	w, opts := loggedIn(t)
	ctx := context.Background()
	if _, err := PublishToCloud(ctx, sampleSite(t), opts); err != nil {
		t.Fatal(err)
	}
	old := mustLoad(t, opts.CredentialsPath).Cloud.Projects["cliente"].PublishToken

	if err := RotateCloudPublishToken(ctx, opts); err != nil {
		t.Fatalf("RotateCloudPublishToken: %v", err)
	}
	now := mustLoad(t, opts.CredentialsPath).Cloud.Projects["cliente"].PublishToken
	if now == "" || now == old {
		t.Fatal("the publish token was not replaced in the credentials file")
	}

	client, err := NewCloudClient(w.URL)
	if err != nil {
		t.Fatal(err)
	}
	site, err := ReadCloudSite(sampleSite(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.UploadSite(ctx, old, "cliente", site)
	var ce *CloudError
	if !errors.As(err, &ce) || ce.Code != "unauthorized" {
		t.Fatalf("upload with the rotated-out token = %v, want unauthorized", err)
	}
	for _, want := range []string{`"cliente"`, "orch cloud rotate --publish"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unauthorized message %q does not name %s", err, want)
		}
	}
}

func TestUnauthorizedAdminNamesLogin(t *testing.T) {
	w := cloudfake.New()
	defer w.Close()
	client, err := NewCloudClient(w.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = client.WhoAmI(context.Background(), cloudfake.Token())
	if !IsCloudCode(err, "unauthorized") || !strings.Contains(err.Error(), "orch cloud login") {
		t.Errorf("WhoAmI with a wrong token = %v, want unauthorized naming `orch cloud login`", err)
	}
}

func TestCloudLoginWithAWrongTokenSavesNothing(t *testing.T) {
	w := cloudfake.New()
	defer w.Close()
	cred := filepath.Join(t.TempDir(), ".orch", "credentials")
	if _, err := CloudLogin(context.Background(), cred, w.URL, cloudfake.Token()); err == nil {
		t.Fatal("login with a wrong admin token succeeded")
	}
	if _, err := os.Stat(cred); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused login left a credentials file behind (stat: %v)", err)
	}
}

func TestCloudLoginToAnotherWorkerDropsTheOldProjectTokens(t *testing.T) {
	_, opts := loggedIn(t)
	ctx := context.Background()
	if _, err := PublishToCloud(ctx, sampleSite(t), opts); err != nil {
		t.Fatal(err)
	}
	other := cloudfake.New()
	defer other.Close()
	dropped, err := CloudLogin(ctx, opts.CredentialsPath, other.URL, other.AdminToken)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	cf := mustLoad(t, opts.CredentialsPath)
	if cf.Cloud.URL != other.URL || len(cf.Cloud.Projects) != 0 {
		t.Errorf("after switching Workers: %+v, want the new URL and no projects", cf.Cloud)
	}
}

// The limits are the Worker's, but the client checks them first: a site the
// Worker would refuse must fail with zero requests, so a first publish never
// registers a project it cannot upload to.
func TestSiteLimitsAreEnforcedBeforeAnyRequest(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"a file over 5 MiB": func(t *testing.T) string {
			return writeSite(t, map[string]string{
				"index.html": "x",
				"big.bin":    strings.Repeat("a", cloudMaxFileBytes+1),
			})
		},
		"more than 200 files": func(t *testing.T) string {
			files := map[string]string{"index.html": "x"}
			for i := 0; i < cloudMaxFiles; i++ {
				files["assets/f"+strconv.Itoa(i)+".js"] = "1"
			}
			return writeSite(t, files)
		},
		"no index.html": func(t *testing.T) string {
			return writeSite(t, map[string]string{"data.json": "{}"})
		},
		"a body over 10 MiB once encoded": func(t *testing.T) string {
			// Three files each under the per-file limit, together over the
			// body limit after base64's 4/3.
			chunk := strings.Repeat("b", 3<<20)
			return writeSite(t, map[string]string{"index.html": "x", "a.bin": chunk, "b.bin": chunk, "c.bin": chunk})
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			w := cloudfake.New()
			defer w.Close()
			cred := filepath.Join(t.TempDir(), "credentials")
			// Credentials written directly: CloudLogin itself makes requests.
			if err := SaveCredentials(cred, CredentialsFile{Cloud: &CloudCredentials{
				URL: w.URL, AdminToken: w.AdminToken}}); err != nil {
				t.Fatal(err)
			}
			_, err := PublishToCloud(context.Background(), mk(t),
				CloudOptions{CredentialsPath: cred, ProjectID: "cliente", Getenv: noEnv})
			if err == nil {
				t.Fatal("publish succeeded")
			}
			if n := w.Requests(); n != 0 {
				t.Errorf("the Worker saw %d request(s) before the limit was enforced (%v)", n, err)
			}
			if w.HasProject("cliente") {
				t.Error("a site that could not be uploaded registered a project")
			}
		})
	}
}

// An export contains only files. A symlink in the directory — pointing at
// something outside it — is refused rather than followed, so an upload can
// never carry a file the export did not write.
func TestReadCloudSiteRefusesASymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("not for the page"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := sampleSite(t)
	if err := os.Symlink(outside, filepath.Join(dir, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := ReadCloudSite(dir)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("ReadCloudSite with a symlink = %v, want it refused as not a regular file", err)
	}
}

func TestPublishToAProjectThatExistsWithoutLocalTokensExplains(t *testing.T) {
	w, opts := loggedIn(t)
	client, err := NewCloudClient(w.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateProject(context.Background(), w.AdminToken, "cliente"); err != nil {
		t.Fatal(err)
	}
	_, err = PublishToCloud(context.Background(), sampleSite(t), opts)
	if !IsCloudCode(err, "conflict") {
		t.Fatalf("publish = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "orch cloud rotate --publish") {
		t.Errorf("conflict message %q does not say how to recover", err)
	}
}

func TestEnvOverridePublishesWithoutACredentialsFile(t *testing.T) {
	w := cloudfake.New()
	defer w.Close()
	client, err := NewCloudClient(w.URL)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := client.CreateProject(context.Background(), w.AdminToken, "cliente")
	if err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(t.TempDir(), ".orch", "credentials")
	env := map[string]string{EnvCloudURL: w.URL, EnvCloudPublishToken: tokens.PublishToken}

	res, err := PublishToCloud(context.Background(), sampleSite(t), CloudOptions{
		CredentialsPath: cred, ProjectID: "cliente", Getenv: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatalf("env publish: %v", err)
	}
	if !res.FromEnv || res.Created || res.ViewerURL != "" || w.SiteVersion("cliente") != 1 {
		t.Errorf("env publish = %+v (site version %d), want from env, not created, no viewer URL, version 1",
			res, w.SiteVersion("cliente"))
	}
	if _, err := os.Stat(cred); !errors.Is(err, os.ErrNotExist) {
		t.Error("an env publish wrote a credentials file")
	}
}

func TestResolveCloudTarget(t *testing.T) {
	cf := CredentialsFile{Cloud: &CloudCredentials{URL: "https://w.example", AdminToken: "a",
		Projects: map[string]CloudProject{"p": {PublishToken: "pub", ViewToken: "view"}}}}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	got, err := ResolveCloudTarget(cf, "p", noEnv)
	if err != nil || got.FromEnv || got.PublishToken != "pub" || got.ViewToken != "view" || got.AdminToken != "a" {
		t.Errorf("from file = %+v, %v", got, err)
	}
	got, err = ResolveCloudTarget(cf, "p", env(map[string]string{EnvCloudURL: "https://ci.example", EnvCloudPublishToken: "citok"}))
	if err != nil || !got.FromEnv || got.URL != "https://ci.example" || got.PublishToken != "citok" || got.AdminToken != "" {
		t.Errorf("from env = %+v, %v — env must win whole, never mixed with the file's admin token", got, err)
	}
	if _, err := ResolveCloudTarget(cf, "p", env(map[string]string{EnvCloudURL: "https://ci.example"})); err == nil {
		t.Error("a URL without a publish token in the env was accepted")
	}
	if _, err := ResolveCloudTarget(CredentialsFile{}, "p", noEnv); err == nil || !strings.Contains(err.Error(), "orch cloud login") {
		t.Errorf("no credentials = %v, want a hint naming `orch cloud login`", err)
	}
}

func TestSaveCredentialsIsPrivateAtomicAndKeepsUnknownBlocks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".orch")
	path := filepath.Join(dir, "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"future":{"k":1},"cloud":{"url":"https://old.example"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cf := mustLoad(t, path)
	cf.Cloud = &CloudCredentials{URL: "https://new.example"}
	if err := SaveCredentials(path, cf); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials mode = %o, want 600", perm)
	}
	var top map[string]json.RawMessage
	raw, err := os.ReadFile(path) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["future"]) == "" {
		t.Error("a block this binary does not know was dropped on save")
	}
	if mustLoad(t, path).Cloud.URL != "https://new.example" {
		t.Error("the cloud block was not replaced")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries after a save, want only the credentials file", len(entries))
	}
}

func TestSaveCredentialsCreatesAPrivateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".orch", "credentials")
	if err := SaveCredentials(path, CredentialsFile{Cloud: &CloudCredentials{URL: "https://w.example"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("~/.orch mode = %o, want 700", perm)
	}
}

// A save that cannot complete leaves the previous file untouched and no
// temporary file behind: the tokens in it are not recoverable from the Worker.
func TestSaveCredentialsFailureLeavesTheOldFileAndNoTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	// A directory where the file should be: the final rename cannot replace it.
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := SaveCredentials(path, CredentialsFile{Cloud: &CloudCredentials{URL: "https://w.example"}})
	if err == nil {
		t.Fatal("save over a directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".credentials-") {
			t.Errorf("a failed save left %s behind", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(path, "occupied")); err != nil {
		t.Error("the failed save disturbed what was at the path")
	}
}

func TestLoadCredentialsMissingFileIsEmpty(t *testing.T) {
	cf, err := LoadCredentials(filepath.Join(t.TempDir(), "nope"))
	if err != nil || cf.Cloud != nil {
		t.Errorf("missing file = %+v, %v; want empty and no error", cf, err)
	}
}

func TestNewCloudClientRefusesPlainHTTPExceptLoopback(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://orch-cloud.example.workers.dev":  true,
		"https://orch-cloud.example.workers.dev/": true,
		"http://127.0.0.1:8787":                   true,
		"http://localhost:8787":                   true,
		"http://orch-cloud.example.workers.dev":   false,
		"ftp://example.com":                       false,
		"https://example.com/?x=1":                false,
		"not a url":                               false,
	} {
		_, err := NewCloudClient(url)
		if (err == nil) != ok {
			t.Errorf("NewCloudClient(%q) error = %v, want ok=%v", url, err, ok)
		}
	}
}

func TestCloudProjectID(t *testing.T) {
	for in, want := range map[string]string{"billing-api": "billing-api", "Cliente": "cliente", "a1": "a1"} {
		if got, err := CloudProjectID(in); err != nil || got != want {
			t.Errorf("CloudProjectID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"billing_api", "-x", "", strings.Repeat("a", 64), "a/b"} {
		if _, err := CloudProjectID(in); err == nil {
			t.Errorf("CloudProjectID(%q) accepted an id the Worker refuses", in)
		}
	}
}

// SiteDigest identifies the bytes: the same set in any order is the same
// digest, and a change to a bundle file — not only to the data — is a new
// one. That second half is why it is not the snapshot's Digest.
func TestSiteDigestCoversEveryFile(t *testing.T) {
	a := map[string][]byte{"index.html": []byte("x"), "assets/app.js": []byte("1")}
	b := map[string][]byte{"assets/app.js": []byte("1"), "index.html": []byte("x")}
	if SiteDigest(a) != SiteDigest(b) {
		t.Error("the digest depends on map order")
	}
	c := map[string][]byte{"index.html": []byte("x"), "assets/app.js": []byte("2")}
	if SiteDigest(a) == SiteDigest(c) {
		t.Error("changing a bundle file did not change the digest")
	}
	d := map[string][]byte{"index.html": []byte("x"), "assets/app2.js": []byte("1")}
	if SiteDigest(a) == SiteDigest(d) {
		t.Error("renaming a file did not change the digest")
	}
}
