import "@testing-library/jest-dom/vitest"
import { render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { EXAMPLE_PORTFOLIO } from "@/lib/portfolio.example"
import { PortfolioPage } from "@/pages/PortfolioPage"

const mockUsePortfolio = vi.fn()
vi.mock("@/hooks/usePortfolio", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/usePortfolio")>(
    "@/hooks/usePortfolio",
  )
  return {
    ...actual,
    usePortfolio: (opts?: { enabled?: boolean }) => mockUsePortfolio(opts),
  }
})

// PortfolioPage also calls useWhoami (to gate its own usePortfolio call
// for a stakeholder who navigates here directly, per opus's review) —
// mocked the same way as usePortfolio so this stays a plain component
// test with no real QueryClientProvider/network needed. Defaulted to
// operator; the one test below that cares about stakeholder overrides it.
const mockUseWhoami = vi.fn(() => ({ data: { profile: "operator" } }))
vi.mock("@/hooks/useWhoami", () => ({
  useWhoami: () => mockUseWhoami(),
}))

describe("PortfolioPage", () => {
  afterEach(() => {
    vi.clearAllMocks()
  })

  it("fits five projects on one screen, each with its own row", () => {
    mockUsePortfolio.mockReturnValue({
      data: EXAMPLE_PORTFOLIO,
      isLoading: false,
      isError: false,
      error: undefined,
    })

    render(<PortfolioPage />)

    // EXAMPLE_PORTFOLIO has exactly five entries in `projects` — this
    // fixture IS the "five projects, one screen" acceptance criterion,
    // and every one of them renders as its own named row.
    expect(EXAMPLE_PORTFOLIO.projects).toHaveLength(5)
    for (const project of EXAMPLE_PORTFOLIO.projects) {
      expect(screen.getByText(project.project_name)).toBeInTheDocument()
    }
  })

  it("shows the unavailable directory's reason, not just that it was skipped", () => {
    mockUsePortfolio.mockReturnValue({
      data: EXAMPLE_PORTFOLIO,
      isLoading: false,
      isError: false,
      error: undefined,
    })

    render(<PortfolioPage />)

    const [unavailable] = EXAMPLE_PORTFOLIO.unavailable
    expect(screen.getByText(unavailable.root)).toBeInTheDocument()
    expect(screen.getByText(unavailable.reason)).toBeInTheDocument()
  })

  it("renders an unavailable PROJECT's own reason as a badge, not just its name", () => {
    // data-pipeline in the fixture is `available: false` — a different
    // case from the top-level `unavailable[]` list (a directory that
    // isn't a project at all vs. a real project whose own DB wouldn't
    // open).
    mockUsePortfolio.mockReturnValue({
      data: EXAMPLE_PORTFOLIO,
      isLoading: false,
      isError: false,
      error: undefined,
    })

    render(<PortfolioPage />)

    const broken = EXAMPLE_PORTFOLIO.projects.find((p) => !p.available)
    expect(broken).toBeDefined()
    expect(screen.getByText(broken!.reason!)).toBeInTheDocument()
  })

  it("shows the portfolio-off message on a 404, not a generic error", () => {
    mockUsePortfolio.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: { response: { status: 404 } },
    })

    render(<PortfolioPage />)

    expect(screen.getByText(/Portfolio mode is off/i)).toBeInTheDocument()
  })

  // opus's review finding: usePortfolio's own doc comment promises a
  // stakeholder session never issues the request, but AppLayout was the
  // only caller actually honoring that — a stakeholder landing on
  // /portfolio directly (bookmark, typed URL) went through this
  // component's OWN unguarded usePortfolio() call. Fixed by reading
  // useWhoami here too; this test is what would have caught the gap.
  it("disables its own portfolio query for a stakeholder session", () => {
    mockUseWhoami.mockReturnValueOnce({ data: { profile: "stakeholder" } })
    mockUsePortfolio.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: false,
      error: undefined,
    })

    render(<PortfolioPage />)

    expect(mockUsePortfolio).toHaveBeenCalledWith({ enabled: false })
  })

  it("enables its portfolio query for an operator session", () => {
    mockUsePortfolio.mockReturnValue({
      data: EXAMPLE_PORTFOLIO,
      isLoading: false,
      isError: false,
      error: undefined,
    })

    render(<PortfolioPage />)

    expect(mockUsePortfolio).toHaveBeenCalledWith({ enabled: true })
  })
})
