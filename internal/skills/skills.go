// Package skills embeds orch's Claude Code skill(s) and installs them into
// whichever agent CLI a project wants: Claude Code itself, Codex/OpenCode
// (via an AGENTS.md section — neither reads ~/.claude/skills/), or Cursor
// (via a .cursor/rules/*.mdc file).
//
// Ports the shipped-skill half of orchestrator/orch.py's
// _run_install_skills_subcommand (H-6): copying a packaged skill into
// ~/.claude/skills/, skip-unless---force on an existing install, --dry-run.
// The multi-target install (codex/opencode/cursor) has no Python
// equivalent — Python ships one static AGENTS.md stub and nothing for
// Cursor at all; this is new capability the G6.5 brief asked for directly,
// not a port. See docs/brainstorm/go-migration-notes/sonnet-2.md.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// skillsFS embeds every "<name>/SKILL.md" next to this file. "orch" is the
// only one that exists today — orch-plan/orch-prd/orch-arch/orch-spec/
// orch-tasks live outside this repo on the operator's machine and have no
// Go (or checked-in) home yet. Adding one later is exactly this: drop
// "<name>/SKILL.md" in a new directory beside orch/ — nothing else in this
// package needs to change, since List walks whatever the embed pattern
// matched rather than naming skills individually.
//
//go:embed */SKILL.md
var skillsFS embed.FS

// Skill is one embedded skill file, verbatim.
type Skill struct {
	// Name is the directory name under this package — also the name Claude
	// Code installs it under (~/.claude/skills/<Name>/SKILL.md).
	Name string
	// Content is the raw SKILL.md bytes, frontmatter included.
	Content string
}

// List returns every embedded skill, sorted by name.
func List() ([]Skill, error) {
	entries, err := fs.ReadDir(skillsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("skills: list embedded skills: %w", err)
	}
	out := make([]Skill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, rerr := skillsFS.ReadFile(e.Name() + "/SKILL.md")
		if rerr != nil {
			// A directory with no SKILL.md isn't a skill — the embed
			// pattern (*/SKILL.md) only matches directories that have
			// one, so this is unreachable barring a packaging mistake;
			// skip rather than fail the whole list over it.
			continue
		}
		out = append(out, Skill{Name: e.Name(), Content: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the named skill, or (Skill{}, false) if it isn't embedded.
func Get(name string) (Skill, bool) {
	all, err := List()
	if err != nil {
		return Skill{}, false
	}
	for _, s := range all {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// description pulls the one-line `description:` field out of a SKILL.md's
// YAML frontmatter, for targets (Cursor) that want a short summary rather
// than the whole file. Returns "" if there's no frontmatter or no
// description key — deliberately not a YAML parse, since the frontmatter
// here is a fixed two-key shape (name, description) and pulling in a YAML
// dependency for one field would be a strange trade.
func description(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return ""
	}
	end := strings.Index(content[4:], "\n---")
	if end == -1 {
		return ""
	}
	for _, line := range strings.Split(content[4:4+end], "\n") {
		if rest, ok := strings.CutPrefix(line, "description:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// body returns content with its YAML frontmatter block removed, or content
// unchanged if it has none.
func body(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	end := strings.Index(content[4:], "\n---")
	if end == -1 {
		return content
	}
	rest := content[4+end+len("\n---"):]
	// The closing fence's own trailing newline and the conventional blank
	// line separating it from the body are both just leading "\n"s here —
	// strip all of them, not only one, so the body never starts with a
	// stray blank line.
	return strings.TrimLeft(rest, "\n")
}
