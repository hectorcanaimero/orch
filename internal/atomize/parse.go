// Package atomize ports orchestrator/atomize.py: parsing markdown specs
// (docs/SPEC-FORMAT.md) into tasks, and merging them non-destructively into
// an existing tasks.json. See parse.go (parser), merge.go (merge rules),
// diff.go (human-readable diff), write.go (Apply/backup) and atomize.go
// (the top-level Atomize entry point).
package atomize

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Header and bullet regexes, ported verbatim from the _RE_* patterns in
// orchestrator/atomize.py. The em/en/hyphen dash class and the tolerance
// for 2-4 space bullet indents are spec requirements (docs/SPEC-FORMAT.md),
// not incidental to the Python implementation.
var (
	rePhase       = regexp.MustCompile(`^#\s+F(\d+)\s*[—\-–]\s*(.+?)\s*$`)
	rePackage     = regexp.MustCompile(`^##\s+F(\d+)\.(\d+)\s*[—\-–]\s*(.+?)\s*$`)
	reTask        = regexp.MustCompile(`^###\s+F(\d+)\.(\d+)\.T(\d+)\s*[—\-–]\s*(.+?)\s*$`)
	reFieldBullet = regexp.MustCompile(`^\s*[-*]\s+\*\*([^*]+)\*\*\s*:\s*(.*?)\s*$`)
	reFileBullet  = regexp.MustCompile("^\\s*[-*]\\s+`([^`]+)`\\s*$")
	reInlineFile  = regexp.MustCompile("`([^`]+)`")
	reEstimate    = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*([dhm]?)`)
	reDepSplit    = regexp.MustCompile(`[,;]`)
)

// fieldMap ports _FIELD_MAP: lower-cased ES/EN bullet labels to the
// ParsedTask field they set.
var fieldMap = map[string]string{
	"modelo": "model", "model": "model",
	"estimación": "estimate_hours", "estimacion": "estimate_hours",
	"estimate": "estimate_hours", "estimation": "estimate_hours",
	"razón": "reason", "razon": "reason", "reason": "reason",
	"dependencies": "dependencies", "dependencias": "dependencies", "deps": "dependencies",
	"files": "files", "archivos": "files",
}

// ParsedTask is one task extracted from a spec markdown file, before merge.
// Field names mirror ParsedTask in orchestrator/atomize.py (snake_case
// Python attributes become Go's exported CamelCase, matched 1:1).
type ParsedTask struct {
	ID            string
	Phase         int
	Package       int
	TaskNum       int
	Title         string
	Description   string
	Model         string
	Reason        string
	EstimateHours float64
	Dependencies  []string
	Files         []string
	SpecRef       string
	SourceFile    string
	Line          int
}

// ParseResult bundles what parsing one or more spec files produced —
// useful for the CLI's diff/list rendering and for tests.
type ParseResult struct {
	Tasks        []ParsedTask
	FilesScanned []string
	Warnings     []string
}

// parseEstimate parses an estimate string like "8h", "1.5h", "30m", "2d"
// (d = 8 work hours). Empty or unparseable input is 0.0. Ports
// parse_estimate exactly, including that trailing junk after a valid
// number/unit is silently ignored (the regex is unanchored at the end).
func parseEstimate(raw string) float64 {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, ",", ".")
	m := reEstimate.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	switch m[2] {
	case "d":
		return val * 8.0
	case "m":
		return val / 60.0
	default:
		return val
	}
}

// parseDeps splits a comma/semicolon separated dependency list, trimming
// whitespace and dropping empty entries. Ports parse_deps.
func parseDeps(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	for _, p := range reDepSplit.Split(raw, -1) {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// finalizeDescription trims leading/trailing blank lines from buf while
// preserving internal blank lines (a description can be multi-paragraph).
// Ports _finalize_description.
func finalizeDescription(buf []string) string {
	for len(buf) > 0 && strings.TrimSpace(buf[0]) == "" {
		buf = buf[1:]
	}
	for len(buf) > 0 && strings.TrimSpace(buf[len(buf)-1]) == "" {
		buf = buf[:len(buf)-1]
	}
	return strings.Join(buf, "\n")
}

// splitLines mirrors Python's str.splitlines(keepends=False) for the line
// endings a markdown spec realistically contains (\n and \r\n); an empty
// body has zero lines, matching Python ("".splitlines() == []) rather than
// strings.Split's single empty-string element.
func splitLines(body string) []string {
	if body == "" {
		return nil
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.Split(body, "\n")
}

// relpathForSpecRef returns the POSIX relative path from docsRoot to
// specPath, or specPath's basename when specPath falls outside docsRoot
// (the --file edge case). Ports _relpath_for_spec_ref.
func relpathForSpecRef(specPath, docsRoot string) string {
	absSpec, err1 := filepath.Abs(specPath)
	absDocs, err2 := filepath.Abs(docsRoot)
	if err1 != nil || err2 != nil {
		return filepath.Base(specPath)
	}
	rel, err := filepath.Rel(absDocs, absSpec)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(specPath)
	}
	return filepath.ToSlash(rel)
}

// ParseText parses spec markdown already read into memory. sourcePath is
// used exactly as Python uses `path`: its basename prefixes warnings, its
// full form (as given) is stored on each ParsedTask.SourceFile, and its
// relation to docsRoot produces SpecRef. expectedProjectID, when non-empty,
// is cross-checked against the frontmatter's project_id (a mismatch is a
// warning, not an error). Ports parse_spec_file.
func ParseText(text, sourcePath, docsRoot, expectedProjectID string) ParseResult {
	result := ParseResult{FilesScanned: []string{sourcePath}}
	relRef := relpathForSpecRef(sourcePath, docsRoot)
	name := filepath.Base(sourcePath)

	fm, body := extractFrontmatter(text)
	for _, w := range fm.Warnings {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %s", name, w))
	}

	if fm.Present && fm.ProjectID != "" && expectedProjectID != "" && fm.ProjectID != expectedProjectID {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%s: frontmatter project_id='%s' no matchea con project activo '%s'",
			name, fm.ProjectID, expectedProjectID))
	}

	var currentPhase *int
	if fm.Present && fm.Phase != nil {
		p := *fm.Phase
		currentPhase = &p
	}

	var currentTask *ParsedTask
	taskState := "desc" // "desc" | "fields" | "files"
	var descBuf []string

	flush := func() {
		if currentTask == nil {
			return
		}
		currentTask.Description = finalizeDescription(descBuf)
		result.Tasks = append(result.Tasks, *currentTask)
		currentTask = nil
		descBuf = nil
		taskState = "desc"
	}

	// A fenced code block is documentation ABOUT the format, never spec
	// content — orch init's own specs/README.md shows the minimum shape
	// inside a ```markdown fence, and without this check `orch atomize
	// --apply` with no --file on a fresh project imports that example as
	// two real tasks (bug 19; the header/task regexes below match line by
	// line and have no idea a fence exists). Ports the fix in Python's
	// _flush_task loop: `startswith("```")` after stripping leading
	// whitespace toggles fence state; a line inside one is dropped
	// entirely, never reaching the header/task/field matchers below.
	inFence := false
	for i, line := range splitLines(body) {
		lineno := i + 1

		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		if m := rePhase.FindStringSubmatch(line); m != nil {
			flush()
			n, _ := strconv.Atoi(m[1])
			currentPhase = &n
			continue
		}

		if m := rePackage.FindStringSubmatch(line); m != nil {
			flush()
			pkgPhase, _ := strconv.Atoi(m[1])
			pkg, _ := strconv.Atoi(m[2])
			if currentPhase == nil {
				currentPhase = &pkgPhase
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"%s:%d: package F%d.%d sin header de fase previo — asumiendo fase implícita",
					name, lineno, pkgPhase, pkg))
			} else if pkgPhase != *currentPhase {
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"%s:%d: package F%d.%d no matchea con fase actual F%d",
					name, lineno, pkgPhase, pkg, *currentPhase))
				currentPhase = &pkgPhase
			}
			continue
		}

		if m := reTask.FindStringSubmatch(line); m != nil {
			flush()
			tPhase, _ := strconv.Atoi(m[1])
			tPkg, _ := strconv.Atoi(m[2])
			tNum, _ := strconv.Atoi(m[3])
			title := strings.TrimSpace(m[4])
			tid := fmt.Sprintf("F%d.%d.T%d", tPhase, tPkg, tNum)
			if currentPhase == nil {
				p := tPhase
				currentPhase = &p
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"%s:%d: task %s sin fase previa", name, lineno, tid))
			}
			currentTask = &ParsedTask{
				ID: tid, Phase: tPhase, Package: tPkg, TaskNum: tNum, Title: title,
				Dependencies: []string{}, Files: []string{},
				SpecRef:    fmt.Sprintf("%s#%s", relRef, tid),
				SourceFile: sourcePath, Line: lineno,
			}
			taskState = "desc"
			continue
		}

		if currentTask == nil {
			// Free text at phase/package level has no destination field.
			continue
		}

		if m := reFieldBullet.FindStringSubmatch(line); m != nil {
			taskState = "fields"
			label := strings.ToLower(strings.TrimSpace(m[1]))
			value := strings.TrimSpace(m[2])
			key, ok := fieldMap[label]
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"%s:%d: campo desconocido '**%s**' en task %s", name, lineno, m[1], currentTask.ID))
				continue
			}
			switch key {
			case "model":
				currentTask.Model = value
			case "reason":
				currentTask.Reason = value
			case "estimate_hours":
				currentTask.EstimateHours = parseEstimate(value)
			case "dependencies":
				currentTask.Dependencies = parseDeps(value)
			case "files":
				taskState = "files"
				if inline := strings.TrimSpace(value); inline != "" {
					for _, im := range reInlineFile.FindAllStringSubmatch(inline, -1) {
						currentTask.Files = append(currentTask.Files, im[1])
					}
				}
			}
			continue
		}

		if taskState == "files" {
			if m := reFileBullet.FindStringSubmatch(line); m != nil {
				currentTask.Files = append(currentTask.Files, m[1])
				continue
			}
			// A non-file, non-blank line ends the Files sub-bullet run;
			// blank lines are tolerated mid-list. Ported as-is: the line
			// itself is then dropped rather than folded into fields/desc.
			if strings.TrimSpace(line) != "" {
				taskState = "fields"
			}
		}

		if taskState == "desc" {
			descBuf = append(descBuf, line)
		}
	}
	flush()
	return result
}

// ParseFile reads path and parses it with ParseText.
func ParseFile(path, docsRoot, expectedProjectID string) (ParseResult, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- caller-supplied spec path, the normal shape of this CLI's file I/O
	if err != nil {
		return ParseResult{}, fmt.Errorf("atomize: read %s: %w", path, err)
	}
	return ParseText(string(data), path, docsRoot, expectedProjectID), nil
}

// WalkSpecFiles lists the *.md files under root in the same deterministic
// (lexicographic) order Python's `sorted(root.rglob("*.md"))` produces. A
// missing root is not an error — it returns no files, matching
// _iter_spec_files's `if not root.exists(): return []`.
func WalkSpecFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("atomize: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("atomize: %s is not a directory", root)
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("atomize: walk %s: %w", p, err)
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// ParseFiles parses every file in paths (in the given order — callers pass
// WalkSpecFiles's output for the deterministic on-disk walk) and aggregates
// the results. A task ID defined in more than one file keeps the LAST
// file's definition and records a warning; ordering paths lexicographically
// makes "last" mean "alphabetically last", matching parse_specs.
func ParseFiles(paths []string, docsRoot, expectedProjectID string) (ParseResult, error) {
	agg := ParseResult{}
	seen := map[string]string{} // task id -> source file that currently owns it
	for _, p := range paths {
		r, err := ParseFile(p, docsRoot, expectedProjectID)
		if err != nil {
			return ParseResult{}, err
		}
		agg.FilesScanned = append(agg.FilesScanned, p)
		agg.Warnings = append(agg.Warnings, r.Warnings...)
		for _, t := range r.Tasks {
			if prevFile, dup := seen[t.ID]; dup {
				agg.Warnings = append(agg.Warnings, fmt.Sprintf(
					"task %s duplicada — primero en %s, ahora en %s (gana el segundo)",
					t.ID, prevFile, t.SourceFile))
				kept := agg.Tasks[:0]
				for _, x := range agg.Tasks {
					if x.ID != t.ID {
						kept = append(kept, x)
					}
				}
				agg.Tasks = kept
			}
			seen[t.ID] = t.SourceFile
			agg.Tasks = append(agg.Tasks, t)
		}
	}
	return agg, nil
}
