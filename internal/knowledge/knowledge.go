// Package knowledge is the project's memory (#451): lessons a task learned,
// kept as one markdown file each under .orch/knowledge/ so they are versioned
// and reviewed with the code, and handed to the tasks that come later.
//
// A lesson is YAML front matter (id, title, area, tags, source, task,
// created) and a short body: what happened and what worked. The files are
// the truth; the FTS5 index in orch.db (state.RankKnowledge) is a cache
// rebuilt from them on every search.
package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Dir is where a project keeps its lessons, relative to its root.
const Dir = ".orch/knowledge"

// MaxLines and MaxChars are how much of one lesson's body a dispatch prompt
// carries; `orch knowledge curate` asks for lessons that fit.
const (
	MaxLines = 15
	MaxChars = 600
)

// Sources are who can write a lesson.
var Sources = []string{"agent", "verify", "review", "conflict", "operator"}

// Lesson is one file under Dir.
type Lesson struct {
	ID      string   `yaml:"id" json:"id"`
	Title   string   `yaml:"title" json:"title"`
	Area    []string `yaml:"area,omitempty" json:"area,omitempty"`
	Tags    []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Source  string   `yaml:"source" json:"source"`
	Task    string   `yaml:"task,omitempty" json:"task,omitempty"`
	Created string   `yaml:"created" json:"created"`
	Body    string   `yaml:"-" json:"body"`
	// Path is the file, relative to the root it was loaded from.
	Path string `yaml:"-" json:"path"`
}

// Load reads every lesson under root's Dir, sorted by id. No directory is
// no lessons.
func Load(root string) ([]Lesson, error) {
	dir := filepath.Join(root, Dir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []Lesson
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- a file in the project's own knowledge dir
		if err != nil {
			return nil, fmt.Errorf("read lesson %s: %w", e.Name(), err)
		}
		l, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("lesson %s: %w", filepath.Join(Dir, e.Name()), err)
		}
		if l.ID == "" {
			l.ID = strings.TrimSuffix(e.Name(), ".md")
		}
		l.Path = filepath.ToSlash(filepath.Join(Dir, e.Name()))
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Parse reads one lesson file: front matter between `---` lines, then the body.
func Parse(raw []byte) (Lesson, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Lesson{}, errors.New("no front matter: the file must start with a --- line")
	}
	head, body, ok := strings.Cut(text[4:], "\n---\n")
	if !ok {
		head, ok = strings.CutSuffix(text[4:], "\n---")
		if !ok {
			return Lesson{}, errors.New("the front matter has no closing --- line")
		}
	}
	var l Lesson
	if err := yaml.Unmarshal([]byte(head), &l); err != nil {
		return Lesson{}, fmt.Errorf("front matter: %w", err)
	}
	l.Body = strings.TrimSpace(body)
	return l, nil
}

// Marshal is the file a lesson is written as.
func Marshal(l Lesson) []byte {
	head, _ := yaml.Marshal(l) // a struct of strings always marshals
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(l.Body))
	b.WriteString("\n")
	return b.Bytes()
}

var reNonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Add validates l, fills its id and created date, and writes it under root's
// Dir without overwriting another lesson. It returns l as written.
func Add(root string, l Lesson, now time.Time) (Lesson, error) {
	l.Title = strings.TrimSpace(l.Title)
	l.Body = strings.TrimSpace(l.Body)
	if l.Title == "" || l.Body == "" {
		return Lesson{}, errors.New("a lesson needs a title and a body")
	}
	if l.Source == "" {
		l.Source = "operator"
	}
	known := false
	for _, s := range Sources {
		known = known || s == l.Source
	}
	if !known {
		return Lesson{}, fmt.Errorf("source %q is not one of %s", l.Source, strings.Join(Sources, ", "))
	}
	l.Area, l.Tags = clean(l.Area), clean(l.Tags)
	l.Created = now.UTC().Format("2006-01-02")
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Lesson{}, fmt.Errorf("create %s: %w", dir, err)
	}
	slug := strings.Trim(reNonSlug.ReplaceAllString(strings.ToLower(l.Title), "-"), "-")
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "-")
	}
	base := now.UTC().Format("20060102") + "-" + slug
	for n := 1; ; n++ {
		l.ID = base
		if n > 1 {
			l.ID = fmt.Sprintf("%s-%d", base, n)
		}
		file := filepath.Join(dir, l.ID+".md")
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302 G304 -- a versioned file in the project, read by people
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return Lesson{}, fmt.Errorf("create %s: %w", file, err)
		}
		_, err = f.Write(Marshal(l))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return Lesson{}, fmt.Errorf("write %s: %w", file, err)
		}
		l.Path = path.Join(Dir, l.ID+".md")
		return l, nil
	}
}

// Remove deletes the lesson with id under root.
func Remove(root, id string) error {
	lessons, err := Load(root)
	if err != nil {
		return err
	}
	for _, l := range lessons {
		if l.ID == id {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(l.Path))); err != nil {
				return fmt.Errorf("remove lesson %s: %w", id, err)
			}
			return nil
		}
	}
	return fmt.Errorf("no lesson %q in %s", id, Dir)
}

func clean(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// AreaMatches reports whether one of the lesson's areas covers one of paths:
// the same path, a directory above it, or a glob matching it (`**` matches
// any depth). A lesson with no area matches nothing here; it can still be
// found by its words.
func AreaMatches(l Lesson, paths []string) bool {
	for _, a := range l.Area {
		a = strings.TrimSuffix(strings.TrimPrefix(a, "./"), "/")
		prefix := strings.TrimSuffix(a, "/**")
		for _, p := range paths {
			p = strings.TrimPrefix(p, "./")
			if p == "" {
				continue
			}
			if ok, _ := path.Match(a, p); ok || p == prefix || strings.HasPrefix(p, prefix+"/") ||
				strings.HasPrefix(a, p+"/") {
				return true
			}
		}
	}
	return false
}

// Ranker is the search index: state.SQLite.
type Ranker interface {
	RankKnowledge(ctx context.Context, docs []state.KnowledgeDoc, query string, limit int) ([]string, error)
}

var reWord = regexp.MustCompile(`[\p{L}\p{N}_]{3,}`)

// stopwords are left out of a search: they match every lesson.
var stopwords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "from": true, "are": true, "was": true, "not": true, "but": true, "you": true,
	"its": true, "into": true, "when": true, "then": true, "than": true, "have": true, "has": true}

// Query turns free text into an FTS5 query: its words, each quoted, any of
// them. Empty when the text has no word worth searching for.
func Query(text string) string {
	seen := map[string]bool{}
	var terms []string
	for _, w := range reWord.FindAllString(strings.ToLower(text), -1) {
		if stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
		if len(terms) == 32 {
			break
		}
	}
	return strings.Join(terms, " OR ")
}

// Search ranks lessons for text by BM25, best first, at most n.
func Search(ctx context.Context, r Ranker, lessons []Lesson, text string, n int) ([]Lesson, error) {
	byID := map[string]Lesson{}
	docs := make([]state.KnowledgeDoc, 0, len(lessons))
	for _, l := range lessons {
		byID[l.ID] = l
		docs = append(docs, state.KnowledgeDoc{ID: l.ID, Title: l.Title, Body: l.Body,
			Tags: strings.Join(l.Tags, " "), Area: strings.Join(l.Area, " ")})
	}
	q := Query(text)
	if q == "" || r == nil || len(lessons) == 0 {
		return nil, nil
	}
	ids, err := r.RankKnowledge(ctx, docs, q, n)
	if err != nil {
		return nil, err
	}
	out := make([]Lesson, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out, nil
}

// Select picks the lessons a task's prompt carries: those whose area covers
// one of the task's paths come first, then those its text finds, each group
// in BM25 order, at most n.
func Select(ctx context.Context, r Ranker, lessons []Lesson, paths []string, text string, n int) ([]Lesson, error) {
	if len(lessons) == 0 || n <= 0 {
		return nil, nil
	}
	ranked, err := Search(ctx, r, lessons, text, len(lessons))
	if err != nil {
		return nil, err
	}
	rank := map[string]int{}
	for i, l := range ranked {
		rank[l.ID] = i + 1
	}
	var picked []Lesson
	for _, l := range lessons {
		if AreaMatches(l, paths) || rank[l.ID] > 0 {
			picked = append(picked, l)
		}
	}
	key := func(l Lesson) (int, int) {
		area, r := 1, rank[l.ID]
		if AreaMatches(l, paths) {
			area = 0
		}
		if r == 0 {
			r = len(lessons) + 1
		}
		return area, r
	}
	sort.SliceStable(picked, func(i, j int) bool {
		ai, ri := key(picked[i])
		aj, rj := key(picked[j])
		if ai != aj {
			return ai < aj
		}
		return ri < rj
	})
	if len(picked) > n {
		picked = picked[:n]
	}
	return picked, nil
}

// ForTask loads root's lessons and selects the ones task's prompt carries: by
// its files and its spec file, then by its title and description.
func ForTask(ctx context.Context, r Ranker, root string, task model.Task, specRoot string, n int) ([]Lesson, error) {
	lessons, err := Load(root)
	if err != nil || len(lessons) == 0 {
		return nil, err
	}
	paths := append([]string(nil), task.Files...)
	if spec, _, _ := strings.Cut(task.SpecRef, "#"); spec != "" {
		paths = append(paths, path.Join(specRoot, spec))
	}
	return Select(ctx, r, lessons, paths, task.Title+"\n"+task.Description, n)
}
