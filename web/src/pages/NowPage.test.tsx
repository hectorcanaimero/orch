import "@testing-library/jest-dom/vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { MemoryRouter } from "react-router-dom"
import { beforeEach, describe, expect, it, vi } from "vitest"
import type { Now } from "@/hooks/useNow"
import { formatElapsed } from "@/lib/time"
import { NowPage } from "@/pages/NowPage"

const NOW = Date.parse("2026-09-16T12:00:00Z")
const state: { now: Now | undefined } = { now: undefined }

vi.mock("@/hooks/useNow", () => ({
  useNow: () => ({ data: state.now, isLoading: false, isError: false }),
  useTicker: () => NOW,
}))
vi.mock("@/hooks/useEventStream", () => ({ useEventStream: () => ({ status: "open", lastEventAt: null }) }))
vi.mock("@/hooks/useMilestones", () => ({
  useMilestones: () => ({
    data: [{ phase: 0, name: "Foundation", status: "active", progress: { total: 3, done: 2, pct: 66 }, in_progress: 1, blocked: 0, eta: null }],
  }),
}))
vi.mock("@/hooks/useBudgetSummary", () => ({
  useBudgetSummary: () => ({
    data: {
      available: true,
      preset: "conservative",
      path: "budgets.yaml",
      waiting: [],
      rows: [
        {
          provider: "claude", token_budget: 480_000, tokens_used: 29_394, raw_tokens_used: 60_735, pct: 6,
          threshold_pct: 80, over_threshold: false, window_hours: 5, reset_at: null,
          cost_usd: 1.2, cost_source: "reported", estimated_cost_usd: 0,
        },
      ],
    },
  }),
}))
vi.mock("@/hooks/useTasks", () => ({ useTasks: () => ({ data: { tasks: [{ id: "F3.T1", title: "Cursor pagination" }] } }) }))

function base(): Now {
  return {
    project: "acme-billing-api",
    run: { run_id: "run-7", started_at: "2026-09-16T11:00:00Z", updated_at: "", mode: "run", status: "live" },
    working: [
      {
        task_id: "F2.T2", title: "POST /items", phase: 2, provider: "claude", model: "claude/sonnet",
        attempt: 2, max_attempts: 3, started_at: "2026-09-16T11:53:19Z",
      },
    ],
    slots: { used: 1, max: 4 },
    summary: { total: 22, done: 14, in_progress: 1, blocked: 1, backlog: 0 },
    pace: {
      available: true, velocity_per_day: 1, done_count: 14, remaining_tasks: 7, remaining_hours: 9,
      blocked_count: 1, eta_days: 7, eta_date: "2026-09-23", confidence: "high", blockers: [],
    },
    attention: [
      { task_id: "F2.T3", title: "DELETE /items/{id}", phase: 2, reason: "needs a product decision", blocked_at: "2026-09-16T09:00:00Z", estimate_hours: 1 },
    ],
    waiting_for_budget: [{ task_id: "F3.T1", provider: "codex", reset_at: "2026-09-16T14:00:00Z", reason: "blocked-by-budget:codex" }],
  }
}

function renderPage() {
  return render(
    <MemoryRouter>
      <NowPage />
    </MemoryRouter>,
  )
}

describe("NowPage", () => {
  beforeEach(() => {
    state.now = base()
  })

  it("shows each agent at work with a running clock and its attempt", () => {
    renderPage()
    const working = screen.getByRole("heading", { name: "Working now" }).closest("section")!
    expect(within(working).getByText("1 of 4 slots")).toBeInTheDocument()
    expect(within(working).getByText("POST /items")).toBeInTheDocument()
    expect(within(working).getByText("claude · claude/sonnet · attempt 2 of 3")).toBeInTheDocument()
    expect(within(working).getByText("6:41")).toBeInTheDocument()
  })

  it("lists blocked and budget-waiting tasks, and unblocking shows the command instead of doing it", () => {
    renderPage()
    const attention = screen.getByRole("heading", { name: "Needs your attention" }).closest("section")!
    expect(within(attention).getByText(/needs a product decision/)).toBeInTheDocument()
    expect(within(attention).getByText("Cursor pagination")).toBeInTheDocument()
    expect(within(attention).getByText(/Waiting for codex's budget window, resets in 2 hours/)).toBeInTheDocument()
    fireEvent.click(within(attention).getByRole("button", { name: "Unblock" }))
    expect(screen.getByText("orch task set --id F2.T3 --status todo")).toBeInTheDocument()
  })

  it("says the ETA leaves blocked work out, and budget shows weighted and raw tokens", () => {
    renderPage()
    expect(screen.getByText(/Estimated finish .* \(excludes 1 blocked task\)/)).toBeInTheDocument()
    expect(screen.getByText("29,394 / 480,000 tokens")).toBeInTheDocument()
    expect(screen.getByText(/60,735 raw/)).toBeInTheDocument()
    expect(screen.getByText(/reported/)).toBeInTheDocument()
  })

  it("says so plainly when nothing runs and nothing needs attention", () => {
    state.now = { ...base(), run: null, working: [], attention: [], waiting_for_budget: [] }
    renderPage()
    expect(screen.getByText(/This project has not run yet/)).toBeInTheDocument()
    expect(screen.getByText(/Nothing is running/)).toBeInTheDocument()
    expect(screen.getByText(/Nothing needs you/)).toBeInTheDocument()
  })
})

describe("formatElapsed", () => {
  it("reads like a clock", () => {
    expect(formatElapsed(37_000)).toBe("0:37")
    expect(formatElapsed(401_000)).toBe("6:41")
    expect(formatElapsed(3_729_000)).toBe("1:02:09")
    expect(formatElapsed(-5)).toBe("0:00")
  })
})
