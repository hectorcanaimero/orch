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

// A summary as /stakeholder/summary sends it, with the spend switched off
// (show_spend_to_stakeholder: false makes spend_rounded_usd null).
function summaryWithoutSpend() {
  return {
    project_id: "demo",
    summary: { total: 4, done: 2, in_progress: 1, blocked: 0, backlog: 1, todo: 0, percent_done: 50, estimate_hours_total: 8 },
    milestones: [],
    spend_rounded_usd: null,
    eta_hours: 3,
    refresh_interval_s: 30,
    phases_timeline: [],
    spend_by_day: [],
    exec_summary: "",
  }
}

vi.mock("@/components/ProjectConfigWidget", () => ({
  ProjectConfigWidget: () => <div>Project configuration</div>,
}))
const mockUseWhoami = vi.fn()
vi.mock("@/hooks/useWhoami", () => ({ useWhoami: () => mockUseWhoami() }))

describe("StakeholderSummaryPage", () => {
  afterEach(() => {
    vi.clearAllMocks()
  })

  // With spend off the card used to render "$0.00": a figure that is not
  // zero, shown as zero, to exactly the reader it was hidden from.
  it("shows no spend card when the spend is not shared", () => {
    mockUseWhoami.mockReturnValue({ data: { profile: "operator" } })
    mockUseStakeholderSummary.mockReturnValue({
      data: summaryWithoutSpend(), isLoading: false, isError: false, error: null, isFetching: false,
    })
    render(<StakeholderSummaryPage />)
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument()
    expect(screen.queryByText(/^Spend$/i)).not.toBeInTheDocument()
  })

  // Project configuration reads /api/config, which a stakeholder token gets
  // 403 on; it is operator material anyway.
  it("hides project configuration from a stakeholder, and keeps it for an operator", () => {
    mockUseStakeholderSummary.mockReturnValue({
      data: summaryWithoutSpend(), isLoading: false, isError: false, error: null, isFetching: false,
    })
    mockUseWhoami.mockReturnValue({ data: { profile: "stakeholder", routes: ["stakeholder_summary_json"] } })
    const { unmount } = render(<StakeholderSummaryPage />)
    expect(screen.queryByText("Project configuration")).not.toBeInTheDocument()
    unmount()

    mockUseWhoami.mockReturnValue({ data: { profile: "operator" } })
    render(<StakeholderSummaryPage />)
    expect(screen.getByText("Project configuration")).toBeInTheDocument()
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
