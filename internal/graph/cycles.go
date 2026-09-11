package graph

import "github.com/hectorcanaimero/orch/internal/model"

// FindCycles returns every dependency cycle as a path of task ids, closed —
// `A -> C -> B -> A`. Ported from `preflight.find_cycles`, including the
// details that decide WHICH path is reported, because `orch validate`'s output
// is diffed between the two binaries.
//
// Three-colour DFS: white unvisited, grey on the stack, black done. A back
// edge to a grey node closes a cycle, and the path is reconstructed from the
// parent chain so the message names the actual edges rather than just
// asserting a cycle exists.
//
// Self-loops are excluded: `validateDependencies` already reports them as
// `dep.cycle` with a clearer message, and including them here would report
// the same mistake twice.
//
// The DFS is iterative. Python's comment says "to avoid stack overflow on very
// deep graphs"; Go's stacks grow, so the reason here is different and smaller
// — an iterative port is easier to check line-by-line against the original
// than a recursive rewrite would be, and this function's whole value is being
// the same as the original.
func FindCycles(tasks []model.Task) [][]string {
	// Insertion order matters: it decides which node a cycle is entered
	// from, and therefore which path gets recorded before canonicalisation.
	order := make([]string, 0, len(tasks))
	adj := make(map[string][]string, len(tasks))
	color := make(map[string]int, len(tasks))
	parent := make(map[string]string, len(tasks))

	for _, t := range tasks {
		if _, seen := adj[t.ID]; !seen {
			order = append(order, t.ID)
		}
		deps := make([]string, 0, len(t.Dependencies))
		for _, d := range t.Dependencies {
			if d != t.ID { // self-loops handled elsewhere
				deps = append(deps, d)
			}
		}
		adj[t.ID] = deps
		color[t.ID] = 0
	}

	var cycles [][]string
	seen := map[string]bool{}

	recordCycle := func(closingFrom, backEdgeTarget string) {
		// Walk the parent chain back to the node the back edge points at.
		var path []string
		node := closingFrom
		for node != "" && node != backEdgeTarget && len(path) <= len(tasks) {
			path = append(path, node)
			node = parent[node]
		}
		path = append(path, backEdgeTarget)
		reverse(path)
		if len(path) == 0 {
			return
		}

		// Canonicalise by rotating the smallest id to the front, so the same
		// loop reached from two different entry points is reported once.
		smallest := 0
		for i, n := range path {
			if n < path[smallest] {
				smallest = i
			}
		}
		rotated := append(append([]string{}, path[smallest:]...), path[:smallest]...)

		key := joinArrows(rotated)
		if seen[key] {
			return
		}
		seen[key] = true
		cycles = append(cycles, append(rotated, rotated[0]))
	}

	type frame struct {
		node string
		idx  int
	}

	visit := func(start string) {
		stack := []frame{{start, 0}}
		for len(stack) > 0 {
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if f.idx == 0 {
				if color[f.node] != 0 {
					continue
				}
				color[f.node] = 1
			}
			children := adj[f.node]
			if f.idx < len(children) {
				stack = append(stack, frame{f.node, f.idx + 1})
				child := children[f.idx]
				if _, known := color[child]; !known {
					// Unknown child: reported as dep.missing elsewhere.
					continue
				}
				switch color[child] {
				case 1:
					recordCycle(f.node, child)
				case 0:
					parent[child] = f.node
					stack = append(stack, frame{child, 0})
				}
			} else {
				color[f.node] = 2
			}
		}
	}

	for _, id := range order {
		if color[id] == 0 {
			visit(id)
		}
	}
	return cycles
}

func reverse(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
