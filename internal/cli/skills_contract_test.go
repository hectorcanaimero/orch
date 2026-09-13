package cli

import (
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/atomize"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/skills"
)

// The contract tests for the embedded skills live here rather than in
// internal/skills because they check skills against the rest of the binary —
// the command tree, the atomize parser, the default router — and
// internal/skills is a leaf that imports none of it.

// G6.5 names six skills. List walks whatever the embed matched, so a missing
// directory would not fail anything else: it would just install one skill
// fewer, silently.
func TestTheSixSkillsAreEmbedded(t *testing.T) {
	all, err := skills.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range all {
		got = append(got, s.Name)
	}
	want := []string{"orch", "orch-arch", "orch-plan", "orch-prd", "orch-spec", "orch-tasks"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("embedded skills = %v, want %v", got, want)
	}
}

// Claude Code finds a skill by its frontmatter `name`, and Cursor's rule file
// is built from `description` read as one line. A name that drifts from its
// directory installs under one name and answers to another.
func TestEverySkillHasAMatchingNameAndAOneLineDescription(t *testing.T) {
	all, err := skills.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		fm, ok := frontmatterBlock(s.Content)
		if !ok {
			t.Errorf("%s: no frontmatter block", s.Name)
			continue
		}
		fields := map[string]string{}
		for _, line := range strings.Split(fm, "\n") {
			if k, v, found := strings.Cut(line, ":"); found && !strings.HasPrefix(line, " ") {
				fields[k] = strings.TrimSpace(v)
			}
		}
		if fields["name"] != s.Name {
			t.Errorf("%s: frontmatter name = %q, want the directory name", s.Name, fields["name"])
		}
		if len(fields["description"]) < 60 {
			t.Errorf("%s: description %q is too short to tell an agent when to load the skill", s.Name, fields["description"])
		}
		if len(fields) != 2 {
			t.Errorf("%s: frontmatter keys = %v, want exactly name and description (the only two the installer reads)", s.Name, fields)
		}
	}
}

// "Sin rutas absolutas": a skill is installed on machines that are not the one
// it was written on. Paths an agent should use are project-relative; the one
// home-relative path a skill may name is where an installer writes.
func TestNoSkillNamesAMachineSpecificPath(t *testing.T) {
	personal := regexp.MustCompile(`/home/|/Users/|/mnt/|/tmp/|/var/folders/|[A-Z]:\\`)
	all, err := skills.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		for i, line := range strings.Split(s.Content, "\n") {
			if personal.MatchString(line) {
				t.Errorf("%s:%d: machine-specific path: %s", s.Name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// orch-spec's example is the format an agent copies. It has to be exactly
// what the real parser reads, or every spec written from it loses tasks
// without a word: the parser drops what it does not match.
func TestTheOrchSpecExampleAtomizesCleanly(t *testing.T) {
	s, ok := skills.Get("orch-spec")
	if !ok {
		t.Fatal("orch-spec is not embedded")
	}
	example, ok := fencedBlock(s.Content, "markdown")
	if !ok {
		t.Fatal("orch-spec has no ```markdown example")
	}

	// Zero warnings covers the frontmatter too: a type other than spec, a
	// project_id other than the active project's, or a phase header that
	// disagrees with `phase:` each surface here and nowhere else.
	res := atomize.ParseText(example, filepath.Join("specs", "f1-auth.md"), "specs", "sample-project")
	if len(res.Warnings) != 0 {
		t.Errorf("the example produces parser warnings: %v", res.Warnings)
	}

	type want struct {
		model string
		hours float64
		deps  []string
		files int
	}
	wantTasks := map[string]want{
		"F1.1.T1": {"claude/claude-sonnet-4-6", 2, nil, 2},
		"F1.2.T1": {"claude/claude-sonnet-4-6", 4, []string{"F1.1.T1"}, 2},
	}
	if len(res.Tasks) != len(wantTasks) {
		t.Fatalf("parsed %d tasks, want %d: %+v", len(res.Tasks), len(wantTasks), res.Tasks)
	}
	for _, task := range res.Tasks {
		w, ok := wantTasks[task.ID]
		if !ok {
			t.Errorf("unexpected task %s", task.ID)
			continue
		}
		if task.Model != w.model || task.EstimateHours != w.hours || len(task.Files) != w.files ||
			!reflect.DeepEqual(nilIfEmpty(task.Dependencies), w.deps) {
			t.Errorf("%s = model %q, %gh, deps %v, %d files; want %q, %gh, %v, %d files",
				task.ID, task.Model, task.EstimateHours, task.Dependencies, len(task.Files),
				w.model, w.hours, w.deps, w.files)
		}
		if task.Phase != 1 {
			t.Errorf("%s phase = %d, want 1", task.ID, task.Phase)
		}
		// The skill tells agents a Done-when line belongs in the description,
		// not as a field; check the parser agrees.
		if !strings.Contains(task.Description, "Done when:") {
			t.Errorf("%s: description lost its Done-when line: %q", task.ID, task.Description)
		}
		if task.SpecRef != "f1-auth.md#"+task.ID {
			t.Errorf("%s specRef = %q", task.ID, task.SpecRef)
		}
	}

	// A model the default router does not route would make the example the
	// first thing `orch router validate` rejects in a templated project.
	rtr, err := router.Load(filepath.Join("..", "scaffold", "defaults", "model_router.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, k := range rtr.Keys() {
		keys[k] = true
	}
	for id, w := range wantTasks {
		if !keys[w.model] {
			t.Errorf("%s: model %q has no route in the default model_router.yaml", id, w.model)
		}
	}
}

// orch-prd and orch-arch write `type: prd` / `type: arch`. Atomize knows both,
// and a typo there would be reported as an invalid type on every atomize run
// that walks past the file.
func TestPRDAndArchFrontmatterTypesAreOnesAtomizeKnows(t *testing.T) {
	for name, typ := range map[string]string{"orch-prd": "prd", "orch-arch": "arch"} {
		s, ok := skills.Get(name)
		if !ok {
			t.Fatalf("%s is not embedded", name)
		}
		block, ok := fencedBlock(s.Content, "yaml")
		if !ok {
			t.Fatalf("%s has no ```yaml frontmatter example", name)
		}
		// Parsing the frontmatter is the only way to see how atomize reads
		// it: a known-but-not-consumed type (what prd and arch are) yields
		// exactly one warning naming that type. An unknown type says
		// "no es válido" instead, and a YAML error — the placeholder text
		// in these examples is where one would come from — says
		// "malformado" and drops the type entirely.
		res := atomize.ParseText(block, name+".md", ".", "")
		wantWarning := "type='" + typ + "' no es un tipo consumible"
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], wantWarning) {
			t.Errorf("%s: warnings = %v, want exactly one containing %q", name, res.Warnings, wantWarning)
		}
		if len(res.Tasks) != 0 {
			t.Errorf("%s: frontmatter example parsed into %d tasks", name, len(res.Tasks))
		}
	}
}

func frontmatterBlock(content string) (string, bool) {
	if !strings.HasPrefix(content, "---\n") {
		return "", false
	}
	end := strings.Index(content[4:], "\n---")
	if end == -1 {
		return "", false
	}
	return content[4 : 4+end], true
}

// fencedBlock returns the body of the first ```lang fence in content.
func fencedBlock(content, lang string) (string, bool) {
	open := "```" + lang + "\n"
	start := strings.Index(content, open)
	if start == -1 {
		return "", false
	}
	body := content[start+len(open):]
	end := strings.Index(body, "\n```")
	if end == -1 {
		return "", false
	}
	return body[:end+1], true
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// TestEmbeddedSkillsOnlyNameCommandsThatExist is the check a skill needs and
// no reader of one does: every `orch ...` invocation an embedded SKILL.md
// shows an agent must resolve against this binary's real command tree —
// subcommand by subcommand, flag by flag — and must not lean on a flag whose
// own help says it is not implemented. A skill is the one document an agent
// follows literally, so a stale verb (`orch stop`) or a flag that only prints
// an error (`task set --model`) there costs a failed step on every run, and
// nothing else in the tree would notice it.
//
// Only code is checked, never prose: inline code spans that start with
// `orch `, and lines of fenced blocks that do. "orch dispatches ready tasks"
// in a sentence is not an invocation.
func TestEmbeddedSkillsOnlyNameCommandsThatExist(t *testing.T) {
	all, err := skills.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no embedded skills: the check below would pass over nothing")
	}
	root, _ := newRootCmd("test")

	checked := 0
	for _, s := range all {
		for _, inv := range orchInvocations(s.Content) {
			checked++
			if problem := resolveInvocation(root, inv); problem != "" {
				t.Errorf("%s: `%s`: %s", s.Name, inv, problem)
			}
		}
	}
	// A regression in orchInvocations that finds nothing would turn this into
	// a test of nothing; the six skills carry far more than this.
	if checked < 30 {
		t.Errorf("found only %d orch invocations across %d skills — is the extractor broken?", checked, len(all))
	}
}

var inlineCode = regexp.MustCompile("`([^`\n]+)`")

// orchInvocations returns every command line in content that invokes orch:
// fenced lines starting with "orch ", and inline code spans that do.
func orchInvocations(content string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			if strings.HasPrefix(trimmed, "orch ") {
				out = append(out, trimmed)
			}
			continue
		}
		for _, m := range inlineCode.FindAllStringSubmatch(line, -1) {
			if strings.HasPrefix(m[1], "orch ") {
				out = append(out, m[1])
			}
		}
	}
	return out
}

// resolveInvocation walks inv ("orch verb sub --flag value ...") down root and
// reports the first thing that does not exist, or "" when all of it does.
// Subcommands are only matched until the first flag or positional argument,
// the way cobra itself parses; a `#` comment, a pipe or a shell operator ends
// the command.
func resolveInvocation(root *cobra.Command, inv string) string {
	fields := strings.Fields(inv)[1:] // drop "orch"
	cmd := root
	descending := true
	for _, tok := range fields {
		if tok == "#" || strings.HasPrefix(tok, "#") || tok == "|" || tok == "&&" || tok == ";" {
			break
		}
		if strings.HasPrefix(tok, "-") {
			descending = false
			name := strings.TrimLeft(strings.SplitN(tok, "=", 2)[0], "-")
			var f = cmd.Flags().Lookup(name)
			if f == nil {
				f = cmd.InheritedFlags().Lookup(name)
			}
			if f == nil && !strings.HasPrefix(tok, "--") && len(name) == 1 {
				f = cmd.Flags().ShorthandLookup(name)
			}
			// cobra only registers --help everywhere and --version on a root
			// with a Version at Execute time, so neither is in the tree yet.
			if f == nil && (name == "help" || name == "h" || (cmd == root && name == "version" && root.Version != "")) {
				continue
			}
			if f == nil {
				return "`" + cmd.CommandPath() + "` has no flag " + tok
			}
			if strings.Contains(f.Usage, "not implemented") {
				return "`" + cmd.CommandPath() + " " + tok + "` is not implemented: " + f.Usage
			}
			continue
		}
		if !descending {
			continue // a flag value or a positional argument
		}
		if strings.HasPrefix(tok, "<") {
			break // `orch <verb> --help`: a placeholder names no command to check
		}
		if sub := findSub(cmd, tok); sub != nil {
			cmd = sub
			continue
		}
		if cmd == root {
			return "no such command " + tok
		}
		descending = false // the first positional argument
	}
	return ""
}

func findSub(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}
