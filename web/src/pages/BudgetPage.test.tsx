import "@testing-library/jest-dom/vitest"
import { render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import type { BudgetRow, BudgetSummary } from "@/hooks/useBudgetSummary"
import { BudgetPage } from "@/pages/BudgetPage"

const mockUseBudgetSummary = vi.fn()
vi.mock("@/hooks/useBudgetSummary", () => ({ useBudgetSummary: () => mockUseBudgetSummary() }))

function row(provider: string, extra: Partial<BudgetRow>): BudgetRow {
  return {
    provider,
    token_budget: 800_000,
    tokens_used: 1_000,
    raw_tokens_used: 1_000,
    pct: 1,
    threshold_pct: 60,
    over_threshold: false,
    window_hours: 5,
    reset_at: null,
    cost_usd: 0,
    estimated_cost_usd: 0,
    cost_source: "none",
    ...extra,
  } as BudgetRow
}

function loaded(data: Partial<BudgetSummary>) {
  mockUseBudgetSummary.mockReturnValue({
    data: { available: true, preset: "conservative", path: "/p/budgets.yaml", rows: [], waiting: [], ...data },
    isLoading: false,
    isError: false,
    error: null,
  })
}

describe("BudgetPage", () => {
  afterEach(() => vi.clearAllMocks())

  // Each spend figure must say where it came from; an estimate or a missing
  // number shown as a plain dollar amount reads as what the provider charged.
  it("labels reported, estimated and missing spend, and weighted vs raw tokens", () => {
    loaded({
      rows: [
        row("claude", { cost_source: "reported", cost_usd: 0.63, tokens_used: 83_003, raw_tokens_used: 139_500 }),
        row("codex", { cost_source: "estimated", estimated_cost_usd: 0.16 }),
        row("gemini", { cost_source: "no_data", over_threshold: true }),
      ],
    })
    render(<BudgetPage />)
    expect(screen.getByText("Reported")).toBeInTheDocument()
    expect(screen.getByText("$0.63")).toBeInTheDocument()
    expect(screen.getByText("Estimated")).toBeInTheDocument()
    expect(screen.getByText("~$0.16")).toBeInTheDocument()
    expect(screen.getByText("No data")).toBeInTheDocument()
    expect(screen.getByText("139,500 raw")).toBeInTheDocument()
    // Over the threshold is a pause (waiting), styled as a warning, not a failure.
    expect(screen.getByText("Paused")).toHaveClass("text-status-blocked")
  })

  it("says loudly when the guardrail is off", () => {
    mockUseBudgetSummary.mockReturnValue({
      data: { available: false, reason: "missing", path: "/p/budgets.yaml", preset: "", rows: [], waiting: [] },
      isLoading: false,
      isError: false,
      error: null,
    })
    render(<BudgetPage />)
    expect(screen.getByRole("alert")).toHaveTextContent("Budget guardrail is off")
    expect(screen.getByText("/p/budgets.yaml")).toBeInTheDocument()
  })
})
