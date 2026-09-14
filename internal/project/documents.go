package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Document is one project file the client portal shows: a title, when it
// last changed, and its markdown. It carries no path — the portal names
// documents by title, and a path says where the operator keeps things.
type Document struct {
	ID        string
	Title     string
	UpdatedAt string
	Markdown  string
}

// documentLimit caps one document and documentsLimit the whole set: the
// snapshot travels to a browser, and a pasted log is not a document.
const (
	documentLimit  = 256 << 10
	documentsLimit = 2 << 20
)

var (
	frontmatter = regexp.MustCompile(`(?s)\A---\r?\n.*?\r?\n---\r?\n`)
	firstHeader = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)
	phasePrefix = regexp.MustCompile(`^F\d+\s*[—–-]\s*`)
	nonSlug     = regexp.MustCompile(`[^a-z0-9]+`)
)

// PortalDocuments reads the files `portal.documents` lists (globs relative to
// the project root), in the order the globs are listed and sorted within
// each, without duplicates. A glob or a match that resolves outside the
// project is skipped: the list is the operator's, but what it can publish
// is the project. Files over the size cap are skipped, and reading stops
// once the set reaches its cap.
func PortalDocuments(projectRoot string, globs []string) ([]Document, error) {
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	var out []Document
	seen := map[string]bool{}
	slugs := map[string]int{}
	total := 0
	for _, glob := range globs {
		if filepath.IsAbs(glob) {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil {
			return nil, fmt.Errorf("portal.documents %q: %w", glob, err)
		}
		sort.Strings(matches)
		for _, m := range matches {
			real, err := filepath.EvalSymlinks(m)
			if err != nil || !within(root, real) || seen[real] {
				continue
			}
			info, err := os.Stat(real)
			if err != nil || info.IsDir() || info.Size() > documentLimit {
				continue
			}
			if total+int(info.Size()) > documentsLimit {
				return out, nil
			}
			data, err := os.ReadFile(real) // #nosec G304 -- a file inside the project, listed by the operator.
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", real, err)
			}
			seen[real] = true
			total += len(data)
			body := frontmatter.ReplaceAllString(string(data), "")
			title := documentTitle(body, real)
			out = append(out, Document{
				ID:        uniqueSlug(title, slugs),
				Title:     title,
				UpdatedAt: info.ModTime().UTC().Format(time.RFC3339),
				Markdown:  strings.TrimSpace(body) + "\n",
			})
		}
	}
	return out, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// documentTitle is the first `# ` heading (without a plan's "F1 — "), or the
// file name made readable.
func documentTitle(body, path string) string {
	if m := firstHeader.FindStringSubmatch(body); m != nil {
		return phasePrefix.ReplaceAllString(m[1], "")
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return strings.NewReplacer("-", " ", "_", " ").Replace(name)
}

// accents folds the Spanish and Portuguese letters a title is likeliest to
// carry, so "Campaña de lanzamiento" links as campana-de-lanzamiento.
var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ñ", "n", "ç", "c",
)

func uniqueSlug(title string, used map[string]int) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(accents.Replace(strings.ToLower(title)), "-"), "-")
	if slug == "" {
		slug = "document"
	}
	used[slug]++
	if n := used[slug]; n > 1 {
		return fmt.Sprintf("%s-%d", slug, n)
	}
	return slug
}
