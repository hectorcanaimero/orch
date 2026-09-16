import "@testing-library/jest-dom/vitest"
import type { ReactNode } from "react"
import { render, screen } from "@testing-library/react"
import { MemoryRouter, useLocation } from "react-router-dom"
import { describe, expect, it, vi } from "vitest"
import { AppRoutes } from "@/App"

// Every page is a stub that names where the router put it: this is about the
// addresses, not the pages.
function Where() {
  const { pathname, search } = useLocation()
  return <output aria-label="location">{pathname + search}</output>
}
vi.mock("@/components/ProtectedRoute", () => ({ ProtectedRoute: ({ children }: { children: ReactNode }) => children }))
vi.mock("@/components/AppLayout", () => ({ AppLayout: ({ children }: { children: ReactNode }) => children }))
vi.mock("@/pages/BudgetPage", () => ({ BudgetPage: Where }))
vi.mock("@/pages/CIPage", () => ({ CIPage: Where }))
vi.mock("@/pages/GraphPage", () => ({ GraphPage: Where }))
vi.mock("@/pages/KanbanPage", () => ({ KanbanPage: Where }))
vi.mock("@/pages/ListPage", () => ({ ListPage: Where }))
vi.mock("@/pages/LoginPage", () => ({ LoginPage: Where }))
vi.mock("@/pages/MetricsPage", () => ({ MetricsPage: Where }))
vi.mock("@/pages/MilestonesPage", () => ({ MilestonesPage: Where }))
vi.mock("@/pages/NowPage", () => ({ NowPage: Where }))
vi.mock("@/pages/PacePage", () => ({ PacePage: Where }))
vi.mock("@/pages/PortfolioPage", () => ({ PortfolioPage: Where }))
vi.mock("@/pages/StakeholderSummaryPage", () => ({ StakeholderSummaryPage: Where }))
vi.mock("@/pages/TunnelPage", () => ({ TunnelPage: Where }))

function landing(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <AppRoutes />
    </MemoryRouter>,
  )
  return screen.getByRole("status", { name: "location" }).textContent
}

describe("routes", () => {
  it.each([
    ["/list", "/work/list"],
    ["/kanban?status=blocked", "/work/kanban?status=blocked"],
    ["/graph", "/work/graph"],
    ["/milestones", "/work/phases"],
    ["/sprint", "/work/pace"],
    ["/pace", "/work/pace"],
    ["/budget", "/cost/budget"],
    ["/metrics", "/cost/metrics"],
    ["/ci", "/delivery/ci"],
    ["/tunnel", "/delivery/share"],
    ["/logs", "/?logs=1"],
    ["/work", "/work/list"],
    ["/nowhere", "/"],
  ])("sends the old address %s to %s", (from, to) => {
    expect(landing(from)).toBe(to)
  })
})
