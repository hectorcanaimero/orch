import "@testing-library/jest-dom/vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { MemoryRouter } from "react-router-dom"
import { describe, expect, it, vi } from "vitest"
import { AppLayout } from "@/components/AppLayout"

vi.mock("@/hooks/useWhoami", () => ({ useWhoami: () => ({ data: { profile: "operator" } }) }))
vi.mock("@/hooks/usePortfolio", () => ({
  usePortfolio: () => ({ data: undefined, error: undefined }),
  isPortfolioNavVisible: () => false,
}))
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ clearToken: vi.fn() }) }))

// At 390px the fixed sidebar stayed and pushed the page 68px past the screen
// ("Export", "Copy" and "86%" cut off). Below md the sidebar gives way to a
// top bar whose menu carries the same pages — a menu, because a sideways strip
// of eleven labels cut "Milestones" in half with no hint it scrolled. jsdom
// applies no media queries, so this checks the structure; the breakpoint
// classes are what switch between the two.
describe("AppLayout", () => {
  it("offers the same pages in a top bar for small screens as in the sidebar", () => {
    render(
      <MemoryRouter>
        <AppLayout>
          <p>page</p>
        </AppLayout>
      </MemoryRouter>,
    )
    const sidebar = screen.getByRole("navigation", { name: "Main" })
    fireEvent.click(screen.getByRole("button", { name: "Menu" }))
    const topbar = screen.getByRole("navigation", { name: "Main (small screens)" })
    const names = (nav: HTMLElement) => within(nav).getAllByRole("link").map((a) => a.getAttribute("href"))
    expect(names(topbar)).toEqual(names(sidebar))
    expect(screen.getByRole("button", { name: "Menu" }).closest("header")).toHaveClass("md:hidden")
    expect(sidebar.closest("aside")).toHaveClass("hidden", "md:flex")
  })
})
