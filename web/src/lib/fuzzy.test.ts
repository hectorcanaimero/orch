import { describe, expect, it } from "vitest"
import { fuzzyScore } from "@/lib/fuzzy"

describe("fuzzyScore", () => {
  it("matches every item on an empty query", () => {
    expect(fuzzyScore("  ", "Kanban")).toBeGreaterThan(0)
  })

  it("needs the query's letters in order", () => {
    expect(fuzzyScore("knb", "Kanban")).toBeGreaterThan(0)
    expect(fuzzyScore("bnk", "Kanban")).toBe(0)
    expect(fuzzyScore("zz", "Kanban")).toBe(0)
  })

  it("ranks a substring above a scattered match, and a word start above the middle of a word", () => {
    const substring = fuzzyScore("bud", "Cost › Budget")
    const scattered = fuzzyScore("bgt", "Cost › Budget")
    expect(substring).toBeGreaterThan(scattered)
    expect(fuzzyScore("list", "Work › List")).toBeGreaterThan(fuzzyScore("list", "Checklist"))
  })

  it("ignores case and the spaces in a query", () => {
    expect(fuzzyScore("F2 T3", "F2.T3 Add DELETE")).toBeGreaterThan(0)
    expect(fuzzyScore("DELETE", "add delete")).toBe(fuzzyScore("delete", "add delete"))
  })
})
