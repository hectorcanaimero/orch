package cli

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/dashboard"
)

// newDashboardTokenCmd is G8.2 (F3.3)'s operator-facing surface: nobody
// opens sqlite3 to manage the row resolveTokenHash (dashboard.go) reads.
func newDashboardTokenCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage this project's stakeholder token",
	}
	cmd.AddCommand(newDashboardTokenRotateCmd(flags))
	cmd.AddCommand(newDashboardTokenShowCmd(flags))
	return cmd
}

func newDashboardTokenRotateCmd(flags *projectFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Generate a new stakeholder token and store its hash in the database",
		Long: "Generates a random token, stores only its SHA-256 hash in the\n" +
			"project's database, and prints the token once — it is not recoverable\n" +
			"afterward. Any URL or client using the previous token stops working\n" +
			"immediately, including on an already-running `orch dashboard`: the\n" +
			"database is checked fresh on every request, not just at startup.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}

			if !yes {
				if _, err := fmt.Fprint(cmd.OutOrStdout(),
					"This invalidates the current stakeholder token — anyone using a "+
						"shared URL loses access immediately. Continue? [y/N] "); err != nil {
					return err
				}
				reply, err := readLine(cmd.InOrStdin())
				if err != nil {
					return err
				}
				reply = strings.ToLower(strings.TrimSpace(reply))
				if reply != "y" && reply != "yes" {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Aborted. No changes written."); err != nil {
						return err
					}
					return withSilentExitCode(1)
				}
			}

			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()

			// A fresh project may have no `projects` row yet — nothing else
			// in the `orch dashboard` path requires one — so this needs its
			// own Bootstrap, the same one-time, idempotent seed `orch
			// status`/`orch run`/`orch task set` already do before their
			// first write. stakeholder_tokens' FK onto projects would
			// otherwise reject the very first rotation on a brand-new
			// project.
			if err := backend.Bootstrap(ctx, loadDAG(paths)); err != nil {
				return fmt.Errorf("bootstrap project: %w", err)
			}

			token, err := randomToken()
			if err != nil {
				return fmt.Errorf("generate token: %w", err)
			}
			if err := backend.SetStakeholderToken(ctx, dashboard.HashToken(token), time.Now()); err != nil {
				return fmt.Errorf("store token: %w", err)
			}

			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "\nNew stakeholder token (shown once — store it now):\n\n  %s\n\n", token); err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, "The database now has this project's token — `orch dashboard "+
				"--profile stakeholder` (no --token needed) picks it up, and a "+
				"shared URL carries it as ?token=<token above>.")
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

func newDashboardTokenShowCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show where this project's stakeholder token comes from, never the token itself",
		Long: "Reports whether a stakeholder token is configured, its source\n" +
			"(database or config.yaml/--token) and, for a database-sourced one, when\n" +
			"it was last rotated. Never prints the token — only its hash is ever\n" +
			"stored, so there is nothing to print even if this command wanted to.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()

			hash, rotatedAt, ok, err := backend.StakeholderToken(ctx)
			if err != nil {
				return fmt.Errorf("read token: %w", err)
			}
			out := cmd.OutOrStdout()
			if ok {
				_, err = fmt.Fprintf(out, "source: database\nrotated_at: %s\n", rotatedAt.UTC().Format(time.RFC3339))
				_ = hash // never printed — see the command's own doc comment.
				return err
			}
			if strings.TrimSpace(cfg.Dashboard.Token) != "" {
				_, err = fmt.Fprintln(out, "source: config.yaml\nrotated_at: n/a (not managed by `orch dashboard token rotate`)")
				return err
			}
			_, err = fmt.Fprintln(out, "source: none — no stakeholder token is configured")
			return err
		},
	}
	return cmd
}

// randomToken generates a 32-byte random token, base64url-encoded without
// padding — long enough that guessing it is not a threat model worth
// discussing, and URL-safe since a shared dashboard link carries it in a
// `?token=` query parameter.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
