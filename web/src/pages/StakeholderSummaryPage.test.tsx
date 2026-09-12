import "@testing-library/jest-dom/vitest"
import { render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { StakeholderSummaryShapeError } from "@/hooks/useStakeholderSummary"
import { StakeholderSummaryPage } from "@/pages/StakeholderSummaryPage"

// Bug 26's own review (PR #205, opus-2's finding): fixing the login wall
// means an operator or a stakeholder with a valid token actually reaches
// this page now, and the Go dashboard doesn't implement
// /stakeholder/summary yet. This must render a named "not available"
// state, not the generic error alert and NOT a crash.
const mockUseStakeholderSummary = vi.fn()
vi.mock("@/hooks/useStakeholderSummary", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/useStakeholderSummary")>(
    "@/hooks/useStakeholderSummary",
  )
  return {
    ...actual,
    useStakeholderSummary: () => mockUseStakeholderSummary(),
  }
})

describe("StakeholderSummaryPage", () => {
  afterEach(() => {
    vi.clearAllMocks()
  })

  it("shows a named 'not available' state when the route isn't implemented (shape error)", () => {
    mockUseStakeholderSummary.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new StakeholderSummaryShapeError(),
      isFetching: false,
    })

    render(<StakeholderSummaryPage />)

    expect(screen.getByText(/Summary not available/i)).toBeInTheDocument()
    expect(screen.queryByText(/Failed to load summary/i)).not.toBeInTheDocument()
  })

  it("shows a named 'not available' state on a real 404 too", () => {
    mockUseStakeholderSummary.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: { response: { status: 404 } },
      isFetching: false,
    })

    render(<StakeholderSummaryPage />)

    expect(screen.getByText(/Summary not available/i)).toBeInTheDocument()
  })

  it("still shows the generic error alert for an unrelated failure", () => {
    mockUseStakeholderSummary.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error("network down"),
      isFetching: false,
    })

    render(<StakeholderSummaryPage />)

    expect(screen.getByText(/Failed to load summary/i)).toBeInTheDocument()
    expect(screen.queryByText(/Summary not available/i)).not.toBeInTheDocument()
  })
})
