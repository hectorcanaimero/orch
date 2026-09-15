package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/hectorcanaimero/orch/internal/worktree"
)

// CheckOrphanWorktrees reports what internal/worktree's own bookkeeping (an
// in-memory map, gone the moment the process exits) can't see across
// restarts:
//
//   - a directory under `<project>.worktrees/` that git no longer recognizes
//     as a worktree — left behind when a process was killed before Remove ran;
//   - any directory under `<project>/.worktrees/`, where orch put worktrees
//     before #249: registered or not, in there it sits below the project's own
//     dependency tree, and no current run reuses it;
//   - a local `orch/<id>` branch with no corresponding worktree directory —
//     Create's F-7 purge only runs the next time THAT task id is created,
//     so a task that finished and pushed leaves its branch sitting there
//     indefinitely otherwise.
//
// None has a Python equivalent to port — internal/worktree (G4.1) postdates
// preflight.py entirely, so this is new coverage the G4.5 brief asked for
// directly rather than a port.
func CheckOrphanWorktrees(root string) Check {
	const name = "worktree.orphans"

	registered := map[string]bool{}
	listOut, err := gitCapture(root, "worktree", "list", "--porcelain")
	if err != nil {
		return Check{Name: name, Status: StatusWarn, Detail: "could not list git worktrees: " + err.Error()}
	}
	for _, line := range strings.Split(listOut, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			registered[filepath.Clean(p)] = true
		}
	}

	dir, legacyDir := worktree.Dir(root), worktree.LegacyDir(root)
	current, err := subdirs(dir)
	if err != nil {
		return Check{Name: name, Status: StatusWarn, Detail: "could not read " + dir + ": " + err.Error()}
	}
	leftovers, err := subdirs(legacyDir)
	if err != nil {
		return Check{Name: name, Status: StatusWarn, Detail: "could not read " + legacyDir + ": " + err.Error()}
	}
	var orphanDirs []string
	for _, id := range current {
		if !registered[filepath.Join(dir, id)] {
			orphanDirs = append(orphanDirs, id)
		}
	}

	var orphanBranches []string
	branchOut, err := gitCapture(root, "for-each-ref", "--format=%(refname:short)", "refs/heads/orch/*")
	if err != nil {
		return Check{Name: name, Status: StatusWarn,
			Detail: "could not list orch/* branches: " + err.Error()}
	}
	for _, branch := range strings.Split(branchOut, "\n") {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}
		taskID := strings.TrimPrefix(branch, "orch/")
		if !slices.Contains(current, taskID) && !slices.Contains(leftovers, taskID) {
			orphanBranches = append(orphanBranches, branch)
		}
	}

	if len(orphanDirs) == 0 && len(leftovers) == 0 && len(orphanBranches) == 0 {
		return Check{Name: name, Status: StatusOK, Detail: "no orphaned worktree directories or branches"}
	}

	sort.Strings(orphanBranches)
	var parts []string
	remediation := "`git worktree prune` for stale directories; `git branch -D orch/<id>` for finished branches"
	if len(orphanDirs) > 0 {
		parts = append(parts, fmt.Sprintf("%d unregistered dir(s) in %s: %s", len(orphanDirs), dir, strings.Join(orphanDirs, ", ")))
	}
	if len(leftovers) > 0 {
		parts = append(parts, fmt.Sprintf("%d worktree dir(s) an older orch left inside the project, in %s: %s",
			len(leftovers), legacyDir, strings.Join(leftovers, ", ")))
		remediation += "; `git worktree remove --force <dir>` for each one left in " + legacyDir
	}
	if len(orphanBranches) > 0 {
		parts = append(parts, fmt.Sprintf("%d orch/* branch(es) with no live worktree: %s", len(orphanBranches), strings.Join(orphanBranches, ", ")))
	}
	return Check{
		Name: name, Status: StatusWarn,
		Detail:      strings.Join(parts, "; "),
		Remediation: remediation + ".",
	}
}

// subdirs lists the directory names in dir, sorted. A missing dir — a
// project that never dispatched under worktree mode, or never under the old
// layout — holds none, which is not a failure. Any other read failure is
// returned: a check that can't see the directory can't vouch for it (rule 19).
func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the worktree directory: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// gitCapture runs a git command in root and returns raw stdout. Unlike
// gitOK (vcs.go), it returns the error too — callers here (CheckOrphanWorktrees)
// distinguish "git itself failed" (worth a warning) from "ran fine, empty
// output" (nothing to report), which gitOK's single-string contract can't.
func gitCapture(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	// #nosec G204 -- args are this package's own fixed git subcommands.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	return string(out), err
}
