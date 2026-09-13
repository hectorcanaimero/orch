package cli

import (
	"context"
	"errors"
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
			"Pages serves; `--to cloud` uploads them to your orch-cloud Worker (see `orch cloud`).\n\n" +
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

			// The cloud destination is checked before the database is
			// opened: a missing login or an id the Worker cannot take is a
			// sentence to print, not a reason to build a snapshot first.
			var cloud publish.CloudOptions
			if dest == "cloud" {
				cloud, err = prepareCloudPublish(paths, token, out)
				if err != nil {
					return err
				}
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
			if dest == "cloud" {
				dir = "" // each cloud publish exports into its own temporary directory
			} else if dest == "git" {
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
				if dest == "cloud" {
					return runOneCloudPublish(ctx, cmd, cloud, snap)
				}
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
		`Destination: "dir", "git" or "cloud" (default: publish.to in config.yaml, else "dir")`)
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

// prepareCloudPublish validates a `--to cloud` invocation before anything is
// built: the flags that have no meaning there, the project id the Worker
// will accept, and that there is a Worker to publish to at all.
func prepareCloudPublish(paths config.Paths, token, out string) (publish.CloudOptions, error) {
	if token != "" {
		// --token puts the export under an obscure path on a static host.
		// On orch-cloud the view token already IS that path, and it is a
		// real, rotatable secret the Worker checks; a second, weaker one on
		// top would only be a second thing to leak.
		return publish.CloudOptions{}, withExitCode(2, errors.New(
			"--token has no meaning with --to cloud: the Worker's view token is the link's secret — "+
				"`orch cloud rotate` replaces it"))
	}
	if out != "" {
		return publish.CloudOptions{}, withExitCode(2, errors.New(
			"--out has no meaning with --to cloud: the site is exported to a temporary directory and uploaded"))
	}
	id, err := publish.CloudProjectID(paths.ID)
	if err != nil {
		return publish.CloudOptions{}, withExitCode(2, err)
	}
	credPath, err := publish.DefaultCredentialsPath()
	if err != nil {
		return publish.CloudOptions{}, err
	}
	opts := publish.CloudOptions{CredentialsPath: credPath, ProjectID: id}
	cf, err := publish.LoadCredentials(credPath)
	if err != nil {
		return opts, err
	}
	if _, err := publish.ResolveCloudTarget(cf, id, os.Getenv); err != nil {
		return opts, withExitCode(2, err)
	}
	return opts, nil
}

// runOneCloudPublish exports into a fresh temporary directory and uploads it.
//
// Fresh every time, never reused: Export leaves unknown files alone, and a
// reused directory would carry an asset from an older bundle into the upload
// — the Worker serves exactly the file set it is sent.
func runOneCloudPublish(ctx context.Context, cmd *cobra.Command, opts publish.CloudOptions,
	snap snapshot.Snapshot) error {
	tmp, err := os.MkdirTemp("", "orch-cloud-")
	if err != nil {
		return fmt.Errorf("creating a temporary export directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if _, err := publish.Export(tmp, snap, publish.Options{}); err != nil {
		return err
	}
	res, err := publish.PublishToCloud(ctx, tmp, opts)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if res.Created {
		if _, err := fmt.Fprintf(out, "created project %s on orch-cloud; its tokens are saved in "+
			"~/.orch/credentials\n", opts.ProjectID); err != nil {
			return err
		}
	}
	if res.Upload.Changed {
		_, err = fmt.Fprintf(out, "published %s (version %d)\n", opts.ProjectID, res.Upload.Version)
	} else {
		_, err = fmt.Fprintf(out, "%s is already up to date (version %d); nothing uploaded\n",
			opts.ProjectID, res.Upload.Version)
	}
	if err != nil {
		return err
	}
	if res.ViewerURL != "" {
		_, err = fmt.Fprintf(out, "stakeholder link: %s\n", res.ViewerURL)
		return err
	}
	_, err = fmt.Fprintf(out, "stakeholder link: not known here — %s carries no view token; "+
		"the operator's `orch cloud status` shows it\n", publish.EnvCloudPublishToken)
	return err
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
	case "dir", "git", "cloud":
		return dest, nil
	default:
		return "", withExitCode(2, fmt.Errorf(
			`publish.to must be "dir", "git" or "cloud", got %q`, dest))
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
