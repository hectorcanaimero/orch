import { useQuery } from "@tanstack/react-query"
import type { AxiosError } from "axios"
import { getPortfolio, PortfolioShapeError } from "@/lib/api"
import type { Portfolio } from "@/lib/types"

/**
 * G8.5 — `/api/portfolio`, polled (no SSE — a portfolio poll is N cheap
 * counter queries, not a stream worth keeping open per opus-2's server
 * design note).
 *
 * `enabled` lets a caller (AppLayout, before it decides whether to show
 * the nav item) hold this off entirely for a stakeholder session —
 * portfolio data is operator-only, so a stakeholder session should never
 * even issue the request, not just fail to render its result.
 *
 * The endpoint 404s (not an empty `projects: []`) when the dashboard
 * wasn't started with `--portfolio` — see isPortfolioDisabled, the one
 * place that distinction is read. That case is not retried: hammering an
 * endpoint that will keep 404ing (or, on a dashboard whose route isn't
 * registered at all, keep returning the SPA's own HTML shell — see
 * PortfolioShapeError) until the process restarts with a different flag
 * wastes a request every 30s for no reason.
 */
export function usePortfolio(opts: { enabled?: boolean } = {}) {
  return useQuery<Portfolio, AxiosError | PortfolioShapeError>({
    queryKey: ["portfolio"],
    queryFn: getPortfolio,
    enabled: opts.enabled ?? true,
    refetchInterval: 30_000,
    retry: (failureCount, error) => {
      if (isPortfolioDisabled(error)) return false
      return failureCount < 2
    },
  })
}

/**
 * True when the query's error means "this dashboard isn't in
 * --portfolio mode" — either the real 404 the server is meant to send,
 * or a PortfolioShapeError (the 200-HTML-fallthrough a dashboard with no
 * route registered sends instead, today's actual behavior — see that
 * class's own doc comment).
 */
export function isPortfolioDisabled(error: unknown): boolean {
  if (error instanceof PortfolioShapeError) return true
  return (error as AxiosError | undefined)?.response?.status === 404
}

/**
 * The Portfolio nav item's visibility decision, factored out of
 * AppLayout as a pure function so the two independent reasons it can be
 * hidden — wrong profile, or the dashboard not started with
 * `--portfolio` — are each covered by a table test instead of only by
 * reading the JSX.
 *
 * Hidden for a stakeholder unconditionally (that query is never even
 * issued for one — see usePortfolio's own doc comment). For an operator,
 * hidden until the first answer arrives (neither `data` nor `error` yet,
 * i.e. still loading) so the link doesn't flash on then off while the
 * initial request is in flight; shown once there's real data, or once a
 * non-404 error says "something's wrong, but this isn't 'the flag was
 * never passed'" (fail open on an unrelated hiccup — hiding a real
 * feature on a transient error is worse than a dead link).
 */
export function isPortfolioNavVisible(args: {
  isStakeholder: boolean
  data: unknown
  error: unknown
}): boolean {
  if (args.isStakeholder) return false
  if (args.data) return true
  return args.error != null && !isPortfolioDisabled(args.error)
}
