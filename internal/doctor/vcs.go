package doctor

import (
	"context"
	"os/exec"
	"strings"
	"syscall"

	"github.com/hectorcanaimero/orch/internal/vcs"
)

// VCSConfig is the subset of config the VCS readiness check needs — the
// same three values probe_vcs_readiness reads from cfg["dispatch"] and
// cfg["vcs"].
type VCSConfig struct {
	WorktreeMode bool
	AutoPR       bool
	// Provider is "github" (default) or "gitlab"; Host is the GitLab
	// instance host, ignored for GitHub. Matches vcs.Config.
	Provider string
	Host     string
}

func (c VCSConfig) cli() string {
	if c.Provider == "gitlab" {
		return "glab"
	}
	return "gh"
}

// CheckVCSReadiness reports whether root is a usable git repo, has a
// remote, and whether the configured VCS CLI is installed AND
// authenticated. Ports check_vcs_readiness/probe_vcs_readiness, with one
// deliberate improvement: Python's `vcs.cli` check only verifies the
// binary is on PATH, not that it's authenticated — this checks both,
// using vcs.CheckAuth (internal/vcs, G4.2), since "gh/glab autenticado" is
// explicitly one of this package's checks.
//
// All three checks `skip` when neither WorktreeMode nor AutoPR is
// requested, and a missing prerequisite is `warn`, never `error`: orch
// degrades to the working subset rather than refusing to run — matching
// Python's reasoning in VcsReadiness's doc comment exactly.
func CheckVCSReadiness(root string, cfg VCSConfig) []Check {
	if !cfg.WorktreeMode && !cfg.AutoPR {
		detail := "dispatch.worktree_mode and vcs.auto_pr are both off"
		return []Check{
			{Name: "vcs.git_repo", Status: StatusSkip, Detail: detail},
			{Name: "vcs.remote", Status: StatusSkip, Detail: detail},
			{Name: "vcs.cli", Status: StatusSkip, Detail: detail},
		}
	}

	isGitRepo := gitOK(root, "rev-parse", "--is-inside-work-tree") == "true"
	var out []Check

	if isGitRepo {
		out = append(out, Check{Name: "vcs.git_repo", Status: StatusOK, Detail: root + " is a git work tree"})
	} else {
		out = append(out, Check{
			Name: "vcs.git_repo", Status: StatusWarn,
			Detail:      root + " is not a git repository — orch will run WITHOUT worktrees and WITHOUT PRs",
			Remediation: "git init",
		})
	}

	switch {
	case !cfg.AutoPR:
		out = append(out, Check{Name: "vcs.remote", Status: StatusSkip, Detail: "vcs.auto_pr is off — no remote needed"})
	case !isGitRepo:
		out = append(out, Check{Name: "vcs.remote", Status: StatusSkip, Detail: "not a git repository — cannot have a remote"})
	case gitOK(root, "remote") != "":
		out = append(out, Check{Name: "vcs.remote", Status: StatusOK, Detail: "git remote configured"})
	default:
		out = append(out, Check{
			Name: "vcs.remote", Status: StatusWarn,
			Detail:      "git repo has no remote — worktrees stay local, no push and no PRs",
			Remediation: "git remote add origin <url>",
		})
	}

	cli := cfg.cli()
	switch {
	case !cfg.AutoPR:
		out = append(out, Check{Name: "vcs.cli", Status: StatusSkip, Detail: "vcs.auto_pr is off — no VCS CLI needed"})
	default:
		if err := vcs.CheckAuth(cli, cfg.Host); err != nil {
			out = append(out, Check{
				Name: "vcs.cli", Status: StatusWarn,
				Detail:      cli + " is not ready (" + err.Error() + ") — orch will run with worktrees but WITHOUT PRs",
				Remediation: vcsCLIInstallHint(cli),
			})
		} else {
			out = append(out, Check{Name: "vcs.cli", Status: StatusOK, Detail: cli + " installed and authenticated"})
		}
	}

	return out
}

func vcsCLIInstallHint(cli string) string {
	if cli == "glab" {
		return "brew install glab  # or https://gitlab.com/gitlab-org/cli — then `glab auth login`"
	}
	return "brew install gh  # or https://cli.github.com — then `gh auth login`"
}

// gitOK runs a git command in root and returns trimmed stdout, or "" on any
// failure (non-zero exit, missing git, timeout). Never raises — ports
// _git_ok's "never raises" contract exactly.
func gitOK(root string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	// #nosec G204 -- args are this package's own fixed git subcommands.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
