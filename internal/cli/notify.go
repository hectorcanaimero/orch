package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/dashboard"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/notify"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
	"github.com/hectorcanaimero/orch/internal/state"
)

// defaultTestMessage is Python's own `--message` default, word for word: an
// operator who sees it in a channel has to be able to tell it apart from a
// real alert, and "if you see this, the webhook works" is what does that.
const defaultTestMessage = "orch: notifier test — if you see this, the webhook works."

// newNotifyCmd ports `orch notify` (orchestrator/orch.py's
// `_run_notify_subcommand`) — the two side-channel helpers from Sprint G-6.
//
// A parent command with two verbs, matching Python's own subparser: `test`
// proves a webhook before a run depends on it, and `digest` prints the
// stakeholder summary an operator would cron.
//
// **orch has no daemon and never schedules anything.** `digest` prints and
// exits; `0 9 * * MON orch notify digest --send` is the operator's line to
// write, which is why the command is worth having at all.
func newNotifyCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Slack/Discord webhook helpers",
		Long: "Slack and Discord webhook helpers.\n\n" +
			"Both channels are off by default — see the `notifications` block " +
			"in docs/CONFIG.md. A webhook URL is a credential: orch never " +
			"writes one to a log line.",
	}
	cmd.AddCommand(newNotifyTestCmd(flags))
	cmd.AddCommand(newNotifyDigestCmd(flags))
	return cmd
}

// newNotifyTestCmd ports `orch notify test`.
//
// The one place in this package that reports success on **stderr**: Python
// prints "sent" there, and the exit code is the machine-readable half. A
// script asking "does my webhook work?" reads `$?`; a human reads the line.
func newNotifyTestCmd(flags *projectFlags) *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Send a canned message to every configured webhook",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			notifier := newNotifier(cfg)
			if !notifier.Enabled() {
				return withExitCode(1, fmt.Errorf(
					"no webhook configured — set notifications.slack_webhook or "+
						"notifications.discord_webhook in config.yaml"))
			}
			if !notifier.Test(cmd.Context(), message) {
				return withExitCode(1, fmt.Errorf("no channel accepted the message"))
			}
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "sent"); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&message, "message", defaultTestMessage, "Custom message body")
	return cmd
}

// newNotifyDigestCmd ports `orch notify digest`.
func newNotifyDigestCmd(flags *projectFlags) *cobra.Command {
	var (
		send     bool
		language string
	)
	cmd := &cobra.Command{
		Use:   "digest",
		Short: "Print (or send) the stakeholder digest",
		Long: "Print the stakeholder digest: the executive summary the " +
			"dashboard shows, followed by each milestone's progress and ETA.\n\n" +
			"Prints and exits — orch has no daemon, so scheduling it is yours:\n" +
			"  0 9 * * MON  orch notify digest --send",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if language != "" && language != "es" && language != "en" {
				return withExitCode(2, fmt.Errorf(
					"invalid --language %q (want es or en)", language))
			}
			ctx := cmd.Context()
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			// Reported, not dropped. A failed Close on a SQLite handle can
			// mean a WAL checkpoint did not land, which is worth a line on
			// stderr — and it must not change the exit code, because the
			// digest the operator asked for has already been printed by
			// then. `orch run` handles its backend the same way; the bare
			// `_ =` other read-only commands use is the weaker half of the
			// house pattern, not the one to copy.
			defer func() {
				cerr := closeDB()
				if cerr == nil {
					return
				}
				// Nothing useful to do if even the report cannot be
				// written, and the digest has already been delivered.
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"closing the state backend: %v\n", cerr)
			}()

			text, err := digestText(ctx, backend, paths, cfg, language)
			if err != nil {
				return err
			}
			// Printed before the POST, and reported even when the POST
			// fails: an operator who cron'd `--send` and lost the webhook
			// still wants the digest in the mail the cron daemon sends.
			if _, err := fmt.Fprint(cmd.OutOrStdout(), text); err != nil {
				return err
			}

			if !send {
				return nil
			}
			notifier := newNotifier(cfg)
			if !notifier.Enabled() {
				return withExitCode(1, fmt.Errorf(
					"--send requested but no webhook is configured"))
			}
			// Reuses the plain-text POST path, as Python does: a digest is a
			// message, and a second code path for "the same POST but longer"
			// would be a second place for the payload shape to drift.
			if !notifier.Test(ctx, text) {
				return withExitCode(1, fmt.Errorf(
					"digest printed, but no webhook accepted it"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&send, "send", false,
		"POST the digest to the configured webhooks as well as printing it")
	cmd.Flags().StringVar(&language, "language", "",
		"Override dashboard.summary_language for this run (es|en)")
	return cmd
}

func newNotifier(cfg config.Config) *notify.Notifier {
	return notify.New(
		cfg.Notifications.SlackWebhook,
		cfg.Notifications.DiscordWebhook,
		float64(cfg.Notifications.TimeoutS),
	)
}

// digestText builds the body.
//
// The summary is `snapshot.Build`'s own `executive_summary` text, reused
// verbatim. That is the whole point of the command sharing a package with the
// dashboard and `orch publish`: one function decides how a percentage, a
// blocked count and a spend figure are worded, and a digest that phrased them
// differently would have an operator reconciling two descriptions of the same
// project.
//
// # Which ETA this is, because there are two
//
// The summary's remaining-work figure is `eta_hours` — estimated hours left,
// scaled by how far the finished tasks ran over their estimates. It is NOT
// `sprint_eta`, which divides remaining *tasks* by tasks-per-day and produces
// a date; `/api/sprint` reports that one. On the same project the two can
// differ by a lot, and they are answers to different questions. Python's
// `orch notify digest` called `executive_summary` with the date; Go's summary
// is trimmed to the hours branch (the only one the stakeholder view uses), so
// the digest says "~12.5h remaining at current pace" where Python said a date.
//
// This is a deliberate divergence, not an oversight: one wording per number
// beats literal parity, and hours are the more honest claim — a date implies
// a precision the calculation does not have. Do not "unify" the two ETAs.
//
// The milestone ETAs below ARE dates, because a milestone's projection is the
// task-count kind (dashboard.MilestoneETADate) — the same figure
// `/api/milestones` renders.
func digestText(ctx context.Context, backend state.Backend, paths config.Paths, cfg config.Config, language string) (string, error) {
	lang := language
	if lang == "" {
		lang = cfg.Dashboard.SummaryLanguage
	}

	tasksFile, err := model.LoadTasksFile(paths.TasksJSON())
	if err != nil {
		return "", fmt.Errorf("read %s: %w", paths.TasksJSON(), err)
	}
	tasks, err := project.Hydrate(ctx, backend, tasksFile.Tasks)
	if err != nil {
		return "", fmt.Errorf("read runtime task status: %w", err)
	}
	events, err := backend.AllEvents(ctx, 0)
	if err != nil {
		return "", fmt.Errorf("read events: %w", err)
	}

	now := time.Now().UTC()
	spends, err := spendSince(ctx, backend, startOfDayUTC(now))
	if err != nil {
		return "", err
	}

	// ShowSpend follows Python's `total_spend = round(sum(...), 2) if spend
	// else None`, and the condition is the ROWS, not the sum: a day with no
	// spend rows gets no spend clause, while a day whose rows all cost zero
	// gets "$0.00" — Python's `if spend` tests a non-empty dict the same
	// way. A free run and a run that did not happen are different facts.
	//
	// It is not the stakeholder flag being ignored.
	// `show_spend_to_stakeholder` governs a page a client opens; this digest
	// goes to the webhook the operator configured in their own config.yaml,
	// and Python's version has always included the day's spend regardless of
	// that key. An operator who does not want spend in the channel does not
	// configure the channel.
	//
	// One inherited quirk, in the safe direction: the shared summary rounds
	// spend UP to the nearest $0.50 (`round_up_to_step`, so a stakeholder
	// page can never under-report). Python's digest printed the exact
	// `round(sum, 2)`. Over-reporting the operator's own cost by less than
	// fifty cents is a smaller price than a second wording of the same
	// figure — which is the thing this command exists to avoid.
	snap := snapshot.Build(snapshot.Input{
		Tasks:       tasks,
		Phases:      tasksFile.Phases,
		Events:      events,
		Spends:      spends,
		ProjectName: paths.ID,
		Language:    lang,
		ShowSpend:   len(spends) > 0,
		Now:         now,
	})

	milestones, err := digestMilestones(ctx, backend, tasks, now)
	if err != nil {
		return "", err
	}
	return notify.DigestText(snap.ExecutiveSummary.Text, milestones), nil
}

// digestMilestones reads the milestones table and projects each one's ETA.
//
// The milestones TABLE, not the snapshot's phase rows: Python's digest calls
// `backend.get_milestones()`, and a phase and a milestone are different
// groupings of the same tasks — a project can have five phases and one
// milestone called "MVP". `notify.Milestone`'s name-or-id fallback exists for
// this shape.
func digestMilestones(ctx context.Context, backend state.Backend, tasks []model.Task, now time.Time) ([]notify.Milestone, error) {
	rows, err := backend.Milestones(ctx)
	if err != nil {
		return nil, fmt.Errorf("read milestones: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	done7d, err := backend.CountDoneLastNDays(ctx, dashboard.VelocityWindowDays)
	if err != nil {
		return nil, fmt.Errorf("count recently finished tasks: %w", err)
	}
	velocity := float64(done7d) / float64(dashboard.VelocityWindowDays)
	today := now.Format("2006-01-02")

	out := make([]notify.Milestone, 0, len(rows))
	for _, m := range rows {
		out = append(out, notify.Milestone{
			Name:    m.Title,
			ID:      m.ID,
			Done:    m.Done,
			Total:   m.Total,
			ETADate: dashboard.MilestoneETADate(m.Total-m.Done, velocity, today, m.TargetDate),
		})
	}
	return out, nil
}

// startOfDayUTC is midnight of `now`'s UTC day — the window Python's digest
// sums spend over (`iter_today_entries`), which is today's cost and not the
// project's running total.
func startOfDayUTC(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// spendSince gathers every backend's rows newer than `since`.
//
// `Backend.SpendSince` takes one provider at a time — it is shaped for the
// budget gate's per-provider window, not for a project-wide report — so a
// total means one call per known backend. Same loop computeCostByTask runs,
// and the same reason it exists.
func spendSince(ctx context.Context, backend state.Backend, since time.Time) ([]state.Spend, error) {
	var out []state.Spend
	for _, b := range knownBackends {
		rows, err := backend.SpendSince(ctx, string(b), since)
		if err != nil {
			return nil, fmt.Errorf("read spend for %q: %w", b, err)
		}
		out = append(out, rows...)
	}
	return out, nil
}
