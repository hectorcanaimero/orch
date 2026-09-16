// Package sitegen is what the public site (site/) needs from orch's own code:
// the playground's spec → DAG build, run in the browser as WebAssembly
// (cmd/orch-wasm), and the static template gallery written by cmd/sitegen.
//
// Both draw the dependency graph with DAGSVG, so a template page and the
// playground show a plan the same way.
package sitegen

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
)

const (
	nodeW     = 220
	nodeH     = 66
	gapX      = 28
	gapY      = 52
	pad       = 12
	lineChars = 30
)

// DAGSVG draws tasks top to bottom: a task sits one layer below the deepest
// task it depends on, and each layer is ordered by where its dependencies sit.
// The critical path is traced only when some task is off it — on a straight
// line every task is critical and the trace says nothing.
//
// ponytail: longest-path layering and one barycenter pass; an edge that skips
// layers can pass behind a node. Fine for template-sized plans (tens of tasks);
// port web/src/lib/graphLayout.ts's second sweep if real specs get tangled.
//
// It returns "" when the graph has a cycle: there is no layering to draw, and
// graph.Validate already reports the cycle.
func DAGSVG(tasks []model.Task) string {
	if len(tasks) == 0 || len(graph.FindCycles(tasks)) > 0 {
		return ""
	}
	byID := make(map[string]model.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}

	depth := map[string]int{}
	var depthOf func(id string) int
	depthOf = func(id string) int {
		if d, ok := depth[id]; ok {
			return d
		}
		d := 0
		for _, dep := range byID[id].Dependencies {
			if _, known := byID[dep]; known && dep != id {
				d = max(d, depthOf(dep)+1)
			}
		}
		depth[id] = d
		return d
	}

	var layers [][]string
	for _, t := range graph.DisplayOrder(tasks) {
		d := depthOf(t.ID)
		for len(layers) <= d {
			layers = append(layers, nil)
		}
		layers[d] = append(layers[d], t.ID)
	}

	pos := map[string]float64{}
	widest := 0
	for li, layer := range layers {
		if li > 0 {
			bary := func(id string) float64 {
				sum, n := 0.0, 0
				for _, dep := range byID[id].Dependencies {
					if p, ok := pos[dep]; ok {
						sum += p
						n++
					}
				}
				if n == 0 {
					return 0
				}
				return sum / float64(n)
			}
			sort.SliceStable(layer, func(i, j int) bool { return bary(layer[i]) < bary(layer[j]) })
		}
		for i, id := range layer {
			pos[id] = float64(i) - float64(len(layer)-1)/2
		}
		widest = max(widest, len(layer))
	}

	width := widest*(nodeW+gapX) - gapX + 2*pad
	height := len(layers)*(nodeH+gapY) - gapY + 2*pad
	center := func(id string) (float64, float64) {
		x := float64(width)/2 + pos[id]*float64(nodeW+gapX)
		y := float64(pad + depth[id]*(nodeH+gapY))
		return x, y
	}

	critical := graph.CriticalPath(tasks)
	trace := len(critical) < len(tasks)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="dag" viewBox="0 0 %d %d" width="%d" height="%d" style="--w:%dpx" role="img" aria-label="Dependency graph: %d tasks in %d layers">`,
		width, height, width, height, width, len(tasks), len(layers))
	b.WriteString(`<defs><marker id="dag-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L8 4L0 8z" class="dag-arrowhead"/></marker></defs>`)

	for _, t := range tasks {
		tx, ty := center(t.ID)
		for _, dep := range t.Dependencies {
			if _, ok := byID[dep]; !ok || dep == t.ID {
				continue
			}
			sx, sy := center(dep)
			sy += nodeH
			mid := (ty - sy) / 2
			class := "dag-edge"
			if trace && critical[dep] && critical[t.ID] {
				class += " is-critical"
			}
			fmt.Fprintf(&b, `<path class="%s" d="M%.1f %.1f C%.1f %.1f %.1f %.1f %.1f %.1f" marker-end="url(#dag-arrow)"/>`,
				class, sx, sy, sx, sy+mid, tx, ty-mid, tx, ty-2)
		}
	}

	for _, t := range tasks {
		cx, y := center(t.ID)
		x := cx - nodeW/2
		class := "dag-node"
		if trace && critical[t.ID] {
			class += " is-critical"
		}
		fmt.Fprintf(&b, `<g class="%s"><title>%s</title>`, class, html.EscapeString(t.ID+" — "+t.Title))
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%d" height="%d" rx="12"/>`, x, y, nodeW, nodeH)
		fmt.Fprintf(&b, `<text class="dag-id" x="%.1f" y="%.1f">%s</text>`, x+14, y+22, html.EscapeString(t.ID))
		for i, line := range wrap(t.Title, 2) {
			fmt.Fprintf(&b, `<text class="dag-title" x="%.1f" y="%.1f">%s</text>`, x+14, y+40+float64(i)*16, html.EscapeString(line))
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// wrap breaks s into at most n lines of lineChars, ellipsizing what is left.
func wrap(s string, n int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len([]rune(line))+1+len([]rune(w)) <= lineChars:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	for i, l := range lines {
		if r := []rune(l); len(r) > lineChars {
			lines[i] = string(r[:lineChars-1]) + "…"
		}
	}
	if len(lines) > n {
		last := []rune(lines[n-1])
		if len(last) > lineChars-1 {
			last = last[:lineChars-1]
		}
		lines = append(lines[:n-1], string(last)+"…")
	}
	return lines
}
