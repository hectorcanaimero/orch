package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The portal's Documents tab: the files the operator listed, by title, with
// the metadata stripped and nothing from outside the project.
func TestPortalDocuments(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/prd/001-portal.md", "---\ntype: prd\nproject_id: x\n---\n\n# Portal para clientes\n\nTexto del PRD.\n")
	write("docs/arch/001-portal.md", "Sin título, solo texto.\n")
	write("specs/f1-seguridad.md", "# F1 — Seguridad\n\n## F1.1 — Package: acceso\n")
	write("docs/brand/logo.md", "# Marca interna\n")
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("# Secreto\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	docs, err := PortalDocuments(root, []string{"docs/prd/*.md", "docs/arch/*.md", "specs/*.md", "specs/*.md", "../*.md", outside})
	if err != nil {
		t.Fatalf("PortalDocuments: %v", err)
	}
	titles := []string{}
	for _, d := range docs {
		titles = append(titles, d.Title)
	}
	want := []string{"Portal para clientes", "001 portal", "Seguridad"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Fatalf("titles = %v, want %v (listed order, deduplicated, nothing outside the project)", titles, want)
	}
	if strings.Contains(docs[0].Markdown, "type: prd") {
		t.Errorf("frontmatter was not stripped: %q", docs[0].Markdown)
	}
	if !strings.Contains(docs[0].Markdown, "Texto del PRD.") {
		t.Errorf("body lost: %q", docs[0].Markdown)
	}
	if docs[0].ID == "" || docs[0].ID == docs[2].ID || strings.ContainsAny(docs[0].ID, "/.") {
		t.Errorf("ids = %q, %q: want distinct slugs, never paths", docs[0].ID, docs[2].ID)
	}
	if got := uniqueSlug("Campaña de lanzamiento, promoción", map[string]int{}); got != "campana-de-lanzamiento-promocion" {
		t.Errorf("slug = %q, want accents folded", got)
	}
	if docs[0].UpdatedAt == "" {
		t.Error("no updated_at")
	}
}
