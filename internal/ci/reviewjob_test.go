package ci

import (
	"strings"
	"testing"
)

func TestNewReview(t *testing.T) {
	if r, err := NewReview("none", "", false); r != nil || err != nil {
		t.Errorf("none = %v, %v; want no review", r, err)
	}
	if r, err := NewReview("", "", true); r != nil || err != nil {
		t.Errorf("empty = %v, %v; want no review", r, err)
	}
	r, err := NewReview("Gemini", "", true)
	if err != nil || r.Provider != "gemini" || r.Model != "gemini-2.5-flash" || !r.Blocking {
		t.Errorf("gemini = %+v, %v; want the default model", r, err)
	}
	// OpenRouter has no default, and its model is routed through opencode's
	// openrouter/ namespace whether or not the person typed it.
	if _, err := NewReview("openrouter", "", false); err == nil || !strings.Contains(err.Error(), "needs a model") {
		t.Errorf("openrouter without a model: err = %v", err)
	}
	r, err = NewReview("openrouter", "anthropic/claude-sonnet-5", false)
	if err != nil || r.Model != "openrouter/anthropic/claude-sonnet-5" {
		t.Errorf("openrouter = %+v, %v; want the prefixed model", r, err)
	}
	if _, err := NewReview("copilot", "x", false); err == nil || !strings.Contains(err.Error(), "claude, gemini, openai, openrouter") {
		t.Errorf("unknown provider: err = %v", err)
	}
}

// The review job waits for the checks, runs on pull requests only, installs
// the provider CLI and orch, passes the secret, and posts its verdict.
func TestRenderWithAGeminiReview(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json":   `{"scripts":{"type-check":"tsc --noEmit","test":"vitest run"}}`,
		"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
	})
	st, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	review, err := NewReview("gemini", "", true)
	if err != nil {
		t.Fatal(err)
	}
	review.OrchVersion = "v0.14.0"
	out, err := Render(Jobs(st, AllChecks), Options{BaseBranch: "dev", Checks: AllChecks, Review: review})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pnpm-gemini-review", out, "node", "review")
	if strings.Contains(string(out), "curl") || strings.Contains(string(out), "| sh") {
		t.Errorf("the workflow pipes a downloaded script into a shell:\n%s", out)
	}
	// The operator's CI page and the client portal read the generated job
	// as a review, not only as its checks.
	wfs, err := ReadWorkflows(writeWorkflows(t, map[string]string{"orch-ci.yml": string(out)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(Gates(wfs), ","); got != "tests,typecheck,review" {
		t.Errorf("Gates = %s, want tests,typecheck,review", got)
	}
	for _, want := range []string{
		"needs: [node]",
		"pull-requests: write",
		"fetch-depth: 0",
		"npm install -g @google/gemini-cli",
		"GEMINI_API_KEY: ${{ secrets.GEMINI_API_KEY }}",
		"orch ci review --provider gemini --model gemini-2.5-flash --post --blocking",
		// orch comes pinned and checksum-verified from its release, not from
		// a script piped into a shell.
		"gh release download v0.14.0 --repo hectorcanaimero/orch",
		"sha256sum --check --ignore-missing checksums.txt",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("workflow is missing %q", want)
		}
	}
}

// Claude reads an API key or a subscription token, so both secrets are
// passed; OpenAI runs through codex.
func TestReviewJobSecretsPerProvider(t *testing.T) {
	st := Stack{Packages: []Package{{Dir: ".", Language: Go, Manager: "go", Test: "go test ./..."}}}
	for provider, want := range map[string][]string{
		"claude": {"ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}", "CLAUDE_CODE_OAUTH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}", "--provider claude --model sonnet --post\n"},
		"openai": {"OPENAI_API_KEY", "npm install -g @openai/codex", "--provider codex --model gpt-5.5"},
	} {
		review, err := NewReview(provider, "", false)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Render(Jobs(st, AllChecks), Options{Review: review})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(out), w) {
				t.Errorf("%s workflow is missing %q:\n%s", provider, w, out)
			}
		}
	}
}

// The generated checklist is the general one plus the rules for the
// languages the repository has, and nothing for the ones it does not.
func TestReviewChecklistFollowsTheStack(t *testing.T) {
	got := ReviewChecklist(Stack{Packages: []Package{{Language: Go}, {Language: Node}}})
	for _, want := range []string{"## General", "**Tests**", "## Go", "## TypeScript / JavaScript", "never overwrites it"} {
		if !strings.Contains(got, want) {
			t.Errorf("checklist is missing %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{"## Python", "## Rust"} {
		if strings.Contains(got, absent) {
			t.Errorf("checklist has %q for a repo without it", absent)
		}
	}
}

// A dev build has no release of its own to pin, so the job takes the latest.
func TestInstallOrchWithoutAVersionTakesTheLatestRelease(t *testing.T) {
	got := installOrch("")
	if !strings.HasPrefix(got, "gh release download --repo hectorcanaimero/orch ") {
		t.Errorf("installOrch(\"\") = %q, want the latest release", got)
	}
	if pinned := installOrch("v0.14.0"); !strings.HasPrefix(pinned, "gh release download v0.14.0 --repo") {
		t.Errorf("installOrch(v0.14.0) = %q, want the pinned tag", pinned)
	}
}
