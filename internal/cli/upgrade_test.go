package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/update"
)

// fakeGitHub serves one release list and one downloadable release, and
// points the update collaborators at it for the duration of a test.
func fakeGitHub(t *testing.T, latest string, critical bool, binary []byte) {
	t.Helper()
	asset := update.AssetName(latest, runtime.GOOS, runtime.GOARCH)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "orch", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)

	body := "- **Fix**: the retry keeps its PR"
	if critical {
		body += "\n" + update.CriticalMarker
	}
	list := fmt.Sprintf(`[{"tag_name":%q,"html_url":"https://example.test/%s","draft":false,"prerelease":false,"body":%q,"assets":[{"name":%q}]}]`,
		latest, latest, body, asset)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/hectorcanaimero/orch/releases":
			_, _ = w.Write([]byte(list))
		case "/dl/" + latest + "/checksums.txt":
			_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		case "/dl/" + latest + "/" + asset:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cache := filepath.Join(t.TempDir(), "update-check.json")
	oldClient, oldDL, oldCache, oldTarget := updateClient, updateDownloader, updateCachePath, upgradeTarget
	t.Cleanup(func() {
		updateClient, updateDownloader, updateCachePath, upgradeTarget = oldClient, oldDL, oldCache, oldTarget
	})
	updateClient = update.Client{API: srv.URL}
	updateDownloader = update.Downloader{Base: srv.URL + "/dl"}
	updateCachePath = func() (string, error) { return cache, nil }
	upgradeTarget = func() (string, error) { return "", fmt.Errorf("no target set by this test") }
	t.Setenv(update.DisableEnv, "")
}

func runRoot(t *testing.T, version string, args ...string) (string, error) {
	t.Helper()
	root, _ := newRootCmd(version)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	_, err := root.ExecuteC()
	return out.String(), err
}

func TestUpgradeInstallsTheVerifiedRelease(t *testing.T) {
	fakeGitHub(t, "v0.13.1", false, []byte("#!orch v0.13.1"))
	target := filepath.Join(t.TempDir(), "orch")
	if err := os.WriteFile(target, []byte("#!orch v0.13.0"), 0o755); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}
	upgradeTarget = func() (string, error) { return target, nil }

	out, err := runRoot(t, "v0.13.0", "upgrade", "--yes")
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	for _, want := range []string{"orch v0.13.0 → v0.13.1", "Fix: the retry keeps its PR", "Installed orch v0.13.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	got, err := os.ReadFile(target) // #nosec G304 -- a temp dir this test made
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "#!orch v0.13.1" {
		t.Errorf("binary = %q, want the new release", got)
	}
}

func TestUpgradeCheckChangesNothing(t *testing.T) {
	fakeGitHub(t, "v0.13.1", false, []byte("new"))
	out, err := runRoot(t, "v0.13.0", "upgrade", "--check")
	if err != nil || !strings.Contains(out, "v0.13.0 → v0.13.1") {
		t.Fatalf("upgrade --check: %v\n%s", err, out)
	}

	out, err = runRoot(t, "v0.13.1", "upgrade")
	if err != nil || !strings.Contains(out, "orch v0.13.1 is the latest release.") {
		t.Errorf("upgrade on the latest: %v\n%s", err, out)
	}
}

func TestUpgradeAsksBeforeReplacing(t *testing.T) {
	fakeGitHub(t, "v0.13.1", false, []byte("new"))
	target := filepath.Join(t.TempDir(), "orch")
	upgradeTarget = func() (string, error) { return target, nil }
	_, err := runRoot(t, "v0.13.0", "upgrade")
	if err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Errorf("err = %v, want a refusal to install unasked without a terminal", err)
	}
}

func TestCriticalReleaseStopsRunAndDashboard(t *testing.T) {
	fakeGitHub(t, "v0.13.1", true, []byte("new"))

	for _, cmd := range []string{"run", "dashboard"} {
		_, err := runRoot(t, "v0.13.0", cmd)
		if err == nil || !strings.Contains(err.Error(), "v0.13.1 is a critical update") {
			t.Errorf("orch %s: err = %v, want the critical-update refusal", cmd, err)
		}
	}
	// The flag starts it anyway, and what fails next is the missing project,
	// not the gate.
	_, err := runRoot(t, "v0.13.0", "run", "--skip-update-check", "--project-root", t.TempDir())
	if err != nil && strings.Contains(err.Error(), "critical update") {
		t.Errorf("--skip-update-check did not skip: %v", err)
	}
	// Other commands are not gated.
	if _, err := runRoot(t, "v0.13.0", "upgrade", "--check"); err != nil {
		t.Errorf("upgrade --check was gated: %v", err)
	}
	// Nor is a version already past it, nor a dev build, nor an opted-out user.
	for _, v := range []string{"v0.13.1", "dev"} {
		if _, err := runRoot(t, v, "run", "--project-root", t.TempDir()); err != nil && strings.Contains(err.Error(), "critical update") {
			t.Errorf("version %s was gated: %v", v, err)
		}
	}
	t.Setenv(update.DisableEnv, "1")
	if _, err := runRoot(t, "v0.13.0", "run", "--project-root", t.TempDir()); err != nil && strings.Contains(err.Error(), "critical update") {
		t.Errorf("%s=1 was gated: %v", update.DisableEnv, err)
	}
}

func TestUpdateNoticeOnlyForAPersonAtATerminal(t *testing.T) {
	t.Setenv(update.DisableEnv, "")
	t.Setenv("CI", "")
	root, _ := newRootCmd("v0.13.0")
	find := func(args ...string) *cobra.Command {
		c, _, err := root.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	status := find("status")

	if !wantsUpdateNotice(status, "v0.13.0", true) {
		t.Errorf("no notice for `orch status` at a terminal")
	}
	if wantsUpdateNotice(status, "v0.13.0", false) {
		t.Errorf("a notice when stderr is not a terminal (an agent, a pipe)")
	}
	if wantsUpdateNotice(status, "dev", true) {
		t.Errorf("a notice on a dev build")
	}
	for _, quiet := range []string{"mcp", "upgrade", "task-status"} {
		if wantsUpdateNotice(find(quiet), "v0.13.0", true) {
			t.Errorf("a notice on `orch %s`, whose output a program reads", quiet)
		}
	}
	if err := status.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if wantsUpdateNotice(status, "v0.13.0", true) {
		t.Errorf("a notice on --json output")
	}
	t.Setenv("CI", "true")
	if wantsUpdateNotice(find("tasks"), "v0.13.0", true) {
		t.Errorf("a notice in CI")
	}
}

func TestUpdateNoticeIsWrittenAfterTheCommand(t *testing.T) {
	fakeGitHub(t, "v0.13.1", false, []byte("new"))
	t.Setenv("CI", "")
	root, _ := newRootCmd("v0.13.0")
	status, _, err := root.Find([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}

	var at bytes.Buffer
	writeUpdateNotice(status, "v0.13.0", &at, true)
	for _, want := range []string{"A new orch is out: v0.13.1 (you have v0.13.0)", "• Fix: the retry keeps its PR"} {
		if !strings.Contains(at.String(), want) {
			t.Errorf("notice is missing %q:\n%s", want, at.String())
		}
	}

	var piped bytes.Buffer
	writeUpdateNotice(status, "v0.13.0", &piped, false)
	if piped.Len() != 0 {
		t.Errorf("wrote a notice to a non-terminal:\n%s", piped.String())
	}
}

func TestExecutablePathIsTheRealBinary(t *testing.T) {
	path, err := executablePath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Errorf("executablePath = %q (%v), want an absolute regular file", path, info.Mode())
	}
}
