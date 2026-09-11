package doctor

import (
	"os/exec"
	"testing"
)

func newTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		// #nosec G204 -- args are this test file's own fixed git subcommands.
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	// A CI runner has no global git identity configured (unlike a dev
	// machine, where this worked by accident) — set one locally so
	// `commit` doesn't fail with "Please tell me who you are".
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-q", "-m", "initial")
	return dir
}

func TestCheckVCSReadinessSkipsWhenNothingRequested(t *testing.T) {
	checks := CheckVCSReadiness(t.TempDir(), VCSConfig{})
	for _, c := range checks {
		if c.Status != StatusSkip {
			t.Errorf("%s = %+v, want skip", c.Name, c)
		}
	}
}

func TestCheckVCSReadinessGitRepoOK(t *testing.T) {
	withFakeBin(t)
	root := newTestGitRepo(t)
	checks := CheckVCSReadiness(root, VCSConfig{WorktreeMode: true})
	byName := checksByName(checks)
	if byName["vcs.git_repo"].Status != StatusOK {
		t.Errorf("vcs.git_repo = %+v", byName["vcs.git_repo"])
	}
	if byName["vcs.remote"].Status != StatusSkip {
		t.Errorf("vcs.remote = %+v, want skip (auto_pr off)", byName["vcs.remote"])
	}
}

func TestCheckVCSReadinessNotAGitRepoWarns(t *testing.T) {
	withFakeBin(t)
	checks := CheckVCSReadiness(t.TempDir(), VCSConfig{WorktreeMode: true})
	byName := checksByName(checks)
	if byName["vcs.git_repo"].Status != StatusWarn {
		t.Errorf("vcs.git_repo = %+v, want warn", byName["vcs.git_repo"])
	}
}

func TestCheckVCSReadinessNoRemoteWarnsWhenAutoPR(t *testing.T) {
	withFakeBin(t)
	root := newTestGitRepo(t)
	checks := CheckVCSReadiness(root, VCSConfig{WorktreeMode: true, AutoPR: true})
	byName := checksByName(checks)
	if byName["vcs.remote"].Status != StatusWarn {
		t.Errorf("vcs.remote = %+v, want warn (no remote configured)", byName["vcs.remote"])
	}
}

func TestCheckVCSReadinessCLIAuthenticatedOK(t *testing.T) {
	withFakeBin(t)
	root := newTestGitRepo(t)
	cmd := exec.Command("git", "remote", "add", "origin", "https://example.com/x.git")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add failed: %v\n%s", err, out)
	}

	checks := CheckVCSReadiness(root, VCSConfig{WorktreeMode: true, AutoPR: true})
	byName := checksByName(checks)
	if byName["vcs.remote"].Status != StatusOK {
		t.Errorf("vcs.remote = %+v", byName["vcs.remote"])
	}
	if byName["vcs.cli"].Status != StatusOK {
		t.Errorf("vcs.cli = %+v, want ok (fake gh authenticated)", byName["vcs.cli"])
	}
}

func TestCheckVCSReadinessCLINotAuthenticatedWarns(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_AUTH_EXIT", "1")
	root := newTestGitRepo(t)
	checks := CheckVCSReadiness(root, VCSConfig{WorktreeMode: true, AutoPR: true})
	byName := checksByName(checks)
	if byName["vcs.cli"].Status != StatusWarn {
		t.Errorf("vcs.cli = %+v, want warn", byName["vcs.cli"])
	}
}

func TestCheckVCSReadinessGitLabHost(t *testing.T) {
	withFakeBin(t)
	root := newTestGitRepo(t)
	checks := CheckVCSReadiness(root, VCSConfig{WorktreeMode: true, AutoPR: true, Provider: "gitlab", Host: "gitlab.example.com"})
	byName := checksByName(checks)
	if byName["vcs.cli"].Status != StatusOK {
		t.Errorf("vcs.cli = %+v, want ok (fake glab authenticated)", byName["vcs.cli"])
	}
}

func checksByName(checks []Check) map[string]Check {
	out := map[string]Check{}
	for _, c := range checks {
		out[c.Name] = c
	}
	return out
}
