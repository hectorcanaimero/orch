package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/update"
)

// The update check's collaborators, swapped by the tests: none of them may
// reach GitHub or overwrite the test binary.
var (
	updateClient     = update.Client{}
	updateDownloader = update.Downloader{}
	updateCachePath  = update.CachePath
	upgradeTarget    = func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("finding this orch binary: %w", err)
		}
		return filepath.EvalSymlinks(exe)
	}
)

const (
	skipUpdateFlag  = "skip-update-check"
	installScript   = "curl -fsSL https://raw.githubusercontent.com/hectorcanaimero/orch/main/scripts/install.sh | sh"
	noticeTimeout   = 2 * time.Second
	gateTimeout     = 3 * time.Second
	upgradeAPIAfter = 10 * time.Second
)

// gatedCommands refuse to start while a critical release is out. They are
// the long-running ones: a run or a dashboard started on a version with a
// known critical bug keeps living with it for hours.
var gatedCommands = map[string]bool{"orch run": true, "orch dashboard": true}

func addSkipUpdateFlag(cmd *cobra.Command) {
	cmd.Flags().Bool(skipUpdateFlag, false,
		"Start even if a critical orch update is out (see `orch upgrade`)")
}

// criticalUpdateGate stops `orch run` and `orch dashboard` on a version older
// than a release marked critical. Anything that gets in the way of knowing —
// a dev build, no network, no home directory — lets the command start: the
// gate exists for a known problem, not to make orch depend on GitHub.
func criticalUpdateGate(cmd *cobra.Command, version string) error {
	if !gatedCommands[cmd.CommandPath()] || !update.IsRelease(version) || os.Getenv(update.DisableEnv) != "" {
		return nil
	}
	if skip, _ := cmd.Flags().GetBool(skipUpdateFlag); skip {
		return nil
	}
	path, err := updateCachePath()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), gateTimeout)
	defer cancel()
	releases, err := update.Check(ctx, updateClient, path, time.Now())
	if err != nil && len(releases) == 0 {
		// Nothing known about releases (offline, GitHub down): the gate is
		// for a known problem, so not knowing lets the command start.
		return nil
	}
	crit, ok := update.Critical(update.Newer(releases, version))
	if !ok {
		return nil
	}
	return withExitCode(1, fmt.Errorf(
		"orch %s is a critical update and this is %s: run `orch upgrade`, "+
			"or pass --%s to start anyway. Release notes: %s",
		crit.Tag, version, skipUpdateFlag, crit.URL))
}

// quietCommands never print the notice: their output is read by a program
// (the MCP client, the scripts agents shell into) or they are the upgrade.
var quietCommands = map[string]bool{
	"mcp": true, "upgrade": true, "task-status": true,
	"completion": true, "help": true, "__complete": true, "__completeNoDesc": true,
}

// wantsUpdateNotice decides whether this invocation may tell a person about
// a new release. Only a person at a terminal, on a release build, running a
// command whose output is not machine-read.
func wantsUpdateNotice(matched *cobra.Command, version string, stderrIsTerminal bool) bool {
	if matched == nil || !stderrIsTerminal || !update.IsRelease(version) {
		return false
	}
	if os.Getenv(update.DisableEnv) != "" || os.Getenv("CI") != "" {
		return false
	}
	for c := matched; c != nil; c = c.Parent() {
		if quietCommands[c.Name()] {
			return false
		}
	}
	if f := matched.Flags().Lookup("json"); f != nil && f.Changed {
		return false
	}
	return true
}

// printUpdateNotice writes the new-release notice to stderr, after the
// command's own output. Best effort: GitHub is asked at most once a day, for
// at most noticeTimeout, and a failure prints nothing.
func printUpdateNotice(matched *cobra.Command, version string, stderr io.Writer) {
	if !wantsUpdateNotice(matched, version, isTerminal(os.Stderr)) {
		return
	}
	path, err := updateCachePath()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
	defer cancel()
	releases, err := update.Check(ctx, updateClient, path, time.Now())
	if err != nil && len(releases) == 0 {
		// Best effort by design: a notice that cannot be checked is not shown,
		// and never turns a command's success into an error.
		return
	}
	if notice := update.Notice(version, update.Newer(releases, version)); notice != "" {
		_, _ = fmt.Fprint(stderr, "\n"+notice)
	}
}

func newUpgradeCmd(version string) *cobra.Command {
	var check, yes bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Install the latest orch release over this binary",
		Long: "Install the latest orch release over this binary.\n\n" +
			"Downloads the release for this OS and architecture from GitHub,\n" +
			"verifies it against the release's checksums.txt, and replaces the\n" +
			"running binary in place. A binary installed by Homebrew is left to\n" +
			"`brew upgrade`.\n\n" +
			"Other commands say when a new release is out, at most once a day and\n" +
			"only at a terminal. A release marked critical stops `orch run` and\n" +
			"`orch dashboard` from starting on older versions (--skip-update-check\n" +
			"overrides it). Set ORCH_NO_UPDATE_CHECK=1 to turn the check off.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), upgradeAPIAfter)
			defer cancel()
			out := cmd.OutOrStdout()

			releases, err := updateClient.Releases(ctx)
			if err != nil {
				return err
			}
			if len(releases) == 0 {
				return fmt.Errorf("no orch release is published for %s/%s yet", runtime.GOOS, runtime.GOARCH)
			}
			latest := releases[0]
			newer := update.Newer(releases, version)
			switch {
			case update.IsRelease(version) && len(newer) == 0:
				_, err := fmt.Fprintf(out, "orch %s is the latest release.\n", version)
				return err
			case !update.IsRelease(version):
				// A dev build has no place in the release order; offer the latest.
				newer = releases[:1]
			}

			if _, err := fmt.Fprintf(out, "orch %s → %s\n", version, latest.Tag); err != nil {
				return err
			}
			for _, r := range newer {
				for _, h := range r.Highlights {
					if _, err := fmt.Fprintf(out, "  • %s\n", h); err != nil {
						return err
					}
				}
			}
			if _, err := fmt.Fprintf(out, "  Release notes: %s\n", latest.URL); err != nil {
				return err
			}
			if check {
				return nil
			}

			target, err := upgradeTarget()
			if err != nil {
				return err
			}
			if manager := update.ManagedBy(target); manager != "" {
				return fmt.Errorf("%s is managed by a package manager; upgrade it with: %s", target, manager)
			}
			if !yes {
				if !isTerminal(cmd.InOrStdin()) {
					return withExitCode(2, errors.New("not a terminal: pass --yes to install without asking"))
				}
				if _, err := fmt.Fprintf(out, "Install %s over %s? [y/N] ", latest.Tag, target); err != nil {
					return err
				}
				answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return fmt.Errorf("reading the answer: %w", err)
				}
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					_, err := fmt.Fprintln(out, "Not installed.")
					return err
				}
			}

			binary, err := updateDownloader.Binary(cmd.Context(), latest.Tag, runtime.GOOS, runtime.GOARCH)
			if err != nil {
				return err
			}
			if err := update.Replace(target, binary); err != nil {
				if errors.Is(err, fs.ErrPermission) {
					return fmt.Errorf("%w\nRun orch upgrade as a user who can write %s, or reinstall with:\n  %s",
						err, filepath.Dir(target), installScript)
				}
				return err
			}
			_, err = fmt.Fprintf(out, "Installed orch %s at %s.\n", latest.Tag, target)
			return err
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Only say whether a newer release is out, and what it brings")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Install without asking")
	return cmd
}
