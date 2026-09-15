package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/ci"
	"github.com/hectorcanaimero/orch/internal/config"
)

// workflowPath is where the pipeline goes, the file orch init also writes.
var workflowPath = filepath.Join(".github", "workflows", "orch-ci.yml")

func newCICmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Set up the repository's CI pipeline, and review pull requests inside it",
		Long: "Detect what the repository is built with and set up the GitHub Actions\n" +
			"pipeline orch's auto-PR loop waits on.\n\n" +
			"Detection reads files and runs nothing: lockfiles name the package manager,\n" +
			"scripts and tool config sections name the checks. A check the repository does\n" +
			"not have is left out rather than guessed.\n\n" +
			"`orch ci review` runs inside that pipeline: an AI review of the pull\n" +
			"request with a coding-agent CLI (see its --help).",
	}
	cmd.AddCommand(newCIDetectCmd(flags), newCISetupCmd(flags), newCIReviewCmd())
	return cmd
}

// ciRoot is the repository to read: --project-root, else ORCH_PROJECT_ROOT,
// else the working directory. It needs no orch project: CI can be set up
// before `orch init`.
func ciRoot(f *projectFlags) (string, error) {
	root := f.root
	if root == "" {
		root = os.Getenv("ORCH_PROJECT_ROOT")
	}
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("finding the working directory: %w", err)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", root, err)
	}
	return abs, nil
}

type ciPackageJSON struct {
	Dir       string `json:"dir"`
	Language  string `json:"language"`
	Manager   string `json:"manager"`
	Version   string `json:"version,omitempty"`
	Install   string `json:"install,omitempty"`
	Lint      string `json:"lint,omitempty"`
	Typecheck string `json:"typecheck,omitempty"`
	Test      string `json:"test,omitempty"`
	Build     string `json:"build,omitempty"`
}

func newCIDetectCmd(flags *projectFlags) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Show what the repository is built with and which checks it has",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := ciRoot(flags)
			if err != nil {
				return err
			}
			st, err := ci.Detect(root)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				pkgs := []ciPackageJSON{}
				for _, p := range st.Packages {
					pkgs = append(pkgs, ciPackageJSON{
						Dir: p.Dir, Language: string(p.Language), Manager: p.Manager, Version: p.Version,
						Install: p.Install, Lint: p.Lint, Typecheck: p.Typecheck, Test: p.Test, Build: p.Build,
					})
				}
				workflows := st.Workflows
				if workflows == nil {
					workflows = []string{}
				}
				return printCompactJSON(out, map[string]any{"root": root, "packages": pkgs, "workflows": workflows})
			}
			return printStack(out, root, st)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the detection as JSON")
	return cmd
}

func printStack(w io.Writer, root string, st ci.Stack) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", root)
	if len(st.Packages) == 0 {
		b.WriteString("  no Go, Node, Python, Rust or Makefile project found\n")
	}
	for _, p := range st.Packages {
		version := ""
		if p.Version != "" {
			version = " " + p.Version
		}
		fmt.Fprintf(&b, "  %s  %s (%s%s)\n", p.Dir, p.Language, p.Manager, version)
		for _, c := range [][2]string{{"install", p.Install}, {"lint", p.Lint}, {"typecheck", p.Typecheck}, {"test", p.Test}, {"build", p.Build}} {
			value := c[1]
			if value == "" {
				value = "—"
			}
			fmt.Fprintf(&b, "    %-10s %s\n", c[0], value)
		}
	}
	if len(st.Workflows) > 0 {
		fmt.Fprintf(&b, "  existing workflows: %s\n", strings.Join(st.Workflows, ", "))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func newCISetupCmd(flags *projectFlags) *cobra.Command {
	var (
		dryRun, yes, force               bool
		noLint, noTypecheck, noTest, noB bool
		base                             string
		reviewProvider, reviewModel      string
		reviewBlocking                   bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Propose a CI pipeline from the repository's files and write it",
		Long: "Propose a CI pipeline from the repository's files and write it to\n" +
			".github/workflows/orch-ci.yml.\n\n" +
			"One job per package (the root, a web app in a subdirectory, a service in\n" +
			"apps/ or services/), each running only the checks the package has: install,\n" +
			"lint, typecheck, test, build. At a terminal it asks which checks to run and\n" +
			"shows the workflow before writing it. An existing orch-ci.yml is only\n" +
			"replaced with --force (or a yes at the prompt).\n\n" +
			"It can add an AI review of every pull request (`orch ci review`) with Claude,\n" +
			"Gemini, OpenAI or OpenRouter and the model you choose: at the prompt, or with\n" +
			"--review, --review-model and --review-blocking. The review runs after the\n" +
			"checks pass and comments on the PR; it writes " + ci.ChecklistPath + " when\n" +
			"the repository has none, and names the secret to set.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := ciRoot(flags)
			if err != nil {
				return err
			}
			st, err := ci.Detect(root)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !st.HasChecks() {
				if err := printStack(out, root, st); err != nil {
					return err
				}
				return withExitCode(1, errors.New("found nothing to check: no lint, typecheck, test or build script, tool config or Makefile target"))
			}

			checks := ci.Checks{Lint: !noLint, Typecheck: !noTypecheck, Test: !noTest, Build: !noB}
			interactive := !dryRun && !yes && isTerminal(cmd.InOrStdin())
			in := bufio.NewReader(cmd.InOrStdin())
			if interactive {
				if err := printStack(out, root, st); err != nil {
					return err
				}
				if checks, err = askChecks(in, out, st, checks); err != nil {
					return err
				}
				if !cmd.Flags().Changed("review") {
					if reviewProvider, reviewModel, reviewBlocking, err = askReview(in, out); err != nil {
						return err
					}
				}
			}
			review, err := ci.NewReview(reviewProvider, reviewModel, reviewBlocking)
			if err != nil {
				return withExitCode(2, err)
			}

			if base == "" {
				base = projectBaseBranch(root, flags)
			}
			body, err := ci.Render(ci.Jobs(st, checks), ci.Options{BaseBranch: base, Checks: checks, Review: review})
			if err != nil {
				return withExitCode(1, err)
			}
			if dryRun {
				_, err := out.Write(body)
				return err
			}

			target := filepath.Join(root, workflowPath)
			current, err := os.ReadFile(target) // #nosec G304 -- the workflow this command manages
			switch {
			case err == nil && bytes.Equal(current, body):
				if _, err := fmt.Fprintf(out, "%s is already up to date.\n", workflowPath); err != nil {
					return err
				}
				return finishReviewSetup(out, root, st, review)
			case err == nil && !force:
				if !interactive {
					return withExitCode(1, fmt.Errorf("%s already exists and differs; pass --force to replace it (see the proposal with --dry-run)", workflowPath))
				}
				if _, err := fmt.Fprintf(out, "\n%s\n", body); err != nil {
					return err
				}
				ok, err := askYesNo(in, out, fmt.Sprintf("%s already exists. Replace it?", workflowPath), false)
				if err != nil || !ok {
					return err
				}
			case err != nil && !errors.Is(err, fs.ErrNotExist):
				return fmt.Errorf("reading %s: %w", target, err)
			case !yes && !interactive && !force:
				return withExitCode(2, errors.New("not a terminal: pass --yes to write the workflow, or --dry-run to see it"))
			case interactive:
				if _, err := fmt.Fprintf(out, "\n%s\n", body); err != nil {
					return err
				}
				ok, err := askYesNo(in, out, "Write "+workflowPath+"?", true)
				if err != nil || !ok {
					return err
				}
			}

			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return fmt.Errorf("creating %s: %w", filepath.Dir(target), err)
			}
			if err := os.WriteFile(target, body, 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", target, err)
			}
			if _, err := fmt.Fprintf(out, "Wrote %s. Commit it: pull requests run it from then on, and orch's auto-PR loop waits on its checks.\n", workflowPath); err != nil {
				return err
			}
			return finishReviewSetup(out, root, st, review)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&dryRun, "dry-run", false, "Print the proposed workflow and write nothing")
	f.BoolVarP(&yes, "yes", "y", false, "Write without asking")
	f.BoolVar(&force, "force", false, "Replace an existing orch-ci.yml")
	f.BoolVar(&noLint, "no-lint", false, "Leave lint out")
	f.BoolVar(&noTypecheck, "no-typecheck", false, "Leave typecheck out")
	f.BoolVar(&noTest, "no-test", false, "Leave tests out")
	f.BoolVar(&noB, "no-build", false, "Leave build out")
	f.StringVar(&base, "base", "", "Branch whose pushes are checked too (default: dispatch.base_branch, else main)")
	f.StringVar(&reviewProvider, "review", "", "Add an AI review of every PR: "+strings.Join(ci.ReviewProviderNames(), ", ")+", or none")
	f.StringVar(&reviewModel, "review-model", "", "Model for the review (defaults per provider; OpenRouter needs openrouter/<vendor>/<model>)")
	f.BoolVar(&reviewBlocking, "review-blocking", false, "Fail the PR's checks when the review finds a blocking problem")
	return cmd
}

// finishReviewSetup writes the review checklist when the repository has none
// and says which secret the review needs. Nothing without a review.
func finishReviewSetup(out io.Writer, root string, st ci.Stack, review *ci.Review) error {
	if review == nil {
		return nil
	}
	path := filepath.Join(root, filepath.FromSlash(ci.ChecklistPath))
	switch _, err := os.Stat(path); {
	case err == nil:
		if _, err := fmt.Fprintf(out, "Kept the existing %s.\n", ci.ChecklistPath); err != nil {
			return err
		}
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(ci.ReviewChecklist(st)), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		if _, err := fmt.Fprintf(out, "Wrote %s: the rules the review applies. Edit it to your project's.\n", ci.ChecklistPath); err != nil {
			return err
		}
	default:
		return fmt.Errorf("checking %s: %w", path, err)
	}
	secrets := ci.ReviewProviders[review.Provider].Secrets
	hint := "gh secret set " + secrets[0]
	if len(secrets) > 1 {
		hint = "gh secret set " + strings.Join(secrets, "   or   gh secret set ")
	}
	_, err := fmt.Fprintf(out, "The review needs a repository secret: %s\n", hint)
	return err
}

// askReview asks whether to add an AI review, with which provider and model,
// and whether it blocks. An empty answer is no review.
func askReview(in *bufio.Reader, out io.Writer) (provider, model string, blocking bool, err error) {
	names := strings.Join(ci.ReviewProviderNames(), "/")
	for {
		provider, err = askLine(in, out, fmt.Sprintf("Add an AI review of every pull request? [none/%s]", names), "none")
		if err != nil {
			return "", "", false, err
		}
		provider = strings.ToLower(provider)
		if _, ok := ci.ReviewProviders[provider]; ok || provider == "none" {
			break
		}
		if _, err := fmt.Fprintf(out, "%q is not one of none/%s.\n", provider, names); err != nil {
			return "", "", false, err
		}
	}
	if provider == "none" {
		return "", "", false, nil
	}
	p := ci.ReviewProviders[provider]
	hint := p.DefaultModel
	if hint == "" {
		hint = p.ModelPrefix + "<vendor>/<model>"
	}
	if model, err = askLine(in, out, "Model", p.DefaultModel); err != nil {
		return "", "", false, err
	}
	if model == "" {
		if _, err := fmt.Fprintf(out, "The %s review needs a model (%s); no review added.\n", provider, hint); err != nil {
			return "", "", false, err
		}
		return "", "", false, nil
	}
	blocking, err = askYesNo(in, out, "Block the merge when the review finds a blocking problem?", false)
	return provider, model, blocking, err
}

// askLine reads one answer; an empty line takes the default, shown in
// parentheses when there is one.
func askLine(in *bufio.Reader, out io.Writer, question, def string) (string, error) {
	prompt := question
	if def != "" {
		prompt += " (" + def + ")"
	}
	if _, err := fmt.Fprintf(out, "%s: ", prompt); err != nil {
		return "", err
	}
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading the answer: %w", err)
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer, nil
	}
	return def, nil
}

// projectBaseBranch is the orch project's dispatch.base_branch when the
// repository is one, "" otherwise.
func projectBaseBranch(root string, flags *projectFlags) string {
	paths, err := config.ResolvePaths(root, flags.id, flags.configPath)
	if err != nil {
		return ""
	}
	res, err := config.Load(paths.ConfigYAML, paths.Root)
	if err != nil {
		return ""
	}
	return res.Config.Dispatch.BaseBranch
}

// askChecks asks, for each kind of check the repository has, whether to run
// it. Kinds it does not have are not asked about.
func askChecks(in *bufio.Reader, out io.Writer, st ci.Stack, checks ci.Checks) (ci.Checks, error) {
	has := func(get func(ci.Package) string) []string {
		var cmds []string
		for _, p := range st.Packages {
			if c := get(p); c != "" {
				cmds = append(cmds, c)
			}
		}
		return cmds
	}
	for _, q := range []struct {
		label string
		cmds  []string
		on    *bool
	}{
		{"lint", has(func(p ci.Package) string { return p.Lint }), &checks.Lint},
		{"typecheck", has(func(p ci.Package) string { return p.Typecheck }), &checks.Typecheck},
		{"tests", has(func(p ci.Package) string { return p.Test }), &checks.Test},
		{"build", has(func(p ci.Package) string { return p.Build }), &checks.Build},
	} {
		if len(q.cmds) == 0 || !*q.on {
			continue
		}
		ok, err := askYesNo(in, out, fmt.Sprintf("Run %s (%s)?", q.label, strings.Join(q.cmds, "; ")), true)
		if err != nil {
			return checks, err
		}
		*q.on = ok
	}
	return checks, nil
}

// askYesNo reads one answer; an empty line takes the default.
func askYesNo(in *bufio.Reader, out io.Writer, question string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	if _, err := fmt.Fprintf(out, "%s %s ", question, hint); err != nil {
		return false, err
	}
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("reading the answer: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def, nil
	case "y", "yes", "s", "si", "sí":
		return true, nil
	default:
		return false, nil
	}
}
