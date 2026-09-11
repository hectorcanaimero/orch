package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
)

// DOT renders the DAG as Graphviz DOT.
//
// **This replaces Python's `orch graph`, it does not port it.** `graph.py`
// writes a self-contained HTML page with inline SVG; there is no DOT anywhere
// in the Python tree. The visual DAG lives in the dashboard now
// (GraphPage + /api/graph), so the CLI's job is the machine-readable form:
// pipeable into `dot`, greppable, and diffable in review. The change is
// recorded in docs/brainstorm/go-migration-notes.md as a visible contract
// change for anyone scripting `orch graph --out plan.html`.
//
// The output is deterministic — phases in ascending order, tasks by id within
// a phase, edges in the order their dependencies are declared. Determinism is
// not a nicety here: it is what lets the output be a golden file and what
// keeps a re-render out of a diff.
//
// Tasks are grouped into a subgraph per phase so `dot` lays them out in
// columns, which is the one thing the HTML version did that was actually
// worth keeping.
func DOT(tasks []model.Task) string {
	var b strings.Builder
	b.WriteString("digraph orch {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=box, style=rounded, fontname=\"Helvetica\"];\n")
	b.WriteString("  edge [color=\"#666666\"];\n")

	// A task with no id is dropped, for the same reason a dangling dependency
	// is: DOT names nodes by string, so an id-less task becomes a node named
	// "" — and a second one silently becomes the SAME node, so a graph with
	// two of them draws one box and the reader has no way to tell. Its label
	// would also start with a newline, `nodeLabel` being id + "\n" + title.
	//
	// A task with no id is a validation error (`schema.tasks`), and
	// `orch validate` is where it gets said out loud. DOT's job is to draw
	// what is there, and there is nothing here it can draw honestly.
	ordered := make([]model.Task, 0, len(tasks))
	for _, t := range DisplayOrder(tasks) {
		if t.ID != "" {
			ordered = append(ordered, t)
		}
	}

	// Group by phase, preserving the (phase, id) order within each group.
	byPhase := map[int][]model.Task{}
	var phases []int
	for _, t := range ordered {
		if _, seen := byPhase[t.Phase]; !seen {
			phases = append(phases, t.Phase)
		}
		byPhase[t.Phase] = append(byPhase[t.Phase], t)
	}
	sort.Ints(phases)

	for _, phase := range phases {
		fmt.Fprintf(&b, "\n  subgraph cluster_phase_%d {\n", phase)
		fmt.Fprintf(&b, "    label=%s;\n", dotQuote(fmt.Sprintf("phase %d", phase)))
		b.WriteString("    style=dashed;\n")
		b.WriteString("    color=\"#cccccc\";\n")
		for _, t := range byPhase[phase] {
			fmt.Fprintf(&b, "    %s [label=%s%s];\n",
				dotQuote(t.ID), dotQuote(nodeLabel(t)), statusAttrs(t.Status))
		}
		b.WriteString("  }\n")
	}

	// Edges last, outside the clusters: an edge declared inside a subgraph
	// pulls its target into that cluster, which would put a dependency in
	// the wrong phase box.
	known := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		known[t.ID] = true
	}

	var edges []string
	for _, t := range ordered {
		for _, dep := range t.Dependencies {
			// A self-loop and a dangling dependency are both validation
			// errors, not drawings. Worse than useless to render: DOT
			// declares a node implicitly on first mention, so an edge from a
			// task that does not exist adds a silent unlabelled box to the
			// picture and the reader has no way to tell it apart from a real
			// one. `orch validate` is where a missing dependency gets said
			// out loud.
			if dep == t.ID || !known[dep] {
				continue
			}
			edges = append(edges, fmt.Sprintf("  %s -> %s;\n", dotQuote(dep), dotQuote(t.ID)))
		}
	}
	if len(edges) > 0 {
		b.WriteString("\n")
		for _, e := range edges {
			b.WriteString(e)
		}
	}

	b.WriteString("}\n")
	return b.String()
}

// nodeLabel is the id and the title on two lines, or just the id when the
// task has no title.
func nodeLabel(t model.Task) string {
	if t.Title == "" {
		return t.ID
	}
	return t.ID + "\n" + t.Title
}

// statusAttrs colours a node by status. Empty for `todo` and for anything
// unrecognised: an unknown status should read as "no information", not as a
// colour someone has to look up.
func statusAttrs(s model.Status) string {
	switch s {
	case model.StatusDone:
		return `, style="rounded,filled", fillcolor="#dcf5e5", color="#15803d"`
	case model.StatusInProgress:
		return `, style="rounded,filled", fillcolor="#fdeed3", color="#b45309"`
	case model.StatusBlocked:
		return `, style="rounded,filled", fillcolor="#fde2e2", color="#b91c1c"`
	case model.StatusBacklog:
		return `, color="#999999", fontcolor="#666666"`
	default:
		return ""
	}
}

// dotQuote wraps a value in double quotes, escaping what DOT requires.
//
// Task ids come from a user's tasks.json, so a quote or a backslash in one is
// possible; unescaped it would produce a file `dot` refuses to parse. `\n` is
// left as the two-character escape on purpose — inside a quoted DOT label
// that is a line break, which is what a two-line node label needs.
func dotQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
