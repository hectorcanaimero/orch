import { describe, expect, it } from "vitest"
import { layoutGraph, wrapLabel } from "./graphLayout"

const n = (id: string) => ({ id, label: `Task ${id}` })

describe("layoutGraph", () => {
  it("puts a task one layer below the deepest task it depends on", () => {
    // a → b → c, and a → c directly: c must still sit below b.
    const { nodes } = layoutGraph([n("a"), n("b"), n("c")], [
      { source: "a", target: "b" },
      { source: "b", target: "c" },
      { source: "a", target: "c" },
    ])
    const y = Object.fromEntries(nodes.map((node) => [node.id, node.y]))
    expect(y.a).toBeLessThan(y.b)
    expect(y.b).toBeLessThan(y.c)
  })

  it("never overlaps two nodes", () => {
    const ids = "abcdefgh".split("")
    const edges = [
      { source: "a", target: "c" },
      { source: "b", target: "c" },
      { source: "a", target: "d" },
      { source: "c", target: "e" },
      { source: "d", target: "f" },
      { source: "b", target: "g" },
      { source: "g", target: "h" },
    ]
    const { nodes } = layoutGraph(ids.map(n), edges)
    for (const p of nodes) {
      for (const q of nodes) {
        if (p === q) continue
        const apart = p.x + p.width <= q.x || q.x + q.width <= p.x || p.y + p.height <= q.y || q.y + q.height <= p.y
        expect(apart, `${p.id} overlaps ${q.id}`).toBe(true)
      }
    }
  })

  it("orders a layer by where its parents sit, uncrossing edges", () => {
    // Listed crossed: x's child is second, y's child is first.
    const { nodes } = layoutGraph([n("x"), n("y"), n("cy"), n("cx")], [
      { source: "x", target: "cx" },
      { source: "y", target: "cy" },
    ])
    const x = Object.fromEntries(nodes.map((node) => [node.id, node.x]))
    expect(Math.sign(x.cx - x.cy)).toBe(Math.sign(x.x - x.y))
  })

  it("still draws a cycle and ignores edges to unknown tasks", () => {
    const layout = layoutGraph([n("a"), n("b")], [
      { source: "a", target: "b" },
      { source: "b", target: "a" },
      { source: "a", target: "ghost" },
    ])
    expect(layout.nodes).toHaveLength(2)
    expect(layout.edges).toHaveLength(2)
    expect(layout.edges.every((e) => e.d.startsWith("M"))).toBe(true)
  })

  it("draws nothing for no tasks", () => {
    expect(layoutGraph([], [])).toMatchObject({ nodes: [], edges: [], height: 0 })
  })
})

describe("wrapLabel", () => {
  it("wraps on words and caps at three lines with an ellipsis", () => {
    const lines = wrapLabel("Stripe customer sync with webhooks retries idempotency keys and a dead letter queue for failures")
    expect(lines).toHaveLength(3)
    expect(lines.every((l) => l.length <= 26)).toBe(true)
    expect(lines[2].endsWith("…")).toBe(true)
  })

  it("cuts a single word longer than a line", () => {
    expect(wrapLabel("x".repeat(40))[0]).toHaveLength(26)
  })
})
