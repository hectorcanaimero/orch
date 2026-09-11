package atomize

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// reFrontmatter matches a Jekyll/Hugo-style frontmatter block at the very
// top of a file: "---\n...\n---\n" (the trailing newline after the closing
// fence is optional). Ported from orchestrator/atomize.py's _RE_FRONTMATTER;
// `(?s)` makes `.` match newlines the way Python's re.DOTALL does, and `^`
// without `(?m)` anchors to the start of the whole string in both languages.
var reFrontmatter = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n?`)

// validFrontmatterTypes / atomizerFrontmatterTypes port _VALID_TYPES /
// _ATOMIZER_TYPES: the atomizer only consumes spec|task-batch; prd/arch/poc
// are other artifacts' metadata that may still appear if a walk over docs/
// picks up a stray file.
var validFrontmatterTypes = map[string]bool{
	"prd": true, "arch": true, "spec": true, "poc": true, "task-batch": true,
}
var atomizerFrontmatterTypes = map[string]bool{"spec": true, "task-batch": true}

// Frontmatter is the metadata parsed from a spec's optional YAML
// frontmatter. Every field is optional — a spec with no frontmatter block
// parses to Frontmatter{Present: false} and every other field zero.
type Frontmatter struct {
	Present     bool
	Type        string
	ProjectID   string
	Phase       *int // nil when absent or not a valid int
	Package     string
	Version     string
	DependsOn   []string
	ConsumedBy  []string
	GeneratedBy string
	GeneratedAt string
	Title       string
	Warnings    []string
}

// extractFrontmatter extracts the frontmatter YAML from the top of text.
//
// Ports extract_frontmatter from orchestrator/atomize.py field for field,
// including two of its quirks that tests pin (see frontmatter_test.go):
//
//   - A parse error in the YAML is NOT fatal: it is recorded as a warning
//     and the ENTIRE original text (fences included) is returned as body,
//     so headers on the fenced lines are not silently lost.
//   - A frontmatter block whose top-level YAML value is not a mapping
//     (a list, a scalar, ...) is also treated as "no frontmatter" — but,
//     matching the Python source exactly, the warning noting that is
//     built and then discarded (the function returns a fresh
//     Frontmatter{Present: false} rather than the one the warning was
//     appended to). This is a preexisting Python quirk, not a Go bug —
//     see docs/brainstorm/go-migration-notes.md.
func extractFrontmatter(text string) (Frontmatter, string) {
	loc := reFrontmatter.FindStringSubmatchIndex(text)
	if loc == nil {
		return Frontmatter{Present: false}, text
	}
	rawYAML := text[loc[2]:loc[3]]
	body := text[loc[1]:]

	var data any
	if err := yaml.Unmarshal([]byte(rawYAML), &data); err != nil {
		return Frontmatter{
			Present:  false,
			Warnings: []string{fmt.Sprintf("frontmatter YAML malformado: %s", err)},
		}, text
	}

	if data == nil {
		// Empty frontmatter ("---\n---\n") is tolerated as "no frontmatter".
		return Frontmatter{Present: false}, body
	}

	m, ok := asStringMap(data)
	if !ok {
		// Ported as-is: Python builds this warning on a Frontmatter it then
		// discards, so it never surfaces. See the doc comment above.
		return Frontmatter{Present: false}, body
	}

	fm := Frontmatter{Present: true}

	if v, ok := m["type"]; ok {
		if s, ok := v.(string); ok {
			fm.Type = s
			if !validFrontmatterTypes[s] {
				keys := make([]string, 0, len(validFrontmatterTypes))
				for k := range validFrontmatterTypes {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				fm.Warnings = append(fm.Warnings, fmt.Sprintf(
					"frontmatter type='%s' no es válido (esperado uno de: %s)", s, pyStrList(keys)))
			} else if !atomizerFrontmatterTypes[s] {
				fm.Warnings = append(fm.Warnings, fmt.Sprintf(
					"frontmatter type='%s' no es un tipo consumible por el atomizer "+
						"(esperado: spec | task-batch) — este archivo no debería estar en el walk", s))
			}
		} else {
			fm.Warnings = append(fm.Warnings, fmt.Sprintf(
				"frontmatter type debe ser string, vino %s", pyTypeName(v)))
		}
	}

	if v, ok := m["project_id"]; ok {
		if s, ok := v.(string); ok {
			fm.ProjectID = s
		} else {
			fm.Warnings = append(fm.Warnings, "frontmatter project_id debe ser string — ignorado")
		}
	}

	if v, ok := m["phase"]; ok {
		if n, ok := asPythonInt(v); ok {
			fm.Phase = &n
		} else {
			fm.Warnings = append(fm.Warnings, fmt.Sprintf(
				"frontmatter phase debe ser int, vino %s — ignorado", pyTypeName(v)))
		}
	}

	if v, ok := m["package"]; ok {
		if s, ok := v.(string); ok {
			fm.Package = s
		} else {
			fm.Warnings = append(fm.Warnings, "frontmatter package debe ser string — ignorado")
		}
	}

	if v, ok := m["version"]; ok {
		fm.Version = pyStr(v)
	}

	if v, ok := m["depends_on"]; ok {
		if list, ok := v.([]any); ok {
			fm.DependsOn = make([]string, len(list))
			for i, x := range list {
				fm.DependsOn[i] = pyStr(x)
			}
		} else {
			fm.Warnings = append(fm.Warnings, "frontmatter depends_on debe ser lista — ignorado")
		}
	}

	if v, ok := m["consumed_by"]; ok {
		if list, ok := v.([]any); ok {
			fm.ConsumedBy = make([]string, len(list))
			for i, x := range list {
				fm.ConsumedBy[i] = pyStr(x)
			}
			if len(fm.ConsumedBy) > 0 && !containsStr(fm.ConsumedBy, "orch-atomizer") {
				fm.Warnings = append(fm.Warnings, fmt.Sprintf(
					"frontmatter consumed_by=%s no incluye 'orch-atomizer' "+
						"— este archivo puede no estar destinado al atomizer", pyStrListQuoted(fm.ConsumedBy)))
			}
		} else {
			fm.Warnings = append(fm.Warnings, "frontmatter consumed_by debe ser lista — ignorado")
		}
	}

	if v, ok := m["generated_by"]; ok {
		if s, ok := v.(string); ok {
			fm.GeneratedBy = s
		}
	}

	if v, ok := m["generated_at"]; ok {
		fm.GeneratedAt = pyStr(v)
	}

	if v, ok := m["title"]; ok {
		if s, ok := v.(string); ok {
			fm.Title = s
		}
	}

	return fm, body
}

// asStringMap normalizes the two shapes yaml.v3 may hand back for a
// mapping node decoded into `any` (map[string]any in modern versions,
// map[any]any is kept as a defensive fallback) into map[string]any.
func asStringMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			s, ok := k.(string)
			if !ok {
				return nil, false
			}
			out[s] = val
		}
		return out, true
	default:
		return nil, false
	}
}

// asPythonInt reports whether v decodes as a YAML int the way Python's
// `isinstance(ph, int) and not isinstance(ph, bool)` would accept it: a
// whole number, not a boolean (Go's yaml decoder never confuses the two —
// bool and int are distinct types on decode — so the bool exclusion is
// implicit here rather than an explicit check).
func asPythonInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		// #nosec G115 -- a spec's frontmatter `phase` is a small task-phase
		// number; yaml.v3 only produces uint64 here for a value too large
		// for int64, which is already not a sane phase and would fail the
		// same way a huge Python int would fail this field's real use.
		return int(n), true
	default:
		return 0, false
	}
}

// pyStr mirrors Python's str(x) for the scalar YAML types this parser
// forwards without validation (version, generated_at, depends_on/
// consumed_by entries).
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	default:
		return fmt.Sprint(x)
	}
}

func pyTypeName(v any) string {
	switch v.(type) {
	case string:
		return "str"
	case bool:
		return "bool"
	case int, int64, uint64:
		return "int"
	case float64:
		return "float"
	case []any:
		return "list"
	case map[string]any, map[any]any:
		return "dict"
	case nil:
		return "NoneType"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// pyStrList renders ["a", "b"] the way Python's f"{sorted_list}" does for a
// list of plain identifiers (used for the fixed, already-sorted valid-types
// set) — e.g. "['arch', 'poc', 'prd', 'spec', 'task-batch']".
func pyStrList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "'" + s + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// pyStrListQuoted mirrors Python's f"{consumed_by}" for a plain list of
// strings straight from YAML (order preserved, not sorted).
func pyStrListQuoted(items []string) string {
	return pyStrList(items)
}
