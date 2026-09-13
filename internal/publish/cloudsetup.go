package publish

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/publish/cloudworker"
)

// CloudSetupOptions drives `orch cloud setup`. Every field with a side effect
// on the machine or the network is injectable so the whole flow runs offline
// in tests, against a fake npx and the in-memory Worker.
type CloudSetupOptions struct {
	// CredentialsPath is ~/.orch/credentials outside tests.
	CredentialsPath string
	// Name is the Worker name (cloudworker.DefaultName when empty).
	Name string

	// Stdin, Stdout and Stderr are the operator's terminal. wrangler's login
	// and deploy run attached to them, so a verification code or a prompt
	// wrangler prints reaches the person who has to act on it.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// LookPath is exec.LookPath outside tests; Getenv is os.Getenv.
	LookPath func(string) (string, error)
	Getenv   func(string) string
	// TempDir is where the Worker is staged; os.TempDir() when empty.
	TempDir string
	// NewToken generates the admin token; 32 random bytes as hex when nil.
	NewToken func() (string, error)

	// WaitTimeout bounds the wait for the new secret to take effect (60 s
	// when zero). Now and Sleep are the clock it is measured with.
	WaitTimeout time.Duration
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
}

// CloudSetupResult is what a successful setup left behind.
type CloudSetupResult struct {
	URL string
	// LoggedIn is true when setup had to run `wrangler login`.
	LoggedIn bool
	// DroppedProjects counts stored project tokens that belonged to a
	// different Worker (see CloudLogin).
	DroppedProjects int
}

const cloudSetupSteps = 6

// CloudSetupPlan is the text `orch cloud setup --dry-run` prints: the steps a
// real run takes, in order, with the exact wrangler invocations. It runs
// nothing — not even a PATH lookup.
func CloudSetupPlan(name string) (string, error) {
	if name == "" {
		name = cloudworker.DefaultName
	}
	if err := cloudworker.ValidName(name); err != nil {
		return "", err
	}
	w := "npx --yes wrangler@" + cloudworker.WranglerVersion
	var b strings.Builder
	fmt.Fprintf(&b, "orch cloud setup — plan for Worker %q (--dry-run: nothing runs)\n\n", name)
	fmt.Fprintf(&b, "  1. find npx on PATH (wrangler runs on Node.js 20+)\n")
	fmt.Fprintf(&b, "  2. %s whoami — and, only if there is no Cloudflare session,\n", w)
	fmt.Fprintf(&b, "     %s login --device (you approve a code in your browser)\n", w)
	fmt.Fprintf(&b, "  3. %s deploy — the orch-cloud Worker built into this orch (API %d),\n", w, CloudAPIVersion)
	fmt.Fprintf(&b, "     named %q; the first deploy creates its KV namespace\n", name)
	fmt.Fprintf(&b, "  4. generate an admin token and pipe it to %s secret put ADMIN_TOKEN\n", w)
	fmt.Fprintf(&b, "     (never printed, never passed as an argument)\n")
	fmt.Fprintf(&b, "  5. wait until the Worker accepts the new admin token\n")
	fmt.Fprintf(&b, "  6. save the Worker URL and the admin token to ~/.orch/credentials (mode 0600)\n\n")
	fmt.Fprintf(&b, "Then: orch publish --to cloud\n")
	return b.String(), nil
}

// CloudSetupRerunNotice explains what setup will do to credentials that
// already name a Worker, or returns "" when there are none. The caller asks
// for confirmation when it is not empty.
func CloudSetupRerunNotice(credentialsPath, name string) (string, error) {
	if name == "" {
		name = cloudworker.DefaultName
	}
	cf, err := LoadCredentials(credentialsPath)
	if err != nil {
		return "", err
	}
	if cf.Cloud == nil || cf.Cloud.URL == "" {
		return "", nil
	}
	if sameWorkerName(cf.Cloud.URL, name) {
		return fmt.Sprintf("This machine is already set up with %s. Setup will redeploy it and issue a NEW "+
			"admin token; the old one stops working. The %d project(s) whose tokens are stored here keep "+
			"working — the Worker keeps their digests.", cf.Cloud.URL, len(cf.Cloud.Projects)), nil
	}
	return fmt.Sprintf("This machine is set up with %s, which is not a Worker named %q. Setup will deploy "+
		"%q and switch to it; the tokens stored for %d project(s) belong to the old Worker and will be "+
		"dropped from ~/.orch/credentials (the old Worker keeps serving their pages).",
		cf.Cloud.URL, name, name, len(cf.Cloud.Projects)), nil
}

// sameWorkerName reports whether a stored Worker URL is <name>.<anything>.
func sameWorkerName(stored, name string) bool {
	u, err := url.Parse(stored)
	if err != nil {
		return false
	}
	return strings.HasPrefix(u.Hostname(), name+".")
}

// SetupCloud takes the operator from "no Cloudflare session" to "orch publish
// --to cloud works": it deploys the embedded Worker with wrangler, stores a
// fresh admin token as its secret, waits for the Worker to accept it and
// saves it through the same verification `orch cloud login` uses.
//
// The admin token exists in three places only: this process's memory, the
// stdin of `wrangler secret put`, and the credentials file. It is never an
// argument, an environment variable or a line of output.
func SetupCloud(ctx context.Context, o CloudSetupOptions) (CloudSetupResult, error) {
	var res CloudSetupResult
	o = o.withDefaults()
	if err := cloudworker.ValidName(o.Name); err != nil {
		return res, err
	}

	o.step(1, "looking for npx (Node.js) on PATH")
	npx, err := o.LookPath("npx")
	if err != nil {
		return res, &CloudSetupMissingNPX{Err: err}
	}
	w := wrangler{npx: npx, o: o}

	o.step(2, "checking for a Cloudflare session (wrangler whoami)")
	authed, err := w.whoami(ctx)
	if err != nil {
		return res, err
	}
	if !authed {
		if o.Getenv("CLOUDFLARE_API_TOKEN") != "" {
			return res, errors.New("CLOUDFLARE_API_TOKEN is set but wrangler reports no session — the token " +
				"is invalid or lacks Workers permissions; fix or unset it (setup does not fall back to a " +
				"browser login while it is set, since wrangler would keep using the token)")
		}
		o.say("No Cloudflare session on this machine. Starting wrangler's device login: open the URL it " +
			"prints, enter the code, and approve.")
		if err := w.run(ctx, "", o.Stdin, o.Stdout, o.Stderr, "login", "--device"); err != nil {
			return res, fmt.Errorf("wrangler login failed: %w", err)
		}
		authed, err = w.whoami(ctx)
		if err != nil {
			return res, err
		}
		if !authed {
			return res, errors.New("wrangler login finished but wrangler whoami still reports no session — " +
				"run `npx wrangler login --device` yourself, then `orch cloud setup` again")
		}
		res.LoggedIn = true
	}

	o.step(3, fmt.Sprintf("deploying the orch-cloud Worker %q (wrangler deploy)", o.Name))
	dir, err := stageWorker(o.TempDir, o.Name)
	if err != nil {
		return res, err
	}
	// One writer for both streams: os/exec then calls it from one goroutine
	// at a time, so the copy setup parses cannot interleave mid-line. The
	// operator sees wrangler's progress as it happens.
	var deployOut bytes.Buffer
	streamed := io.MultiWriter(o.Stdout, &deployOut)
	deployErr := w.run(ctx, dir, o.Stdin, streamed, streamed, "deploy")
	if deployErr != nil {
		return res, fmt.Errorf("wrangler deploy failed (the Worker files are in %s): %w", dir, deployErr)
	}
	workerURL, err := ParseDeployURL(deployOut.String())
	if err != nil {
		return res, fmt.Errorf("%w (the Worker files are in %s)", err, dir)
	}
	res.URL = workerURL

	o.step(4, "storing a new admin token as the Worker secret ADMIN_TOKEN (wrangler secret put)")
	token, err := o.NewToken()
	if err != nil {
		return res, fmt.Errorf("generating the admin token: %w", err)
	}
	var secretOut bytes.Buffer
	if err := w.run(ctx, dir, strings.NewReader(token), &secretOut, &secretOut, "secret", "put", "ADMIN_TOKEN"); err != nil {
		o.echo(redact(secretOut.String(), token))
		return res, fmt.Errorf("wrangler secret put failed (the Worker files are in %s): %w", dir, err)
	}
	o.echo(redact(secretOut.String(), token))

	o.step(5, "waiting for the Worker to accept the new admin token")
	client, err := NewCloudClient(workerURL)
	if err != nil {
		return res, err
	}
	if err := waitForAdmin(ctx, client, token, o); err != nil {
		return res, err
	}

	o.step(6, "saving the Worker URL and admin token to "+o.CredentialsPath)
	dropped, err := CloudLogin(ctx, o.CredentialsPath, workerURL, token)
	if err != nil {
		return res, err
	}
	res.DroppedProjects = dropped

	// Only a finished setup removes the staged files: on any failure above
	// they stay, named in the error, so the operator can deploy them by hand.
	_ = os.RemoveAll(dir)
	return res, nil
}

// CloudSetupMissingNPX is setup's one precondition failure: no npx on PATH.
type CloudSetupMissingNPX struct{ Err error }

func (e *CloudSetupMissingNPX) Error() string {
	return "npx was not found on PATH — orch cloud setup drives Cloudflare's wrangler, which runs on " +
		"Node.js: install Node.js 20+ (https://nodejs.org), then run `orch cloud setup` again"
}

func (e *CloudSetupMissingNPX) Unwrap() error { return e.Err }

func (o CloudSetupOptions) withDefaults() CloudSetupOptions {
	if o.Name == "" {
		o.Name = cloudworker.DefaultName
	}
	if o.Stdin == nil {
		o.Stdin = strings.NewReader("")
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.NewToken == nil {
		o.NewToken = newAdminToken
	}
	if o.WaitTimeout == 0 {
		o.WaitTimeout = 60 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	return o
}

func (o CloudSetupOptions) step(n int, what string) {
	_, _ = fmt.Fprintf(o.Stdout, "\n[%d/%d] %s\n", n, cloudSetupSteps, what)
}

func (o CloudSetupOptions) say(line string) {
	_, _ = fmt.Fprintln(o.Stdout, line)
}

func (o CloudSetupOptions) echo(text string) {
	if text = strings.TrimRight(text, "\n"); text != "" {
		_, _ = fmt.Fprintln(o.Stdout, text)
	}
}

// wrangler runs the pinned wrangler through npx.
type wrangler struct {
	npx string
	o   CloudSetupOptions
}

func (w wrangler) args(sub ...string) []string {
	return append([]string{"--yes", "wrangler@" + cloudworker.WranglerVersion}, sub...)
}

func (w wrangler) run(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, sub ...string) error {
	cmd := exec.CommandContext(ctx, w.npx, w.args(sub...)...) // #nosec G204 -- npx from PATH with orch's own fixed arguments
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Markers from wrangler 4.131.1's whoami, captured against a real account:
// "👋 You are logged in with an OAuth Token, associated with the email …"
// (an API token says "…with an API Token…") and
// "You are not authenticated. Please run `wrangler login`."
const (
	whoamiLoggedIn = "You are logged in"
	whoamiNoAuth   = "not authenticated"
)

// whoami reports whether wrangler has a Cloudflare session. It reads the
// text rather than the exit status, and refuses output that says neither —
// a network failure is not "not logged in", and starting a browser login for
// it would send the operator to approve a code that fixes nothing.
func (w wrangler) whoami(ctx context.Context) (bool, error) {
	var out bytes.Buffer
	runErr := w.run(ctx, "", strings.NewReader(""), &out, &out, "whoami")
	text := out.String()
	switch {
	case strings.Contains(text, whoamiLoggedIn):
		return true, nil
	case strings.Contains(text, whoamiNoAuth):
		return false, nil
	}
	if runErr != nil {
		return false, fmt.Errorf("wrangler whoami failed: %w\n%s", runErr, strings.TrimSpace(text))
	}
	return false, fmt.Errorf("wrangler whoami answered neither %q nor %q — cannot tell whether this machine "+
		"has a Cloudflare session:\n%s", whoamiLoggedIn, whoamiNoAuth, strings.TrimSpace(text))
}

// stageWorker writes the embedded Worker and its config to a fresh directory.
func stageWorker(tempDir, name string) (string, error) {
	cfg, err := cloudworker.Config(name)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(tempDir, "orch-cloud-setup-")
	if err != nil {
		return "", fmt.Errorf("create a directory for the Worker: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker.js"), cloudworker.Script, 0o600); err != nil {
		return "", fmt.Errorf("write the Worker to %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wrangler.jsonc"), cfg, 0o600); err != nil {
		return "", fmt.Errorf("write wrangler.jsonc to %s: %w", dir, err)
	}
	return dir, nil
}

// ParseDeployURL finds the Worker's address in `wrangler deploy` output: the
// first line after "Deployed <name> triggers" that is a bare URL. wrangler
// 4.131.1 prints it on its own indented line, e.g.
//
//	Deployed orch-cloud triggers (2.41 sec)
//	  https://orch-cloud.example.workers.dev
//
// Plain http is accepted only for a loopback host (a local fake), because a
// real deploy is always https and the token is about to travel to it.
func ParseDeployURL(output string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	afterDeployed := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "Deployed ") {
			afterDeployed = true
			continue
		}
		if !afterDeployed || strings.ContainsAny(line, " \t") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Host == "" {
			continue
		}
		if u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
			return strings.TrimRight(line, "/"), nil
		}
	}
	return "", errors.New("wrangler deploy finished but its output has no Worker URL — expected a line " +
		"like `https://<name>.<subdomain>.workers.dev` after `Deployed <name> triggers`; if the account " +
		"has no workers.dev subdomain yet, register one in the Cloudflare dashboard (Workers & Pages) " +
		"and run setup again")
}

// waitForAdmin polls whoami until the Worker accepts token. A secret takes a
// few seconds to reach the Worker after `secret put` returns; until then the
// Worker answers 401, which is expected here and not an error.
func waitForAdmin(ctx context.Context, client *CloudClient, token string, o CloudSetupOptions) error {
	deadline := o.Now().Add(o.WaitTimeout)
	delay := time.Second
	var last error
	for {
		err := client.WhoAmI(ctx, token)
		if err == nil {
			return nil
		}
		last = err
		if !o.Now().Before(deadline) {
			return fmt.Errorf("the Worker at %s did not accept the new admin token within %s (last answer: %v) — "+
				"the secret may still be propagating: run `orch cloud setup --yes` again in a minute",
				client.URL(), o.WaitTimeout, redactErr(last, token))
		}
		if err := o.Sleep(ctx, delay); err != nil {
			return err
		}
		if delay < 5*time.Second {
			delay *= 2
		}
	}
}

func newAdminToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// redact replaces token in text. wrangler does not echo a secret, but setup
// does not rely on that: the promise is that the token never reaches output.
func redact(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "<admin token>")
}

func redactErr(err error, token string) string {
	if err == nil {
		return ""
	}
	return redact(err.Error(), token)
}
