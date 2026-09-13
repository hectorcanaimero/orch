package publish

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/publish/cloudfake"
	"github.com/hectorcanaimero/orch/internal/publish/cloudworker"
)

// setupRig is one offline `orch cloud setup`: the fake npx in
// testdata/fakebin playing wrangler, the in-memory Worker playing Cloudflare,
// a credentials path and a temp dir of the test's own, and a clock that never
// sleeps for real.
type setupRig struct {
	t       *testing.T
	state   string
	worker  *cloudfake.Worker
	creds   string
	staging string
	out     bytes.Buffer
	clock   *fakeClock
	opts    CloudSetupOptions
}

type fakeClock struct {
	now     time.Time
	sleeps  int
	onSleep func(n int)
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.sleeps++
	c.now = c.now.Add(d)
	if c.onSleep != nil {
		c.onSleep(c.sleeps)
	}
	return nil
}

func newSetupRig(t *testing.T) *setupRig {
	t.Helper()
	npx, err := filepath.Abs(filepath.Join("testdata", "fakebin", "npx"))
	if err != nil {
		t.Fatal(err)
	}
	r := &setupRig{
		t:       t,
		state:   t.TempDir(),
		worker:  cloudfake.New(),
		creds:   filepath.Join(t.TempDir(), ".orch", "credentials"),
		staging: t.TempDir(),
		clock:   &fakeClock{now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)},
	}
	t.Cleanup(r.worker.Close)
	t.Setenv("FAKE_WRANGLER_STATE", r.state)
	r.worker.SetAdminTokenFile(filepath.Join(r.state, "admin-token"))
	r.write("url", r.worker.URL)
	r.opts = CloudSetupOptions{
		CredentialsPath: r.creds,
		Stdin:           strings.NewReader(""),
		Stdout:          &r.out,
		Stderr:          &r.out,
		LookPath: func(name string) (string, error) {
			if name != "npx" {
				t.Errorf("LookPath(%q), want npx", name)
			}
			return npx, nil
		},
		Getenv:      func(string) string { return "" },
		TempDir:     r.staging,
		WaitTimeout: 60 * time.Second,
		Now:         r.clock.Now,
		Sleep:       r.clock.Sleep,
	}
	return r
}

func (r *setupRig) write(name, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.state, name), []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *setupRig) read(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.state, name)) // #nosec G304 -- t.TempDir()
	if err != nil {
		return ""
	}
	return string(b)
}

// calls is the wrangler subcommand of every npx call, in order. It also
// checks that every call pinned wrangler.
func (r *setupRig) calls() []string {
	r.t.Helper()
	var out []string
	pin := "[--yes] [wrangler@" + cloudworker.WranglerVersion + "]"
	for _, line := range strings.Split(strings.TrimSpace(r.read("argv.log")), "\n") {
		if line == "" {
			continue
		}
		rest, ok := strings.CutPrefix(line, "argv: "+pin)
		if !ok {
			r.t.Errorf("npx call without the pinned wrangler: %s", line)
			continue
		}
		out = append(out, strings.TrimSpace(rest))
	}
	return out
}

func (r *setupRig) run() (CloudSetupResult, error) {
	return SetupCloud(context.Background(), r.opts)
}

func TestSetupCloudLogsInDeploysAndSaves(t *testing.T) {
	r := newSetupRig(t)

	res, err := r.run()
	if err != nil {
		t.Fatalf("SetupCloud: %v\n%s", err, r.out.String())
	}

	want := []string{"[whoami]", "[login] [--device]", "[whoami]", "[deploy]", "[secret] [put] [ADMIN_TOKEN]"}
	if got := r.calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrangler calls = %v, want %v", got, want)
	}
	if !res.LoggedIn || res.URL != r.worker.URL {
		t.Errorf("result = %+v, want LoggedIn and URL %s", res, r.worker.URL)
	}

	// The login ran attached to the operator's terminal: its code reached them.
	out := r.out.String()
	if !strings.Contains(out, "FAKECODE") || !strings.Contains(out, "dash.cloudflare.com/oauth2/device/verify") {
		t.Errorf("the device login's URL and code did not reach stdout:\n%s", out)
	}
	for i := 1; i <= 6; i++ {
		if !strings.Contains(out, "["+string(rune('0'+i))+"/6]") {
			t.Errorf("step %d/6 was not announced:\n%s", i, out)
		}
	}

	// What was deployed is the embedded Worker under the chosen name, with
	// no KV id.
	deployed, err := os.ReadFile(filepath.Join(r.state, "deployed", "worker.js")) // #nosec G304 -- t.TempDir()
	if err != nil || !bytes.Equal(deployed, cloudworker.Script) {
		t.Errorf("deployed worker.js differs from the embedded Worker (err %v)", err)
	}
	if cfg := r.read("deployed/wrangler.jsonc"); !strings.Contains(cfg, `"name": "orch-cloud"`) || strings.Contains(cfg, `"id"`) {
		t.Errorf("deployed wrangler.jsonc:\n%s", cfg)
	}

	// The token: generated here, piped to secret put, saved, and nowhere else.
	token := r.read("secret-stdin")
	if len(token) != 64 {
		t.Fatalf("secret put received %q on stdin, want a 64-hex admin token", token)
	}
	cf, err := LoadCredentials(r.creds)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Cloud == nil || cf.Cloud.URL != r.worker.URL || cf.Cloud.AdminToken != token {
		t.Errorf("credentials = %+v, want the Worker URL and the piped token", cf.Cloud)
	}
	if strings.Contains(out, token) {
		t.Error("the admin token appeared on stdout/stderr")
	}
	if strings.Contains(r.read("argv.log"), token) {
		t.Error("the admin token appeared in a wrangler argument")
	}
	info, err := os.Stat(r.creds)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("credentials mode = %v (err %v), want 0600", info.Mode().Perm(), err)
	}

	// A finished setup leaves no staged copy behind.
	if entries, _ := os.ReadDir(r.staging); len(entries) != 0 {
		t.Errorf("staging dir not cleaned up: %d entries", len(entries))
	}
}

func TestSetupCloudSkipsLoginWithASession(t *testing.T) {
	r := newSetupRig(t)
	r.write("authed", "")

	res, err := r.run()
	if err != nil {
		t.Fatalf("SetupCloud: %v\n%s", err, r.out.String())
	}
	want := []string{"[whoami]", "[deploy]", "[secret] [put] [ADMIN_TOKEN]"}
	if got := r.calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrangler calls = %v, want %v", got, want)
	}
	if res.LoggedIn {
		t.Error("LoggedIn with an existing session")
	}
}

func TestSetupCloudWithoutNPXSaysToInstallNode(t *testing.T) {
	r := newSetupRig(t)
	r.opts.LookPath = func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }

	_, err := r.run()
	var missing *CloudSetupMissingNPX
	if !errors.As(err, &missing) || !strings.Contains(err.Error(), "Node.js 20+") {
		t.Fatalf("err = %v, want CloudSetupMissingNPX naming Node.js 20+", err)
	}
	if r.read("argv.log") != "" {
		t.Error("wrangler ran without npx")
	}
}

func TestSetupCloudRefusesAWhoamiItCannotRead(t *testing.T) {
	r := newSetupRig(t)
	r.write("whoami-garbage", "")

	_, err := r.run()
	if err == nil || !strings.Contains(err.Error(), "wrangler whoami failed") {
		t.Fatalf("err = %v, want a whoami failure", err)
	}
	// A network error is not "not logged in": no browser login is started.
	if got := r.calls(); len(got) != 1 {
		t.Errorf("wrangler calls = %v, want only whoami", got)
	}
}

func TestSetupCloudStopsWhenLoginLeavesNoSession(t *testing.T) {
	r := newSetupRig(t)
	r.write("login-noop", "")

	_, err := r.run()
	if err == nil || !strings.Contains(err.Error(), "still reports no session") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range r.calls() {
		if c == "[deploy]" {
			t.Error("deployed without a session")
		}
	}
}

func TestSetupCloudDoesNotBrowserLoginOverAnInvalidAPIToken(t *testing.T) {
	r := newSetupRig(t)
	r.opts.Getenv = func(k string) string {
		if k == "CLOUDFLARE_API_TOKEN" {
			return "set-by-ci"
		}
		return ""
	}

	_, err := r.run()
	if err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN is set") {
		t.Fatalf("err = %v", err)
	}
	if got := r.calls(); len(got) != 1 {
		t.Errorf("wrangler calls = %v, want only whoami", got)
	}
}

func TestSetupCloudDeployWithoutAURLKeepsTheFiles(t *testing.T) {
	r := newSetupRig(t)
	r.write("authed", "")
	r.write("deploy-no-url", "")

	_, err := r.run()
	if err == nil || !strings.Contains(err.Error(), "no Worker URL") {
		t.Fatalf("err = %v, want a missing-URL error", err)
	}
	entries, _ := os.ReadDir(r.staging)
	if len(entries) != 1 || !strings.Contains(err.Error(), filepath.Join(r.staging, entries[0].Name())) {
		t.Errorf("the error should name the kept staging dir; err = %v, entries = %v", err, entries)
	}
	if got := r.calls(); got[len(got)-1] != "[deploy]" {
		t.Errorf("wrangler calls = %v, want nothing after the deploy", got)
	}
	if _, statErr := os.Stat(r.creds); statErr == nil {
		t.Error("credentials were written after a failed deploy")
	}
}

// A secret reaches the Worker a little after `secret put` returns. Setup
// waits for it, and stops waiting at the first 200.
func TestSetupCloudWaitsForTheSecretThenStops(t *testing.T) {
	r := newSetupRig(t)
	r.write("authed", "")
	// The Worker only learns the token on the first sleep: until then whoami
	// answers 401, as a real Worker does while the secret propagates.
	live := filepath.Join(r.state, "admin-token-live")
	r.worker.SetAdminTokenFile(live)
	r.clock.onSleep = func(n int) {
		if n == 1 {
			if err := os.WriteFile(live, []byte(r.read("admin-token")), 0o600); err != nil {
				t.Error(err)
			}
		}
	}

	if _, err := r.run(); err != nil {
		t.Fatalf("SetupCloud: %v", err)
	}
	if r.clock.sleeps != 1 {
		t.Errorf("slept %d times, want exactly 1 (one 401, then the 200)", r.clock.sleeps)
	}
}

func TestSetupCloudGivesUpOnASecretThatNeverArrives(t *testing.T) {
	r := newSetupRig(t)
	r.write("authed", "")
	r.worker.SetAdminTokenFile(filepath.Join(r.state, "never-written"))

	_, err := r.run()
	if err == nil || !strings.Contains(err.Error(), "did not accept the new admin token within 1m0s") {
		t.Fatalf("err = %v, want a bounded-wait error", err)
	}
	if r.clock.sleeps == 0 || r.clock.sleeps > 30 {
		t.Errorf("slept %d times inside a 60 s bound", r.clock.sleeps)
	}
	if token := r.read("secret-stdin"); token != "" && strings.Contains(err.Error(), token) {
		t.Error("the admin token appeared in the error")
	}
	if _, statErr := os.Stat(r.creds); statErr == nil {
		t.Error("credentials were written although the Worker never accepted the token")
	}
}

func TestParseDeployURL(t *testing.T) {
	real := "Total Upload: 13.92 KiB / gzip: 3.98 KiB\n" +
		"Uploaded orch-cloud (3.01 sec)\n" +
		"Deployed orch-cloud triggers (2.41 sec)\n" +
		"  https://orch-cloud.example.workers.dev\n" +
		"Current Version ID: 00000000-0000-0000-0000-000000000000\n"
	if got, err := ParseDeployURL(real); err != nil || got != "https://orch-cloud.example.workers.dev" {
		t.Errorf("ParseDeployURL(real output) = %q, %v", got, err)
	}
	if got, err := ParseDeployURL("Deployed x triggers\n  http://127.0.0.1:8787\n"); err != nil || got != "http://127.0.0.1:8787" {
		t.Errorf("loopback http = %q, %v", got, err)
	}
	for name, out := range map[string]string{
		"no URL at all":         "Uploaded orch-cloud (3.01 sec)\n",
		"URL before Deployed":   "  https://orch-cloud.example.workers.dev\nDeployed orch-cloud triggers\n",
		"plain http on the net": "Deployed x triggers\n  http://orch-cloud.example.workers.dev\n",
		"URL inside a sentence": "Deployed x triggers\nsee https://developers.cloudflare.com for help\n",
	} {
		if got, err := ParseDeployURL(out); err == nil {
			t.Errorf("%s: parsed %q, want an error", name, got)
		}
	}
}

func TestCloudSetupRerunNotice(t *testing.T) {
	creds := filepath.Join(t.TempDir(), "credentials")
	if n, err := CloudSetupRerunNotice(creds, "orch-cloud"); err != nil || n != "" {
		t.Fatalf("no credentials: notice %q, err %v", n, err)
	}
	cf := CredentialsFile{Cloud: &CloudCredentials{
		URL: "https://orch-cloud.example.workers.dev", AdminToken: cloudfake.Token(),
		Projects: map[string]CloudProject{"a": {}, "b": {}},
	}}
	if err := SaveCredentials(creds, cf); err != nil {
		t.Fatal(err)
	}
	same, err := CloudSetupRerunNotice(creds, "orch-cloud")
	if err != nil || !strings.Contains(same, "NEW admin token") || !strings.Contains(same, "2 project(s)") ||
		!strings.Contains(same, "keep working") {
		t.Errorf("same Worker: %q, %v", same, err)
	}
	other, err := CloudSetupRerunNotice(creds, "acme-viewer")
	if err != nil || !strings.Contains(other, "will be dropped") {
		t.Errorf("different Worker: %q, %v", other, err)
	}
}

func TestCloudSetupPlanNamesEveryStepAndThePinnedWrangler(t *testing.T) {
	plan, err := CloudSetupPlan("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"npx --yes wrangler@" + cloudworker.WranglerVersion + " whoami",
		"login --device", " deploy", "secret put ADMIN_TOKEN", "~/.orch/credentials", "orch publish --to cloud",
		`"orch-cloud"`,
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	if _, err := CloudSetupPlan("Not A Name"); err == nil {
		t.Error("plan accepted an invalid Worker name")
	}
}
