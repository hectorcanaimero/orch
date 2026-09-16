import "@testing-library/jest-dom/vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom"
import { describe, expect, it, vi } from "vitest"
import { AppLayout } from "@/components/AppLayout"

vi.mock("@/hooks/useWhoami", () => ({ useWhoami: () => ({ data: { profile: "operator" } }) }))
vi.mock("@/hooks/usePortfolio", () => ({
  usePortfolio: () => ({ data: undefined, error: undefined }),
  isPortfolioNavVisible: () => false,
}))
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ clearToken: vi.fn() }) }))
vi.mock("@/pages/LogsPage", () => ({ LogsPage: () => <p>event log</p> }))

function Where() {
  const { pathname, search } = useLocation()
  return <output aria-label="location">{pathname + search}</output>
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          path="*"
          element={
            <AppLayout>
              <Where />
            </AppLayout>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

const hrefs = (nav: HTMLElement) => within(nav).getAllByRole("link").map((a) => a.getAttribute("href"))

describe("AppLayout", () => {
  // At 390px a sideways strip of eleven labels cut "Milestones" in half. The
  // phone gets the same four destinations as the sidebar, as a bottom bar.
  // jsdom applies no media queries, so this checks the structure; the
  // breakpoint classes are what switch between the two.
  it("offers the same four destinations in the sidebar and the phone's bottom bar", () => {
    renderAt("/")
    const sidebar = screen.getByRole("navigation", { name: "Main" })
    const bottom = screen.getByRole("navigation", { name: "Main (small screens)" })
    expect(hrefs(sidebar)).toEqual(["/", "/work/list", "/cost/budget", "/delivery/summary"])
    expect(hrefs(bottom)).toEqual(hrefs(sidebar))
    expect(bottom).toHaveClass("md:hidden")
    expect(sidebar.closest("aside")).toHaveClass("hidden", "md:flex")
  })

  it("shows a destination's pages as tabs that keep the filters in the query string", () => {
    renderAt("/work/kanban?status=blocked&logs=1")
    const tabs = screen.getByRole("navigation", { name: "Sections" })
    expect(hrefs(tabs)).toEqual([
      "/work/list?status=blocked",
      "/work/kanban?status=blocked",
      "/work/graph?status=blocked",
      "/work/phases?status=blocked",
      "/work/pace?status=blocked",
    ])
    expect(within(tabs).getByRole("link", { current: "page" })).toHaveTextContent("Kanban")
  })

  it("has no tab strip on a destination with a single page", () => {
    renderAt("/")
    expect(screen.queryByRole("navigation", { name: "Sections" })).not.toBeInTheDocument()
  })

  it("opens the event log over the current page and closes it again", () => {
    renderAt("/cost/budget")
    expect(screen.queryByText("event log")).not.toBeInTheDocument()
    fireEvent.click(within(screen.getByRole("complementary")).getByRole("button", { name: "Logs" }))
    expect(screen.getByRole("region", { name: "Logs" })).toHaveTextContent("event log")
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/cost/budget?logs=1")
    fireEvent.click(screen.getByRole("button", { name: "Close logs" }))
    expect(screen.queryByText("event log")).not.toBeInTheDocument()
  })
})
