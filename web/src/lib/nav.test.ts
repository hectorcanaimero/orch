import { describe, expect, it } from "vitest"
import { NAV_ITEMS, isPathAllowed, visibleNavItems } from "@/lib/nav"

// The stakeholder allow-list exactly as the Go server sends it in /api/whoami
// (internal/dashboard/access.go DefaultStakeholderRoutes).
const STAKEHOLDER_ROUTES = [
  "stakeholder_summary_json",
  "api_tunnel_capabilities",
  "api_whoami",
  "api_docs_list",
  "api_docs_content",
  "spa",
]

const labels = (items: { label: string }[]) => items.map((i) => i.label)

describe("visibleNavItems", () => {
  // The bug: the sidebar offered Tasks, Kanban, Milestones and Sprint to a
  // stakeholder token that the server answered 403 on every one of them.
  it("shows a stakeholder only the pages whose data route the server allows", () => {
    const items = visibleNavItems({
      isStakeholder: true,
      allowedRoutes: STAKEHOLDER_ROUTES,
      portfolioAvailable: false,
    })
    expect(labels(items)).toEqual(["Summary"])
  })

  it("follows a wider allow-list when the server grants one", () => {
    const items = visibleNavItems({
      isStakeholder: true,
      allowedRoutes: [...STAKEHOLDER_ROUTES, "api_tasks", "api_sprint"],
      portfolioAvailable: false,
    })
    expect(labels(items)).toEqual(["Summary", "Tasks", "Kanban", "Sprint"])
  })

  it("falls back to hiding operator-only pages when an older server sends no list", () => {
    const items = visibleNavItems({ isStakeholder: true, allowedRoutes: undefined, portfolioAvailable: false })
    expect(labels(items)).not.toContain("Logs")
    expect(labels(items)).toContain("Summary")
  })

  it("shows an operator everything, Portfolio only when available", () => {
    expect(labels(visibleNavItems({ isStakeholder: false, portfolioAvailable: false }))).toEqual(
      NAV_ITEMS.filter((i) => !i.portfolioGated).map((i) => i.label),
    )
    expect(labels(visibleNavItems({ isStakeholder: false, portfolioAvailable: true }))).toContain("Portfolio")
  })
})

describe("isPathAllowed", () => {
  it("rejects a page a stakeholder opens by URL when its route is not allowed", () => {
    expect(isPathAllowed("/kanban", { isStakeholder: true, allowedRoutes: STAKEHOLDER_ROUTES })).toBe(false)
    expect(isPathAllowed("/", { isStakeholder: true, allowedRoutes: STAKEHOLDER_ROUTES })).toBe(true)
  })

  it("never blocks an operator, an unknown path, or a server that sent no list", () => {
    expect(isPathAllowed("/kanban", { isStakeholder: false })).toBe(true)
    expect(isPathAllowed("/login", { isStakeholder: true, allowedRoutes: STAKEHOLDER_ROUTES })).toBe(true)
    expect(isPathAllowed("/kanban", { isStakeholder: true, allowedRoutes: undefined })).toBe(true)
  })
})
