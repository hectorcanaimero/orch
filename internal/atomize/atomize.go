package atomize

import "github.com/hectorcanaimero/orch/internal/model"

// Atomize parses one spec file's markdown (already read into specMD, from
// sourcePath under docsRoot — needed to compute SpecRef the same way
// ParseFile does) and merges the result into existing, returning the
// merged TasksFile and the diff. It never writes anything; call Apply on
// the result to persist it.
//
// This fuses ParseText + MergeTasks for the single-spec-file case. Real
// projects have many spec files under docs/ (that's the whole point of the
// atomizer), so internal/cli's `orch atomize` wires WalkSpecFiles +
// ParseFiles + MergeTasks directly rather than calling this per file —
// Atomize exists for the common single-file/programmatic case (e.g. a
// skill that just produced one spec and wants an immediate merge).
func Atomize(sourcePath, docsRoot, specMD string, existing model.TasksFile) (model.TasksFile, MergeDiff, error) {
	parse := ParseText(specMD, sourcePath, docsRoot, "")
	merged, diff := MergeTasks(existing, parse.Tasks)
	return merged, diff, nil
}

// AtomizeDir walks docsRoot for *.md spec files (matching --specs-dir mode
// of the Python CLI), parses and aggregates them, and merges the result
// into existing. expectedProjectID, when non-empty, is cross-checked
// against each file's frontmatter project_id.
func AtomizeDir(docsRoot, expectedProjectID string, existing model.TasksFile) (model.TasksFile, MergeDiff, ParseResult, error) {
	files, err := WalkSpecFiles(docsRoot)
	if err != nil {
		return model.TasksFile{}, MergeDiff{}, ParseResult{}, err
	}
	parse, err := ParseFiles(files, docsRoot, expectedProjectID)
	if err != nil {
		return model.TasksFile{}, MergeDiff{}, ParseResult{}, err
	}
	merged, diff := MergeTasks(existing, parse.Tasks)
	return merged, diff, parse, nil
}
