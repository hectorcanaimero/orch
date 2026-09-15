package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/ci"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "What orch does inside a project's CI",
	}
	cmd.AddCommand(newCIReviewCmd())
	return cmd
}

// postReviewComment is where --post goes. A variable so tests can stand in
// for gh.
var postReviewComment = vcs.UpsertComment

type ciReviewOptions struct {
	provider  string
	model     string
	checklist string
	base      string
	pr        int
	post      bool
	blocking  bool
	asJSON    bool
	maxDiff   int
	timeout   time.Duration
}

const ciReviewLong = `Review a pull request's diff with a coding-agent CLI, from inside the
project's CI job. orch builds a prompt from a checklist and the diff, runs the
provider read-only in an empty scratch directory, parses its JSON verdict and
prints it as markdown.

Providers, and the secret each CLI reads:
  claude    Claude             ANTHROPIC_API_KEY, or CLAUDE_CODE_OAUTH_TOKEN (subscription)
  gemini    Gemini             GEMINI_API_KEY
  codex     OpenAI             OPENAI_API_KEY
  opencode  OpenRouter         OPENROUTER_API_KEY, with --model openrouter/<vendor>/<model>

The diff is 'git diff --merge-base BASE'. BASE defaults to origin/$GITHUB_BASE_REF,
else origin/main; the base branch must be fetched (actions/checkout with
fetch-depth: 0). The checklist defaults to .github/orch-review.md, and a
built-in general one is used when that file does not exist.

--post creates or edits one comment on the pull request, the one marked
<!-- orch:ci-review -->. It needs gh, GH_TOKEN, GITHUB_REPOSITORY, and --pr or
GITHUB_EVENT_PATH.

Exit codes:
  0  the review ran; or it did not complete, without --blocking (a warning)
  1  with --blocking: a blocking finding, a request_changes verdict, or a
     review that did not complete (provider failure, unreadable answer)
  2  usage error`

func newCIReviewCmd() *cobra.Command {
	var o ciReviewOptions
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review a pull request with a coding-agent CLI, from inside CI",
		Long:  ciReviewLong,
		Args: func(c *cobra.Command, args []string) error {
			if err := cobra.NoArgs(c, args); err != nil {
				return withExitCode(2, err)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCIReview(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.provider, "provider", "", "claude, gemini, codex or opencode (required)")
	f.StringVar(&o.model, "model", "", "model id the provider's CLI takes (required)")
	f.StringVar(&o.checklist, "checklist", ".github/orch-review.md", "markdown checklist the review applies")
	f.StringVar(&o.base, "base", "", "ref to diff against (default origin/$GITHUB_BASE_REF, else origin/main)")
	f.IntVar(&o.pr, "pr", 0, "pull request number for --post (default: from GITHUB_EVENT_PATH)")
	f.BoolVar(&o.post, "post", false, "create or update the review comment on the pull request")
	f.BoolVar(&o.blocking, "blocking", false, "exit 1 on blocking findings, or when the review does not complete")
	f.BoolVar(&o.asJSON, "json", false, "print the parsed verdict as JSON instead of markdown")
	f.IntVar(&o.maxDiff, "max-diff-bytes", 200_000, "cut the diff, file by file, beyond this size")
	f.DurationVar(&o.timeout, "timeout", 10*time.Minute, "how long the provider may take")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return withExitCode(2, err) })
	return cmd
}

func runCIReview(cmd *cobra.Command, o ciReviewOptions) error {
	backend := model.Backend(o.provider)
	if !slices.Contains(ci.ReviewBackends, backend) {
		return withExitCode(2, fmt.Errorf("--provider %q: expected claude, gemini, codex or opencode", o.provider))
	}
	if o.model == "" {
		return withExitCode(2, errors.New("--model is required"))
	}
	if o.maxDiff <= 0 || o.timeout <= 0 {
		return withExitCode(2, errors.New("--max-diff-bytes and --timeout must be positive"))
	}

	// Everything --post needs is checked before the provider is paid for.
	var repo string
	pr := o.pr
	if o.post {
		repo = os.Getenv("GITHUB_REPOSITORY")
		if repo == "" {
			return withExitCode(2, errors.New("--post needs GITHUB_REPOSITORY (owner/name)"))
		}
		if pr == 0 {
			event := os.Getenv("GITHUB_EVENT_PATH")
			if event == "" {
				return withExitCode(2, errors.New("--post needs --pr or GITHUB_EVENT_PATH"))
			}
			n, err := ci.PRFromEvent(event)
			if err != nil {
				return withExitCode(2, err)
			}
			pr = n
		}
	}

	// A review that does not complete fails the job only with --blocking.
	incomplete := func(err error) error {
		if ci.ExitCode(ci.Verdict{}, err, o.blocking) != 0 {
			return withExitCode(1, fmt.Errorf("orch ci review: %w", err))
		}
		// Best effort: a warning that cannot be written must not turn an
		// advisory review into a failed job.
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: orch ci review did not complete (not failing the job without --blocking): %v\n", err)
		return nil
	}

	base := o.base
	if base == "" {
		base = "origin/main"
		if ref := os.Getenv("GITHUB_BASE_REF"); ref != "" {
			base = "origin/" + ref
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return incomplete(fmt.Errorf("finding the working directory: %w", err))
	}
	diff, err := ci.Diff(cmd.Context(), wd, base)
	if err != nil {
		return incomplete(err)
	}
	if strings.TrimSpace(diff) == "" {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Nothing to review: no changes against %s.\n", base); err != nil {
			return fmt.Errorf("writing the review: %w", err)
		}
		return nil
	}

	var notes []string
	checklist, err := os.ReadFile(o.checklist) // #nosec G304 -- the operator's own checklist path
	switch {
	case errors.Is(err, fs.ErrNotExist):
		checklist = []byte(ci.BuiltinChecklist)
		notes = append(notes, fmt.Sprintf("No checklist at %s: the built-in general checklist was used.", o.checklist))
	case err != nil:
		return incomplete(fmt.Errorf("reading the checklist: %w", err))
	}
	diff, cut := ci.TruncateDiff(diff, o.maxDiff)
	if len(cut) > 0 {
		notes = append(notes, fmt.Sprintf("The diff is over %d bytes and was cut short in: %s.", o.maxDiff, strings.Join(cut, ", ")))
	}

	answer, err := ci.Ask(cmd.Context(), backend, o.model, ci.BuildPrompt(string(checklist), diff, cut), o.timeout)
	if err != nil {
		return incomplete(err)
	}
	verdict, err := ci.ParseVerdict(answer)
	if err != nil {
		return incomplete(fmt.Errorf("unreadable answer from %s: %w", backend, err))
	}

	markdown := verdict.Markdown(o.provider+", "+o.model, notes)
	if o.asJSON {
		for _, n := range notes {
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "note: "+n); err != nil {
				return fmt.Errorf("writing the review: %w", err)
			}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(verdict); err != nil {
			return fmt.Errorf("writing the verdict: %w", err)
		}
	} else if _, err := fmt.Fprint(cmd.OutOrStdout(), markdown); err != nil {
		return fmt.Errorf("writing the review: %w", err)
	}

	if o.post {
		url, created, err := postReviewComment(repo, pr, ci.CommentMarker, ci.CommentBody(markdown))
		if err != nil {
			return incomplete(err)
		}
		action := "Updated"
		if created {
			action = "Posted"
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s the review comment: %s\n", action, url); err != nil {
			return fmt.Errorf("writing the review: %w", err)
		}
	}

	if ci.ExitCode(verdict, nil, o.blocking) != 0 {
		return withExitCode(1, fmt.Errorf("orch ci review: %s with --blocking", verdict.Verdict))
	}
	return nil
}
