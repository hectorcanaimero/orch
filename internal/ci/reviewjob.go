package ci

import (
	"fmt"
	"sort"
	"strings"
)

// Repo is where orch is released, for the review job's install step.
const Repo = "hectorcanaimero/orch"

// ChecklistPath is the review checklist `orch ci setup` writes and
// `orch ci review` reads by default.
const ChecklistPath = ".github/orch-review.md"

// ReviewProvider is one AI review provider as a person names it, and what
// the generated job needs to run it.
type ReviewProvider struct {
	// Backend is `orch ci review --provider`: the coding-agent CLI.
	Backend string
	// Package installs that CLI with npm on the runner.
	Package string
	// Secrets are the repository secrets the CLI reads; one is enough.
	Secrets []string
	// DefaultModel is suggested when none is given; "" means one is required.
	DefaultModel string
	// ModelPrefix is prepended to a model given without it.
	ModelPrefix string
}

// ReviewProviders are the providers the wizard offers, by the name a person
// types.
var ReviewProviders = map[string]ReviewProvider{
	"claude": {Backend: "claude", Package: "@anthropic-ai/claude-code",
		Secrets: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}, DefaultModel: "sonnet"},
	"gemini": {Backend: "gemini", Package: "@google/gemini-cli",
		Secrets: []string{"GEMINI_API_KEY"}, DefaultModel: "gemini-2.5-flash"},
	"openai": {Backend: "codex", Package: "@openai/codex",
		Secrets: []string{"OPENAI_API_KEY"}, DefaultModel: "gpt-5.5"},
	"openrouter": {Backend: "opencode", Package: "opencode-ai",
		Secrets: []string{"OPENROUTER_API_KEY"}, ModelPrefix: "openrouter/"},
}

// ReviewProviderNames lists the providers, sorted, for prompts and errors.
func ReviewProviderNames() []string {
	names := make([]string, 0, len(ReviewProviders))
	for n := range ReviewProviders {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Review is an AI review added to the pipeline.
type Review struct {
	Provider string // a ReviewProviders key
	Model    string
	// OrchVersion is the orch release the job installs, the one that wrote
	// the workflow; "" installs the latest release (a dev build).
	OrchVersion string
	// Blocking fails the review job, and so the PR's checks, on a blocking
	// finding.
	Blocking bool
}

// NewReview validates a provider and model, filling the default model and
// the model prefix. An empty provider or "none" is no review: nil, nil.
func NewReview(provider, model string, blocking bool) (*Review, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || provider == "none" {
		return nil, nil
	}
	p, ok := ReviewProviders[provider]
	if !ok {
		return nil, fmt.Errorf("unknown review provider %q: use one of %s, or none", provider, strings.Join(ReviewProviderNames(), ", "))
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.DefaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("the %s review needs a model: %s<vendor>/<model>", provider, p.ModelPrefix)
	}
	if p.ModelPrefix != "" && !strings.HasPrefix(model, p.ModelPrefix) {
		model = p.ModelPrefix + model
	}
	return &Review{Provider: provider, Model: model, Blocking: blocking}, nil
}

// installOrch downloads orch's release archive for the runner with gh (already
// on GitHub's runners, authenticated by GH_TOKEN), checks it against the
// release's checksums.txt, and puts the binary on PATH. No script is piped
// into a shell. version pins the release; "" takes the latest one.
func installOrch(version string) string {
	tag := ""
	if version != "" {
		tag = version + " "
	}
	return "gh release download " + tag + "--repo " + Repo +
		` --pattern "orch_*_linux_amd64.tar.gz" --pattern checksums.txt --dir "$RUNNER_TEMP/orch"` +
		` && cd "$RUNNER_TEMP/orch" && sha256sum --check --ignore-missing checksums.txt` +
		` && mkdir -p "$HOME/.local/bin" && tar -xzf orch_*_linux_amd64.tar.gz -C "$HOME/.local/bin" orch` +
		` && echo "$HOME/.local/bin" >> "$GITHUB_PATH"`
}

// writeReviewJob writes the review job. It runs on pull requests only, after
// the checks pass (reviewing code that does not build spends tokens on
// nothing), with the permission to comment on the PR.
func writeReviewJob(b *strings.Builder, r *Review, needs []string) {
	p := ReviewProviders[r.Provider]
	b.WriteString("  review:\n")
	fmt.Fprintf(b, "    name: %s\n", scalar("AI review ("+r.Provider+")"))
	if len(needs) > 0 {
		fmt.Fprintf(b, "    needs: [%s]\n", strings.Join(needs, ", "))
	}
	fmt.Fprintf(b, "    if: %s\n", scalar("github.event_name == 'pull_request'"))
	b.WriteString("    runs-on: ubuntu-latest\n")
	b.WriteString("    permissions:\n      contents: read\n      pull-requests: write\n")
	b.WriteString("    steps:\n")
	b.WriteString("      - uses: actions/checkout@v4\n        with:\n          fetch-depth: 0\n")
	b.WriteString("      - uses: actions/setup-node@v4\n        with:\n          node-version: \"22\"\n")
	fmt.Fprintf(b, "      - name: Install the review CLI\n        run: %s\n", scalar("npm install -g "+p.Package))
	fmt.Fprintf(b, "      - name: Install orch\n        env:\n          GH_TOKEN: ${{ github.token }}\n        run: %s\n", scalar(installOrch(r.OrchVersion)))
	b.WriteString("      - name: Review\n        env:\n")
	b.WriteString("          GH_TOKEN: ${{ github.token }}\n")
	for _, s := range p.Secrets {
		fmt.Fprintf(b, "          %s: ${{ secrets.%s }}\n", s, s)
	}
	run := fmt.Sprintf("orch ci review --provider %s --model %s --post", p.Backend, r.Model)
	if r.Blocking {
		run += " --blocking"
	}
	fmt.Fprintf(b, "        run: %s\n", scalar(run))
}

// languageRules are review rules for one kind of package, added to the
// general checklist when the repository has one.
var languageRules = []struct {
	lang  Language
	title string
	rules string
}{
	{Go, "Go", "- Errors are returned with context (`fmt.Errorf(\"...: %w\", err)`), never discarded with `_`.\n" +
		"- A context received is passed on, not replaced with `context.Background()`; goroutines and files are not leaked."},
	{Node, "TypeScript / JavaScript", "- No `any`, `@ts-ignore` or disabled lint rule added to silence a check.\n" +
		"- UI changes handle the loading, empty and error states, not only the happy path."},
	{Python, "Python", "- New code keeps its type hints; no bare `except:` swallowing errors."},
	{Rust, "Rust", "- No `unwrap()` or `expect()` on a path that can fail outside tests."},
}

// ReviewChecklist is the checklist written for a repository: the general
// rules, then the rules for the languages it has.
func ReviewChecklist(st Stack) string {
	var b strings.Builder
	b.WriteString("# Review checklist\n\n")
	b.WriteString("`orch ci review` applies this to every pull request. A finding is blocking only\n")
	b.WriteString("when the change breaks a rule below. Edit it freely: orch never overwrites it.\n\n")
	b.WriteString("## General\n\n")
	b.WriteString(BuiltinChecklist)
	b.WriteString("\n")
	has := map[Language]bool{}
	for _, p := range st.Packages {
		has[p.Language] = true
	}
	for _, lr := range languageRules {
		if has[lr.lang] {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", lr.title, lr.rules)
		}
	}
	return b.String()
}
