import "@testing-library/jest-dom/vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { MemoryRouter } from "react-router-dom"
import { beforeEach, describe, expect, it, vi } from "vitest"
import type { Now } from "@/hooks/useNow"
import type { Onboarding, ReceiptPayload } from "@/hooks/useReceipt"
import { formatElapsed } from "@/lib/time"
import { NowPage } from "@/pages/NowPage"

const NOW = Date.parse("2026-09-16T12:00:00Z")
const state: { now: Now | undefined; receipt: ReceiptPayload | undefined; onboarding: Onboarding | undefined } = {
  now: undefined,
  receipt: undefined,
  onboarding: undefined,
}
const refetch = vi.fn()

vi.mock("@/hooks/useReceipt", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useReceipt")>()),
  useReceipt: () => ({ data: state.receipt }),
  useOnboarding: () => ({ data: state.onboarding, refetch, isFetching: false }),
}))

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
    state.receipt = undefined
    state.onboarding = { complete: true, items: [] }
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

  it("shows the last finished run's receipt, with costs labelled by where they come from", () => {
    state.receipt = {
      markdown: "## orch run `run-6` — finished\n",
      built_with: "Built with [orch](https://github.com/hectorcanaimero/orch)",
      receipt: {
        run_id: "run-6", started_at: "2026-09-15T08:00:00Z", finished_at: "2026-09-15T10:05:00Z", finished: true,
        wall_seconds: 7500, agent_seconds: 3000, dispatches: 3, failed_attempts: 1, retries: 1,
        done: [{ task_id: "A", title: "Alpha" }, { task_id: "B", title: "Beta" }], blocked: [{ task_id: "C", title: "Gamma" }],
        providers: [
          { provider: "claude", dispatches: 2, tokens_in: 12000, tokens_out: 800, weighted_tokens: 4000, cost_usd: 1.25, estimated_cost_usd: 0, cost_source: "reported" },
          { provider: "codex", dispatches: 1, tokens_in: 3000, tokens_out: 200, weighted_tokens: 3200, cost_usd: 0.4, estimated_cost_usd: 0.4, cost_source: "estimated" },
          { provider: "gemini", dispatches: 1, tokens_in: 0, tokens_out: 0, weighted_tokens: 0, cost_usd: 0, estimated_cost_usd: 0, cost_source: "no_data" },
        ],
        total_cost_usd: 1.65, estimated_cost_usd: 0.4,
        prs: [{ task_id: "A", title: "Alpha", url: "https://github.com/x/y/pull/7", ci_status: "success" }],
      },
    }
    renderPage()
    const last = screen.getByRole("heading", { name: "Last run" }).closest("section")!
    expect(within(last).getByText("2 tasks done")).toBeInTheDocument()
    expect(within(last).getByText(/1 blocked/)).toBeInTheDocument()
    expect(within(last).getByText("2h 5m wall time · 50m agent time · 1 PR, 1 passed CI")).toBeInTheDocument()
    expect(within(last).getByText("4,000 weighted · 12,800 raw")).toBeInTheDocument()
    expect(within(last).getByText("~$0.40")).toBeInTheDocument()
    expect(within(last).getByText("no data")).toBeInTheDocument()
  })

  it("walks a new project to its first run, with the command for each unmet step", () => {
    state.now = { ...base(), run: null, working: [], attention: [], waiting_for_budget: [] }
    state.onboarding = {
      complete: false,
      items: [
        { id: "providers", done: false, optional: false, detail: "claude not on PATH", command: "orch doctor" },
        { id: "budget", done: true, optional: false, detail: "" },
        { id: "tasks", done: true, optional: false, detail: "" },
        { id: "vcs", done: false, optional: true, detail: "", link: "/delivery/ci" },
        { id: "first_run", done: false, optional: false, detail: "", command: "orch run" },
      ],
    }
    renderPage()
    const card = screen.getByRole("heading", { name: "Get to your first run" }).closest("section")!
    expect(within(card).getByText("2 of 4 required")).toBeInTheDocument()
    expect(within(card).getByText("claude not on PATH")).toBeInTheDocument()
    expect(within(card).getByText("orch doctor")).toBeInTheDocument()
    expect(within(card).getByText("orch run")).toBeInTheDocument()
    expect(within(card).getByText("Needed only for worktrees and automatic PRs.")).toBeInTheDocument()
    // The checklist replaces the bare "has not run yet" line.
    expect(screen.queryByText(/This project has not run yet/)).not.toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: "Check again" }))
    expect(refetch).toHaveBeenCalled()
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
