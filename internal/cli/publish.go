package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/publish"
	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
	"github.com/hectorcanaimero/orch/internal/state"
)

// defaultPublishDir is where `--to dir` writes when nothing says otherwise.
const defaultPublishDir = "public"

// newPublishCmd is `orch publish` — the static stakeholder site.
//
// One verb, not two. The task this implements named `orch dashboard export`
// alongside it, and they would have been the same code behind two spellings:
// the thing being exported is the stakeholder snapshot, `publish` is what you
// do with it, and a second noun for it is a second place to document, to keep
// in step, and to be surprised by.
func newPublishCmd(flags *projectFlags) *cobra.Command {
	var (
		to       string
		out      string
		token    string
		branch   string
		watch    bool
		interval int
	)

	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Export the stakeholder snapshot as a static site",
		Long: "Write the client-facing page — progress, milestones, what is blocked " +
			"and why, and (only if\n`dashboard.show_spend_to_stakeholder` is on) what it " +
			"has cost — as a self-contained static site.\n\n" +
			"No login, no API, no server: the page and its data are files. " +
			"`--to dir` leaves them in a\ndirectory; `--to git` replaces a branch " +
			"(gh-pages by default) with them and pushes, which is what\nGitHub " +
			"Pages serves.\n\n" +
			"Every figure comes from the same builder the PDF and the dashboard's " +
			"stakeholder route use, so\nthe three cannot disagree.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			dest, err := resolveDestination(to, cfg.Publish)
			if err != nil {
				return err
			}
			every := resolveInterval(cmd, interval, cfg.Publish)
			gitBranch := branch
			if gitBranch == "" {
				gitBranch = cfg.Publish.GitBranch
			}

			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := closeDB(); cerr != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"[warn] closing the database: %v\n", cerr)
				}
			}()

			// The export directory. For `--to git` it is a scratch
			// directory: the artefact is the branch, and writing the site
			// into the project tree on the way there would leave a `public/`
			// nobody asked for next to the source.
			dir := out
			if dest == "git" {
				if dir == "" {
					tmp, err := os.MkdirTemp("", "orch-export-")
					if err != nil {
						return fmt.Errorf("creating a temporary export directory: %w", err)
					}
					defer func() { _ = os.RemoveAll(tmp) }()
					dir = tmp
				}
			} else if dir == "" {
				dir = filepath.Join(paths.Root, publishDirFromConfig(cfg.Publish))
			}

			build := func(ctx context.Context) (snapshot.Snapshot, error) {
				return buildPublishSnapshot(ctx, paths, cfg, backend, every, time.Now())
			}
			publisher := func(ctx context.Context, snap snapshot.Snapshot) error {
				return runOnePublish(ctx, cmd, dir, dest, token, gitBranch, paths, snap)
			}

			if !watch {
				snap, err := build(ctx)
				if err != nil {
					return err
				}
				return publisher(ctx, snap)
			}

			// Ctrl+C ends the watch, and it ends it cleanly: a publish is
			// files and a git push, and the half-written one is the case
			// worth avoiding. The context is cancelled, the loop returns nil,
			// and the deferred database close still runs.
			wctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()

			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"watching; re-publishing when the snapshot changes, every %s. Ctrl+C to stop.\n",
				every)
			return publish.Watch(wctx, every, build, publisher, cmd.ErrOrStderr())
		},
	}

	cmd.Flags().StringVar(&to, "to", "",
		`Destination: "dir" or "git" (default: publish.to in config.yaml, else "dir")`)
	cmd.Flags().StringVar(&out, "out", "",
		"Directory to write; default = <project-root>/"+defaultPublishDir+" (publish.dir). "+
			"With --to git the artefact is the branch, so this only says where the export is staged "+
			"(default: a temporary directory, removed afterwards)")
	cmd.Flags().StringVar(&token, "token", "",
		"Put the site in a subdirectory of this name. Obscurity, NOT authentication — see docs/CLI.md")
	cmd.Flags().StringVar(&branch, "branch", "",
		"Branch for --to git; default = publish.git_branch, else "+publish.DefaultGitBranch)
	cmd.Flags().BoolVar(&watch, "watch", false,
		"Keep running and re-publish whenever the snapshot's content changes")
	cmd.Flags().IntVar(&interval, "interval", 0,
		"Seconds between --watch checks; default = publish.interval_s, else 30")

	return cmd
}

// runOnePublish exports once and, for the git destination, pushes it.
func runOnePublish(ctx context.Context, cmd *cobra.Command, dir, dest, token, branch string,
	paths config.Paths, snap snapshot.Snapshot) error {
	res, err := publish.Export(dir, snap, publish.Options{Token: token})
	if err != nil {
		return err
	}

	if dest != "git" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", filepath.Join(res.Dir, "index.html"))
		return nil
	}

	git, err := publish.PublishToGit(ctx, dir, publish.GitOptions{
		RepoDir: paths.Root,
		Branch:  branch,
		Now:     time.Now(),
	})
	if err != nil {
		return err
	}
	if !git.Pushed {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"%s is already up to date; nothing pushed\n", git.Branch)
		return nil
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "pushed %s to %s (%s)\n",
		git.Commit[:min(7, len(git.Commit))], git.Branch, git.URL)
	return nil
}

// buildPublishSnapshot is buildStakeholderSnapshot with the refresh interval
// filled in.
//
// The gather is shared rather than copied: `orch report pdf` already had one,
// and the day a field is added to the snapshot's Input is the day two of them
// would quietly stop agreeing. The one thing publish knows that the PDF does
// not is how often the page will be refreshed, which is the field the document
// carries for the viewer's benefit.
func buildPublishSnapshot(ctx context.Context, paths config.Paths, cfg config.Config,
	backend state.Backend, every time.Duration, now time.Time) (snapshot.Snapshot, error) {
	snap, err := buildStakeholderSnapshot(ctx, paths, cfg, backend, now)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	snap.RefreshIntervalS = int(every.Seconds())
	return snap, nil
}

// resolveDestination applies the flag-beats-config rule the rest of the CLI
// uses: an unset flag never overwrites a configured value with its zero.
func resolveDestination(flag string, cfg config.Publish) (string, error) {
	dest := flag
	if dest == "" {
		dest = cfg.To
	}
	if dest == "" {
		dest = "dir"
	}
	switch dest {
	case "dir", "git":
		return dest, nil
	case "cloud":
		// Named in the config struct since F2, and deliberately not
		// implemented here: "cloud" is a destination with no provider, no
		// credentials story and no acceptance criterion. Refused by name so
		// the answer is this sentence rather than a confusing "unknown".
		return "", withExitCode(2, fmt.Errorf(
			`publish.to: "cloud" is not implemented — use "dir" (and upload it) or "git"`))
	default:
		return "", withExitCode(2, fmt.Errorf(
			`publish.to must be "dir" or "git", got %q`, dest))
	}
}

// resolveInterval reads --interval, then publish.interval_s, then the default.
func resolveInterval(cmd *cobra.Command, flag int, cfg config.Publish) time.Duration {
	if cmd.Flags().Changed("interval") && flag > 0 {
		return time.Duration(flag) * time.Second
	}
	if cfg.IntervalS > 0 {
		return time.Duration(cfg.IntervalS) * time.Second
	}
	return publish.DefaultIntervalS * time.Second
}

func publishDirFromConfig(cfg config.Publish) string {
	if cfg.Dir != "" {
		return cfg.Dir
	}
	return defaultPublishDir
}
