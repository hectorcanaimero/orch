package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
)

// Outline is what the specs name that tasks.json does not: every phase and
// every package, by the titles people read.
type Outline struct {
	// Phases maps a phase number to its `# F<n> — <title>` title.
	Phases map[int]string
	// Packages maps "<phase>.<package>" to its `## F<n>.<k> — <title>` title,
	// with a leading "Package: " dropped.
	Packages map[string]string
}

var (
	phaseHeader   = regexp.MustCompile(`^#\s+F(\d+)\s*[—–-]\s*(.+?)\s*$`)
	packageHeader = regexp.MustCompile(`^##\s+F(\d+)\.(\d+)\s*[—–-]\s*(?:Package:\s*)?(.+?)\s*$`)
	packageID     = regexp.MustCompile(`^F(\d+)\.(\d+)\.T\d+$`)
)

// SpecOutline reads phase and package titles from the specs the tasks point
// at. A task with no specRef, or a spec that is not there, contributes
// nothing; a spec that exists and cannot be read is an error, because that is
// a broken project rather than an absent spec.
//
// A specRef is tried under spec_root first and then as written, because
// specs from before bug 12 carry the `specs/` prefix themselves.
func SpecOutline(projectRoot, specRoot string, tasks []model.Task) (Outline, error) {
	out := Outline{Phases: map[int]string{}, Packages: map[string]string{}}
	seen := map[string]bool{}
	for _, t := range tasks {
		file, _, _ := strings.Cut(t.SpecRef, "#")
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		for _, candidate := range []string{
			filepath.Join(projectRoot, specRoot, file),
			filepath.Join(projectRoot, file),
		} {
			data, err := os.ReadFile(candidate) // #nosec G304 -- a spec path inside the operator's own project root.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return Outline{}, fmt.Errorf("reading spec %s: %w", candidate, err)
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimRight(line, "\r")
				if m := phaseHeader.FindStringSubmatch(line); m != nil {
					n, _ := strconv.Atoi(m[1])
					if _, ok := out.Phases[n]; !ok {
						out.Phases[n] = m[2]
					}
				} else if m := packageHeader.FindStringSubmatch(line); m != nil {
					key := m[1] + "." + m[2]
					if _, ok := out.Packages[key]; !ok {
						out.Packages[key] = m[3]
					}
				}
			}
			break
		}
	}
	return out, nil
}

// PackageKey is the "<phase>.<package>" a task id like F6.1.T3 belongs to, or
// "" for an id outside the atomizer's scheme (a synced issue, a hand-added
// task).
func PackageKey(taskID string) string {
	m := packageID.FindStringSubmatch(taskID)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}
