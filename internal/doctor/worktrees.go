package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// CheckOrphanWorktrees reports two things internal/worktree's own
// bookkeeping (an in-memory map, gone the moment the process exits) can't
// see across restarts:
//
//   - a directory under .worktrees/ that git no longer recognizes as a
//     worktree — left behind when a process was killed before Remove ran;
//   - a local `orch/<id>` branch with no corresponding worktree directory —
//     Create's F-7 purge only runs the next time THAT task id is created,
//     so a task that finished and pushed leaves its branch sitting there
//     indefinitely otherwise.
//
// Neither has a Python equivalent to port — internal/worktree (G4.1) postdates
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

	var orphanDirs []string
	entries, err := os.ReadDir(filepath.Join(root, ".worktrees"))
	switch {
	case err == nil:
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(root, ".worktrees", e.Name())
			if !registered[filepath.Clean(p)] {
				orphanDirs = append(orphanDirs, e.Name())
			}
		}
	case os.IsNotExist(err):
		// No .worktrees/ at all — a project that has never dispatched a
		// task under worktree mode. Nothing to find, not a failure.
	default:
		// A real read failure (permissions, ...) must not read as "found
		// zero orphans" (rule 19) — a doctor check that can't see the
		// directory can't vouch for it either.
		return Check{Name: name, Status: StatusWarn,
			Detail: "could not read .worktrees/: " + err.Error()}
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
		if _, err := os.Stat(filepath.Join(root, ".worktrees", taskID)); os.IsNotExist(err) {
			orphanBranches = append(orphanBranches, branch)
		}
	}

	if len(orphanDirs) == 0 && len(orphanBranches) == 0 {
		return Check{Name: name, Status: StatusOK, Detail: "no orphaned worktree directories or branches"}
	}

	sort.Strings(orphanDirs)
	sort.Strings(orphanBranches)
	var parts []string
	if len(orphanDirs) > 0 {
		parts = append(parts, fmt.Sprintf("%d unregistered .worktrees/ dir(s): %s", len(orphanDirs), strings.Join(orphanDirs, ", ")))
	}
	if len(orphanBranches) > 0 {
		parts = append(parts, fmt.Sprintf("%d orch/* branch(es) with no live worktree: %s", len(orphanBranches), strings.Join(orphanBranches, ", ")))
	}
	return Check{
		Name: name, Status: StatusWarn,
		Detail:      strings.Join(parts, "; "),
		Remediation: "`git worktree prune` for stale directories; `git branch -D orch/<id>` for finished branches.",
	}
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
