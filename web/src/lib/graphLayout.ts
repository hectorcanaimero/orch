/**
 * Layered layout for the task DAG, top to bottom: a task sits one layer below
 * the deepest task it depends on, and each layer is ordered by where its
 * parents (then its children) sit, which keeps most edges from crossing.
 *
 * ponytail: longest-path layering + two barycenter sweeps, no crossing
 * minimization beyond that and edges that skip layers may pass behind a node.
 * Good for task graphs of a few hundred nodes; reach for dagre/elk if the
 * drawings of real projects get tangled.
 */

export interface LayoutNode {
  id: string
  x: number
  y: number
  width: number
  height: number
  lines: string[]
}

export interface LayoutEdge {
  source: string
  target: string
  /** SVG path from the bottom of the source to the top of the target. */
  d: string
}

export interface Layout {
  nodes: LayoutNode[]
  edges: LayoutEdge[]
  width: number
  height: number
}

export const NODE_WIDTH = 200
const LINE_HEIGHT = 16
const PAD_Y = 12
const GAP_X = 32
const GAP_Y = 56
const CHARS_PER_LINE = 26
const MAX_LINES = 3

/** Word-wrap a label into at most MAX_LINES lines, ellipsizing the last. */
export function wrapLabel(label: string): string[] {
  const words = label.replace(/\s+/g, " ").trim().split(" ")
  const lines: string[] = []
  let line = ""
  for (const word of words) {
    const next = line ? `${line} ${word}` : word
    if (next.length <= CHARS_PER_LINE || !line) {
      line = next
      continue
    }
    lines.push(line)
    line = word
  }
  if (line) lines.push(line)
  const kept = lines.slice(0, MAX_LINES).map((l) => (l.length > CHARS_PER_LINE ? `${l.slice(0, CHARS_PER_LINE - 1)}…` : l))
  if (lines.length > MAX_LINES) kept[MAX_LINES - 1] = `${kept[MAX_LINES - 1].slice(0, CHARS_PER_LINE - 1)}…`
  return kept
}

export function layoutGraph(nodeList: { id: string; label: string }[], edgeList: { source: string; target: string }[]): Layout {
  const ids = nodeList.map((n) => n.id)
  const known = new Set(ids)
  const edges = edgeList.filter((e) => known.has(e.source) && known.has(e.target) && e.source !== e.target)
  const parents = new Map<string, string[]>(ids.map((id) => [id, []]))
  const children = new Map<string, string[]>(ids.map((id) => [id, []]))
  for (const e of edges) {
    parents.get(e.target)!.push(e.source)
    children.get(e.source)!.push(e.target)
  }

  // Longest-path rank via Kahn's order. A cycle (validate rejects them, but
  // the dashboard must still draw) leaves its nodes unranked: they go to 0.
  const rank = new Map<string, number>(ids.map((id) => [id, 0]))
  const indegree = new Map(ids.map((id) => [id, parents.get(id)!.length]))
  const queue = ids.filter((id) => indegree.get(id) === 0)
  while (queue.length > 0) {
    const id = queue.shift()!
    for (const child of children.get(id)!) {
      rank.set(child, Math.max(rank.get(child)!, rank.get(id)! + 1))
      indegree.set(child, indegree.get(child)! - 1)
      if (indegree.get(child) === 0) queue.push(child)
    }
  }

  const layers: string[][] = []
  for (const id of ids) {
    const r = rank.get(id)!
    ;(layers[r] ??= []).push(id)
  }
  for (let r = 0; r < layers.length; r++) layers[r] ??= []

  const order = new Map<string, number>()
  const index = () => layers.forEach((layer) => layer.forEach((id, i) => order.set(id, i)))
  const sortBy = (layer: string[], neighbours: Map<string, string[]>) => {
    const score = new Map(
      layer.map((id, i) => {
        const ns = neighbours.get(id)!
        return [id, ns.length ? ns.reduce((sum, n) => sum + order.get(n)!, 0) / ns.length : i]
      }),
    )
    layer.sort((a, b) => score.get(a)! - score.get(b)!)
  }
  index()
  for (let sweep = 0; sweep < 2; sweep++) {
    for (let r = 1; r < layers.length; r++) {
      sortBy(layers[r], parents)
      index()
    }
    for (let r = layers.length - 2; r >= 0; r--) {
      sortBy(layers[r], children)
      index()
    }
  }

  const labels = new Map(nodeList.map((n) => [n.id, wrapLabel(n.label)]))
  const widest = Math.max(...layers.map((l) => l.length), 1)
  const width = widest * NODE_WIDTH + (widest - 1) * GAP_X
  const placed = new Map<string, LayoutNode>()
  let y = 0
  for (const layer of layers) {
    const height = Math.max(...layer.map((id) => labels.get(id)!.length * LINE_HEIGHT + 2 * PAD_Y), 0)
    const rowWidth = layer.length * NODE_WIDTH + Math.max(layer.length - 1, 0) * GAP_X
    layer.forEach((id, i) => {
      placed.set(id, {
        id,
        x: (width - rowWidth) / 2 + i * (NODE_WIDTH + GAP_X),
        y,
        width: NODE_WIDTH,
        height,
        lines: labels.get(id)!,
      })
    })
    y += height + GAP_Y
  }

  return {
    nodes: ids.map((id) => placed.get(id)!),
    edges: edges.map((e) => {
      const s = placed.get(e.source)!
      const t = placed.get(e.target)!
      const x1 = s.x + s.width / 2
      const y1 = s.y + s.height
      const x2 = t.x + t.width / 2
      const y2 = t.y
      const mid = (y1 + y2) / 2
      return { source: e.source, target: e.target, d: `M${x1},${y1} C${x1},${mid} ${x2},${mid} ${x2},${y2}` }
    }),
    width,
    height: Math.max(y - GAP_Y, 0),
  }
}
