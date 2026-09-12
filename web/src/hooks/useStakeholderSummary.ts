import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { StakeholderSummary } from "@/lib/types"

const DEFAULT_INTERVAL_MS = 10000

/**
 * Thrown by `fetchStakeholderSummary` when `/stakeholder/summary`
 * answered with something that isn't this payload — the Go dashboard
 * doesn't implement this route yet (unlike Python's, see
 * `server.py:1731`), so the request falls through to the SPA's own
 * catch-all and comes back **200 with the HTML shell**, not a 404 or a
 * connection error. Found via bug 26's own review (PR #205, opus-2's
 * finding): fixing the login wall meant an operator or a stakeholder
 * with a valid token now actually REACHES this page, and without this
 * guard `data.summary.done` throws a bare TypeError on the HTML string
 * axios happily resolved with — the exact same class of bug
 * `PortfolioShapeError` (api.ts) exists to catch for `/api/portfolio`.
 * opus-2 is separately making every unimplemented `/api/*` and
 * `/stakeholder/*` route answer 404 instead of falling through — this
 * guard stands independently of that landing, the same reasoning
 * `PortfolioShapeError`'s own doc comment gives.
 */
export class StakeholderSummaryShapeError extends Error {
  constructor() {
    super(
      "`/stakeholder/summary` did not return a summary payload — likely " +
        "the SPA's own HTML shell, meaning this dashboard doesn't " +
        "implement the route yet",
    )
    this.name = "StakeholderSummaryShapeError"
  }
}

export async function fetchStakeholderSummary(): Promise<StakeholderSummary> {
  const { data } = await apiClient.get<StakeholderSummary | unknown>("/stakeholder/summary")
  if (!data || typeof (data as StakeholderSummary).summary !== "object") {
    throw new StakeholderSummaryShapeError()
  }
  return data as StakeholderSummary
}

/**
 * True when the query's error means "this dashboard doesn't serve the
 * stakeholder summary" — either a real 404 (once the server starts
 * sending one for an unimplemented route) or a `StakeholderSummaryShapeError`
 * (what it sends today — see that class's own doc comment). The page
 * reads this to show a named, purpose-built state instead of the
 * generic "failed to load" alert every other fetch failure gets.
 */
export function isStakeholderSummaryUnavailable(error: unknown): boolean {
  if (error instanceof StakeholderSummaryShapeError) return true
  return (error as unknown as { response?: { status?: number } })?.response?.status === 404
}

/**
 * The retry policy, factored out as a pure function (same reasoning as
 * `isPortfolioNavVisible` in usePortfolio.ts) so each branch — auth
 * errors, the route-not-implemented case, and the two-attempt default —
 * has its own test instead of only being exercised through a live
 * `useQuery` instance.
 *
 * Doesn't retry 401/403 (a real auth failure retrying won't fix) or
 * `isStakeholderSummaryUnavailable` (a route that will keep answering
 * the same way — 404, or today's HTML shell — every 10s; hammering it
 * wastes a request for no reason, same reasoning usePortfolio's own
 * retry uses). Everything else gets up to 2 retries, react-query's
 * default shape.
 */
export function shouldRetryStakeholderSummary(failureCount: number, error: unknown): boolean {
  if (isStakeholderSummaryUnavailable(error)) return false
  const status = (error as unknown as { response?: { status?: number } })?.response?.status
  if (status === 401 || status === 403) return false
  return failureCount < 2
}

export function useStakeholderSummary() {
  return useQuery<StakeholderSummary, Error>({
    queryKey: ["stakeholder", "summary"],
    queryFn: fetchStakeholderSummary,
    refetchInterval: (query) => {
      const data = query.state.data
      if (!data) return DEFAULT_INTERVAL_MS
      const seconds = data.refresh_interval_s
      if (!seconds || seconds <= 0) return DEFAULT_INTERVAL_MS
      return seconds * 1000
    },
    refetchOnWindowFocus: false,
    retry: shouldRetryStakeholderSummary,
  })
}
