package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/publish"
	"github.com/hectorcanaimero/orch/internal/publish/cloudworker"
)

// newCloudCmd is `orch cloud` — the operator's side of an orch-cloud Worker
// (G8.1). The Worker is self-hosted: each operator deploys their own to
// their own Cloudflare account (docs/CLOUD.md), and these commands hold the
// tokens for it in ~/.orch/credentials. The upload itself is
// `orch publish --to cloud`.
func newCloudCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud",
		Short: "Connect to your orch-cloud Worker and manage its tokens",
		Long: "orch-cloud is a Cloudflare Worker you deploy to your own account; it serves the\n" +
			"stakeholder page at https://<worker>/v/<view token>/. These commands store its URL and\n" +
			"tokens in ~/.orch/credentials (mode 0600). `orch publish --to cloud` uploads the page.\n\n" +
			"`orch cloud setup` deploys the Worker and logs in, in one step; see docs/CLOUD.md.",
	}
	cmd.AddCommand(newCloudSetupCmd())
	cmd.AddCommand(newCloudLoginCmd())
	cmd.AddCommand(newCloudRotateCmd(flags))
	cmd.AddCommand(newCloudLogoutCmd())
	cmd.AddCommand(newCloudStatusCmd(flags))
	return cmd
}

func newCloudSetupCmd() *cobra.Command {
	var name string
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Deploy your orch-cloud Worker to Cloudflare and log in to it, in one step",
		Long: "Takes this machine from no Cloudflare session to a working `orch publish --to cloud`:\n\n" +
			"  1. finds npx (wrangler, Cloudflare's CLI, runs on Node.js 20+)\n" +
			"  2. checks for a Cloudflare session and, if there is none, runs wrangler's device\n" +
			"     login — open the URL it prints, enter the code, approve\n" +
			"  3. deploys the orch-cloud Worker built into this orch to your account\n" +
			"  4. generates an admin token and stores it as the Worker's secret\n" +
			"  5. waits until the Worker accepts it\n" +
			"  6. saves the Worker URL and the admin token to ~/.orch/credentials (mode 0600)\n\n" +
			"The admin token is never printed or passed as an argument. Run it again to redeploy\n" +
			"the Worker; that issues a new admin token and keeps the project tokens stored here valid.\n" +
			"--dry-run prints the plan and runs nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if dryRun {
				plan, err := publish.CloudSetupPlan(name)
				if err != nil {
					return withExitCode(2, err)
				}
				_, err = fmt.Fprint(out, plan)
				return err
			}
			credPath, err := publish.DefaultCredentialsPath()
			if err != nil {
				return err
			}
			notice, err := publish.CloudSetupRerunNotice(credPath, name)
			if err != nil {
				return err
			}
			if notice != "" && !yes {
				if _, err := fmt.Fprintf(out, "%s\nContinue? [y/N] ", notice); err != nil {
					return err
				}
				reply, err := readLine(cmd.InOrStdin())
				if err != nil {
					return err
				}
				reply = strings.ToLower(strings.TrimSpace(reply))
				if reply != "y" && reply != "yes" {
					if _, err := fmt.Fprintln(out, "Aborted. No changes made."); err != nil {
						return err
					}
					return withSilentExitCode(1)
				}
			}

			res, err := publish.SetupCloud(cmd.Context(), publish.CloudSetupOptions{
				CredentialsPath: credPath,
				Name:            name,
				Stdin:           cmd.InOrStdin(),
				Stdout:          out,
				Stderr:          cmd.ErrOrStderr(),
			})
			var missing *publish.CloudSetupMissingNPX
			if errors.As(err, &missing) {
				return withExitCode(2, err)
			}
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "\norch-cloud is ready at %s\n", res.URL); err != nil {
				return err
			}
			if res.DroppedProjects > 0 {
				if _, err := fmt.Fprintf(out, "dropped the stored tokens of %d project(s): they belonged to the "+
					"previous Worker\n", res.DroppedProjects); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(out, "Next, in a project: orch publish --to cloud")
			return err
		},
	}
	cmd.Flags().StringVar(&name, "name", cloudworker.DefaultName,
		"Worker name; the Worker is served at https://<name>.<your subdomain>.workers.dev")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation when this machine is already set up")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the steps and run nothing")
	return cmd
}

func newCloudLoginCmd() *cobra.Command {
	var url string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Verify the Worker's admin token and store it with the Worker's URL",
		Long: "Reads the admin token (the Worker's ADMIN_TOKEN secret) from stdin, checks it against\n" +
			"the Worker, and only then saves both to ~/.orch/credentials.\n\n" +
			"The token must be piped, never typed: an interactive prompt would echo it to the\n" +
			"terminal. For example:\n\n" +
			"  printf %s \"$ADMIN_TOKEN\" | orch cloud login --url https://orch-cloud.<you>.workers.dev",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if url == "" {
				return withExitCode(2, errors.New("--url is required: the Worker's address, as printed by `wrangler deploy`"))
			}
			in := cmd.InOrStdin()
			if isTerminal(in) {
				return withExitCode(2, errors.New("the admin token is read from stdin and stdin is a terminal — "+
					"pipe it instead so it is never echoed: printf %s \"$ADMIN_TOKEN\" | orch cloud login --url "+url))
			}
			// A token is one line; a limit keeps a mistaken `< bigfile`
			// from being read whole.
			raw, err := io.ReadAll(io.LimitReader(in, 4096))
			if err != nil {
				return fmt.Errorf("reading the admin token from stdin: %w", err)
			}
			credPath, err := publish.DefaultCredentialsPath()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			dropped, err := publish.CloudLogin(ctx, credPath, url, string(raw))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "logged in to %s; credentials saved to %s\n",
				strings.TrimRight(url, "/"), credPath); err != nil {
				return err
			}
			if dropped > 0 {
				_, err = fmt.Fprintf(out, "dropped the stored tokens of %d project(s): they belonged to the "+
					"previous Worker\n", dropped)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "The orch-cloud Worker's base URL (https://…)")
	return cmd
}

func newCloudRotateCmd(flags *projectFlags) *cobra.Command {
	var publishTok, yes bool
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Issue a new viewer link for this project (or, with --publish, a new publish token)",
		Long: "Without flags: issues a new view token. The stakeholder link changes and the old one\n" +
			"stops working immediately; the new link is printed.\n\n" +
			"With --publish: re-issues this project's publish token using the admin token and stores\n" +
			"it — for a CI token that leaked, or a machine that lost its credentials file. The\n" +
			"viewer link is unchanged.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := cloudProjectIDFromFlags(flags)
			if err != nil {
				return err
			}
			credPath, err := publish.DefaultCredentialsPath()
			if err != nil {
				return err
			}
			opts := publish.CloudOptions{CredentialsPath: credPath, ProjectID: id}
			out := cmd.OutOrStdout()
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			if publishTok {
				if err := publish.RotateCloudPublishToken(ctx, opts); err != nil {
					return err
				}
				_, err = fmt.Fprintf(out, "issued and stored a new publish token for %s; the old one no longer works\n", id)
				return err
			}

			if !yes {
				if _, err := fmt.Fprintf(out, "This replaces the stakeholder link for %s — anyone using the "+
					"current link loses access immediately. Continue? [y/N] ", id); err != nil {
					return err
				}
				reply, err := readLine(cmd.InOrStdin())
				if err != nil {
					return err
				}
				reply = strings.ToLower(strings.TrimSpace(reply))
				if reply != "y" && reply != "yes" {
					if _, err := fmt.Fprintln(out, "Aborted. No changes made."); err != nil {
						return err
					}
					return withSilentExitCode(1)
				}
			}
			viewer, err := publish.RotateCloudViewToken(ctx, opts)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "new stakeholder link for %s:\n\n  %s\n", id, viewer)
			return err
		},
	}
	cmd.Flags().BoolVar(&publishTok, "publish", false, "Re-issue the publish token instead of the viewer link")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

func newCloudLogoutCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove the orch-cloud URL and every token from ~/.orch/credentials",
		Long: "Deletes the cloud block from ~/.orch/credentials. The Worker is not touched: published\n" +
			"pages keep serving. The project publish tokens stored here cannot be recovered afterwards —\n" +
			"`orch cloud rotate --publish` re-issues them after the next login.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if !yes {
				if _, err := fmt.Fprint(out, "This removes the stored admin and project tokens from "+
					"~/.orch/credentials. Continue? [y/N] "); err != nil {
					return err
				}
				reply, err := readLine(cmd.InOrStdin())
				if err != nil {
					return err
				}
				reply = strings.ToLower(strings.TrimSpace(reply))
				if reply != "y" && reply != "yes" {
					if _, err := fmt.Fprintln(out, "Aborted. No changes made."); err != nil {
						return err
					}
					return withSilentExitCode(1)
				}
			}
			credPath, err := publish.DefaultCredentialsPath()
			if err != nil {
				return err
			}
			had, err := publish.CloudLogout(credPath)
			if err != nil {
				return err
			}
			if !had {
				_, err = fmt.Fprintln(out, "not logged in; nothing to remove")
				return err
			}
			_, err = fmt.Fprintln(out, "logged out; the cloud credentials were removed")
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

func newCloudStatusCmd(flags *projectFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the Worker, whether it answers, and which projects have tokens here — never the tokens",
		Long: "Prints the stored Worker URL, whether it is reachable and which contract version it\n" +
			"speaks, whether an admin token is stored, and for each project whether a publish and a\n" +
			"view token are stored. Tokens are never printed; the one exception is the current\n" +
			"project's stakeholder link, which is the thing you share.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			credPath, err := publish.DefaultCredentialsPath()
			if err != nil {
				return err
			}
			cf, err := publish.LoadCredentials(credPath)
			if err != nil {
				return err
			}
			if cf.Cloud == nil || cf.Cloud.URL == "" {
				_, err = fmt.Fprintln(out, "not logged in — run `orch cloud login --url <worker URL>`")
				return err
			}
			client, err := publish.NewCloudClient(cf.Cloud.URL)
			if err != nil {
				return err
			}

			if _, err := fmt.Fprintf(out, "worker:      %s\n", client.URL()); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			var reach string
			if h, herr := client.Health(ctx); herr != nil {
				reach = "NOT reachable: " + herr.Error()
			} else {
				reach = fmt.Sprintf("reachable (orch-cloud %s, API %d)", h.Version, h.API)
			}
			if _, err := fmt.Fprintf(out, "status:      %s\nadmin token: %s\n", reach,
				storedWord(cf.Cloud.AdminToken)); err != nil {
				return err
			}

			ids := make([]string, 0, len(cf.Cloud.Projects))
			for id := range cf.Cloud.Projects {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) == 0 {
				if _, err := fmt.Fprintln(out, "projects:    none yet — `orch publish --to cloud` creates one"); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(out, "projects:"); err != nil {
					return err
				}
				for _, id := range ids {
					p := cf.Cloud.Projects[id]
					if _, err := fmt.Fprintf(out, "  %-24s publish token %s, view token %s\n", id,
						storedWord(p.PublishToken), storedWord(p.ViewToken)); err != nil {
						return err
					}
				}
			}

			// The current project is optional: status is useful outside one.
			if id, err := cloudProjectIDFromFlags(flags); err == nil {
				if p := cf.Cloud.Projects[id]; p.ViewToken != "" {
					_, err = fmt.Fprintf(out, "\nstakeholder link for %s:\n  %s\n", id, client.ViewerURL(p.ViewToken))
					return err
				}
			}
			return nil
		},
	}
}

func storedWord(s string) string {
	if s == "" {
		return "not stored"
	}
	return "stored"
}

// cloudProjectIDFromFlags resolves the current project and maps its id onto
// the contract's shape.
func cloudProjectIDFromFlags(flags *projectFlags) (string, error) {
	paths, err := resolveAndValidate(flags)
	if err != nil {
		return "", err
	}
	return publish.CloudProjectID(paths.ID)
}

// isTerminal reports whether r is a character device — a terminal a person
// would type into. Anything that is not an *os.File (a test's buffer, a
// pipe cobra was handed) is not.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
