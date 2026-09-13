package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/cli"
)

func TestInstallSkillsClaudeTargetWritesSkillMD(t *testing.T) {
	dir := t.TempDir()
	rc := cli.Run("test", []string{"install-skills", "--path", dir})
	if rc != 0 {
		t.Fatalf("install-skills: rc = %d, want 0", rc)
	}
	got, err := os.ReadFile(filepath.Join(dir, "orch", "SKILL.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "---\n") {
		t.Errorf("installed SKILL.md doesn't look like a skill file: %q", string(got)[:20])
	}
}

func TestInstallSkillsDryRunTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	rc := cli.Run("test", []string{"install-skills", "--path", dir, "--dry-run"})
	if rc != 0 {
		t.Fatalf("install-skills --dry-run: rc = %d, want 0", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "orch")); !os.IsNotExist(err) {
		t.Error("--dry-run must not create anything under --path")
	}
}

func TestInstallSkillsSecondRunIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if rc := cli.Run("test", []string{"install-skills", "--path", dir}); rc != 0 {
			t.Fatalf("run %d: rc = %d, want 0", i, rc)
		}
	}
}

func TestInstallSkillsCodexTargetWritesAgentsMD(t *testing.T) {
	dir := t.TempDir()
	rc := cli.Run("test", []string{"install-skills", "--target", "codex", "--project-root", dir})
	if rc != 0 {
		t.Fatalf("install-skills --target codex: rc = %d, want 0", rc)
	}
	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "orch:skill:orch:start") {
		t.Errorf("AGENTS.md missing the orch skill section: %q", string(got))
	}
}

func TestInstallSkillsCursorTargetWritesMDC(t *testing.T) {
	dir := t.TempDir()
	rc := cli.Run("test", []string{"install-skills", "--target", "cursor", "--project-root", dir})
	if rc != 0 {
		t.Fatalf("install-skills --target cursor: rc = %d, want 0", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, ".cursor", "rules", "orch.mdc")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallSkillsMultipleTargetsInOneRun(t *testing.T) {
	claudeDir := t.TempDir()
	projectDir := t.TempDir()
	rc := cli.Run("test", []string{
		"install-skills",
		"--target", "claude", "--target", "opencode",
		"--path", claudeDir, "--project-root", projectDir,
	})
	if rc != 0 {
		t.Fatalf("install-skills multi-target: rc = %d, want 0", rc)
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "orch", "SKILL.md")); err != nil {
		t.Errorf("claude target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "AGENTS.md")); err != nil {
		t.Errorf("opencode target: %v", err)
	}
}

// G6.5's acceptance: every target leaves the expected files in a test repo,
// for every embedded skill — not just `orch`, which is all the tests above
// install. Codex and opencode share one AGENTS.md, so installing the second
// finds its sections already there; the check is that each skill appears
// exactly once, not once per target.
func TestInstallSkillsAllTargetsLeaveEverySkillInATestRepo(t *testing.T) {
	names := []string{"orch", "orch-arch", "orch-plan", "orch-prd", "orch-spec", "orch-tasks"}
	claudeDir := t.TempDir()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Project rules\n\nKeep this line.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"install-skills", "--all",
		"--target", "claude", "--target", "codex", "--target", "opencode", "--target", "cursor",
		"--path", claudeDir, "--project-root", repo,
	}
	if rc := cli.Run("test", args); rc != 0 {
		t.Fatalf("install-skills --all, four targets: rc = %d, want 0", rc)
	}

	agents, err := os.ReadFile(filepath.Join(repo, "AGENTS.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(agents), "# Project rules\n\nKeep this line.\n") {
		t.Errorf("AGENTS.md lost the project's own content:\n%s", agents)
	}
	for _, name := range names {
		skill, err := os.ReadFile(filepath.Join(claudeDir, name, "SKILL.md")) // #nosec G304 -- t.TempDir()
		if err != nil {
			t.Errorf("claude: %v", err)
		} else if !strings.Contains(string(skill), "\nname: "+name+"\n") {
			t.Errorf("claude: %s/SKILL.md does not carry its own name", name)
		}

		start := "<!-- orch:skill:" + name + ":start -->"
		if n := strings.Count(string(agents), start); n != 1 {
			t.Errorf("codex/opencode: AGENTS.md has %d %s sections, want 1", n, name)
		}

		mdc, err := os.ReadFile(filepath.Join(repo, ".cursor", "rules", name+".mdc")) // #nosec G304 -- t.TempDir()
		if err != nil {
			t.Errorf("cursor: %v", err)
		} else if !strings.HasPrefix(string(mdc), "---\ndescription: ") || strings.Contains(string(mdc), "\nname: ") {
			t.Errorf("cursor: %s.mdc is not a rule file with a description and no skill frontmatter:\n%.200s", name, mdc)
		}
	}

	// And a second run over the same repo changes nothing.
	before := string(agents)
	if rc := cli.Run("test", args); rc != 0 {
		t.Fatalf("second run: rc = %d, want 0", rc)
	}
	after, err := os.ReadFile(filepath.Join(repo, "AGENTS.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Error("a second identical install rewrote AGENTS.md")
	}
}

func TestInstallSkillsUnknownSkillNameErrors(t *testing.T) {
	dir := t.TempDir()
	rc := cli.Run("test", []string{"install-skills", "--path", dir, "--skill", "does-not-exist"})
	if rc == 0 {
		t.Fatal("expected a non-zero exit for an unknown --skill")
	}
}

func TestInstallSkillsUnknownTargetErrors(t *testing.T) {
	dir := t.TempDir()
	rc := cli.Run("test", []string{"install-skills", "--path", dir, "--target", "bogus-agent"})
	if rc == 0 {
		t.Fatal("expected a non-zero exit for an unknown --target")
	}
}

func TestInstallSkillsAllAndSkillAreMutuallyExclusive(t *testing.T) {
	dir := t.TempDir()
	rc := cli.Run("test", []string{"install-skills", "--path", dir, "--all", "--skill", "orch"})
	if rc == 0 {
		t.Fatal("expected a non-zero exit when --all and --skill are combined")
	}
}
