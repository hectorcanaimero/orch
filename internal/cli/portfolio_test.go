package cli

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// scaffoldProject writes the two files resolveAndValidate and the portfolio
// opener look for, plus a config.
func scaffoldProject(t *testing.T, root, configYAML string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".orchestrator"), 0o750); err != nil {
		t.Fatal(err)
	}
	const tasks = `{"meta":{"project":"p"},"tasks":[
	  {"id":"T-1","phase":0,"title":"One","model":"claude/sonnet","status":"todo",
	   "dependencies":[],"estimateHours":1.0}]}`
	if err := os.WriteFile(filepath.Join(root, "tasks.json"), []byte(tasks), 0o600); err != nil {
		t.Fatal(err)
	}
	if configYAML != "" {
		if err := os.WriteFile(filepath.Join(root, ".orchestrator", "config.yaml"),
			[]byte(configYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOpenPortfolioSkipsWhatIsNotAProject is the property the whole command
// rests on: a glob over a directory of work matches things that are not orch
// projects, and every one of them has to become a named row rather than a
// reason to refuse to start.
func TestOpenPortfolioSkipsWhatIsNotAProject(t *testing.T) {
	dir := t.TempDir()
	scaffoldProject(t, filepath.Join(dir, "alpha"), "spec_root: specs\n")
	scaffoldProject(t, filepath.Join(dir, "beta"), "spec_root: specs\n")
	// A directory that is not a project, and a plain file — a real glob over
	// ~/projects catches both.
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	projects, unavailable, closeAll, err := openPortfolio(
		context.Background(), filepath.Join(dir, "*"), http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatalf("openPortfolio: %v", err)
	}
	defer closeAll()

	var ids []string
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "beta" {
		t.Errorf("projects = %v, want [alpha beta] in glob order", ids)
	}
	// The directory is reported; the file is not. A README is not a broken
	// project and saying so would be noise on every start.
	if len(unavailable) != 1 {
		t.Fatalf("unavailable = %+v, want just the non-project directory", unavailable)
	}
	if filepath.Base(unavailable[0].Root) != "notes" {
		t.Errorf("unavailable names %q, want notes", unavailable[0].Root)
	}
	if !strings.Contains(unavailable[0].Reason, "tasks.json") {
		t.Errorf("reason = %q; it must say what is missing", unavailable[0].Reason)
	}
}

// Two directories can resolve to one project id — the id is the directory's
// base name — and the second would collide under /p/. It becomes a row naming
// the first, because "rename one of these two" is the fix and only the
// operator can make it.
func TestOpenPortfolioReportsADuplicateID(t *testing.T) {
	dir := t.TempDir()
	scaffoldProject(t, filepath.Join(dir, "a", "billing"), "spec_root: specs\n")
	scaffoldProject(t, filepath.Join(dir, "b", "billing"), "spec_root: specs\n")

	projects, unavailable, closeAll, err := openPortfolio(
		context.Background(), filepath.Join(dir, "*", "billing"), http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatalf("openPortfolio: %v", err)
	}
	defer closeAll()

	if len(projects) != 1 {
		t.Fatalf("%d projects, want 1 — the duplicate must not be served", len(projects))
	}
	if len(unavailable) != 1 || !strings.Contains(unavailable[0].Reason, "already taken") {
		t.Fatalf("unavailable = %+v; want the duplicate, named", unavailable)
	}
	// The message has to name the directory that won, or the operator cannot
	// tell which of two identically-named projects is being served.
	if !strings.Contains(unavailable[0].Reason, filepath.Join(dir, "a", "billing")) {
		t.Errorf("reason = %q; it must name the directory that took the id", unavailable[0].Reason)
	}
}

// A stakeholder project with no token is refused by Config.Validate. In a
// portfolio that refusal has to be a row, not a dead process: one
// misconfigured project out of ten is exactly what the unavailable list is
// for.
func TestOpenPortfolioReportsAMisconfiguredProject(t *testing.T) {
	dir := t.TempDir()
	scaffoldProject(t, filepath.Join(dir, "good"), "spec_root: specs\n")
	scaffoldProject(t, filepath.Join(dir, "tokenless"),
		"dashboard:\n  profile: stakeholder\n  token: \"\"\n")

	projects, unavailable, closeAll, err := openPortfolio(
		context.Background(), filepath.Join(dir, "*"), http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatalf("openPortfolio: %v", err)
	}
	defer closeAll()

	if len(projects) != 1 || projects[0].ID != "good" {
		t.Fatalf("projects = %+v, want only `good`", projects)
	}
	if len(unavailable) != 1 || filepath.Base(unavailable[0].Root) != "tokenless" {
		t.Fatalf("unavailable = %+v", unavailable)
	}
	if unavailable[0].Reason == "" {
		t.Error("the misconfigured project gives no reason")
	}
}

func TestOpenPortfolioRefusesAGlobThatMatchesNothing(t *testing.T) {
	_, _, _, err := openPortfolio(
		context.Background(), filepath.Join(t.TempDir(), "nothing-here-*"), http.NotFoundHandler(), nil)
	if err == nil {
		t.Fatal("a glob matching nothing was accepted")
	}
	// The shell expanding the glob before orch sees it is the likeliest
	// cause, and the message has to say so or the operator retries the same
	// command.
	if !strings.Contains(err.Error(), "quote it") {
		t.Errorf("error = %q; it does not mention quoting", err)
	}
}

// TestPortfolioRefusesFlagsItCannotHonour covers the three that are refused
// rather than ignored.
//
// Each is a flag whose single-project meaning has no portfolio equivalent, and
// accepting one silently would be the exact failure this port keeps finding: a
// flag that promises a behaviour nothing implements. They are checked before
// any I/O, so this test opens nothing.
func TestPortfolioRefusesFlagsItCannotHonour(t *testing.T) {
	cases := []struct {
		name  string
		flags portfolioFlags
		want  string
	}{
		{
			// A stakeholder portfolio would show every project's counters to
			// a token holder scoped to one of them.
			name:  "a stakeholder profile",
			flags: portfolioFlags{profile: "stakeholder"},
			want:  "operator-only",
		},
		{
			name:  "both is not operator either",
			flags: portfolioFlags{profile: "both"},
			want:  "operator-only",
		},
		{
			// Accepting one would suggest a shared portfolio token exists.
			name:  "a token",
			flags: portfolioFlags{token: "test-token-stakeholder"},
			want:  "each project keeps its own token",
		},
		{
			name:  "a tunnel",
			flags: portfolioFlags{withTunnel: true},
			want:  "configured per project",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A glob that would match nothing, so a case that wrongly got
			// past the checks fails on the glob instead of quietly passing.
			err := runPortfolio(newPortfolioTestCmd(t), filepath.Join(t.TempDir(), "*"), tc.flags)
			if err == nil {
				t.Fatal("the flag was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q; it does not explain %q", err, tc.want)
			}
		})
	}
}

// An explicit `--profile operator` is the one profile value that IS honoured:
// it says what the portfolio already is, so refusing it would be pedantry.
func TestPortfolioAcceptsAnExplicitOperatorProfile(t *testing.T) {
	err := runPortfolio(newPortfolioTestCmd(t), filepath.Join(t.TempDir(), "*"),
		portfolioFlags{profile: "operator"})
	if err == nil {
		t.Fatal("expected the empty-glob error, got none")
	}
	// It got past the flag checks and failed on the glob, which is the
	// distinction: a refused profile never reaches the filesystem.
	if !strings.Contains(err.Error(), "matched no directories") {
		t.Errorf("error = %q; want the glob error, not a profile refusal", err)
	}
}

func newPortfolioTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "dashboard"}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())
	return cmd
}

// TestHostIsLoopback is the predicate the exposure gate rests on, so the two
// cases that decide it are the unspecified addresses: `0.0.0.0` and `::`
// parse as valid IPs and are NOT loopback — they bind every interface, which
// is the whole reason the gate exists. An `ip.IsLoopback()` check alone gets
// those right; a naive `host != "127.0.0.1"` gets `::1` wrong in the other
// direction and starts nagging an operator who did bind locally.
func TestHostIsLoopback(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"", true}, // unset means the default, which is loopback
		{"127.0.0.1", true},
		{"127.0.0.53", true}, // the whole /8 is loopback, not just .1
		{"::1", true},
		{"localhost", true},

		{"0.0.0.0", false}, // every interface
		{"::", false},      // every interface, v6
		{"192.168.1.10", false},
		{"10.0.0.5", false},
		// A hostname that is not localhost is assumed reachable rather than
		// resolved: a DNS lookup would make a security decision depend on
		// what a resolver happened to answer.
		{"dashboard.internal", false},
	}
	for _, tc := range cases {
		if got := hostIsLoopback(tc.host); got != tc.want {
			t.Errorf("hostIsLoopback(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestPortfolioRefusesARemoteHostWithoutTheFlag is the gate itself.
//
// `/api/portfolio` is ungated by design, so the listener is the boundary — and
// a boundary that matters has to be a decision on the command line rather than
// a sentence in a doc. The refusal happens BEFORE the listener opens, which is
// the part worth asserting: a process that binds and then complains has
// already exposed what it was warning about.
func TestPortfolioRefusesARemoteHostWithoutTheFlag(t *testing.T) {
	dir := t.TempDir()
	scaffoldProject(t, filepath.Join(dir, "alpha"), "spec_root: specs\n")
	glob := filepath.Join(dir, "*")

	cases := []struct {
		name        string
		host        string
		allowRemote bool
		wantRefused bool
	}{
		{"every interface, no flag", "0.0.0.0", false, true},
		{"every interface v6, no flag", "::", false, true},
		{"a LAN address, no flag", "192.168.1.10", false, true},
		// The flag is what makes it the operator's decision, so with it the
		// startup gets past this check. It then fails on the listener (a LAN
		// address this machine does not have), which is the proof it got
		// through rather than being refused earlier.
		{"a LAN address with the flag", "192.168.1.10", true, false},
		// Loopback never needs the flag.
		{"loopback, no flag", "127.0.0.1", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newPortfolioTestCmd(t)
			// Port 0 so the loopback case does not hold a fixed port; it is
			// cancelled immediately below.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			cmd.SetContext(ctx)

			err := runPortfolio(cmd, glob, portfolioFlags{
				host: tc.host, port: 0, allowRemote: tc.allowRemote,
			})
			refused := err != nil && strings.Contains(err.Error(), "--allow-remote")
			if refused != tc.wantRefused {
				t.Fatalf("refused = %v (err = %v), want %v", refused, err, tc.wantRefused)
			}
			if tc.wantRefused {
				// The message has to name the host and say how many projects
				// are at stake; "refused" on its own tells the operator
				// nothing about what they nearly did.
				if !strings.Contains(err.Error(), tc.host) {
					t.Errorf("the refusal does not name the host: %v", err)
				}
				// Named, not counted: the gate runs before anything is
				// opened, so the glob the operator typed is what it has
				// to hand back.
				if !strings.Contains(err.Error(), glob) {
					t.Errorf("the refusal does not name the glob: %v", err)
				}
			}
		})
	}
}

// `--allow-remote` alone is refused rather than quietly doing nothing.
//
// Same rule as `--token` and `--tunnel` with `--portfolio`: a flag that is
// accepted and has no effect is the failure this port keeps finding. A single
// project's exposure is decided by its profile and its token, and pretending
// this flag participates in that would be a third story about one question.
func TestAllowRemoteWithoutPortfolioIsRefused(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	scaffoldProject(t, root, "spec_root: specs\n")
	// scaffoldProject writes what the portfolio opener needs;
	// resolveAndValidate also wants scripts/task-start.sh, so the
	// single-project path would fail later either way. The assertion is that
	// it fails HERE, on the flag, before any of that.
	code := Run("v-test", []string{"dashboard", "--allow-remote", "--project-root", root})
	if code == 0 {
		t.Fatal("--allow-remote was accepted without --portfolio")
	}
}
