package atomize

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
)

// reNewFormatID matches the atomizer's own ID shape (F<n>.<n>.T<n>), used to
// separate genuinely orphaned atomizer tasks from legacy pre-atomizer IDs
// (R-001, B-003, ...) in the diff output. Ports _looks_new_format.
var reNewFormatID = regexp.MustCompile(`^F\d+\.\d+\.T\d+$`)

// RenderDiff renders diff as human-readable, deterministic plain text —
// the CLI's "--apply not passed" preview and the body of a dry-run report.
// It follows the same section layout as Python's render_diff (rich-colored
// console output) but is plain text: parity here is about which sections
// exist and what they say, not byte-identical formatting — Python's own
// tests for this function only assert on substrings too.
func RenderDiff(diff MergeDiff, parse ParseResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "=== Atomizer diff ===\n")
	fmt.Fprintf(&b, "Specs escaneados: %d archivos, %d tasks encontradas\n",
		len(parse.FilesScanned), len(parse.Tasks))

	if len(parse.Warnings) > 0 {
		fmt.Fprintf(&b, "Parser warnings: %d\n", len(parse.Warnings))
		shown := parse.Warnings
		if len(shown) > 10 {
			shown = shown[:10]
		}
		for _, w := range shown {
			fmt.Fprintf(&b, "  · %s\n", w)
		}
		if len(parse.Warnings) > 10 {
			fmt.Fprintf(&b, "  ... +%d más\n", len(parse.Warnings)-10)
		}
	}

	fmt.Fprintf(&b, "\n+ NUEVAS (%d):\n", len(diff.NewTasks))
	for _, row := range diff.NewTasks {
		deps := "—"
		if len(row.Dependencies) > 0 {
			deps = strings.Join(row.Dependencies, ", ")
		}
		fmt.Fprintf(&b, "  + %-14s — %-45s %-24s %gh  deps: %s\n",
			row.ID, row.Title, row.Model, row.EstimateHours, deps)
	}

	fmt.Fprintf(&b, "\n~ ACTUALIZADAS (%d):\n", len(diff.Updated))
	for _, u := range diff.Updated {
		fmt.Fprintf(&b, "  ~ %s — %s\n", u.New.ID, u.New.Title)
		for _, f := range u.Changed {
			fmt.Fprintf(&b, "    - %s:  %s  →  %s\n", f, fieldValue(u.Old, f), fieldValue(u.New, f))
		}

	}

	fmt.Fprintf(&b, "\n= SIN CAMBIOS (%d)\n", len(diff.Unchanged))

	if len(diff.Orphans) > 0 {
		fmt.Fprintf(&b, "\n⚠ HUÉRFANAS en tasks.json (no en specs actuales, NO se borran) (%d):\n", len(diff.Orphans))
		for _, row := range diff.Orphans {
			if reNewFormatID.MatchString(row.ID) {
				fmt.Fprintf(&b, "  ? %-14s (status=%s) — %s\n", row.ID, row.Status, row.Title)
			}
		}
		for _, row := range diff.Orphans {
			if !reNewFormatID.MatchString(row.ID) {
				fmt.Fprintf(&b, "  ? %-14s (status=%s) — %s [formato legacy, ok]\n", row.ID, row.Status, row.Title)
			}
		}
	}

	if len(diff.DepWarnings) > 0 {
		fmt.Fprintf(&b, "\n⚠ DEPENDENCIAS HUÉRFANAS (%d):\n", len(diff.DepWarnings))
		shown := diff.DepWarnings
		if len(shown) > 20 {
			shown = shown[:20]
		}
		for _, w := range shown {
			fmt.Fprintf(&b, "  · %s\n", w)
		}
		if len(diff.DepWarnings) > 20 {
			fmt.Fprintf(&b, "  ... +%d más\n", len(diff.DepWarnings)-20)
		}
	}

	b.WriteString("\n")
	if len(diff.NewTasks) > 0 || len(diff.Updated) > 0 {
		b.WriteString("Para aplicar: agregá --apply\n")
	} else {
		b.WriteString("Nada que aplicar.\n")
	}

	return b.String()
}

// fieldValue renders one declarative field of t for the ACTUALIZADAS
// section, short enough to keep the diff scannable.
func fieldValue(t model.Task, field string) string {
	switch field {
	case "title":
		return shortDiffValue(t.Title)
	case "description":
		return shortDiffValue(t.Description)
	case "model":
		return shortDiffValue(t.Model)
	case "reason":
		return shortDiffValue(t.Reason)
	case "dependencies":
		return shortDiffList(t.Dependencies)
	case "estimateHours":
		return fmt.Sprintf("%g", t.EstimateHours)
	case "specRef":
		return shortDiffValue(t.SpecRef)
	case "phase":
		return fmt.Sprintf("%d", t.Phase)
	default:
		return ""
	}
}

// shortDiffValue trims a string value the way Python's _short does for the
// common case: empty renders as ∅, long strings are truncated with "...".
func shortDiffValue(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" {
		return `""`
	}
	if len(v) > 60 {
		return v[:57] + "..."
	}
	return v
}

func shortDiffList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	shown := items
	suffix := "]"
	if len(shown) > 4 {
		shown = shown[:4]
		suffix = "...]"
	}
	return "[" + strings.Join(shown, ", ") + suffix
}
