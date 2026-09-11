package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Target is an agent CLI orch can install a skill into.
type Target string

const (
	// TargetClaude copies the skill verbatim into a "skills" directory
	// (default ~/.claude/skills/<name>/SKILL.md) — Claude Code's own
	// on-demand skill discovery mechanism.
	TargetClaude Target = "claude"
	// TargetCodex and TargetOpencode neither read ~/.claude/skills/ nor
	// have a skills mechanism of their own; both read an AGENTS.md at the
	// project root, so both install as a clearly-delimited section in it.
	TargetCodex    Target = "codex"
	TargetOpencode Target = "opencode"
	// TargetCursor installs a Cursor rule file under .cursor/rules/.
	TargetCursor Target = "cursor"
)

// Action is what Install actually did, for a caller to report.
type Action string

const (
	ActionInstalled Action = "installed"
	ActionUpdated   Action = "updated"
	ActionUnchanged Action = "unchanged"
	ActionSkipped   Action = "skipped" // already present and different; needs --force
)

// Result is the outcome of installing one skill into one target.
type Result struct {
	Skill  string
	Target Target
	Path   string
	Action Action
}

// Options configures where each target writes. ClaudeDir and ProjectRoot
// both default when empty: ClaudeDir to "~/.claude/skills", ProjectRoot to
// ".". Force overwrites a differing existing install instead of skipping
// it; DryRun computes and returns the Result without writing anything —
// Action on a dry run is what WOULD happen, not what did.
type Options struct {
	ClaudeDir   string
	ProjectRoot string
	Force       bool
	DryRun      bool
}

func (o Options) claudeDir() string {
	if o.ClaudeDir != "" {
		return o.ClaudeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".claude", "skills")
	}
	return filepath.Join(home, ".claude", "skills")
}

func (o Options) projectRoot() string {
	if o.ProjectRoot != "" {
		return o.ProjectRoot
	}
	return "."
}

// Install writes skill into target, per Options. Idempotent: installing
// the same content twice is always Action == unchanged the second time,
// and a differing existing file is only overwritten with Force.
func Install(skill Skill, target Target, opts Options) (Result, error) {
	switch target {
	case TargetClaude:
		return writeFile(skill.Name, target,
			filepath.Join(opts.claudeDir(), skill.Name, "SKILL.md"), skill.Content, opts)
	case TargetCodex, TargetOpencode:
		return installAgentsMD(skill, target, opts)
	case TargetCursor:
		return installCursor(skill, opts)
	default:
		return Result{}, fmt.Errorf("skills: unknown target %q", target)
	}
}

// writeFile is the common "create parent dirs, compare, write-or-skip"
// path every target other than AGENTS.md's in-place section edit uses.
func writeFile(skillName string, target Target, path, content string, opts Options) (Result, error) {
	existing, err := os.ReadFile(path) // #nosec G304 -- path is built from this package's own target roots + a skill name it embeds.
	switch {
	case err == nil:
		if string(existing) == content {
			return Result{Skill: skillName, Target: target, Path: path, Action: ActionUnchanged}, nil
		}
		if !opts.Force {
			return Result{Skill: skillName, Target: target, Path: path, Action: ActionSkipped}, nil
		}
		if opts.DryRun {
			return Result{Skill: skillName, Target: target, Path: path, Action: ActionUpdated}, nil
		}
		if werr := os.WriteFile(path, []byte(content), 0o600); werr != nil {
			return Result{}, fmt.Errorf("skills: write %s: %w", path, werr)
		}
		return Result{Skill: skillName, Target: target, Path: path, Action: ActionUpdated}, nil
	case os.IsNotExist(err):
		if opts.DryRun {
			return Result{Skill: skillName, Target: target, Path: path, Action: ActionInstalled}, nil
		}
		if merr := os.MkdirAll(filepath.Dir(path), 0o750); merr != nil {
			return Result{}, fmt.Errorf("skills: create %s: %w", filepath.Dir(path), merr)
		}
		if werr := os.WriteFile(path, []byte(content), 0o600); werr != nil {
			return Result{}, fmt.Errorf("skills: write %s: %w", path, werr)
		}
		return Result{Skill: skillName, Target: target, Path: path, Action: ActionInstalled}, nil
	default:
		return Result{}, fmt.Errorf("skills: read %s: %w", path, err)
	}
}

// ---- AGENTS.md (codex, opencode) -------------------------------------------

func agentsMarkers(skillName string) (start, end string) {
	return fmt.Sprintf("<!-- orch:skill:%s:start -->", skillName),
		fmt.Sprintf("<!-- orch:skill:%s:end -->", skillName)
}

// installAgentsMD writes skill as a clearly-delimited, replaceable section
// of <projectRoot>/AGENTS.md — creating the file if absent, replacing an
// existing section with the same markers in place, or appending a new one.
// Never touches anything outside its own markers, so a project's own
// AGENTS.md content (or another tool's section) survives untouched.
func installAgentsMD(skill Skill, target Target, opts Options) (Result, error) {
	path := filepath.Join(opts.projectRoot(), "AGENTS.md")
	start, end := agentsMarkers(skill.Name)
	section := start + "\n\n" + skill.Content + "\n\n" + end

	existing, err := os.ReadFile(path) // #nosec G304 -- path is opts.projectRoot() joined with a fixed filename.
	if err != nil && !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("skills: read %s: %w", path, err)
	}
	current := string(existing)

	next, action := mergeSection(current, start, end, section)
	if action == ActionUnchanged {
		return Result{Skill: skill.Name, Target: target, Path: path, Action: ActionUnchanged}, nil
	}
	if action == ActionSkipped && !opts.Force {
		return Result{Skill: skill.Name, Target: target, Path: path, Action: ActionSkipped}, nil
	}
	if action == ActionSkipped {
		// Force: replace the differing section anyway.
		next = replaceSectionForce(current, start, end, section)
		action = ActionUpdated
	}
	if opts.DryRun {
		return Result{Skill: skill.Name, Target: target, Path: path, Action: action}, nil
	}
	if merr := os.MkdirAll(filepath.Dir(path), 0o750); merr != nil {
		return Result{}, fmt.Errorf("skills: create %s: %w", filepath.Dir(path), merr)
	}
	if werr := os.WriteFile(path, []byte(next), 0o600); werr != nil { // #nosec G703 -- path is opts.projectRoot() joined with a fixed filename, same as the read above; next is this file's own content plus the embedded skill body, not attacker input.
		return Result{}, fmt.Errorf("skills: write %s: %w", path, werr)
	}
	return Result{Skill: skill.Name, Target: target, Path: path, Action: action}, nil
}

// mergeSection reports what installing section (delimited by start/end)
// into current would do, without Force applied yet: unchanged if an
// identical section is already there, skipped if a DIFFERENT one is (the
// caller decides whether Force allows overwriting it), or installed if the
// markers aren't present at all (section will be appended). It does not
// itself produce the "replace" text for the skipped-but-forced case —
// replaceSectionForce does that — so a caller can decide before paying for
// the string-building.
func mergeSection(current, start, end, section string) (string, Action) {
	// section is start+"\n\n"+body+"\n\n"+end; extractSection returns the
	// same slice-and-trim of an EXISTING section, so compare like for like
	// (trimmed inner body) rather than against section's full marker-
	// wrapped text, which an existing section's extracted inner text could
	// never equal.
	newInner := strings.TrimSpace(section[len(start) : len(section)-len(end)])
	existingSection, ok := extractSection(current, start, end)
	switch {
	case !ok:
		if current == "" {
			return section + "\n", ActionInstalled
		}
		sep := "\n"
		if strings.HasSuffix(current, "\n") {
			sep = ""
		}
		return current + sep + "\n" + section + "\n", ActionInstalled
	case existingSection == newInner:
		return current, ActionUnchanged
	default:
		return current, ActionSkipped
	}
}

func replaceSectionForce(current, start, end, section string) string {
	replaced, ok := replaceSection(current, start, end, section)
	if !ok {
		// Markers vanished between mergeSection's check and here — treat
		// as append, same as the "not present" path.
		return current + "\n" + section + "\n"
	}
	return replaced
}

// extractSection returns the text between start and end (exclusive of the
// markers themselves), and whether both markers were found in order.
func extractSection(content, start, end string) (string, bool) {
	si := strings.Index(content, start)
	if si == -1 {
		return "", false
	}
	ei := strings.Index(content[si:], end)
	if ei == -1 {
		return "", false
	}
	return strings.TrimSpace(content[si+len(start) : si+ei]), true
}

// replaceSection swaps the text between start and end (markers included)
// for replacement, reporting whether it found both markers to replace.
func replaceSection(content, start, end, replacement string) (string, bool) {
	si := strings.Index(content, start)
	if si == -1 {
		return content, false
	}
	relEnd := strings.Index(content[si:], end)
	if relEnd == -1 {
		return content, false
	}
	ei := si + relEnd + len(end)
	return content[:si] + replacement + content[ei:], true
}

// ---- Cursor -----------------------------------------------------------

// installCursor writes a Cursor project rule at
// <projectRoot>/.cursor/rules/<skill>.mdc — Cursor's own format: a small
// YAML frontmatter (description, globs, alwaysApply) followed by Markdown.
// alwaysApply is always false: this is reference material Cursor should
// pull in when relevant (its description matches), not inject into every
// prompt.
func installCursor(skill Skill, opts Options) (Result, error) {
	path := filepath.Join(opts.projectRoot(), ".cursor", "rules", skill.Name+".mdc")
	desc := description(skill.Content)
	content := fmt.Sprintf("---\ndescription: %s\nalwaysApply: false\n---\n\n%s",
		desc, strings.TrimSpace(body(skill.Content))+"\n")
	return writeFile(skill.Name, TargetCursor, path, content, opts)
}
