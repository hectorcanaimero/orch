package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/skills"
)

// newInstallSkillsCmd ports `orch install-skills` (orchestrator/orch.py's
// _run_install_skills_subcommand, H-6): copying the packaged skill(s) into
// ~/.claude/skills/, skip-unless---force, --dry-run, --path override.
//
// --target is new capability, not a port: Python only ever wrote Claude
// Code's own skills directory. codex/opencode read no such directory — an
// AGENTS.md section is the closest equivalent they do read — and cursor has
// no Python-side equivalent at all. See
// docs/brainstorm/go-migration-notes/sonnet-2.md.
//
// --all installs whatever internal/skills embeds — today orch and the five
// planning-pipeline skills — so adding one needs no CLI change (see
// internal/skills' package doc).
func newInstallSkillsCmd(flags *projectFlags) *cobra.Command {
	var (
		path    string
		force   bool
		dryRun  bool
		all     bool
		targets []string
		names   []string
	)
	cmd := &cobra.Command{
		Use:   "install-skills",
		Short: "Install orch's Claude Code skill(s) into one or more agent CLIs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstallSkills(cmd.OutOrStdout(), installSkillsArgs{
				path: path, projectRoot: flags.root, force: force, dryRun: dryRun,
				all: all, targets: targets, names: names,
			})
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "Claude target install directory (default: ~/.claude/skills)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an already-installed skill of the same name")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be installed without writing anything")
	cmd.Flags().BoolVar(&all, "all", false, "Install every embedded skill (default when --skill is omitted)")
	cmd.Flags().StringSliceVar(&targets, "target", []string{string(skills.TargetClaude)},
		"Agent(s) to install into: claude, codex, opencode, cursor (repeatable)")
	cmd.Flags().StringSliceVar(&names, "skill", nil, "Install only this skill (repeatable); default is --all")
	return cmd
}

type installSkillsArgs struct {
	path        string
	projectRoot string
	force       bool
	dryRun      bool
	all         bool
	targets     []string
	names       []string
}

func runInstallSkills(w io.Writer, a installSkillsArgs) error {
	if a.all && len(a.names) > 0 {
		return fmt.Errorf("install-skills: --all and --skill are mutually exclusive")
	}
	selected, err := selectSkills(a.names)
	if err != nil {
		return err
	}

	targets, err := parseTargets(a.targets)
	if err != nil {
		return err
	}

	if a.dryRun {
		if _, err := fmt.Fprintf(w, "--dry-run: would install into target(s) %s:\n", joinTargets(targets)); err != nil {
			return err
		}
		for _, s := range selected {
			if _, err := fmt.Fprintf(w, "  - %s\n", s.Name); err != nil {
				return err
			}
		}
		return nil
	}

	opts := skills.Options{ClaudeDir: a.path, ProjectRoot: a.projectRoot, Force: a.force}
	var installed, updated, skipped, unchanged []skills.Result
	for _, target := range targets {
		for _, s := range selected {
			res, err := skills.Install(s, target, opts)
			if err != nil {
				return fmt.Errorf("install-skills: %w", err)
			}
			switch res.Action {
			case skills.ActionInstalled:
				installed = append(installed, res)
			case skills.ActionUpdated:
				updated = append(updated, res)
			case skills.ActionSkipped:
				skipped = append(skipped, res)
			case skills.ActionUnchanged:
				unchanged = append(unchanged, res)
			}
		}
	}

	if err := printInstallResults(w, "installed", installed); err != nil {
		return err
	}
	if err := printInstallResults(w, "updated", updated); err != nil {
		return err
	}
	if len(skipped) > 0 {
		if _, err := fmt.Fprintln(w, "↷ skipped (already present, differs — pass --force to overwrite):"); err != nil {
			return err
		}
		for _, r := range skipped {
			if _, err := fmt.Fprintf(w, "    %s -> %s (%s)\n", r.Skill, r.Path, r.Target); err != nil {
				return err
			}
		}
	}
	if len(installed) == 0 && len(updated) == 0 && len(skipped) == 0 && len(unchanged) > 0 {
		if _, err := fmt.Fprintln(w, "nothing to do (already up to date)"); err != nil {
			return err
		}
	}
	return nil
}

func printInstallResults(w io.Writer, verb string, results []skills.Result) error {
	if len(results) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "✓ %s %d skill(s):\n", verb, len(results)); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := fmt.Fprintf(w, "    %s -> %s (%s)\n", r.Skill, r.Path, r.Target); err != nil {
			return err
		}
	}
	return nil
}

func selectSkills(names []string) ([]skills.Skill, error) {
	if len(names) == 0 {
		all, err := skills.List()
		if err != nil {
			return nil, fmt.Errorf("install-skills: %w", err)
		}
		if len(all) == 0 {
			return nil, fmt.Errorf("install-skills: no skills bundled with this build")
		}
		return all, nil
	}
	out := make([]skills.Skill, 0, len(names))
	for _, name := range names {
		s, ok := skills.Get(name)
		if !ok {
			return nil, fmt.Errorf("install-skills: no embedded skill named %q", name)
		}
		out = append(out, s)
	}
	return out, nil
}

func parseTargets(raw []string) ([]skills.Target, error) {
	out := make([]skills.Target, 0, len(raw))
	for _, r := range raw {
		t := skills.Target(strings.TrimSpace(r))
		switch t {
		case skills.TargetClaude, skills.TargetCodex, skills.TargetOpencode, skills.TargetCursor:
			out = append(out, t)
		default:
			return nil, fmt.Errorf("install-skills: unknown --target %q (want claude, codex, opencode, or cursor)", r)
		}
	}
	return out, nil
}

func joinTargets(targets []skills.Target) string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = string(t)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
