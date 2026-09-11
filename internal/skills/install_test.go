package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSkill() Skill {
	return Skill{
		Name: "orch",
		Content: "---\nname: orch\ndescription: test skill for install unit tests\n---\n\n" +
			"# Orch\n\nbody text.\n",
	}
}

func TestInstallClaudeCreatesTheSkillFile(t *testing.T) {
	dir := t.TempDir()
	res, err := Install(testSkill(), TargetClaude, Options{ClaudeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionInstalled {
		t.Errorf("Action = %q, want %q", res.Action, ActionInstalled)
	}
	want := filepath.Join(dir, "orch", "SKILL.md")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
	got, err := os.ReadFile(want) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != testSkill().Content {
		t.Errorf("file content mismatch")
	}
}

func TestInstallClaudeSecondCallIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(testSkill(), TargetClaude, Options{ClaudeDir: dir}); err != nil {
		t.Fatal(err)
	}
	res, err := Install(testSkill(), TargetClaude, Options{ClaudeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionUnchanged {
		t.Errorf("Action = %q, want %q", res.Action, ActionUnchanged)
	}
}

func TestInstallClaudeDifferingContentSkippedWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(testSkill(), TargetClaude, Options{ClaudeDir: dir}); err != nil {
		t.Fatal(err)
	}
	changed := testSkill()
	changed.Content += "\nextra line\n"
	res, err := Install(changed, TargetClaude, Options{ClaudeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionSkipped {
		t.Errorf("Action = %q, want %q", res.Action, ActionSkipped)
	}
	got, err := os.ReadFile(filepath.Join(dir, "orch", "SKILL.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != testSkill().Content {
		t.Error("skipped install must not have touched the existing file")
	}
}

func TestInstallClaudeForceOverwritesDifferingContent(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(testSkill(), TargetClaude, Options{ClaudeDir: dir}); err != nil {
		t.Fatal(err)
	}
	changed := testSkill()
	changed.Content += "\nextra line\n"
	res, err := Install(changed, TargetClaude, Options{ClaudeDir: dir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", res.Action, ActionUpdated)
	}
	got, err := os.ReadFile(filepath.Join(dir, "orch", "SKILL.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != changed.Content {
		t.Error("forced install must overwrite with the new content")
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	res, err := Install(testSkill(), TargetClaude, Options{ClaudeDir: dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionInstalled {
		t.Errorf("Action = %q, want %q (what WOULD happen)", res.Action, ActionInstalled)
	}
	if _, err := os.Stat(filepath.Join(dir, "orch", "SKILL.md")); !os.IsNotExist(err) {
		t.Error("--dry-run must not create the file")
	}
}

func TestInstallDryRunOnDifferingExistingFileReportsSkippedWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(testSkill(), TargetClaude, Options{ClaudeDir: dir}); err != nil {
		t.Fatal(err)
	}
	changed := testSkill()
	changed.Content += "\nextra\n"
	res, err := Install(changed, TargetClaude, Options{ClaudeDir: dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionSkipped {
		t.Errorf("Action = %q, want %q", res.Action, ActionSkipped)
	}
}

func TestInstallAgentsMDCreatesFileWithMarkedSection(t *testing.T) {
	dir := t.TempDir()
	res, err := Install(testSkill(), TargetCodex, Options{ProjectRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionInstalled {
		t.Errorf("Action = %q, want %q", res.Action, ActionInstalled)
	}
	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)
	start, end := agentsMarkers("orch")
	if !strings.Contains(content, start) || !strings.Contains(content, end) {
		t.Errorf("AGENTS.md missing markers: %q", content)
	}
	if !strings.Contains(content, "body text.") {
		t.Errorf("AGENTS.md missing skill body: %q", content)
	}
}

func TestInstallAgentsMDPreservesExistingContentOutsideMarkers(t *testing.T) {
	dir := t.TempDir()
	preexisting := "# My project\n\nSome human-written notes.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(preexisting), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(testSkill(), TargetCodex, Options{ProjectRoot: dir}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Some human-written notes.") {
		t.Errorf("existing AGENTS.md content was lost: %q", string(got))
	}
}

func TestInstallAgentsMDSecondCallIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(testSkill(), TargetOpencode, Options{ProjectRoot: dir}); err != nil {
		t.Fatal(err)
	}
	res, err := Install(testSkill(), TargetOpencode, Options{ProjectRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionUnchanged {
		t.Errorf("Action = %q, want %q", res.Action, ActionUnchanged)
	}
}

func TestInstallAgentsMDReplacesItsOwnSectionInPlaceOnForce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(testSkill(), TargetCodex, Options{ProjectRoot: dir}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}

	changed := testSkill()
	changed.Content = "---\nname: orch\ndescription: updated\n---\n\n# Orch\n\nnew body.\n"
	res, err := Install(changed, TargetCodex, Options{ProjectRoot: dir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", res.Action, ActionUpdated)
	}
	after, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "body text.") {
		t.Error("old section body should have been replaced")
	}
	if !strings.Contains(string(after), "new body.") {
		t.Error("new section body should be present")
	}
	// Only one skill section should exist — not appended alongside the old one.
	if strings.Count(string(before), "orch:skill:orch:start") != 1 ||
		strings.Count(string(after), "orch:skill:orch:start") != 1 {
		t.Error("expected exactly one marker pair before and after replace")
	}
}

func TestInstallCursorWritesMDCFileWithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	res, err := Install(testSkill(), TargetCursor, Options{ProjectRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionInstalled {
		t.Errorf("Action = %q, want %q", res.Action, ActionInstalled)
	}
	want := filepath.Join(dir, ".cursor", "rules", "orch.mdc")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
	got, err := os.ReadFile(want) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)
	if !strings.Contains(content, "description: test skill for install unit tests") {
		t.Errorf(".mdc missing extracted description: %q", content)
	}
	if !strings.Contains(content, "alwaysApply: false") {
		t.Errorf(".mdc missing alwaysApply: false: %q", content)
	}
	if strings.Contains(content, "name: orch\n") {
		t.Errorf(".mdc should not carry over the SKILL.md frontmatter verbatim: %q", content)
	}
	if !strings.Contains(content, "body text.") {
		t.Errorf(".mdc missing body: %q", content)
	}
}

func TestInstallUnknownTargetErrors(t *testing.T) {
	if _, err := Install(testSkill(), Target("nonsense"), Options{}); err == nil {
		t.Error("expected an error for an unknown target")
	}
}
