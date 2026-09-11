package skills

import (
	"strings"
	"testing"
)

func TestListFindsTheOrchSkill(t *testing.T) {
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("List() returned no skills — expected at least the embedded orch/ one")
	}
	var names []string
	for _, s := range all {
		names = append(names, s.Name)
	}
	found := false
	for _, n := range names {
		if n == "orch" {
			found = true
		}
	}
	if !found {
		t.Errorf("List() = %v, want it to contain %q", names, "orch")
	}
}

func TestListIsSortedByName(t *testing.T) {
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Name > all[i].Name {
			t.Fatalf("List() not sorted: %q before %q", all[i-1].Name, all[i].Name)
		}
	}
}

func TestGetReturnsTheNamedSkill(t *testing.T) {
	s, ok := Get("orch")
	if !ok {
		t.Fatal("Get(\"orch\") ok = false, want true")
	}
	if s.Name != "orch" {
		t.Errorf("Get(\"orch\").Name = %q, want \"orch\"", s.Name)
	}
	if !strings.HasPrefix(s.Content, "---\n") {
		t.Errorf("Get(\"orch\").Content does not start with YAML frontmatter")
	}
}

func TestGetUnknownSkillReturnsFalse(t *testing.T) {
	_, ok := Get("does-not-exist")
	if ok {
		t.Error("Get(\"does-not-exist\") ok = true, want false")
	}
}

func TestDescriptionExtractsFrontmatterField(t *testing.T) {
	content := "---\nname: foo\ndescription: does the thing\n---\n\n# Foo\n"
	got := description(content)
	want := "does the thing"
	if got != want {
		t.Errorf("description() = %q, want %q", got, want)
	}
}

func TestDescriptionEmptyWithoutFrontmatter(t *testing.T) {
	if got := description("# Foo\nno frontmatter here\n"); got != "" {
		t.Errorf("description() = %q, want empty", got)
	}
}

func TestDescriptionEmptyWhenFrontmatterUnterminated(t *testing.T) {
	if got := description("---\nname: foo\ndescription: x\n"); got != "" {
		t.Errorf("description() = %q, want empty (no closing ---)", got)
	}
}

func TestBodyStripsFrontmatter(t *testing.T) {
	content := "---\nname: foo\ndescription: bar\n---\n\n# Foo\nbody text\n"
	got := body(content)
	want := "# Foo\nbody text\n"
	if got != want {
		t.Errorf("body() = %q, want %q", got, want)
	}
}

func TestBodyUnchangedWithoutFrontmatter(t *testing.T) {
	content := "# Foo\nno frontmatter\n"
	if got := body(content); got != content {
		t.Errorf("body() = %q, want unchanged %q", got, content)
	}
}

func TestOrchSkillDescriptionExtractsCleanly(t *testing.T) {
	s, ok := Get("orch")
	if !ok {
		t.Fatal("Get(\"orch\") not found")
	}
	d := description(s.Content)
	if d == "" {
		t.Fatal("description(orch SKILL.md) is empty — frontmatter parsing regressed")
	}
	if strings.Contains(d, "\n") {
		t.Errorf("description(orch SKILL.md) contains a newline: %q", d)
	}
}
